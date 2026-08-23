# Feature Tracker

## Integration Contracts

**Feature:** Define clear contracts for task systems, runners, and agent harnesses.

**Justification:** Organizations must be able to use their own task systems, execution environments, and agent harnesses without changing relay-flow's core logic.

## Repo-Scoped Polling

**Feature:** Workflows declare their repos and ticket filters. Rename existing `project` terminology and fields to `repo`.

**At startup:** Load stored workflows, group them by repo, and create one shared poller per repo.

**At each poll:** Fetch active parent tickets for the repo once; mailbox subtasks are not routed as runs. Honor exactly one `wf:*` claim first, reject parents carrying multiple workflow claims, then apply workflow filters in memory for unclaimed parents; ignore zero matches and reject multiple matches. The task system discovers existing mailbox subtasks only through `EnsureMailboxes(parent, workflow, nodeSpecs)`.

**Justification:** This avoids duplicate Jira calls across workflows while keeping recovery stateless: after a restart, the same repo-to-workflow mapping is rebuilt from stored workflow files and Jira is queried again.

## Durable Execution

**Feature:** Run each ticket as a durable workflow through a small execution interface, initially implemented with `go-workflows` and SQLite. Keep engine-specific types outside core logic so Temporal or another engine can replace it later.

- **Repo Poller:** Fetches active tickets for one repo and sends each batch to the Ticket Router.
- **Ticket Router:** Uses an existing `wf:<name>` label first; otherwise matches ticket fields against workflow filters. Zero matches are ignored and multiple matches are rejected.
- **Run Manager:** Claims an unmatched ticket with `wf:<name>`, then starts or ensures the deterministic run `repo/workflow/ticket`. Labeled tickets skip claiming and ensure their existing run. Missing execution state after database loss is handled only through explicit `serve --recover`.
- **Workflow Workers:** Interpret the workflow graph, process one selected node at a time, wait durably for reports, and schedule external work.
- **Activity Workers:** Perform Jira, runner, and harness calls with bounded concurrency and durable retries.

Workflow Workers schedule work for Activity Workers through the embedded engine's SQLite-backed queues. Waiting runs consume no permanent goroutine or worker. Use these terms consistently in code.

**At startup:** Open SQLite, register one generic YAML workflow interpreter and its activities, start bounded Workflow Workers and Activity Workers, load stored workflows, derive each repo's workflow references, start one Repo Poller per repo, and resume unfinished runs.

**Workflow submission:** Validate and atomically store the YAML, then update the affected repo references. Replacing an existing workflow is rejected while any of its runs are active.

**No workflow versioning:** Parse stored YAML into in-memory `Workflow` structs. Reject updates while that workflow has any active run, including HITL waits or retries. When no runs remain, atomically replace the disk file and in-memory struct. New runs always use the current validated struct.

**Unassigned ticket:** Repo Poller fetches it → Ticket Router finds exactly one matching workflow → Run Manager adds `wf:<name>` → after the claim succeeds, Run Manager starts or ensures `repo/workflow/ticket`.

**Assigned ticket:** Repo Poller fetches a ticket with `wf:<name>` → Ticket Router resolves that workflow directly and validates it belongs to the repo → Run Manager skips claiming and ensures `repo/workflow/ticket`. Put the workflow label on the parent and every mailbox subtask.

**Each run:** Ensure all agent/HITL mailbox subtasks exist → choose the current node → create `nodeVisitID` → move its subtask to `In Progress` → find or create its agent session → wait durably for valid structured output → write `SUMMARY` to the current node mailbox → write `FEEDBACK` to the selected next-node mailbox → move the current subtask to `Done` → process the selected next node → repeat until the reserved `end` node.

**Transition safety:** Process writing both mailbox comments, completing the current subtask, processing the next subtask, and starting its session as ordered, separately durable activities. Jira and runner calls cannot form one ACID transaction, so the embedded engine records progress and retries unfinished activities after a crash. Recovery always rolls forward:

- Jira call fails: retry that unfinished activity, then continue.
- Server crashes mid-transition: resume the unfinished activities from SQLite.
- Runner cannot start the next session: keep retrying with backoff, expose the current error, and do not reopen the previous node.
- Task-system state was manually changed: mark the run blocked, retry with backoff, and continue automatically when the expected state is restored.

Relay-flow comments include a stable marker derived from the node visit and comment type. Check for that marker before posting; rare duplicates after ambiguous task-system failures remain an accepted trade-off.

**Cancellation:** `relay-flow run cancel --ticket <key>` resolves the active run from the ticket, cancels it, stops scheduling new work, then closes active runner terminals while preserving the workspace/code after any running activity returns and posts a cancellation comment on the parent ticket. Use a stable `runID + cancellation` comment marker; rare duplicate Jira comments are accepted. Leave subtask statuses and mailbox history unchanged. Database-loss recovery skips parents carrying that cancellation marker.

**Database-loss recovery:** `relay-flow init` initializes SQLite. Normal `relay-flow serve` requires that valid database and refuses to start if it is missing or unusable. With a healthy database, a labeled ticket whose deterministic run is missing is a claim-before-run crash and the Run Manager safely creates it. `relay-flow serve --recover` treats all previous SQLite run/node/visit/report/route/timer/activity state as unknown and creates fresh execution state; database loss is never inferred from one missing run. Repo Pollers discover active parents through `wf:<name>` labels; parents already completed through `end` and parents carrying a cancellation marker are skipped. The runner closes all discovered run-owned terminals while preserving worktrees, branches, and code. `EnsureMailboxes` finds existing subtasks and creates only missing ones. Reset mailbox tasks to their fresh-run state, preserve comments and labels, create a new durable run using the deterministic repo/workflow/ticket `runID` (naturally the same logical ID), generate fresh random `nodeVisitID`s, and process from `start`. No previous node, visit, route, or activity is resumed. This mode is never automatic; repeated LLM work and occasional duplicate comments are accepted.

**No compensation:** Do not implement rollback compensation for now. We have no demonstrated requirement to delete comments or reverse subtask states, and doing so could destroy human or agent work.

**Justification:** Transition atomicity and recovery are critical because a crash may occur between any Jira or runner call. Shared bounded workers provide queues, retries, HITL waits, deduplication, and crash recovery without one goroutine or worker per ticket and without maintaining a custom Saga engine. Jira labels remain the durable assignment and cross-workflow lock; SQLite stores execution progress within that assignment.

## Node Mailboxes

**Feature:** Create one subtask per agent or HITL node; reserved `start` and `end` lifecycle nodes do not have subtasks. A mailbox means that node subtask's description and comment section. The description defines the node's work, and comments store summaries and feedback. Reuse the same subtask when a node is revisited. When the next step is `end`, all feedback fields are `None` and no feedback comment is written. Only a valid structured node output may advance the run; manual subtask status changes never select or process the next node.

**Justification:** Agents read only their own relevant context instead of the parent ticket's full conversation, preventing unrelated node feedback from polluting prompts while keeping the workflow human-visible in Jira.

## Structured Node Output

**Feature:** Require every agent to return this validated contract:

```text
STATUS: success | failure
NEXT STEP: <one valid node name>

SUMMARY:
COMPLETED:
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

Every section is required; use `None` when empty. `NEXT STEP` must match one configured option for the reported status. `SUMMARY` records the current node's work and `FEEDBACK` guides the selected next node. The plugin parses the contract into JSON; `nodeVisitID` is metadata and is not generated by the agent. Invalid normal-agent output triggers a nudge, while invalid or missing HITL output waits silently.

**Justification:** A deterministic output records what happened, where work goes next, and the exact context the next node needs, including after a crash.

## Single-Target Routing

**Feature:** A node may declare multiple routes with explanations, but each agent execution must select exactly one valid target. Persist the selected route before performing Jira or runner actions.

**Justification:** Each run remains serial and deterministic, while durable execution can resume the chosen transition after a crash without asking the agent to choose again.

## Reliable Report Delivery

**Feature:** Harness plugins send the complete parsed node-output contract and `nodeVisitID` directly to the server as JSON. The server acknowledges only after the embedded workflow engine persists it in SQLite. Until acknowledged, the plugin quietly retries the same output with exponential backoff and jitter, capped at five minutes, with only one retry loop per node visit. The workflow consumes only the first report for a visit. A retry after the run has moved on is acknowledged as an old duplicate and ignored; no deduplication table or payload hash is maintained. After a plugin restart, it may reread the valid harness message and submit it again without repeating mailbox, task-system, or runner work. Normal agent nodes are nudged when output is invalid or missing; HITL nodes remain silent until the human-guided agent returns valid output. Do not use JSONL or allow plugins to access SQLite.

**Justification:** An LLM may produce different or shorter feedback when asked again, so retries must preserve the original output rather than regenerate it. If the server crashes before persistence, the retry is accepted; if it crashes after persistence but before acknowledgement, the retry is safely deduplicated. Direct JSON safely carries multiline feedback and keeps SQLite as the single durable store, avoiding JSONL locking, scanning, cleanup, and file churn.

## Commands

**Feature:** Expose this command surface through the server's Unix-socket API:

```text
relay-flow init
relay-flow serve
relay-flow stop
relay-flow report

relay-flow workflow submit --file <path>
relay-flow workflow remove --name <name>
relay-flow workflow list
relay-flow workflow get --name <name>

relay-flow repo register
relay-flow repo list
relay-flow repo get --name <name>
relay-flow repo remove --name <name>

relay-flow run list
relay-flow run get --ticket <key>
relay-flow run cancel --ticket <key>
```

`workflow submit` creates a new workflow or replaces an existing workflow only when it has no active runs. `report` is a relay-flow command that sends the `nodeVisitID` and complete node-output contract to the server as JSON; harness plugins are its callers. JSON safely preserves multiline fields.

**Justification:** The CLI and future UI should use one server API for workflow management and run operations, avoiding separate behavior or state paths.

## Repo Registration

**Feature:** `relay-flow init` selects and stores the task, runner, and harness plugin names only. `relay-flow repo register` uses the runner to discover/select a repo, assigns its stable name and path, then collects the explicit required YAML keys returned by the selected task plugin, such as `project` and `component`. Do not use reflection or maintain separate prompt metadata. Validate the repo and task mapping before atomically saving it. Repo entries contain only `path` and `taskConfig`; runners resolve internal workspace IDs from the repo name and path. Reject duplicate names, paths, and canonical task-system scopes so one repo maps to one physical task isolation. Reject removal while active runs or stored workflows reference the repo.

**Justification:** Plugin selection is a one-time machine choice, while repos are added independently over time. Explicit required keys avoid plugin-specific CLI code, reflection, and separate prompt models. Excluding repo-level runner/harness config keeps registration minimal until a real integration requires more.

## Concurrency And Retries

**Feature:** Use bounded shared workers with simple defaults: at most 10 Repo Pollers executing concurrently, 10 Workflow Workers, and 20 Activity Workers. Durable runs waiting for agent or HITL output consume no worker. Apply one shared exponential-backoff package with jitter to all retryable runtime operations, capped at five minutes. Validate configuration, credentials, permissions, and connectivity before starting workflows. Runtime failures retry indefinitely and runs continue automatically when the dependency recovers; no resume command is required. Harness plugins follow the same retry schedule. During shutdown, stop accepting new work and allow current calls a short bounded period to finish.

**Justification:** Hundreds of runs can share a small number of workers because most time is spent waiting on external systems or humans. Bounded concurrency protects task systems, runners, SQLite, and the machine, while one retry policy avoids integration-specific behavior and maintenance overhead. Fail-fast startup validation prevents known permanent configuration errors from creating runs; indefinite runtime retries preserve roll-forward recovery during outages or later credential failures.

## Configuration Schemas

**Feature:** Use these root and workflow schemas. The task plugin owns one typed config and validates merged root/repo/workflow/node values during startup or submission. Raw values remain serializable for durable execution and are decoded by the adapter when an operation runs; do not add typed runtime caches or separate task-config structs per scope. Runner and harness plugins each own and validate their root config.

**Root config: `~/.relay-flow/config.yaml`**

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

repos:
  payments:
    path: /work/payments
    taskConfig:
      project: PAY
      component: payments
```

`pollIntervalSeconds` is machine-wide because polling is repo-scoped, not workflow-scoped. It defaults to `15` when omitted; every Repo Poller uses it. Per-repo polling intervals are not supported.

`completedRunRetentionDays` is machine-wide and defaults to `30`. Retention cleanup removes completed or canceled durable histories and their run-projection rows; starting, running, waiting, blocked, and canceling runs are never removed. Permanent workflow/cancellation markers remain in the task system.

**Workflow config**

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
    nudgePrompt: "Continue {{ticket}} at {{node}}. Valid next steps: {{nextSteps}}. Return the complete output contract."
    taskConfig:
      transitionTo:
        taskStatus: In Progress
    onSuccess:
      - target: reviewing
        when: Exploration is complete
    onFailure:
      - target: exploration
        when: More exploration is required

  reviewing:
    type: hitl
    agent: build
    description: Review the implementation with a human.
    taskConfig:
      transitionTo:
        parentStatus: In Review
        taskStatus: In Review
    onSuccess:
      - target: end
        when: Human approved
    onFailure:
      - target: exploration
        when: Human requested changes

  end:
    taskConfig:
      transitionTo:
        parentStatus: Done
```

Normal agent nodes may define `nudgePrompt`; otherwise a default is used. Supported variables are `{{ticket}}`, `{{workflow}}`, `{{repo}}`, `{{node}}`, and `{{nextSteps}}`. Templates are validated at submission. HITL nodes are never automatically nudged.

For Jira, omitted transition values default to parent `In Progress` at `start`, mailbox `In Progress` at work nodes, and parent `Done` at `end`. An omitted work-node parent transition leaves the parent unchanged.

**Justification:** Consistent plugin-owned config keeps core independent of Jira status transitions, Linear assignments, runner behavior, and harness settings. Reusing one config type prevents duplicate backend models and conversion code.
