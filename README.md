# relay-flow

Durable, graph-based agent workflow runner. A ticket is the unit of work; a workflow YAML declares nodes and routes; a durable engine (go-workflows + SQLite) drives progression, waits, retries, and recovery. The task system (Jira built-in) supplies parent tickets and mailbox subtasks; the runner (Orca built-in) owns worktrees and terminals; the harness (OpenCode built-in) owns agent sessions and report semantics. All three are pluggable.

This is a ground-up rewrite. The previous per-workflow, in-memory daemon is gone. There is no migration path and no compatibility layer.

---

## Setup

### Prerequisites

| Tool | Why |
|---|---|
| [opencode](https://opencode.ai) | Agents run in opencode sessions (harness) |
| [Orca](https://github.com/Necmttn/orca) CLI + app | Worktrees + terminals (runner) |
| [acli](https://developer.atlassian.com/cloud/acli) | Jira access (task system) |
| Go 1.24+ | Build the CLI |

### Install

```sh
go install github.com/rajpopat27/relay-flow/cmd/relay-flow@latest
```

OpenCode plugin: add `"relay-flow-plugin"` to the `plugin` array in your repo's `opencode.json`:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "plugin": ["relay-flow-plugin"]
}
```

The plugin is the report-path half of the harness contract: it parses the agent's structured report, applies the agent/HITL nudge policy, and delivers `{runId, node, reportId, report}` via `relay-flow report` with retry.

### One-time machine setup

```sh
relay-flow init
```

Selects the task system, runner, and harness (singleton options are automatic), prints the selections, atomically writes `~/.relay-flow/config.yaml` (0600), and initializes `~/.relay-flow/state.db`. A normal rerun refuses existing state. `relay-flow init --force` updates plugin selections only when the server is stopped and all runs are terminal; it preserves the database, completed history, workflows, logs, repos, and other machine settings.

The full machine layout is fixed under `~/.relay-flow` (0700):

```
config.yaml           0600  machine config
state.db              0600  durable execution (SQLite)
server.sock           0600  CLI ↔ server
server.lock           0600  single-process flock
server.log            0600
plugin.log            0600
workflows/<name>.yaml 0644  submitted workflow definitions
```

### Register a repo

```sh
relay-flow repo register
```

Shows a multi-select titled `Select repositories`; use Space to select Orca repos and Enter to confirm. Enter the Jira project once. Each repo is registered sequentially with its Orca name/path and a Jira component derived from that repo name. Earlier registrations remain if a later one fails.

For scripts, use `relay-flow repo register --name <name> --path <path> --set project=<project>`. Component is always derived from `--name` and cannot be overridden. Registration is rejected while another repo already holds the same canonical task scope.

### Submit a workflow

```sh
relay-flow workflow submit --file <path>
```

Workflows live at `~/.relay-flow/workflows/<name>.yaml` after submit. Replacement and removal are rejected while any run of that workflow is active.

Use [`examples/default-story-workflow.yaml`](examples/default-story-workflow.yaml) as a fully annotated starting point. Replace its repo name and uncomment only the optional fields you need.

### Run

```sh
relay-flow serve              # normal start; requires an initialized database
relay-flow serve --background # detached; returns after the server is ready
relay-flow serve --recover    # explicit destructive rebuild from the task system
relay-flow stop
```

`--background` preserves `--debug` and `--recover`, logs to `~/.relay-flow/server.log`, and remains stoppable with `relay-flow stop`. Plain `serve` remains foreground and blocking.

`serve --recover` treats ALL SQLite execution state as gone, closes surviving run-owned terminals (preserving worktrees and code), resets Jira parent+mailbox state, and starts every labeled parent in a fresh deterministic run from `start` with fresh `nodeVisitID`s. Recovery never runs automatically; database loss is never inferred.

---

## Workflow YAML

```yaml
name: basicFlow                  # lowerCamel; determines claim label wf:basicFlow
repos: [payments]                # one or more registered repos, unique
cleanupRunnerOnEnd: false        # optional; when true the runner tears down at end

taskConfig:                      # optional; adapter-owned; merged root → repo → workflow → node
  transitions:
    start:  { parent: "In Progress" }
    work:   { mailbox: "In Progress" }
    end:    { parent: "Done" }

nodes:
  start:
    onSuccess: [{ target: coding }]

  coding:
    type: agent                  # or hitl
    agent: build                 # opencode agent
    description: |               # becomes the mailbox description and launch prompt
      Implement the ticket.
    onSuccess: [{ target: reviewing, when: "work complete" }]
    onFailure: [{ target: coding,   when: "retry" }]
    nudgePrompt: "Check edge cases for {{ticket}} before reporting." # optional custom instructions

  reviewing:
    type: hitl
    agent: build
    description: Human review.
    onSuccess: [{ target: end }]
    onFailure: [{ target: coding }]

  end: {}
```

Rules enforced at submit:

- `start` and `end` are reserved lifecycle nodes. `start` has exactly one success target and no type/agent/description/routes on failure. `end` has no type/agent/description/routes.
- Every other node is `agent` or `hitl`, has an agent and a description, and declares at least one valid route for every permitted outcome.
- Routes are single-target; no route may target `start`.
- The graph must be fully reachable from `start`; unknown fields are rejected; `runnerPlugin`/`harnessPlugin`/`closeOn`/legacy `tasks`/`runner` keys are rejected.
- Only `agent` and `hitl` nodes receive mailbox subtasks; `start` and `end` never do.
- `cleanupRunnerOnEnd` is the only workflow cleanup knob and takes priority over terminal retention after `end`; the word `terminal` refers to runner terminals only.

### Task-config merge

`taskConfig` may appear at root, repo, workflow, and node scopes. The adapter merges in that order: maps merge recursively, later scalar/list replaces, omitted keys inherit, explicit YAML `null` is rejected. The merged values decode against one adapter-owned typed config at use time.

### Jira transition defaults

Omitted transitions default to:

- `start`: parent → `In Progress`
- work node: mailbox → `In Progress` (parent unchanged)
- `end`: parent → `Done`

---

## Structured node report

Every visit (agent or HITL) ends with the same contract:

```
STATUS: success | failure
NEXT STEP: <one configured route for that status>

SUMMARY
- Completed: ...
- Not completed: ... | None
- Issues discovered: ... | None
- Verification: ...
- Notes: ... | None

FEEDBACK
- Reason for next step: ...
- Required actions: ...
- Relevant context: ...
- Expected result: ...
```

`None` is the literal marker for an intentionally empty section. When `NEXT STEP` is `end`, every FEEDBACK field must be `None` and no feedback comment is written.

The plugin delivers the report as one JSON object via `relay-flow report` stdin with the shared backoff (initial 2s, factor 2, jitter 0.2, max 5m) until acknowledged. Duplicate/stale reports are acked safely with no repeated graph effects. Invalid agent output is nudged; invalid HITL output stays silent.

---

## Architecture

- **Task system** owns parent tickets, mailbox subtasks, task state, labels, comments, and adapter config. The parent ticket is the unit of work.
- **Durable workflow engine** (go-workflows + SQLite) owns graph progression, waits, reports, retries, and recovery. No custom state machine.
- **Mailbox subtask** is one agent/HITL node's scratch space; its description defines the node's work and its comments hold the node's summary plus selected incoming feedback.
- **Harness** owns agent launch, session/report behavior, parsing, nudging, and resume semantics.
- **Runner** owns ticket worktrees/environments, terminals, liveness, and execution of harness commands.
- **Compensation/rollback never exists.** Recovery always rolls forward through idempotent activities.

### Identity

- `runID` is deterministic from `repo/workflow/ticket`.
- `nodeVisitID` is generated once per node entry as a durable replay-safe side effect; it changes on revisit and on fresh runs after `--recover`.
- Terminal titles are stable `<ticket>:<node>` — they never carry `nodeVisitID`, workflow, or agent.
- Report wire keys are `runId` / `node` / `reportId`; `nodeVisitID` stays internal.

### Poll cycle

One Repo Poller per registered repo (not per workflow) fetches active parent tickets on the configured `pollIntervalSeconds` (default 15). Per ticket, the Ticket Router resolves at most one workflow: multiple `wf:*` claims → `InvalidClaimError`; exactly one claim resolves directly; zero filter matches → `ErrNoMatch`; one match → that workflow; multiple matches → `AmbiguousError` with no mutation. Successful resolution goes to the Run Manager; the run is claimed with `wf:<name>` before `EnsureRun` fires.

### Shutdown and recovery

- Graceful shutdown stops accepting requests and new polls immediately, cancels worker polling, waits up to 30s for running calls, then closes the socket and database. Durable unfinished work resumes on the next normal start.
- Completed/canceled runs are removed after `completedRunRetentionDays` (default 30). The retention sweep runs once at startup, never on a ticker.

---

## Development

```sh
go test ./...
cd plugin && bun test
```
