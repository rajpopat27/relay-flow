// Package daemon implements the orca-jira-loop poll/dispatch cycle and the
// standalone report handler invoked by the opencode plugin. There is no
// network transport: report is a one-shot stateless CLI call, not an RPC
// to a running process.
package daemon

import (
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"orca-jira-loop/internal/acli"
	"orca-jira-loop/internal/config"
	"orca-jira-loop/internal/opencode"
	"orca-jira-loop/internal/orcacli"
)

// claimLabel is the Jira label added to a ticket on first dispatch,
// permanently, so no two workflows ever operate on the same ticket. The
// config ID separates different YAML files; the workflow ID separates
// workflows nested inside the same YAML file.
func claimLabel(configName, workflowName string) string {
	return "orca-workflow:" + configName + ":" + workflowName
}

type Daemon struct {
	ConfigName string
	Config     *config.Config
	Acli       *acli.Client
	Orca       *orcacli.Client
	DryRun     bool
	// RepoID/RepoDisplayName identify the repo this config instance runs
	// in; every ticket must match this repo's component.
	RepoID          string
	RepoDisplayName string

	// inFlight guards against two poll ticks both dispatching the same
	// ticket concurrently: opencode renames a terminal's title while
	// working, so the terminal-title-based "already running" check alone
	// isn't reliable during an active run (only after it exits/goes idle).
	inFlight   map[string]bool
	inFlightMu sync.Mutex

	// nudged records "<workflow>|<status>|<agent>" for each ticket we've
	// already delivered a prompt to (either as the initial --prompt on
	// terminal create, or as a nudge sent into an existing terminal).
	// PollOnce clears the entry when the ticket's current workflow, status,
	// or mapped agent no longer matches, re-arming the nudge. In-memory
	// only: a daemon restart replays at most one harmless extra nudge (the
	// tui-idle guard makes even that a no-op while busy).
	nudged   map[string]string
	nudgedMu sync.Mutex
}

func New(configName string, cfg *config.Config, repoID, repoDisplayName string, dryRun bool) *Daemon {
	return &Daemon{
		ConfigName:      configName,
		Config:          cfg,
		Acli:            acli.New(),
		Orca:            orcacli.New(dryRun),
		DryRun:          dryRun,
		RepoID:          repoID,
		RepoDisplayName: repoDisplayName,
		inFlight:        make(map[string]bool),
		nudged:          make(map[string]string),
	}
}

// buildJQL appends the component filter (this repo only) to the workflow's
// authored base query, plus a fixed ordering. config.Validate rejects a
// user-authored ORDER BY, so this is always the query's sole/final clause.
func (d *Daemon) buildJQL(base string) string {
	return fmt.Sprintf("(%s) AND component = %q ORDER BY updated", base, d.RepoDisplayName)
}

// claimedByOtherWorkflow reports whether t carries an orca-workflow:* label
// belonging to a different workflow.
func (d *Daemon) claimedByOtherWorkflow(t acli.Ticket, workflowName string) bool {
	mine := claimLabel(d.ConfigName, workflowName)
	for _, l := range t.Labels {
		if strings.HasPrefix(l, "orca-workflow:") && l != mine {
			return true
		}
	}
	return false
}

// PollOnce runs a single poll cycle for every workflow in this config.
func (d *Daemon) PollOnce() {
	for _, workflowName := range d.sortedWorkflowNames() {
		d.pollWorkflow(workflowName, d.Config.Workflows[workflowName])
	}
}

func (d *Daemon) pollWorkflow(workflowName string, wf config.Workflow) {
	// Prefix includes ConfigName so one shared server log stays greppable
	// when multiple configs poll in the same process.
	prefix := d.ConfigName + "/" + workflowName
	jql := d.buildJQL(wf.JQL)
	tickets, err := d.Acli.Search(jql)
	if err != nil {
		log.Printf("poll[%s]: acli search failed: %v", prefix, err)
		return
	}
	log.Printf("poll[%s]: jql=%q returned %d ticket(s)", prefix, jql, len(tickets))

	for _, t := range tickets {
		if d.claimedByOtherWorkflow(t, workflowName) {
			log.Printf("poll[%s]: ticket %s: claimed by a different workflow, skipping", prefix, t.Key)
			continue
		}
		agent, ok := d.Config.AgentForStatus(workflowName, t.Status)
		if !ok {
			if d.Config.ShouldCloseTerminals(workflowName, t.Status) {
				log.Printf("poll[%s]: ticket %s: status %q is in closeOn, closing agent terminals", prefix, t.Key, t.Status)
				d.closeLeftoverTerminals(t)
			} else {
				log.Printf("poll[%s]: ticket %s: no agent mapped for status=%q, leaving terminals alone", prefix, t.Key, t.Status)
			}
			d.clearNudged(t.Key, workflowName, t.Status, "")
			continue
		}
		d.clearNudged(t.Key, workflowName, t.Status, agent)
		go d.dispatch(workflowName, t, agent)
	}
}

// clearNudged drops the nudge marker for key if the ticket's current
// workflow+status+agent no longer matches what we last prompted for.
func (d *Daemon) clearNudged(key, workflowName, status, agent string) {
	d.nudgedMu.Lock()
	defer d.nudgedMu.Unlock()
	if cur, ok := d.nudged[key]; ok && cur != workflowName+"|"+status+"|"+agent {
		delete(d.nudged, key)
	}
}

// markNudged records that key's workflow agent for status has been prompted.
func (d *Daemon) markNudged(key, workflowName, status, agent string) {
	d.nudgedMu.Lock()
	defer d.nudgedMu.Unlock()
	d.nudged[key] = workflowName + "|" + status + "|" + agent
}

// closeLeftoverTerminals closes any daemon-created terminal (title
// "<key>:<agent>:<status>") that is still open after its report transitioned the
// ticket to a workflow closeOn status. Stateless by design: the handle is
// re-discovered from the worktree each poll, so the daemon needs no memory
// of its own past runs and can start anytime.
func (d *Daemon) closeLeftoverTerminals(t acli.Ticket) {
	terms, err := d.Orca.TerminalList("name:" + t.Key)
	if err != nil {
		log.Printf("poll: ticket %s: terminal list failed: %v", t.Key, err)
		return
	}
	for _, term := range terms {
		if !strings.HasPrefix(term.Title, t.Key+":") {
			continue
		}
		log.Printf("poll: ticket %s: closing leftover terminal %q (%s)", t.Key, term.Title, term.Handle)
		if err := d.Orca.TerminalClose(term.Handle); err != nil {
			log.Printf("poll: ticket %s: terminal close failed: %v", t.Key, err)
		}
	}
}

func (d *Daemon) dispatch(workflowName string, t acli.Ticket, agent string) {
	d.inFlightMu.Lock()
	if d.inFlight[t.Key] {
		d.inFlightMu.Unlock()
		log.Printf("dispatch %s: already dispatching from a prior poll tick, skipping this cycle", t.Key)
		return
	}
	d.inFlight[t.Key] = true
	d.inFlightMu.Unlock()
	defer func() {
		d.inFlightMu.Lock()
		delete(d.inFlight, t.Key)
		d.inFlightMu.Unlock()
	}()

	if ok, err := opencode.Exists(agent); err != nil {
		log.Printf("dispatch %s: could not verify opencode agent %q: %v", t.Key, agent, err)
		return
	} else if !ok {
		log.Printf("dispatch %s: opencode agent %q does not exist, skipping", t.Key, agent)
		return
	}

	if err := d.Acli.AddLabel(t.Key, t.Labels, claimLabel(d.ConfigName, workflowName)); err != nil {
		log.Printf("dispatch %s: failed to add claim label: %v", t.Key, err)
		return
	}

	if err := d.ensureWorktree(t); err != nil {
		log.Printf("dispatch %s: ensure worktree failed: %v", t.Key, err)
		return
	}

	// Title includes the Jira status: one agent can handle multiple
	// statuses (v3 per-status outcomes), and each status visit must get a
	// FRESH terminal — reusing the old session would leak the previous
	// status's context into the new task.
	title := fmt.Sprintf("%s:%s:%s", t.Key, agent, t.Status)

	// If a terminal with our exact title already exists, the agent's
	// session is still live — nudge it in place instead of creating a
	// fresh one (a new session would have to re-gather all context,
	// burning tokens). The agent is guaranteed idle here: it only
	// transitions the ticket after reporting, and closeLeftoverTerminals
	// only closes terminals for closeOn statuses. Match on title, not just
	// worktree presence, because ensureWorktree's first-time worktree
	// creation spawns its own default scaffolding tabs (e.g. "Terminal 1",
	// "Setup") which are not ours and must not block our own dispatch.
	existing, err := d.Orca.TerminalList("name:" + t.Key)
	if err != nil {
		log.Printf("dispatch %s: terminal list failed: %v", t.Key, err)
		return
	}
	for _, term := range existing {
		if term.Title == title {
			// Already prompted for this exact workflow+status+agent (initial
			// --prompt on create counts) — do nothing until Jira status
			// changes (clearNudged re-arms us).
			d.nudgedMu.Lock()
			already := d.nudged[t.Key] == workflowName+"|"+t.Status+"|"+agent
			d.nudgedMu.Unlock()
			if already {
				log.Printf("dispatch %s: already prompted for workflow=%s status=%q agent=%s, skipping (nudge once per status visit)", t.Key, workflowName, t.Status, agent)
				return
			}
			// Agent mid-turn: typed text would corrupt its input box.
			// Skip this cycle WITHOUT marking — next poll retries until
			// the nudge actually lands.
			if err := d.Orca.TerminalWait(term.Handle, "tui-idle", 3000); err != nil {
				log.Printf("dispatch %s: terminal %q busy, skipping nudge this cycle", t.Key, title)
				return
			}
			nudge := d.buildNudge(workflowName, t, agent)
			log.Printf("dispatch %s: terminal %q already exists, nudging it (handle=%s)", t.Key, title, term.Handle)
			if err := d.Orca.TerminalSend(term.Handle, nudge); err != nil {
				log.Printf("dispatch %s: terminal send failed (will retry next poll): %v", t.Key, err)
				return
			}
			d.markNudged(t.Key, workflowName, t.Status, agent)
			log.Printf("dispatch %s: nudge sent for workflow=%s status=%q agent=%s", t.Key, workflowName, t.Status, agent)
			return
		}
	}

	// ORCA_JIRA_LOOP_CONFIG tells the plugin which .workflow/<name>.yaml to
	// load. ORCA_JIRA_LOOP_WORKFLOW tells it which workflow inside that
	// file governs this terminal. ORCA_JIRA_LOOP_TICKET/_AGENT tell it
	// what to report against without it needing to inspect the terminal
	// title itself. A developer's own opencode session (started manually)
	// never has these set, so it never triggers a report.
	command := fmt.Sprintf("ORCA_JIRA_LOOP_CONFIG=%s ORCA_JIRA_LOOP_WORKFLOW=%s ORCA_JIRA_LOOP_TICKET=%s ORCA_JIRA_LOOP_AGENT=%s opencode --agent %s --model opencode/deepseek-v4-flash-free --auto --prompt %s",
		shellQuote(d.ConfigName), shellQuote(workflowName), shellQuote(t.Key), shellQuote(agent), shellQuote(agent), shellQuote(d.buildPrompt(workflowName, t, agent)))

	handle, err := d.Orca.TerminalCreate(t.Key, title, command)
	if err != nil {
		log.Printf("dispatch %s: terminal create failed: %v", t.Key, err)
		return
	}
	// The initial --prompt IS this workflow+status+agent's prompt — mark it
	// so the next poll doesn't immediately nudge the fresh terminal.
	d.markNudged(t.Key, workflowName, t.Status, agent)
	log.Printf("dispatch %s: terminal %q created (handle=%s), waiting for tui-idle", t.Key, title, handle)

	if err := d.Orca.TerminalWait(handle, "tui-idle", 10*60*1000); err != nil {
		log.Printf("dispatch %s: terminal wait failed: %v", t.Key, err)
		return
	}
	log.Printf("dispatch %s: terminal %q went idle; awaiting plugin report", t.Key, title)
}

// buildPrompt tells the agent only which ticket it owns and the
// STATUS/SUMMARY handoff contract it must end with. The agent is expected
// to use the `acli` CLI (not the Jira MCP tool) to fetch the ticket's
// current summary, description, and full comment history itself — we
// deliberately don't duplicate any of that here, so review
// feedback/comments are always picked up fresh on every dispatch without
// us needing to inject them.
func (d *Daemon) buildPrompt(workflowName string, t acli.Ticket, agent string) string {
	// Outcomes are per-Jira-status in v3: list only those valid for the
	// ticket's CURRENT status, so the agent never sees targets that belong
	// to a different handle entry.
	agentCfg, _ := d.Config.AgentConfigFor(workflowName, agent)
	outcomes := agentCfg.OutcomesFor(t.Status)
	statusLines := make([]string, 0, len(outcomes))
	for status, target := range outcomes {
		statusLines = append(statusLines, fmt.Sprintf("%s (moves ticket to: %s)", status, target))
	}
	sort.Strings(statusLines)
	// Flattened to one line: --command is typed into the pty via keystroke
	// simulation, so an embedded newline would submit early.
	prompt := fmt.Sprintf(
		"You have been assigned Jira ticket %s. Do NOT use any Jira MCP tool. Instead, run `acli jira workitem view %s --fields summary,description,comment --json` "+
			"in the shell to fetch the ticket's details and full comment history, so you understand what's being asked and any prior feedback. "+
			"When you are done, end your final message with exactly: STATUS: <one status name from below> SUMMARY: <one-line summary of what you did>. Statuses: %s",
		t.Key, t.Key, strings.Join(statusLines, "; "))
	return strings.Join(strings.Fields(prompt), " ")
}

// buildNudge renders the agent's configured nudgePrompt ({{ticket}} and
// {{status}} placeholders) for re-injection into an existing idle terminal
// when its ticket lands back on one of this agent's statuses. Flattened to
// one line: terminal send types via keystroke simulation, so an embedded
// newline would submit early.
func (d *Daemon) buildNudge(workflowName string, t acli.Ticket, agent string) string {
	agentCfg, _ := d.Config.AgentConfigFor(workflowName, agent)
	nudge := strings.ReplaceAll(agentCfg.NudgePrompt, "{{ticket}}", t.Key)
	nudge = strings.ReplaceAll(nudge, "{{status}}", t.Status)
	return strings.Join(strings.Fields(nudge), " ")
}

// shellQuote wraps s in single quotes for safe use inside a shell command
// string, escaping any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ensureWorktree checks for an existing worktree up to 3 times (2s apart)
// before creating one — orca worktree list can be briefly stale right
// after a create, and orca worktree create silently auto-suffixes
// (e.g. "-2") on a name collision instead of erroring, so a false
// negative here would pile up duplicate worktrees forever.
func (d *Daemon) ensureWorktree(t acli.Ticket) error {
	for attempt := 0; attempt < 3; attempt++ {
		if _, ok, err := d.Orca.FindWorktree(d.RepoID, t.Key); err != nil {
			return err
		} else if ok {
			return nil
		}
		if attempt < 2 {
			time.Sleep(2 * time.Second)
		}
	}

	parentID, baseBranch, err := d.resolveWorktreeParent(t)
	if err != nil {
		return err
	}
	if err := d.Orca.WorktreeCreate(t.Key, d.RepoID, parentID, baseBranch); err != nil {
		return err
	}
	// Verify the worktree landed under the exact ticket-key name: Orca
	// silently auto-suffixes (KCC-1377-2, -3, ...) instead of erroring on
	// a name/branch collision, and without this check every poll creates
	// another suffixed duplicate forever. One retry cycle is enough —
	// the create just happened, so the list should be fresh.
	for attempt := 0; attempt < 3; attempt++ {
		if _, ok, err := d.Orca.FindWorktree(d.RepoID, t.Key); err != nil {
			return err
		} else if ok {
			return nil
		}
		if attempt < 2 {
			time.Sleep(2 * time.Second)
		}
	}
	return fmt.Errorf("worktree %q not found after create (Orca likely auto-suffixed it on a name/branch collision) — clean up the suffixed worktree/branch manually", t.Key)
}

// resolveWorktreeParent picks the worktree ancestry for a new ticket
// worktree:
//  1. an explicit "baseBranch:<branch>" label on the ticket itself wins;
//  2. otherwise, a subtask (has a Jira parent) reuses its parent ticket's
//     actual worktree/branch, whatever that is;
//  3. otherwise, the repo's default/main worktree.
func (d *Daemon) resolveWorktreeParent(t acli.Ticket) (parentWorktreeID, baseBranch string, err error) {
	if branch, ok := t.LabelValue("baseBranch"); ok {
		wts, err := d.Orca.WorktreeList()
		if err != nil {
			return "", "", err
		}
		for _, w := range wts {
			if w.RepoID == d.RepoID && w.Branch == branch {
				return w.ID, w.Branch, nil
			}
		}
		return "", "", fmt.Errorf("ticket %s: baseBranch label %q matched no existing worktree", t.Key, branch)
	}
	if t.ParentKey != "" {
		w, ok, err := d.Orca.FindWorktree(d.RepoID, t.ParentKey)
		if err != nil {
			return "", "", err
		}
		if !ok {
			return "", "", fmt.Errorf("parent ticket %s has no worktree yet; cannot create subtask worktree for %s", t.ParentKey, t.Key)
		}
		return w.ID, w.Branch, nil
	}
	w, ok, err := d.Orca.MainWorktree(d.RepoID)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", "", fmt.Errorf("could not find main worktree for repo %s", d.RepoID)
	}

	// A branch for this ticket may already exist (e.g. left over from a
	// worktree that was removed without its branch being deleted). Reuse
	// it instead of the parent's branch — Orca can't create a second
	// branch with the same name and silently renames the worktree
	// instead, which desyncs every ticket-key-based lookup we do
	// afterward.
	if existing, ok, err := orcacli.FindExistingBranch(w.Path, t.Key); err != nil {
		log.Printf("resolveWorktreeParent %s: existing-branch check failed, falling back to default base branch: %v", t.Key, err)
	} else if ok {
		return w.ID, existing, nil
	}
	return w.ID, w.Branch, nil
}

// ProjectKeyFromJQL extracts the project key from a base JQL like
// "project = KCC AND ...". Needed for the startup status validation,
// which scopes its checks to the project.
func ProjectKeyFromJQL(jql string) (string, error) {
	re := regexp.MustCompile(`(?i)\bproject\s*=\s*("?[A-Za-z][A-Za-z0-9]*"?)`)
	m := re.FindStringSubmatch(jql)
	if m == nil {
		return "", fmt.Errorf("could not find 'project = <KEY>' in jql %q; it is required for status validation", jql)
	}
	return strings.Trim(m[1], `"`), nil
}

// StatusValidator is the seam ValidateConfigStatuses uses to check one
// status name against a Jira project. *acli.Client satisfies it; tests
// substitute fakes.
type StatusValidator interface {
	ValidateStatus(projectKey, status string) error
}

// ValidateStatuses checks every Jira status name referenced anywhere in
// the workflow config (handles, outcomes targets, closeOn) against the
// Jira project, using JQL parse errors as the validator. Returns the list
// of invalid names (empty = all good).
func ValidateStatuses(cfg *config.Config, v StatusValidator, projectKey string) ([]string, error) {
	seen := map[string]bool{}
	var names []string
	add := func(s string) {
		key := strings.ToLower(strings.TrimSpace(s))
		if key != "" && !seen[key] {
			seen[key] = true
			names = append(names, s)
		}
	}
	for _, wf := range cfg.Workflows {
		for _, status := range wf.CloseOn {
			add(status)
		}
		for _, a := range wf.Agents {
			for _, status := range a.AllJiraStatuses() {
				add(status)
			}
		}
	}
	var bad []string
	for _, name := range names {
		if err := v.ValidateStatus(projectKey, name); err != nil {
			log.Printf("status validation: %v", err)
			bad = append(bad, name)
		}
	}
	return bad, nil
}

// ValidateConfigStatuses runs ValidateStatuses for every workflow in cfg,
// scoping each to the project key extracted from that workflow's JQL.
// Returns the sorted list of invalid statuses as "<workflow>: <status>"
// entries (empty = all good). Fails fast if any workflow's JQL has no
// extractable project key.
func ValidateConfigStatuses(cfg *config.Config, v StatusValidator) ([]string, error) {
	var bad []string
	for _, workflowName := range sortedWorkflowNamesFrom(cfg) {
		wf := cfg.Workflows[workflowName]
		projectKey, err := ProjectKeyFromJQL(wf.JQL)
		if err != nil {
			return nil, fmt.Errorf("workflow %s: %w", workflowName, err)
		}
		// ValidateStatuses takes a full Config; scope it to this one
		// workflow so each workflow is checked against its own project.
		scoped := &config.Config{Workflows: map[string]config.Workflow{workflowName: wf}}
		workflowBad, err := ValidateStatuses(scoped, v, projectKey)
		if err != nil {
			return nil, err
		}
		for _, s := range workflowBad {
			bad = append(bad, workflowName+": "+s)
		}
	}
	sort.Strings(bad)
	return bad, nil
}

func sortedWorkflowNamesFrom(cfg *config.Config) []string {
	names := make([]string, 0, len(cfg.Workflows))
	for name := range cfg.Workflows {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ReportResult describes the outcome of a single Report call. On
// "nudged", Detail is the message text the caller (the opencode plugin,
// via its own client) should inject back into the same session — Go
// never talks to Orca/opencode to deliver it; it only decides.
type ReportResult struct {
	Action string // "transitioned", "nudged", "unknown_agent"
	Detail string
}

// Report handles a single STATUS/SUMMARY report from an agent, exactly
// once, with no internal retry loop — invoked fresh by the `report` CLI
// subcommand on every session.idle. It either transitions the Jira
// ticket, or (if the status is not one this agent may report) returns a
// nudge message for the caller to deliver. Status and summary arrive
// already parsed from the plugin — the deterministic plugin-side regex
// is the only place LLM output is interpreted, so no parsing happens
// here. There is no attempt limit or exhaustion state: a stuck agent
// gets nudged again on its next idle, indefinitely, and a human will
// notice a runaway loop.
// ReportAcli is the seam Report uses to talk to Jira. *acli.Client
// satisfies it; tests substitute fakes.
type ReportAcli interface {
	View(key string) (acli.Ticket, error)
	Comment(key, body string) error
	Transition(key, status string) error
}

func Report(cfg *config.Config, acliClient ReportAcli, workflowName, ticketKey, agentName, status, summary string) (ReportResult, error) {
	ticket, err := acliClient.View(ticketKey)
	if err != nil {
		return ReportResult{}, fmt.Errorf("view ticket %s: %w", ticketKey, err)
	}
	agentCfg, ok := cfg.AgentConfigFor(workflowName, agentName)
	if !ok {
		return ReportResult{Action: "unknown_agent"}, fmt.Errorf("unknown agent %q for workflow %q", agentName, workflowName)
	}

	// Outcomes are per-Jira-status: resolve against the ticket's CURRENT
	// status (from View), so the same agent can report "done" from To Do
	// (→ In Progress) and from In Review (→ Done) with different targets.
	if !agentCfg.HasStatus(ticket.Status, status) {
		msg := "Your last message did not include a valid STATUS/SUMMARY block. Please end your turn with:\nSTATUS: <one of: " +
			strings.Join(agentCfg.StatusNamesFor(ticket.Status), ", ") + ">\nSUMMARY: <summary>"
		return ReportResult{Action: "nudged", Detail: msg}, nil
	}

	// Comment first: it's the durable record of what the agent did, and
	// must land before we ever consider the job "reported". Only once it
	// succeeds do we attempt the Transition — the thing that actually
	// hands the ticket off to the next agent. On either failure, Action
	// stays "error" so the plugin knows to just call `report` again
	// (cheap, no new LLM turn) rather than treat it as done.
	if err := acliClient.Comment(ticketKey, fmt.Sprintf("[%s] %s", agentName, summary)); err != nil {
		return ReportResult{Action: "error"}, fmt.Errorf("comment failed, retry report: %w", err)
	}

	// Transitioning to jiraStatus is what selects the next agent: the next
	// poll resolves it via Workflows[workflow].Agents[*].Handles.
	jiraStatus := agentCfg.OutcomesFor(ticket.Status)[status]
	// Self-loop (target == current status): Jira has no self-transitions,
	// so the comment above is the whole outcome — skip the API call.
	if strings.EqualFold(jiraStatus, ticket.Status) {
		return ReportResult{Action: "transitioned", Detail: jiraStatus}, nil
	}
	if err := acliClient.Transition(ticketKey, jiraStatus); err != nil {
		return ReportResult{Action: "error"}, fmt.Errorf("transition failed (comment already posted, safe to retry report): %w", err)
	}
	return ReportResult{Action: "transitioned", Detail: jiraStatus}, nil
}

// PollLoop runs PollOnce every interval until stopped via the returned channel close.
func (d *Daemon) PollLoop(stop <-chan struct{}) {
	interval := time.Duration(d.Config.PollIntervalSeconds) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	d.PollOnce()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			d.PollOnce()
		}
	}
}

func (d *Daemon) sortedWorkflowNames() []string {
	names := make([]string, 0, len(d.Config.Workflows))
	for name := range d.Config.Workflows {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
