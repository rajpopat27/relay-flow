## Context

relay-flow currently runs one in-memory daemon per submitted workflow. Each daemon independently queries Jira, maps the parent ticket's Jira status to a workflow node, claims the ticket with a `wf:<name>` label, and asks the Orca runner to launch OpenCode. The OpenCode plugin parses a small `STATUS`/`SUMMARY` block and sends it back to the server, which transitions and comments on the same parent ticket.

That design is intentionally small, but it cannot support the target operating model:

- A machine may register roughly 100 repos, each with several workflows and many concurrent ticket runs.
- Multiple workflows may target the same repo, while one shared workflow may target many repos.
- Organizations must be able to replace the task system, runner, and agent harness without changing graph routing.
- Every node needs isolated context rather than sharing one increasingly noisy parent-ticket comment stream.
- Moving from one node to another requires several external task-system and runner calls that must recover after a crash.
- Agent and human-in-the-loop nodes may wait for long periods without consuming a worker or receiving unwanted nudges.
- Submitted workflows and active runs must survive server restarts.

This is therefore a core replacement, not an incremental extension of `internal/daemon`. Useful low-level seams remain: the Unix socket, process flock, ACLI wrapper, Orca CLI wrapper, strict YAML decoding, adapter registration, and fakeable CLI clients.

### Target System Shape

```text
stored workflow YAML + machine config
                |
                v
        registered Repo values
                |
       one Repo Poller per repo
                |
        active parent tickets
                |
        Ticket Router (in memory)
                |
      Run Manager: claim + EnsureRun
                |
       durable workflow instance
        /                     \
Workflow Worker          Activity Workers
graph + durable waits    task/runner/harness calls
        ^                     |
        |                     v
harness report ------> Unix socket / SQLite history
```

The task system remains the human-visible work surface and ownership record. SQLite stores durable execution progress. Workflow YAML files remain the desired definitions. None of those stores substitutes for the others.

### Simple Mental Model

The task system supplies a parent task, such as a Jira ticket, which is the unit of work moving through the workflow. Each workflow node is served by a ticket-scoped agent with its own mailbox/scratch space in a node subtask; the harness launches the agent, and the runner provides the terminal/environment that runs the harness, so terminal titles are scoped by ticket and node.

One generic Go durable-workflow interpreter executes the YAML graph and checkpoints every external action, avoiding a custom Saga implementation. Crash recovery always rolls forward through unfinished task-system, runner, and harness activities; relay-flow does not compensate by undoing completed human or agent work.

## Goals / Non-Goals

**Goals:**

- Route each active parent ticket to exactly one workflow using one poll per repo.
- Execute every claimed ticket as a durable, serial graph that survives process crashes.
- Isolate node context in reusable mailbox subtasks.
- Persist a deterministic agent result and one selected next node before applying external effects.
- Recover partial node transitions by rolling forward through idempotent activities.
- Support agent and HITL nodes without treating an idle human session as an error.
- Define narrow task-system, runner, harness, and durable-execution boundaries.
- Keep installation as one relay-flow server process with embedded SQLite.
- Keep commands and a future UI on one Unix-socket API.
- Preserve KISS: no event bus, DI framework, generic repository layer, parallel graph branches, or worker per ticket.

**Non-Goals:**

- Dynamically loading third-party Go plugins without rebuilding the binary.
- Running different task-system, runner, or harness plugin types per workflow.
- Supporting arbitrary JQL or task-system query languages in workflow YAML.
- Supporting parallel workflow branches; one node visit selects one next node.
- Providing manual pause/resume or compensation/rollback APIs.
- Making separate Jira, Linear, runner, and SQLite calls ACID-atomic.
- Maintaining concurrent workflow definition versions.
- Recovering a lost database automatically or inferring database loss from one missing run.
- Eliminating every possible duplicate external comment after an ambiguous network failure.

## Decisions

### 1. Replace the Per-Workflow Daemon

**Decision:** Replace `internal/daemon` with Repo Pollers, a pure Ticket Router, a Run Manager, and one generic durable workflow interpreter.

The existing daemon owns polling, matching, claiming, node state, spawning, nudging, and recovery in one object. Expanding it would retain the wrong unit of ownership: the workflow rather than the repo for polling, and process memory rather than durable history for execution.

**Alternatives rejected:**

| Alternative | Why rejected |
|---|---|
| Incrementally add subtasks and SQLite to `Daemon` | Keeps duplicate per-workflow polling, in-memory nudge state, and mixed responsibilities. |
| Create a dispatcher per workflow | Adds another long-lived object without solving duplicate repo polling. |
| Create one goroutine/worker per ticket | Thousands of waiting runs would consume unnecessary resources and complicate shutdown. |

### 2. Select Plugins Once Per Machine

**Decision:** Root config selects one task plugin, one runner plugin, and one harness plugin. Workflows reference repos and contain task-plugin configuration, but cannot override plugin types.

Each registered repo belongs to the selected task system and runner/harness environment. A single task plugin per machine keeps one Repo Poller contract and avoids conflicting interpretations of the same repo.

```yaml
pollIntervalSeconds: 15
completedRunRetentionDays: 30

taskPlugin: jira
taskConfig:
  assignee: relay-bot

runnerPlugin: orca
runnerConfig:
  baseRef: main

harnessPlugin: opencode
harnessConfig:
  defaultAgent: build
```

Task config may appear at root, repo, workflow, and node scope. The adapter merges those raw values in that order and validates them against one adapter-owned typed config. Raw values remain serializable in durable workflow inputs; adapter operations decode their effective values when called.

Merge semantics are fixed: maps merge recursively; a later scalar or list replaces the earlier value; omitted keys inherit; explicit YAML `null` is rejected. This small operation is implemented and tested locally rather than adding a merge library whose zero-value/slice semantics do not match the contract.

**Alternatives rejected:**

| Alternative | Why rejected |
|---|---|
| Plugin overrides in every workflow | Workflows sharing one Repo Poller could disagree about the task system for the same repo. |
| Separate config structs for root/repo/workflow/node | Duplicates models and conversion code for one plugin schema. |
| Typed config objects persisted in workflow history | Couples durable replay to concrete Go types and plugin versions. |
| Reflection-driven prompts and config scopes | Adds runtime metadata and testing complexity for a small set of required repo keys. |
| Repo-level runner/harness config | No current runner or harness requirement needs more than repo name/path plus root config. |

Task factories explicitly return required repo YAML keys such as `project` and `component`. `repo register` collects values for those keys without a separate prompt-description model.

`completedRunRetentionDays` is a machine-wide root setting that controls when completed or canceled durable histories and run-projection rows are removed. Starting, running, waiting, blocked, and canceling runs are never removed by retention cleanup. The permanent parent cancellation marker prevents a cleaned canceled run from being recreated.

### 3. Register Repos Separately From Initialization

**Decision:** `relay-flow init` is a one-time operation that selects plugin names, writes root config, and initializes SQLite. If relay-flow is already initialized, it refuses to overwrite configuration or execution history. `relay-flow repo register` discovers/selects a runner repo through a searchable `charmbracelet/huh` selection, assigns its stable name/path, collects required task config, validates connectivity, and stores the registration. Standard-library `flag` continues to parse commands and options; the TUI dependency is limited to interactive selection/forms.

Repos are added over time and should not make initialization a large interactive flow. Runner-internal IDs are resolved from the stored repo name/path and are never manually configured.

The task factory also derives an opaque canonical task-scope key from root and repo task config, such as Jira site/project/component. Two registered repos cannot use the same task scope; this preserves the one-to-one mapping between a repo and the task-system isolation it polls.

**Alternatives rejected:**

| Alternative | Why rejected |
|---|---|
| Register every repo during `init` | Makes initial setup large and forces re-running initialization for routine repo changes. |
| Ask for runner/workspace IDs | Exposes adapter internals and creates stale hand-maintained mappings. |
| Store runner config per repo | YAGNI; name and path are sufficient for current runners. |
| Register two repos for one task-system scope | Duplicates polling and allows two repo identities to claim the same parent tasks. |
| Build fuzzy search and keyboard navigation with `bufio` | Searchable selection across hundreds of repos is non-trivial terminal UI code; `huh` provides it behind a small registration boundary. |
| Use Cobra/Viper for command parsing/config | The command tree is fixed and standard `flag` remains sufficient; adding a framework would broaden dependencies without benefit. |

Repo removal is rejected while either a stored workflow references the repo or an active run uses it.

### 4. Workflows Declare Repos; Repos Hold a Derived Index

**Decision:** `Workflow.Repos` is the source of truth. Startup and workflow submission rebuild `Repo.Workflows`, a derived in-memory list of workflow references and compiled ticket matchers.

This supports both common usage patterns: a repo-local workflow listing one repo and a global workflow listing many repos. The derived index lets each Repo Poller route only against relevant workflows.

**Alternatives rejected:**

| Alternative | Why rejected |
|---|---|
| Make repo the persisted parent of workflows | Forces duplicate workflow submissions for global workflows. |
| Persist a separate repo-to-workflow mapping | Creates a second source of truth that can drift from workflow YAML. |
| Scan every workflow for every ticket | Simple but unnecessarily scales routing work with all machine workflows rather than repo workflows. |

### 5. Poll Once Per Repo

**Decision:** Create one lightweight Repo Poller timer per repo. A shared semaphore allows at most 10 task-system polls to execute concurrently. Every poller uses root `pollIntervalSeconds`, default 15.

The task system fetches active parent tickets for the repo. Mailbox subtasks are not returned as candidate runs. The Ticket Router receives the parent batch after each poll.

**Alternatives rejected:**

| Alternative | Why rejected |
|---|---|
| One global task-system poll | Produces very large responses, weak failure isolation, and awkward repo ownership. |
| One poll per workflow | Re-fetches the same repo tickets for every workflow; roughly 500 workflows would create excessive ACLI calls. |
| Poller implementation inside every task adapter | Duplicates timers, concurrency limits, shutdown, and retry mechanics across adapters. |
| Per-workflow or per-repo interval overrides | Adds policy complexity without a demonstrated need; polling is already repo-scoped. |

The adapter owns query construction. Jira scopes by project/component and fetches a fixed set of supported fields. Core owns the timer and concurrency boundary.

### 6. Use Structured Filters and In-Memory Routing

**Decision:** Workflows provide adapter-owned key-value filters. The task adapter compiles those filters into matchers over its normalized ticket fields. A repo poll fetches all active parent tickets once, then applies relevant workflow matchers in memory.

Routing order is deterministic:

```text
ticket has exactly one wf:<name>
  -> resolve that workflow for this repo; do not re-run filters

ticket has more than one wf:* label
  -> report invalid ownership; mutate nothing

ticket has no workflow claim
  -> zero matcher results: ignore
  -> one matcher result: claim, then start/ensure run
  -> multiple matcher results: report ambiguity; mutate nothing
```

**Alternatives rejected:**

| Alternative | Why rejected |
|---|---|
| Arbitrary JQL in workflow YAML | Cannot be reproduced reliably in memory and prevents one shared repo query. |
| Generate a union JQL from all workflow filters | Requires dynamic query generation and workflow tracking while still needing local ambiguity handling. |
| Query Jira again after the repo poll | Duplicates calls and defeats the shared-poller design. |
| First-match or priority routing | Hides ambiguous configuration and can claim a ticket for the wrong workflow. |

### 7. Keep `wf:<name>` Labels Alongside SQLite

**Decision:** Add the workflow label to the parent when claimed and to every mailbox subtask when ensured. Labels are never removed.

The label is the durable task-system assignment, cross-workflow mutex, audit marker, and claim-before-run recovery anchor. SQLite stores execution progress inside that assignment. Neither replaces the other.

**Alternatives rejected:**

| Alternative | Why rejected |
|---|---|
| Use SQLite assignment only | Other relay-flow instances/workflows and humans cannot see ownership; a claim-before-run crash loses routing. |
| Remove the label after completion | Loses audit and disaster-recovery identification. |
| Encode current node in another parent label | Moves the multi-call atomicity problem and accumulates presentation state in labels. |

### 8. Persist Workflow Definitions, But Do Not Version Them

**Decision:** Store workflow files under `~/.relay-flow/workflows/<name>.yaml`, parse them into in-memory structs, and reload them at startup. `workflow submit` creates or replaces a file atomically. Replacement and removal are rejected while any run of that workflow is active.

Each durable run receives an immutable value snapshot of the accepted workflow for deterministic replay. That snapshot is an execution requirement, not a user-managed workflow version.

**Alternatives rejected:**

| Alternative | Why rejected |
|---|---|
| Keep submitted workflows in server memory only | Restart requires manual resubmission and cannot resume runs reliably. |
| Support concurrent workflow versions | Renamed/deleted nodes and changed routes make active-run semantics difficult; no current need justifies it. |
| Let active runs read the latest file repeatedly | Breaks deterministic replay and can change routing mid-run. |

### 9. Use Reserved Lifecycle Nodes and Adapter-Owned Task Config

**Decision:** YAML contains reserved `start` and `end` lifecycle nodes plus agent/HITL work nodes. `start` and `end` do not get mailbox subtasks or sessions. The word `terminal` is reserved for runner terminals that host agents. Work nodes have an agent, description, task config, and success/failure routes.

`start.onSuccess` is the single entry edge after repo resolution, claim, durable-run creation, mailbox ensuring, and start task config succeed. Before processing that edge, the runner environment must be ensured and every referenced agent must have passed harness validation. `end` applies end task config and optionally cleans runner-owned resources.

Task config describes work for the selected task system. Jira may transition parent/subtask statuses; Linear may assign users or update fields. Core does not hardcode status semantics.

For the initial Jira adapter, omitted transition values default to parent `In Progress` at `start`, mailbox `In Progress` at work nodes, and parent `Done` at `end`. Work-node parent status remains unchanged when omitted.

```yaml
name: basicFlow
repos: [payments]
cleanupRunnerOnEnd: true

taskConfig:
  filters:
    parentStatuses: [To Do]
    issueTypes: [Task]
    labels: [coding]

nodes:
  start:
    taskConfig:
      transitionTo:
        parentStatus: In Progress
    onSuccess:
      - target: exploration

  exploration:
    type: agent
    agent: build
    description: Explore the ticket and existing code.
    taskConfig:
      transitionTo:
        taskStatus: In Progress
    onSuccess:
      - target: reviewing
        when: Exploration is complete
    onFailure:
      - target: exploration
        when: More exploration is required

  end:
    taskConfig:
      transitionTo:
        parentStatus: Done
```

**Alternatives rejected:**

| Alternative | Why rejected |
|---|---|
| Node-level `when` mapped directly to parent status | Couples the graph to Jira and prevents mailbox subtasks or non-status task systems. |
| `closeOn` as a separate set of arbitrary nodes | Reserved `end` plus `cleanupRunnerOnEnd` gives one explicit lifecycle exit. |
| Task config as Jira-specific top-level fields | Prevents Linear or another task adapter from defining different work semantics. |

### 10. Use Mailbox Subtasks for Node Context

**Decision:** At run start, call `EnsureMailboxes` for every agent/HITL node. It finds existing child mailboxes by workflow/node identity, creates only missing ones, ensures labels/descriptions, and returns the complete map. Revisits reuse the same mailbox.

Mailbox titles use the same stable ticket/node identity as runner terminals: `<ticket>:<node>`, for example `PAY-101:coding`. Parent-child relation plus this title identifies the mailbox without storing provider IDs in workflow state.

A mailbox is the description and comment section of an agent/HITL node's subtask. Its description contains the complete node instructions, report contract, legal routes, and HITL approval instructions when applicable. The current node's structured summary is written to its own subtask comments. Feedback is written to the selected next node's subtask comments. An agent reads only its mailbox plus explicitly requested parent context; the launch prompt only identifies the parent Jira ticket and current mailbox subtask.

`end` has no mailbox. When `NEXT STEP: end`, every feedback subsection is `None` and no feedback comment is written; the current node's summary remains in its own mailbox.

For human review, use the same review mailbox and configured review status. Reopen/reprocess it on a later visit. A separate PR-review subtask exists only when PR review is intentionally a separate graph node.

**Alternatives rejected:**

| Alternative | Why rejected |
|---|---|
| Put every agent comment on the parent | Pollutes prompts with unrelated node history and becomes difficult for humans to follow. |
| Create a new subtask on every visit | Produces mailbox sprawl and fragments one node's history. |
| Give `start`/`end` subtasks | They perform lifecycle orchestration, not agent work. |
| Let agents read every other mailbox | Defeats context isolation; cross-node context should be deliberate feedback. |

### 11. Require a Human-Readable Structured Report

**Decision:** Every completed agent/HITL node emits this contract:

```text
STATUS: success | failure
NEXT STEP: <one valid node name>

SUMMARY:
COMPLETED:
COMMITS:
NOT COMPLETED:
ISSUES DISCOVERED:
VERIFICATION:
NOTES:

FEEDBACK:
REASON FOR NEXT STEP:
REQUIRED ACTIONS:
RELEVANT CONTEXT:
EXPECTED RESULT:
```

Every section is present; `None` means intentionally empty. The mailbox description includes valid next steps and their `when` explanations. `NEXT STEP` must name one configured route for the reported status.

Summary documents the current node and includes relevant commit IDs or `None`. Feedback guides the selected next node. Feedback comments identify the source node and repeat those commit IDs. `nodeVisitID` is internal workflow metadata; the plugin uses the OpenCode session and assistant-message IDs as stable report identity.

The JSON wire format uses lower-camel keys: `runId`, `node`, `reportId`, `report`, `status`, `nextStep`, `summary`, and `feedback`, with lower-camel nested section keys.

**Alternatives rejected:**

| Alternative | Why rejected |
|---|---|
| `ROUTE`, `HANDOFF`, `VERDICT`, `DETAILS` | Implementation terminology was not sufficiently human-readable. |
| Only `STATUS` and one-line `SUMMARY` | Does not capture incomplete work, discovered issues, verification, or actionable next-node context. |
| Allow several selected targets | The engine is serial; one visit must make one deterministic transition. |
| Generate an idempotency UUID in the LLM response | LLM-generated identifiers are unstable and unnecessary. |

### 12. Treat HITL as Nudge Policy, Not Different Routing

**Decision:** Agent and HITL nodes declare the same success/failure route lists and emit the same report contract. The difference is idle behavior:

- `nudgePrompt` is optional custom node instruction text, rendered into every new node visit. It is appended to fresh/resumed launch prompts and to the short follow-up sent when a new visit reuses a live terminal. Same-visit retry/restart sends nothing.
- Agent node with invalid/missing output: the harness plugin sends its fixed correction containing the exact report contract.
- HITL node with invalid/missing output and no approval: the plugin remains silent.
- HITL node with valid output after an explicit `Approve` Question answer: report normally.
- HITL node with valid output but no approval: the plugin asks the assistant to present it through the Question tool with `Approve` and `Reject`.
- HITL node with invalid output after approval: the plugin asks the assistant to regenerate the complete valid report.
- A `Reject` answer or rejected Question does not map to workflow failure; the plugin submits nothing, clears authorization, and any later report requires a new Question and approval.

This allows a human to leave an idle session unattended without relay-flow pressuring the agent for a decision.

**Alternatives rejected:**

| Alternative | Why rejected |
|---|---|
| HITL nodes have no edges and may route anywhere | Removes graph validation and makes legal next steps unknowable to the agent/human. |
| Nudge every idle session | Breaks human-paced collaboration. |
| Create a separate task after human review | Unnecessary when review is one logical node and mailbox. |

### 13. Use an Embedded Durable Workflow Engine

**Decision:** Implement the durable-execution boundary with `github.com/cschleiden/go-workflows` pinned to `v1.4.2`, Go `1.24.6`, and its embedded `modernc.org/sqlite` backend. Register one generic `TicketWorkflow` interpreter, not one Go function per YAML workflow.

One deterministic workflow instance ID identifies each run: `repo/workflow/ticket`. Each node entry generates one opaque `nodeVisitID` through a replay-safe durable side effect. The value survives normal restart/replay, changes on every revisit, and changes when explicit database recovery creates a fresh run. Workflow history stores the visit ID, accepted report, selected next node, activity completion, timers, cancellation, and durable waits.

Before the main rewrite, run a compatibility spike covering SQLite restart, explicit instance IDs, duplicate start, separate workflow/activity workers, signals, durable timers, disconnected cancellation cleanup, and history inspection. If `v1.4.2` fails a required behavior, stop and revise the design rather than silently switching to a nightly release.

The SQLite backend uses WAL mode, a five-second busy timeout, and one open writer connection. The engine owns and initializes its internal workflow tables. relay-flow owns and initializes only its `relay_runs` projection in the same database. No adapter or harness plugin accesses SQLite directly.

**Alternatives rejected:**

| Alternative | Why rejected |
|---|---|
| No database; infer progress from comments/statuses and ask the LLM again | Requires many recovery branches, can regenerate different/truncated output, and becomes difficult to operate at scale. |
| Custom SQLite Saga/state machine | Feasible but relay-flow would own durable queues, timers, retries, leases, replay, cancellation, and observability. |
| Temporal SDK alone | The SDK requires Temporal Server or Cloud; it is not an embedded engine. |
| Launch Temporal dev server as a child process | Unsuitable for production and complicates packaging/lifecycle. |
| Self-host Temporal Server | Mature but excessive infrastructure for a self-contained local tool. The executor boundary allows migration later. |
| Floxy | Offers Saga, HITL, retries, and DLQ, but its SQLite backend is marked unstable, APIs are young, and compensation adds features not needed here. |
| Stateless (`qmuntal/stateless`) | Models transitions but has no durable queue, replay, activity retry, or crash recovery. |
| Dagu/other daemon workflow products | Add another service/process and do not fit the embedded typed Go execution boundary as closely. |

`go-workflows` does not make Jira calls exactly once. It makes ordered activity progress durable; adapters still need idempotent operations and tolerated duplicate comments.

### 14. Use Shared Bounded Workers, Not Workers Per Run

**Decision:** Create one Workflow Worker object with maximum 10 concurrent workflow tasks and one Activity Worker object with maximum 20 concurrent activities. Repo polling permits 10 concurrent polls. Waiting on an agent/HITL signal consumes no Activity Worker and no permanent relay-flow goroutine.

```text
Repo Poller
  -> router.ResolveWorkflow
  -> RunManager.EnsureRun
  -> durable workflow task queue
  -> Workflow Worker schedules typed activity
  -> Activity Worker performs external call
```

**Alternatives rejected:**

| Alternative | Why rejected |
|---|---|
| Worker/goroutine per ticket | Hundreds or thousands of waiting runs waste memory and complicate lifecycle. |
| Unbounded goroutines for ticket dispatch | Can overload Jira, Orca, SQLite, and the machine. |
| One queue/worker pool per workflow | Reintroduces workflow-level operational objects and scales with definitions rather than machine capacity. |

Repeated `EnsureRun` for an existing active run checks the current ticket/node terminal by its stable title. It sends a durable reconcile signal only when that terminal is missing or unusable. While waiting for a report, the workflow also accepts that signal and relaunches the same visit with the same `nodeVisitID`. This covers both server restart and later terminal death without periodic workflow timers or signal spam. HITL reconciliation restores a missing session but never nudges an idle live session.

### 15. Persist the Route Before Ordered External Activities

**Decision:** A node transition is not an external ACID transaction. The workflow first records the validated report and selected next node in durable workflow history, then performs ordered, separately checkpointed activities:

```text
persist report + selected next node
  -> write SUMMARY to current mailbox
  -> write FEEDBACK to next mailbox
  -> complete current mailbox through the task plugin
  -> apply processing taskConfig to next mailbox/parent
  -> ensure/relaunch next harness session
```

After a crash, replay skips completed activities and retries unfinished ones. Task status updates use read-before-write or provider idempotency. Runner launch uses find-before-create. Relay-flow comments include a stable marker based on node visit and comment type; adapters check the marker before posting. Rare duplicates after an ambiguous provider failure remain acceptable.

`CompleteMailbox` is deliberately narrow: it only applies the task system's completed state to the current mailbox, such as Jira `Done`. Summary, feedback, route selection, next-node task configuration, and runner launch remain separate durable activities.

**Alternatives rejected:**

| Alternative | Why rejected |
|---|---|
| Treat four Jira calls as one transaction | Jira exposes no cross-issue transaction; no Go library can create one. |
| Parent current-node label/custom field | Records only a cursor and still leaves comments/status/session calls non-atomic. |
| Use `In Review` as an internal handoff protocol state | Status alone does not record the selected target and abuses business-visible statuses. HITL may still configure `In Review` for actual review work. |
| Parse a transition journal from Jira comments | Recreates a durable state machine in human comments and requires repeated parsing/reconciliation. |
| Roll back on any failure | Deleting comments, reopening tasks, and closing sessions can destroy human or agent work; compensation can fail too. |

Recovery always rolls forward. No rollback compensation is implemented.

### 16. Use One Backoff Policy Across Execution Environments

**Decision:** Define `BackoffPolicy` with an initial 2 seconds, factor 2, 20 percent jitter, and 5 minute maximum. Calculation is shared in Go; each environment provides only its waiting mechanism:

- Repo Pollers use Go timers.
- Durable activity retries use workflow timers and replay-safe jitter.
- TypeScript report retries mirror the constants and use `setTimeout`.

Startup validates known permanent configuration, credential, permission, connectivity, repo, and agent errors before workers/pollers begin. Existing runtime failures retry indefinitely because dependencies and credentials may recover. The run projection exposes the current error.

Manual task-system conflicts mark the run blocked, retry with backoff, and continue automatically when external state becomes compatible. There is no manual resume command.

**Alternatives rejected:**

| Alternative | Why rejected |
|---|---|
| Different policies for Jira, runner, reports, and polling | Drifts behavior and multiplies tests without evidence that integrations need distinct schedules. |
| Stop after a fixed attempt count | Turns temporary outages into permanently stranded runs and requires a resume API. |
| Retry invalid startup configuration forever | Starts a system known to be unusable and hides actionable setup errors. |
| One generic untyped durable retry helper | Loses type safety through `activity any`; private typed loops are clearer. |

### 17. Deliver Reports as JSON and Acknowledge After Persistence

**Decision:** The runtime harness plugin parses the complete assistant contract and calls `relay-flow report` with JSON over stdin. The command forwards JSON over the Unix socket. Multiline fields never become shell arguments.

The server validates the payload and signals the durable run. It acknowledges only after the signal is persisted in SQLite. The plugin retries the exact parsed output quietly until acknowledgement.

Deduplication is deliberately simple:

- The plugin derives `reportId` from stable OpenCode session and assistant-message IDs and permits only one unacknowledged report per run/node.
- Before graph transition effects, the workflow records each consumed `reportId`; the SQLite receipt stores only that ID and its exact internal visit.
- If an ID is already processed, the server immediately returns an accepted duplicate acknowledgement without validating or inspecting the payload.
- A same-ID signal racing before the receipt update is ignored by replay-safe workflow state.
- The plugin never reads SQLite and never receives or updates `nodeVisitID`.

After a plugin restart, the harness may reread the valid assistant message and submit it again. The processed ID is immediately ignored.

**Alternatives rejected:**

| Alternative | Why rejected |
|---|---|
| Pass summary/feedback as CLI flags | Multiline text and shell quoting are fragile. |
| One JSONL report outbox | Requires concurrent locking, fsync, indexing/scanning, cleanup, and compaction beside SQLite. |
| One JSON file per report | Creates avoidable file churn for hundreds or thousands of sessions. |
| Let plugins write SQLite | Couples every harness to the engine schema and creates cross-process ownership/locking. |
| Server fetches a harness message by message ID | Forces core to implement query logic for every harness. |
| Nudge the LLM to regenerate output after server failure | The LLM may shorten or change feedback; retry the exact original output. |

### 18. Separate Runner and Harness Responsibilities

**Decision:** The runner owns execution environments, terminals/processes, liveness, and cleanup. It exposes separate operations to close terminals while preserving the environment and to clean the complete run environment. The harness owns agent validation, prior agent-session lookup, resume syntax, and command construction. The runtime harness plugin owns message parsing, title pinning where supported, nudge behavior, and report retry.

The harness returns a structured executable/args/env command; the runner executes it safely. Orca does not construct OpenCode commands, and OpenCode does not manipulate Orca worktrees.

One run uses one ticket-scoped runner environment, such as one Orca worktree, shared by all node agents so sequential work builds on the same files. Nodes have separate terminals and harness sessions inside that environment. Initial and revisit prompts identify the parent Jira ticket and exact mailbox subtask; the mailbox description is the complete instruction source.

Terminal/session title contains only the ticket key and node name: `<ticket>:<node>`, for example `PAY-101:coding`. It never contains `nodeVisitID`, workflow name, agent name, or other changing metadata. `nodeVisitID` remains internal. On a revisit, the retained terminal receives only the new prompt; the mailbox remains the correctness context.

Every node visit checkpoints prior-session lookup, closes the old node terminal if present, then starts a terminal with the stable title and current visit environment. If start acknowledgement is lost, retry uses the already-checkpointed close followed by find-before-create, so it does not close the newly started terminal. Cancellation and database recovery close terminals only and preserve the workspace/code; `cleanupRunnerOnEnd` performs full run-environment cleanup.

**Alternatives rejected:**

| Alternative | Why rejected |
|---|---|
| Keep OpenCode command building inside Orca runner | Every new harness would require runner changes. |
| Put `nodeVisitID` in the terminal title | Causes title churn and complicates stable lookup on revisits/restarts. |
| Use title as the only report identity | Cannot distinguish repeated visits to the same node. |
| Persist runner/harness IDs in workflow YAML | Exposes local adapter internals and becomes stale across machines. |

### 19. Keep Visit IDs Internal and Use Harness Report IDs

**Decision:** `runID` is deterministic from repo/workflow/ticket. `nodeVisitID` is generated once as a durable replay-safe side effect for each node entry and remains internal. The plugin derives `reportId` as `<sessionID>:<assistantMessageID>`.

The server injects `runID` and stable node metadata when launching the harness. The LLM sees only its human-readable task and output contract. The plugin sends the harness message ID as transport idempotency metadata.

Every harness launch receives `RELAY_FLOW_RUN_ID`, `RELAY_FLOW_WORKFLOW`, `RELAY_FLOW_REPO`, `RELAY_FLOW_TICKET`, `RELAY_FLOW_NODE`, `RELAY_FLOW_NODE_TYPE`, `RELAY_FLOW_NUDGE_PROMPT`, and `RELAY_FLOW_NEXT_STEPS_JSON`. Legal next steps and explanations are encoded in the final JSON value.

**Alternatives rejected:**

| Alternative | Why rejected |
|---|---|
| Random report ID generated by the plugin | Adds another persistence problem and is not stable across plugin restart. |
| Derive visit ID only from run/node/sequence | Explicit database recovery restarts the sequence and could collide with stale pre-recovery reports. |
| Infer visits from terminal idle state | Idle is normal for HITL and does not prove whether a report was persisted. |

### 20. Use a Small Derived Run Projection

**Decision:** `go-workflows` history is authoritative for graph progression, report acceptance, routes, and activity completion. Add one `relay_runs` table in the same SQLite database as a derived query projection for run list/get, ticket-to-run lookup, active-workflow/repo checks, current node/visit display, error display, and cancellation lookup.

The projection does not store selected-route authority. Projection updates are idempotent durable activities, so replay repairs an interrupted update. Command and query contracts remain separate: `Executor` handles ensure/report/cancel; `RunQueries` handles reads.

Schema initialization is explicit and versioned for relay-flow-owned tables, but there is no legacy data conversion. This rewrite starts with the new schema; old runtime state is not imported.

**Alternatives rejected:**

| Alternative | Why rejected |
|---|---|
| Query and replay full engine history for every CLI/API read | Slow, engine-specific, and awkward for active-run checks. |
| Make `relay_runs` a second workflow-state authority | Creates consistency and recovery conflicts with durable history. |
| Add a generic repository/ORM abstraction | One projection table does not justify it. |
| Put commands and queries into one large interface | Forces execution mocks to implement unrelated query behavior and couples projection changes to command callers. |

### 21. Cancellation Stops; It Does Not Roll Back

**Decision:** `relay-flow run cancel --ticket <key>` resolves the active run, cancels its durable context, stops scheduling normal activities, waits for any already-running activity to return, closes active runner terminals while preserving the workspace/code, and posts one parent cancellation comment using a stable `runID:cancellation` marker.

Mailbox history and task statuses remain unchanged. Rare duplicate cancellation comments are accepted after ambiguous failures.

**Alternatives rejected:**

| Alternative | Why rejected |
|---|---|
| Reopen previous node and reset next node | Cancellation only means stop; rollback semantics were not requested. |
| Delete comments or work | Destroys audit history and possibly human/agent contributions. |
| Force-interrupt an activity | `go-workflows` cannot cancel an already-running activity safely. |
| Add `--force` and operator route choices | Premature complexity without a demonstrated use case. |

### 22. Distinguish Normal Recovery From Database-Loss Recovery

**Decision:** `relay-flow init` initializes SQLite. Normal `serve` requires a valid existing database and refuses to start if it is missing or unusable.

With a healthy database, a labeled ticket whose deterministic run is missing means the process crashed after claim but before run creation. `EnsureRun` safely creates that run.

Before creating a missing claimed run, the Run Manager checks the stable cancellation marker. A canceled ticket is ignored even after its durable history has aged out.

Database loss is never inferred from a missing run. `serve --recover` is an explicit destructive mode:

```text
create fresh execution state
  -> load machine config and workflow files
  -> poll active labeled parents (parents completed through end are excluded)
  -> close stale agent terminals while preserving workspace/code
  -> EnsureMailboxes (find existing; create missing)
  -> reset mailbox tasks to To Do
  -> create fresh durable runs
  -> execute each reserved start node
```

Comments, labels, worktrees, and code are preserved. Repeated LLM work and occasional duplicate comments are accepted. Recovery never runs automatically.

All previous SQLite execution history is treated as unknown. Recovery does not resume an old node, visit, report, route, timer, or activity. It creates a new durable run using the deterministic repo/workflow/ticket run ID, which naturally equals the prior logical ID, and generates fresh random durable node visit IDs before processing from `start`.

Parents carrying the stable `<runID>:cancellation` comment marker are not restarted during database-loss recovery. The marker is the task-system recovery record for cancellation after SQLite state is lost.

**Alternatives rejected:**

| Alternative | Why rejected |
|---|---|
| Infer database loss whenever a run ID is absent | Confuses normal claim-before-run recovery with catastrophic state loss. |
| Reconstruct exact engine progress from Jira | Requires machine-readable transition journals and recreates the complexity SQLite was chosen to own. |
| Automatically restart from current subtask | Can skip or duplicate partially applied transitions without operator awareness. |
| Automatically restart every labeled run on normal startup | Repeats work after every server restart. |

### 23. Expose One Unix-Socket API for CLI and Future UI

**Decision:** Keep HTTP-over-Unix-socket as the trusted same-user server API. CLI commands and a future UI call the same workflow, repo, run, report, and stop handlers.

```text
relay-flow init
relay-flow serve [--recover]
relay-flow stop
relay-flow report

relay-flow workflow submit|remove|list|get
relay-flow repo register|remove|list|get
relay-flow run list|get|cancel
```

`workflow submit` means create or replace, with replacement blocked by active runs. No separate update command exists.

Every API response uses JSON. Success responses contain `{"ok":true,"data":...}` and errors contain `{"ok":false,"error":{"code":"<lowerCamel>","message":"..."}}`. Malformed requests return HTTP 400, missing resources 404, state conflicts 409, wrong methods 405, and unexpected server failures 500. CLI commands exit 0 on success, 2 for command/flag usage errors, and 1 for server, validation, or operation failures.

Shutdown stops accepting requests and new polls immediately, cancels worker polling, waits up to 30 seconds for running calls, then closes the socket and database. Durable unfinished work resumes on the next normal start.

Run claiming/creation and workflow replacement/removal share one lifecycle gate. Run creation holds the gate from final workflow resolution through claim and durable `EnsureRun`; replacement/removal holds it while checking active runs and swapping disk/in-memory definitions. This prevents a run from starting with an old definition while that definition is replaced.

**Alternatives rejected:**

| Alternative | Why rejected |
|---|---|
| Separate CLI implementation paths | Causes behavior and validation drift from a future UI. |
| Expose network authentication now | Socket is same-user/local by design; remote service operation is out of scope. |
| Add pause/resume and manual reconciliation commands | Runtime retry and blocked-state reconciliation are automatic; no present requirement needs them. |

### 24. Package by Responsibility and Keep Interfaces Narrow

**Decision:** Use concrete services by default and interfaces only for replaceable plugin/executor boundaries or a consumer's small query need.

```text
internal/config                  stored machine/raw config and I/O
internal/workflow                workflow schema, validation, storage
internal/repo                    registrations and Repo Pollers
internal/router                  pure workflow resolution
internal/run                     Run Manager and executor/query contracts
internal/execution/goworkflows   engine adapter, activities, projection
internal/task/jira               Jira adapter and nested ACLI wrapper
internal/runner/orca             Orca adapter and nested Orca CLI wrapper
internal/harness/opencode        OpenCode harness
internal/retry                   shared backoff calculation/classification
```

No `common`, event-bus, DI-container, generic store, or generic plugin-loading framework is added.

Filesystem layout is fixed under owner-only `~/.relay-flow` (`0700`): `config.yaml`, `state.db`, `server.sock`, `server.lock`, `server.log`, `plugin.log`, and `workflows/<name>.yaml`. Config, database, lock, and log files use `0600`; workflow files use `0644`; the Unix socket uses `0600`. Config and workflow replacement uses `github.com/google/renameio/v2`. No merge, CLI-framework, ORM, validation, UUID, DI, or additional state-machine library is added.

**Alternatives rejected:**

| Alternative | Why rejected |
|---|---|
| Generic repository pattern | Workflow files and one SQLite projection have different simple needs. |
| Generic event bus between poller/router/manager | Direct function calls are clearer and easier to test. |
| One giant task/runner/harness orchestration interface | Leaks relay-flow lifecycle into adapters and makes replacements difficult. |
| Runtime external Go plugin loading | ABI, distribution, and security complexity can be revisited after contracts stabilize. |

## Risks / Trade-offs

- **[go-workflows compatibility]** Pinned `v1.4.2` may not satisfy all assumed signal, cancellation, SQLite, or worker behavior. -> Complete the compatibility spike before the main rewrite and revise the design if it fails.
- **[Go toolchain upgrade]** `go-workflows v1.4.2` requires Go `1.24.6`, newer than the current module. -> Upgrade and verify release packaging during the compatibility task.
- **[SQLite write contention]** Workflow history, activities, and run projection share one SQLite writer. -> Use WAL/busy timeout, bounded workers, and load tests at expected run counts; Jira/runner latency should remain the dominant cost.
- **[External exactly-once is impossible]** A provider may apply a comment/status/session call and time out before acknowledgement. -> Use read-before-write, stable comment markers, find-before-create, and accept rare duplicate comments.
- **[Infinite retries can hide stuck runs]** Runtime failures intentionally remain active. -> Expose state and last error through `run list/get`; validate known permanent failures before starting.
- **[Manual task-system edits]** Humans may move tasks during an activity. -> Mark the run blocked, avoid blind overwrite, retry with backoff, and continue when compatible.
- **[Workflow history growth]** Terminal histories consume disk over time. -> Remove completed/canceled durable histories and projection rows after the configured `completedRunRetentionDays` period; permanent task-system markers retain assignment/cancellation meaning.
- **[Database loss repeats work]** Explicit recovery starts runs from `start`. -> Require `--recover`, preserve code/mailboxes, close stale sessions, and clearly warn about repeated LLM work.
- **[One task plugin per machine]** A single server cannot mix Jira and Linear repos initially. -> This is an intentional KISS constraint; revisit only with a concrete multi-task-system deployment.
- **[Adapter config schema changes]** Raw durable values may outlive plugin code changes. -> Block workflow replacement during active runs, preserve backwards-compatible decoding within a deployment, and test replay before upgrades.
- **[Breaking migration]** Existing workflow files and daemon behavior are incompatible. -> Treat this as a clean architecture migration, not silent compatibility code.

## Migration Plan

1. Upgrade to Go `1.24.6`, pin `go-workflows v1.4.2`, and complete its compatibility spike.
2. Add paths, machine config, workflow schema/storage, identity, retry, and strict validation foundations.
3. Define task, runner, harness, executor, and query contracts; move ACLI, Orca CLI, and OpenCode logic under their adapters.
4. Add repo registration, repo-bound task systems, derived workflow bindings, Repo Pollers, and in-memory routing.
5. Add SQLite engine startup, generic ticket workflow, bounded workers, run projection, report signaling, and cancellation.
6. Implement Jira parent polling, claims, mailbox subtasks, task-config application, comments, markers, and recovery reset.
7. Implement Orca environment/session behavior and OpenCode launch/report/HITL behavior with stable run/node metadata and assistant-message report IDs.
8. Add workflow/repo/run/report Unix-socket APIs and CLI commands.
9. Add crash-boundary, duplicate-report, ambiguous-comment, blocked-state, cancellation, restart, and database-loss recovery tests.
10. Remove the old daemon/config/report execution paths and publish clean-replacement instructions for the new root/workflow YAML.

There is no legacy data migration or conversion tooling. SQLite schema initialization creates the new engine and relay-flow-owned tables. Rollback is operational rather than transactional: stop the new server, preserve/backup `~/.relay-flow`, and restore the previous binary/config if necessary. Jira labels, mailbox subtasks, comments, and work performed by agents are not automatically deleted or reversed.
