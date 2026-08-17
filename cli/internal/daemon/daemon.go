// Package daemon runs one workflow's poll loop: list tickets from the
// tasks adapter, route each through the 3-way claim switch, and dispatch
// agent sessions through the runner adapter. It contains no tracker- or
// runner-specific logic — both arrive as interfaces.
package daemon

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/rajpopat27/relayflow/cli/internal/config"
	"github.com/rajpopat27/relayflow/cli/internal/runner"
	"github.com/rajpopat27/relayflow/cli/internal/tasks"
)

// Daemon polls one workflow and dispatches tickets. Long-lived: one poll
// goroutine per submitted workflow; dispatch/bounce run as short-lived
// goroutines per ticket.
type Daemon struct {
	cfg      *config.Config
	tasks    tasks.Tasks
	runner   runner.Runner
	repoID   string
	repoName string
	dryRun   bool

	// nudged marks key → node for which a prompt/nudge was already
	// delivered, so each node visit prompts exactly once. Cleared when a
	// report moves the ticket (re-arming the next visit).
	nudgedMu sync.Mutex
	nudged   map[string]string

	wg sync.WaitGroup // tracks dispatch/bounce goroutines; Wait blocks tests
}

// New builds a daemon for one validated workflow config.
func New(cfg *config.Config, tk tasks.Tasks, rn runner.Runner, repoID, repoName string, dryRun bool) *Daemon {
	return &Daemon{
		cfg: cfg, tasks: tk, runner: rn,
		repoID: repoID, repoName: repoName, dryRun: dryRun,
		nudged: map[string]string{},
	}
}

// PollLoop ticks until ctx is cancelled (server shutdown/remove).
func (d *Daemon) PollLoop(ctx context.Context) {
	interval := time.Duration(d.cfg.PollIntervalSeconds) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	d.PollOnce()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.PollOnce()
		}
	}
}

// Wait blocks until in-flight dispatch/bounce goroutines finish. Tests
// call it after PollOnce; production never needs it.
func (d *Daemon) Wait() { d.wg.Wait() }

// PollOnce lists tickets once and routes each:
//
//	claimed by another workflow → skip (cross-workflow mutex)
//	unmapped tracker state        → log + skip
//	node in closeOn               → runner.Close (terminal teardown)
//	claimed by me, not prompted   → bounce: Find → Nudge (or respawn)
//	unclaimed                     → dispatch: Claim → Spawn
func (d *Daemon) PollOnce() {
	found, err := d.tasks.List()
	if err != nil {
		log.Printf("poll %s: %v", d.cfg.Name, err)
		return
	}
	for _, t := range found {
		switch {
		case t.ClaimedBy != "" && t.ClaimedBy != d.cfg.Name:
			// Foreign workflow owns it — never touch.
		case t.Node == "":
			log.Printf("poll %s: %s at unmapped state, skipping", d.cfg.Name, t.Key)
		case d.cfg.CloseOn.Has(t.Node):
			log.Printf("poll %s: %s at terminal node %q, closing terminals", d.cfg.Name, t.Key, t.Node)
			if err := d.runner.Close(t); err != nil {
				log.Printf("poll %s: close %s: %v", d.cfg.Name, t.Key, err)
			}
			d.ClearNudged(t.Key)
		case d.cfg.Nodes[t.Node].Agent == "":
			// Human gate: no automation. Claim it so foreign workflows
			// leave it alone, then leave the ticket for the human.
			if t.ClaimedBy == "" {
				if err := d.tasks.Claim(t); err != nil {
					log.Printf("poll %s: claim %s (gate node): %v", d.cfg.Name, t.Key, err)
				}
			}
		case t.ClaimedBy == d.cfg.Name:
			d.wg.Add(1)
			go d.bounce(t)
		default:
			d.wg.Add(1)
			go d.dispatch(t)
		}
	}
}

// dispatch claims an unclaimed ticket and spawns its agent session.
func (d *Daemon) dispatch(t tasks.Ticket) {
	defer d.wg.Done()
	node := d.cfg.Nodes[t.Node]
	if err := d.tasks.Claim(t); err != nil {
		log.Printf("dispatch %s: claim: %v", t.Key, err)
		return
	}
	prompt := initialPrompt(d.cfg, t.Node, t)
	env := map[string]string{
		"RELAYFLOW_WORKFLOW": d.cfg.Name,
		"RELAYFLOW_TICKET":   t.Key,
		"RELAYFLOW_NODE":     t.Node,
		"RELAYFLOW_AGENT":    node.Agent,
	}
	if err := d.runner.Spawn(t, t.Node, node.Agent, prompt, env); err != nil {
		log.Printf("dispatch %s: spawn: %v", t.Key, err)
		return
	}
	d.markNudged(t.Key, t.Node)
	log.Printf("dispatch %s: spawned %q for node %q", t.Key, node.Agent, t.Node)
}

// bounce handles a ticket this workflow claimed but has no in-memory
// record of (server restart): find the live terminal and nudge it, once
// per node visit. No terminal → spawn fresh (claim already held).
func (d *Daemon) bounce(t tasks.Ticket) {
	defer d.wg.Done()
	node := d.cfg.Nodes[t.Node]
	sess, ok, err := d.runner.Find(t, t.Node)
	if err != nil {
		log.Printf("bounce %s: find: %v", t.Key, err)
		return
	}
	if !ok {
		// No live session: marker is irrelevant — always respawn (a
		// terminal may have died after we marked it prompted).
		d.ClearNudged(t.Key)
		// Crash took the terminal with it: spawn a fresh session.
		prompt := initialPrompt(d.cfg, t.Node, t)
		env := map[string]string{
			"RELAYFLOW_WORKFLOW": d.cfg.Name,
			"RELAYFLOW_TICKET":   t.Key,
			"RELAYFLOW_NODE":     t.Node,
			"RELAYFLOW_AGENT":    node.Agent,
		}
		if err := d.runner.Spawn(t, t.Node, node.Agent, prompt, env); err != nil {
			log.Printf("bounce %s: spawn: %v", t.Key, err)
			return
		}
		d.markNudged(t.Key, t.Node)
		log.Printf("bounce %s: no live session, spawned fresh for node %q", t.Key, t.Node)
		return
	}
	if d.nudgedNode(t.Key) == t.Node {
		return // session alive and already prompted for this visit
	}
	prompt := renderNudge(node.NudgePrompt, t.Key, t.Node)
	if err := d.runner.Nudge(sess, prompt); err != nil {
		log.Printf("bounce %s: nudge: %v (retry next poll)", t.Key, err)
		return
	}
	d.markNudged(t.Key, t.Node)
	log.Printf("bounce %s: nudged %q for node %q", t.Key, sess.Title, t.Node)
}

// ClearNudged drops the prompted marker for a ticket — called when a
// report moves it to a new node, re-arming the next visit's nudge.
func (d *Daemon) ClearNudged(key string) {
	d.nudgedMu.Lock()
	delete(d.nudged, key)
	d.nudgedMu.Unlock()
}

func (d *Daemon) markNudged(key, node string) {
	d.nudgedMu.Lock()
	d.nudged[key] = node
	d.nudgedMu.Unlock()
}

func (d *Daemon) nudgedNode(key string) string {
	d.nudgedMu.Lock()
	defer d.nudgedMu.Unlock()
	return d.nudged[key]
}

// initialPrompt tells the agent which ticket it owns and the STATUS/
// SUMMARY handoff contract (statuses success|failure). The agent fetches
// ticket details itself via acli — nothing is injected here so feedback
// is always picked up fresh. Flattened to one line: the command is typed
// into a pty via keystroke simulation.
func initialPrompt(cfg *config.Config, nodeName string, t tasks.Ticket) string {
	prompt := fmt.Sprintf(
		"You have been assigned ticket %s. Run `acli jira workitem view %s --fields summary,description,comment --json` "+
			"to fetch its details and full comment history. When you are done, end your final message with exactly: "+
			"STATUS: <success or failure> SUMMARY: <one-line summary of what you did>",
		t.Key, t.Key)
	return strings.Join(strings.Fields(prompt), " ")
}

// renderNudge applies {{ticket}}/{{node}} templating and flattens to one
// line (keystroke simulation submits on newline).
func renderNudge(tmpl, ticket, node string) string {
	s := strings.ReplaceAll(tmpl, "{{ticket}}", ticket)
	s = strings.ReplaceAll(s, "{{node}}", node)
	return strings.Join(strings.Fields(s), " ")
}
