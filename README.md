# relay-flow

Durable, graph-based agent workflow runner. A ticket is the unit of work; a workflow YAML declares nodes and routes; a durable engine (go-workflows + SQLite) drives progression, waits, retries, and recovery. The task system (Jira and Beads built-ins) supplies parent tickets and mailbox subtasks; the runner (Orca built-in) owns worktrees and terminals; the harness (OpenCode and Pi built-ins) owns agent sessions and report semantics. All three are pluggable.

This is a ground-up rewrite. The previous per-workflow, in-memory daemon is gone. There is no migration path and no compatibility layer.

---

## Setup

### Prerequisites

| Tool | Why |
|---|---|
| [opencode](https://opencode.ai) | Agents run in opencode sessions (harness) |
| [Orca](https://github.com/Necmttn/orca) CLI + app | Worktrees + terminals (runner) |
| `bd` CLI | Beads task-system access (required when `taskPlugin: beads`) |
| Dolt | External/server-backed Beads only |
| Jira API token | Jira REST API v3 access (required when `taskPlugin: jira`) |
| Go 1.24+ | Build the CLI |

### Install

```sh
go install github.com/rajpopat27/relay-flow/cmd/relay-flow@latest
```

OpenCode plugin configuration uses both entrypoints. The server entrypoint is listed in `opencode.json`:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "plugin": ["relay-flow-plugin"]
}
```

The native HITL approval entrypoint is listed in `.opencode/tui.json`:

```json
{
  "$schema": "https://opencode.ai/tui.json",
  "plugin": ["relay-flow-plugin"]
}
```

The OpenCode harness adds both entries when a repo is registered. The server
entrypoint registers sessions, handles agent reports, and nudges invalid agent
output. The TUI entrypoint handles only HITL reports: after a valid completed
assistant report it shows a native Approve/Reject dialog. Approval delivers
`{runId, node, reportId, report}` via `relay-flow report` with retry; rejection
delivers nothing. `reportId` comes from the harness session/message identity;
`nodeVisitID` is internal and is never part of either plugin payload.

Pi plugin: install the same published package manually in Pi's global package
settings before starting a Pi harness session:

```sh
pi install npm:relay-flow-plugin@<version>
```

Relay-flow does not install or configure the package automatically. Pi resolves
`pi.ts` from the package's `pi.extensions` manifest entry, so do not add
`-e`/`--extension` to the relay-flow launch command. The runner launches Pi in
an interactive PTY; the package's OpenCode entry point remains available for
OpenCode sessions.

The plugin is the report-path half of the harness contract: it registers each
emitted harness session with `{runId, node, sessionId}`, parses the agent's
structured report, applies the agent/HITL nudge policy, and delivers
`{runId, node, reportId, report}` via `relay-flow report` with retry.
`reportId` comes from the harness session/message identity; `nodeVisitID` is
internal and is never part of either plugin payload.

### One-time machine setup

```sh
relay-flow init
relay-flow task auth
```

`init` only selects the task system, runner, and harness (singleton options are automatic), writes machine config, and initializes SQLite. `task auth` delegates authentication to that selected task plug-in. Jira prompts for its site, email, and masked API token, validates `/myself`, and owns the system-wide `credentials.yaml`; for scripts, pass `task auth --site`, `--email`, and `--token`. A normal init rerun refuses existing state. `relay-flow init --force` updates safe stopped instances while preserving durable and repo state.

For Beads, select the plugin explicitly when scripting setup:

```sh
relay-flow init --task-plugin beads --runner-plugin orca --harness-plugin opencode
```

Beads authentication is owned by the Beads workspace and its `bd`/Dolt configuration. `relay-flow task auth` is a no-op for Beads and does not create relay-flow credentials; do not add a Jira token to a Beads-only installation.

Pi is available through the existing harness selection. For a non-interactive
setup, select it with the existing flags:

```sh
relay-flow init --task-plugin jira --runner-plugin orca --harness-plugin pi
```

The full machine layout is fixed under `~/.relay-flow` (0700):

```
config.yaml           0600  machine config
credentials.yaml      0600  selected task plug-in credentials
state.db              0600  durable execution (SQLite)
server.sock           0600  CLI ↔ server
server.lock           0600  single-process flock
server.log            0600
plugin.log            0600
workflows/<name>.yaml 0644  submitted workflow definitions
```

### Register a repo

`repo register` and `workflow submit` are server-backed commands: start the
server first, otherwise they fail with `dial unix ~/.relay-flow/server.sock:
connect: no such file or directory`.

```sh
relay-flow serve --background
```

The repo must already exist in the runner. For the Orca runner, add it first
and keep the names aligned — the runner resolves a repo by path **and** display
name, so `--name` must equal the Orca display name (Orca derives it from the
directory name):

```sh
orca repo add --path /work/payments   # displayName becomes "payments"
```

```sh
relay-flow repo register
```

Shows a multi-select titled `Select repositories`; use Space to select Orca repos and Enter to confirm. Enter the Jira project once. Each repo is registered sequentially with its Orca name/path and a Jira component derived from that repo name. Earlier registrations remain if a later one fails.

Each Jira poll uses REST v3 enhanced search and requests linked-issue status with the candidate fields. Tickets with any unfinished inward `Blocks` issue are filtered before routing; no per-ticket blocker lookup is made.

For scripts, use `relay-flow repo register --name <name> --path <path> --set project=<project>`. Component is always derived from `--name` and cannot be overridden. Registration is rejected while another repo already holds the same canonical task scope.

### Beads task system

Beads uses the local `bd` CLI and supports both an embedded workspace and a workspace backed by an externally managed Dolt server. Install and verify the tools before registering a Beads repo:

```sh
bd --version
# Required only for server-backed workspaces:
dolt version
```

Initialize the Beads workspace before `repo register`; relay-flow never initializes a workspace, runs migrations, or starts `bd serve`.

For a local workspace, keep the workspace beside the code repository and use its `.beads` directory as `beadsDir`:

```sh
cd /work/payments
bd init --prefix payments
# code repository: /work/payments
# Beads workspace: /work/payments/.beads
```

For a server-backed workspace, an operator starts and manages Dolt separately, then initializes Beads with external-server metadata. The exact server host, port, credentials, and data directory belong to that Beads/Dolt deployment; relay-flow does not manage their lifecycle:

```sh
# Run this from the externally managed Beads/Dolt environment, not from relay-flow.
BEADS_DOLT_SERVER_TLS=0 \
BEADS_DOLT_PASSWORD= \
bd init \
  --server \
  --external \
  --server-host 127.0.0.1 \
  --server-port 13307 \
  --server-user root \
  --prefix payments \
  --non-interactive \
  --skip-hooks \
  --skip-agents
# Example external workspace: /var/lib/beads/payments/.beads
```

Register the code repository and its Beads workspace separately. `beadsDir` is required for every Beads repo, must name an existing directory, and is the task scope used to reject duplicate workspace registration:

```sh
# Local/embedded workspace
relay-flow repo register \
  --name payments \
  --path /work/payments \
  --set beadsDir=/work/payments/.beads

# External/server-backed workspace
relay-flow repo register \
  --name payments \
  --path /work/payments \
  --set beadsDir=/var/lib/beads/payments/.beads
```

The registered `--path` remains the code/runner repository. Every `bd` command runs with that path as its working directory and the configured `beadsDir` as `BEADS_DIR`, even when unrelated Beads selector variables exist in the relay-flow environment. Two repos may register different canonical `beadsDir` values; a second repo pointing at the same workspace is rejected. A Beads prefix such as `payments-...` is optional and only makes issue IDs recognizable—it is not a component, workspace selector, poller selector, or database isolation mechanism.

Beads workflow filters are structured and evaluated in relay-flow. For example, `examples/beads-workflow.yaml` uses the Beads status and issue-type fields:

```yaml
taskConfig:
  filters:
    parentStatuses: [open]
    issueTypes: [task]
    labels: [relay-ready]
```

Jira and Beads use the same conceptual `Task` issue type, but each adapter
keeps its provider-native spelling: Jira uses `Task` and Beads uses `task`.
Filter values are exact and are not translated between providers.

The supported Beads status names are `open`, `in_progress`, `blocked`, `deferred`, `hooked`, and `closed`. The claimed-parent poll uses the canonical active set `open,in_progress,blocked,deferred`; it intentionally does not substitute `hooked` for `deferred`. Omitted lifecycle settings move the parent to `in_progress` at `start`, a work-node mailbox to `in_progress`, and the parent to `closed` at `end` — the same shape as Jira, with Beads-native values. Relay-flow creates one Repo Poller per registered repo, not one poller per workflow. Each poll reads ready top-level parents and relay-owned active parents, deduplicates them, and never routes mailbox children. Claims are permanent `wf:<workflow>` labels.

Beads does not need relay-flow credentials or a Beads-specific poller. In server mode, leave Dolt and Beads server setup running outside relay-flow and point each repo at its own `beadsDir`.

### Submit a workflow

```sh
relay-flow workflow submit --file <path>
```

Workflows live at `~/.relay-flow/workflows/<name>.yaml` after submit. Replacement and removal are rejected while any run of that workflow is active.

Use [`examples/config-reference.yaml`](examples/config-reference.yaml) for the complete machine configuration, [`examples/workflow-reference.yaml`](examples/workflow-reference.yaml) for the complete workflow schema, or the provider-specific minimal workflows [`examples/minimal-jira-task-workflow.yaml`](examples/minimal-jira-task-workflow.yaml) and [`examples/minimal-beads-task-workflow.yaml`](examples/minimal-beads-task-workflow.yaml). Runtime node agents should follow [`docs/agent-instructions.md`](docs/agent-instructions.md). The existing [`examples/default-story-workflow.yaml`](examples/default-story-workflow.yaml) remains a more detailed Jira Story example, while [`examples/beads-workflow.yaml`](examples/beads-workflow.yaml) shows the Beads lifecycle shape. Replace the repo name and uncomment only the optional fields you need.

Task, runner, and harness plugins are selected machine-wide. A single relay-flow
configuration cannot run Jira and Beads simultaneously; use separate
`RELAY_FLOW_HOME` directories or machine configurations when both providers are
needed.

### Run

The server must already be running for `repo register` and `workflow submit`;
the same process polls repos and drives runs.

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
  filters:
    parentStatuses: [To Do]

nodes:
  start:
    taskConfig:                  # transitionTo belongs on a node: it applies to
      transitionTo:              # the lifecycle point the node represents
        parentStatus: In Progress
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

`taskConfig` may appear at root, repo, workflow, and node scopes. The adapter merges in that order: maps merge recursively, later scalar/list replaces, omitted keys inherit, explicit YAML `null` is rejected. The merged values decode against one adapter-owned typed config at use time. Adapter lifecycle defaults sit underneath all four scopes, so the effective precedence is `adapter default < root < repo < workflow < node`.

`transitionTo` describes one lifecycle point, so configure it on a node. A `transitionTo` set at root, repo, or workflow scope applies to **every** lifecycle point that reads it — including `end`, where a `parentStatus` other than the closing status stops the parent from being closed.

### Jira transition defaults

Omitted transitions default to:

- `start`: parent → `In Progress`
- work node: mailbox → `In Progress` (parent unchanged)
- `end`: parent → `Done`

### Beads transition defaults

Beads follows the same lifecycle shape with Beads-native values:

- `start`: parent → `in_progress`
- work node: mailbox → `in_progress` (parent unchanged)
- `end`: parent → `closed`

A parent moved to `in_progress` stays visible to the claimed-parent poll, which reads `open,in_progress,blocked,deferred`. Entering a node reuses that node's mailbox: a fresh mailbox moves `open → in_progress` and a revisited one moves `closed → in_progress`. Beads reads the issue before every status write, so an already-applied status is a no-op and a status relay-flow did not set (for example a human marking an issue `blocked`) blocks the transition and retries instead of being overwritten.

### Shared task configuration and provider status values

Beads and Jira use the shared `filters`, `templates`, optional top-level
`assignee`, and `transitionTo` field names. `transitionTo` uses
`parentStatus` for the parent issue and `taskStatus` for a mailbox. Both
adapters support the structured `filters.assignees` list. Jira values are
normalized account emails; Beads values are the provider's assignee strings.
Jira additionally supports the reserved `currentUser()` value inside
`filters.assignees`; relay-flow resolves it to the authenticated Jira email
without sending it as JQL. On first Jira authentication, when no root
`taskConfig.assignee` is configured, relay-flow stores the authenticated email
as the default assignee filter. In both adapters `assignee` is the default
assignee filter when `filters.assignees` is absent, and an `assignee` in effect
for a node also assigns that node's mailbox. Beads requires the repo-only
`taskConfig.beadsDir` shown above; Jira instead uses its repo `project` and
`component` keys. `project` and `component` are not Beads fields, and a Beads
issue prefix is not a component or workspace selector. Beads status values remain native (`open`,
`in_progress`, `blocked`, `deferred`, `hooked`, `closed`), while Jira values
remain native (`In Progress`, `Done`, and so on); relay-flow does not
translate arbitrary values between providers. Beads rejects workflow/node-level
template overrides because the fixed task text rendering contract has no
lower-scope input.

### Pi harness

Pi has one built-in coding agent. When the machine harness is `pi`, workflow
nodes use the logical agent label `default`; Pi keeps the user's configured
model, provider, tools, extensions, and settings. It does not interpret the
workflow agent as a model ID or pass an OpenCode-style `--agent` option.

For example, a Pi workflow node uses:

```yaml
type: agent
agent: default
```

Pi node launches use the installed Pi 0.84.1 interactive command contract:

```text
pi --name <ticket>:<node> [--session-id <persisted-session-id>] <prompt>
```

The prompt is one positional argument. Pi 0.84.1 rejects a bare `--`, so the
launch command does not include one. A persisted session uses
`--session-id`; print mode, JSON/RPC mode, and extension-install flags are not
used. The runner supplies a PTY for both standard streams, and the Pi process
remains available for interactive input after a response settles.

For a `hitl` node, a valid report is approved directly in Pi's host UI with
`ctx.ui.select`:

```text
Approve relay-flow report for <ticket>:<node>
  Approve
  Reject
```

Approve delivers the report. Reject or Escape submits nothing and leaves the
durable run waiting; Pi does not require an LLM Question tool for this step.

---

## Structured node report

Every visit (agent or HITL) submits one report with the same fixed labels:

```
STATUS: success | failure
NEXT STEP: <one configured route for that status>

SUMMARY:
COMPLETED: ...
COMMITS: <commit IDs or None>
NOT COMPLETED: ... | None
ISSUES DISCOVERED: ... | None
VERIFICATION: ...
NOTES: ... | None

FEEDBACK:
REASON FOR NEXT STEP: ...
REQUIRED ACTIONS: ...
RELEVANT CONTEXT: ...
EXPECTED RESULT: ...
```

The labels above are fixed; configurable templates do not change the parsed report contract. The plugin submits one `report` object containing both lower-camel `summary` and `feedback` objects. Relay-flow validates that complete shape once, renders `summaryReport` through the task system's summary-comment template on the current mailbox, and renders `feedbackReport` through its feedback-comment template on only the selected next mailbox. `None` is the literal marker for an intentionally empty section. When `NEXT STEP` is `end`, every FEEDBACK field must be `None` and no feedback comment is written.

The plugin delivers `{runId, node, reportId, report}` as one JSON object via `relay-flow report` stdin with the shared backoff (initial 2s, factor 2, jitter 0.2, max 5m) until acknowledged. It derives `reportId` from the harness session/message identity. Duplicate/stale reports are acked safely with no repeated graph effects. Invalid agent output is nudged; invalid or missing HITL output stays silent, while a valid HITL report opens the native TUI approval dialog. Relay-flow HITL approval does not use OpenCode's Question tool.

---

## Canceled run restart

Cancellation is permanent for the current execution. A canceled ticket is not
restarted by polling or by a ticket-status change. Start a fresh attempt
explicitly:

```sh
relay-flow run restart --ticket PAY-101
```

The new attempt starts at `start`, preserves the existing worktree/mailboxes/
comments/labels, and uses a numeric attempt ID (`2`, `3`, ...), with a fenced
execution ID such as `payments/basicFlow/PAY-101~attempt~2`. If a human has
moved the parent ticket to an incompatible status, `run get` shows `blocked`
with an instruction to move it to an allowed active start status; relay-flow
retries automatically and never overwrites the human-owned status. Done/Closed
tickets are not reopened automatically.

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
- Runtime registration is exactly `{runId, node, sessionId}`; normal execution persists that session ID and uses it to resume the harness session.
- Reports are exactly `{runId, node, reportId, report}`; `reportId` is derived from harness session/message identity and `nodeVisitID` stays internal.

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
