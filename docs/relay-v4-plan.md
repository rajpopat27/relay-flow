# Relay — v4 Master Plan (complete design doc)

Tracker-agnostic, graph-based agent workflow engine. Greenfield rewrite of
orca-jira-loop. This doc is the single source of truth for the rewrite.

---

## 1. Core idea (ELI5)

Each ticket is a token moving across squares on a board. Squares = **nodes**
(you name them). Each node has a robot = **agent** (an OpenCode agent). When a
token lands on a square the robot works, then says **success** or **failure**,
and the node's own edges (`onSuccess`/`onFailure`) decide the next square.
Jira/Linear/beads/GitHub are just scoreboards recording which square the token
is on — swap the scoreboard, the game is unchanged.

- One node = one tracker state. The same agent may serve many nodes.
- Self-loops allowed (`onFailure: coding`) → comment only, no transition.
- Terminal node (no agent, listed in `closeOn`) → tear down terminals.
- Cycles allowed = rework loops.

## 2. Naming

- Binary: **`relay`**
- State dir: `~/.relay/` → `server.lock` (flock), `server.sock`, `server.log`,
  `plugin.log`, `config.yaml` (machine config, 0600)
- Env vars injected into agent terminals: `RELAY_WORKFLOW`, `RELAY_TICKET`,
  `RELAY_NODE`, `RELAY_AGENT`
- Claim label on tickets: `wf:<name>` (`name` = YAML name, global identity)

## 3. Workflow YAML (one workflow per file)

```yaml
name: xyzTaskFlow               # required, camelCase, global identity (dup submit → 409)
pollIntervalSeconds: 15         # optional, default 15

tasks:                          # ticket-system adapter (whose tickets, whose states)
  type: jira
  config:                       # opaque to core; validated strictly by the adapter
    query: project = xyz        # required; JQL fragment; issuetype/assignee/ORDER BY banned
    issueTypes: [Task]          # required; scalar or list
    stateField: status          # optional, default status (beads could use label/assignee)
    assigneeIsAgent: true       # optional; false → append AND assignee = "<machine assignee>"

runner:                         # execution backend (how/where agents run)
  type: orca
  config: {}                    # orca needs nothing (workspaces dir comes from Orca)

closeOn: [done]                 # required, ≥1; these nodes must have no agent

nodes:
  coding:
    agent: build                # required for agent nodes (absent → terminal/runless node)
    when: "In Progress"         # required scalar; poll-time condition; globally unique per file
    onSuccess: reviewing        # required for agent nodes
    onFailure: coding           # required for agent nodes; self-loop allowed
    nudgePrompt: "..."          # optional; {{ticket}} {{node}} templating; sane default
  reviewing:
    agent: build                # same agent on multiple nodes = normal
    when: "In Review"
    onSuccess: done
    onFailure: coding
  done:
    when: "Done"                # no agent → must be in closeOn
```

Why `when`: it describes the poll-time condition ("run this node when the
ticket's state is X"), not storage. Scalar only — one node, one state; a node
with many states collapses the graph into a self-loop and kills per-state
outcomes. Same agent on many statuses → many nodes sharing the agent name.

Machine identity never lives in YAML (committed/team-shared). Assignee lives in
`~/.relay/config.yaml` via `relay init --assignee "<display name|accountId>"`
(probe-validated through the tracker).

## 4. Interfaces (the plug points)

```go
// internal/tasks — ticket systems (jira built-in; beads/linear/github user-supplied)
type Ticket struct { Key, Summary, Node, ClaimedBy string }
type Tasks interface {
    List() ([]Ticket, error)                        // Node filled via when-reverse-map; ClaimedBy from labels
    Claim(t Ticket) error                           // add label wf:<name>
    Report(t Ticket, outcome, targetNode, summary string) error
}

// internal/runner — execution backends (orca built-in; tmux etc. user-supplied)
type Session struct{ ID, Title string }
type Runner interface {
    Spawn(t tasks.Ticket, node, agent, prompt string, env map[string]string) error
    Find(t tasks.Ticket, node string) (Session, bool, error) // bounce: exact title key:agent:node
    Nudge(s Session, prompt string) error
    Close(t tasks.Ticket) error                       // close terminals with key: prefix
}
```

Registries: `tasks.Register("jira", factory)` in adapter `init()` — the
database/sql driver pattern. External adapters = fork + import. No dynamic
loading (YAGNI). Factory receives: opaque config map, workflow name, nodes
(for `when` maps), machine assignee.

## 5. Runtime flow

### Poll cycle (per submitted workflow, 1 goroutine)
```
tick → tasks.List()
  per ticket:
    ClaimedBy == other workflow     → skip (cross-workflow mutex)
    Node == ""                      → unmapped state, log + skip
    Node in closeOn                 → runner.Close(ticket)
    ClaimedBy == mine, not in memory → bounce: runner.Find → runner.Nudge(nudgePrompt)
    unclaimed                       → go dispatch: tasks.Claim → runner.Spawn
```

### Dispatch goroutine (short-lived, per ticket)
Claim (label) → Spawn: worktree ensure → terminal titled
`<key>:<agent>:<node>` → inject 4 env vars → initial prompt → goroutine exits.
Multiple tickets at the same node → parallel terminals, isolated worktrees.

### Report loopback
Agent ends turn with STATUS/SUMMARY → plugin (session.idle) parses
deterministically → `relay report` (thin socket client) →
`POST /report {workflow, ticket, node, outcome, summary}` → server:
- outcome ∉ {success, failure} → 400
- edge lookup: nodes[node].onSuccess/onFailure → targetNode
- tasks.Report → adapter: if target's `when` == ticket's current state →
  comment only (self-loop), else transition + comment
- reply `{action: transitioned|commented|error, detail}`
Plugin: action==error → retry 3× → then nudge session with detail.

### Crash/restart (bounce)
Server down = system down (no standalone report — by design). Restart =
resubmit. Claim label survives in the tracker; in-memory claimed set is lost.
First poll sees `ClaimedBy == mine` but unknown → bounce: find terminal by
exact title, nudge the existing session → agent re-reports → self-heals.

### Terminal titles & sessions
`<key>:<agent>:<node>`. Old terminals kept; bounce reuses same-node session;
only closeOn closes terminals. Runner sets title at spawn; plugin re-pins at
first idle (OpenCode naming-agent race).

## 6. Server

- Unix socket `~/.relay/server.sock`; flock `server.lock` single-instance
  (kernel-released on any exit); second serve fails fast in parent.
- `POST /submit` — parse YAML → validate → build tasks+runner via registries →
  probe machine assignee (unless assigneeIsAgent) → dup name → 409 → start
  poll goroutine (ctx-cancelled on shutdown).
- `POST /report` — above.
- `POST /shutdown` — idempotent (sync.Once), cancels poll loops.
- Stateless: restart = resubmit. No DB (tracker is the source of truth;
  in-memory sets are hints only).

## 7. Plugin (report-status.ts)

Unchanged logic; renames only: env `RELAY_*`, binary `relay`, log
`~/.relay/plugin.log`, STATUS values become `success`/`failure`, nudge text
mentions the new values. STATUS/SUMMARY block format and parser unchanged.

## 8. Execution plan (TDD; go build green every commit)

- **P0 — Deletion**: old config symbols (Workflow/AgentConfig/HandleSpec/
  OutcomesFor/StatusNamesFor/HandlesStatus/AllJiraStatuses/AgentForStatus/
  ShouldCloseTerminals/Workflows map), daemon internals (buildJQL/claimLabel/
  claimedByOtherWorkflow/ReportAcli/acli dispatch), cmdReport acli path, their
  tests. main.go keeps serve/submit-stub/stop/init. Repo is build-green but
  feature-dead until P6 — acceptable on a branch.
- **P1 — config v4** `internal/config/schema.go`: types + Validate (strict
  unknown fields; camelCase name; closeOn rules; edge targets exist; dup `when`
  rejected; agent nodes need both edges; agentless must be in closeOn).
- **P2 — interfaces + registries** (`internal/tasks`, `internal/runner`) +
  fakes for tests.
- **P3 — tasks/jira**: List (JQL assembly incl. issuetype/component/assignee
  clauses, when-reverse-map, ClaimedBy), Claim, Report (self-loop skip). Port
  existing daemon test cases.
- **P4 — runner/orca**: Spawn/Find/Nudge/Close over orcacli+opencode pkgs.
- **P5 — daemon v2** `daemon/loop.go`: New(cfg, tasks, runner, assignee,
  dryRun); PollOnce 3-way switch; dispatch/bounce; closeOn; nudged-set;
  templating.
- **P6 — server /report + CLI rewiring**: binary rename, paths, client.Report.
- **P7 — plugin renames; README/cli README rewrite (placeholders xyz/ABCD/
  Jane Doe only); .workflow/workflow.yaml new-schema demo; `go test ./...
  -race`; `go install ./...`; reindex GitNexus; live chain test.**

## 9. Go design patterns & principles

Patterns deliberately used:

- **Interface segregation (small ifaces)**: `Tasks` (3 methods) and `Runner`
  (4 methods) — consumers depend on the iface, not the adapter. Daemon never
  imports `tasks/jira` or `runner/orca` directly; main wires them via blank
  imports.
- **Registry / driver pattern** (like `database/sql`): adapters self-register
  in `init()`; `tasks.New(type, ...)` resolves. Extension = fork + import, no
  core edits. No plugin .so, no reflection hacks.
- **Factory pattern**: `Factory{UnmarshalConfig, New}` — each adapter owns its
  opaque config decoding (strict unmarshal), core stays field-agnostic.
- **Dependency injection**: `daemon.New(cfg, tasks, runner, ...)` — tests
  inject fakes; ProdDeps seam for probes. No globals, no init-time side
  effects outside registration.
- **Functional options rejected** (YAGNI — fixed ctor params suffice).
- **Idempotency & sync.Once**: server Shutdown idempotent; Claim/Report
  adapter ops safe to retry.
- **Context-driven lifecycle**: poll goroutines cancel via ctx on shutdown;
  no orphan tickers.
- **Error wrapping with %w**, sentinel-free; errors carry ticket/node context.
- **Table-driven tests** everywhere; fakes over mocks (hand-rolled structs
  implementing the ifaces — no mock libs).
- **Package hygiene**: `internal/` for everything non-importable; adapters in
  subpackages; no circular deps (core ← adapters, never reverse).

Principles enforced:

- **YAGNI**: no `run:` script nodes, no SQLite, no dynamic loading, no v3
  compat, no functional options, no multi-state nodes — all deferred or
  rejected until a real need shows up.
- **KISS**: one workflow per file; one node = one state; one query per poll;
  3-way ticket switch; report = thin socket client. If a design needs a
  paragraph to justify, it's cut.
- **Single source of truth**: tracker holds state; in-memory maps are hints
  only; labels are the cross-restart mutex.
- **Fail fast**: strict YAML validation at submit (unknown fields, dup `when`,
  missing edges) — errors surface before any goroutine starts.

## 10. Explicit non-goals

- No v3 migration/compat, no fallback loaders, no dead code.
- No SQLite/journal (stateless; tracker = truth). Revisit only if bounce
  proves painful in live use.
- No `run:` script nodes yet (terminal nodes just close) — future extension.
- No dynamic plugin loading for adapters (fork+import).
- Cross-machine same-repo polling: documented known limitation (label mutex
  makes it safe but only one machine wins).
