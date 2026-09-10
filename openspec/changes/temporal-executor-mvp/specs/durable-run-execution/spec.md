## MODIFIED Requirements

### Requirement: Each parent ticket has one durable run
The system SHALL execute each claimed parent ticket as one durable workflow instance with a deterministic ID derived from repo, workflow, and ticket. The configured executor SHALL be either the embedded `goworkflows` engine or the external Temporal executor, and exactly one executor SHALL be used by an initialized relay-flow home. One generic interpreter SHALL execute all validated YAML workflow definitions in either backend.

In `goworkflows` mode, the embedded engine history and queues are durable in SQLite. In `temporal` mode, Temporal Server history and queues are durable outside relay-flow. The `relay_*` SQLite tables are a derived query/runtime projection in both modes and SHALL NOT own graph progression.

#### Scenario: First poll starts a run
- **WHEN** a claimed parent has no durable instance in a healthy selected backend
- **THEN** the Run Manager creates one instance using the deterministic run ID and a value snapshot of the workflow

#### Scenario: Repeated start request
- **WHEN** `EnsureRun` is called again with the same deterministic run ID
- **THEN** it returns the existing run without restarting the graph or selecting another executor

#### Scenario: Server restarts normally with goworkflows
- **WHEN** the server restarts with a valid SQLite database and `goworkflows` is selected
- **THEN** unfinished embedded durable runs resume from their recorded waits and activity progress

#### Scenario: Server restarts normally with Temporal
- **WHEN** the server restarts with the same valid projection database, Temporal address, and namespace
- **THEN** the worker reconnects to Temporal and unfinished Temporal workflows resume from Temporal history without restarting from `start`

#### Scenario: Temporal workflow history remains after relay restart
- **WHEN** relay-flow loses its process memory but Temporal Server remains available
- **THEN** Temporal retains the workflow execution and relay-flow reconstructs any missing in-memory snapshot from Temporal history

### Requirement: Shared workers bound concurrency
The system SHALL use bounded shared workers in both executor modes. `goworkflows` SHALL use its existing workflow/activity worker objects with limits of 10 and 20. `temporal` SHALL use one aggregate Temporal worker for the fixed task queue with workflow-task execution limited to 10 and activity execution limited to 20. Waiting for a report or HITL decision SHALL consume neither an activity execution slot nor a permanent per-run goroutine.

#### Scenario: Hundreds of runs wait for agents
- **WHEN** hundreds of runs are waiting for structured reports
- **THEN** they remain durable without reserving hundreds of workers or goroutines in either executor

#### Scenario: More than twenty activities are ready
- **WHEN** more than twenty external activities are ready
- **THEN** no more than twenty activities execute concurrently in the selected executor

#### Scenario: Temporal worker shape
- **WHEN** the Temporal executor is running normally
- **THEN** one aggregate worker polls the configured namespace/task queue rather than one worker per ticket or one worker per workflow definition

### Requirement: Durable workflow history owns transition progress
The selected durable backend SHALL persist the accepted structured report and selected next node before scheduling external mailbox, task-system, runner, or harness activities. External operations SHALL execute as ordered, separately durable activity checkpoints. In Temporal mode, Temporal history is authoritative; in `goworkflows` mode, embedded engine history is authoritative.

The `relay_*` projection SHALL be updated idempotently from workflow execution and SHALL never be used to choose a route or resume a different node than the authoritative backend selected.

#### Scenario: Crash after report persistence in goworkflows
- **WHEN** the embedded engine persists a report and selected route but the process crashes before writing comments
- **THEN** embedded replay uses the persisted route and continues with the first unfinished activity without asking the agent to choose again

#### Scenario: Crash after report persistence in Temporal
- **WHEN** Temporal persists a report signal and selected route but the relay worker stops before writing comments
- **THEN** Temporal replay uses the persisted route and continues with the first unfinished activity without asking the agent to choose again

#### Scenario: Projection update is interrupted
- **WHEN** a projection activity fails after the authoritative backend has recorded workflow progress
- **THEN** the projection activity retries idempotently without changing the route or repeating completed external effects

### Requirement: Database lifecycle distinguishes normal and disaster recovery
`relay-flow init` SHALL initialize the local SQLite projection and the selected durable backend. Normal `serve` SHALL require a valid existing local database and SHALL refuse to silently create missing execution state. A missing deterministic run in a healthy database SHALL be handled by the selected executor's claim-before-run logic.

`serve --recover` SHALL have backend-specific, explicit semantics:

- with `goworkflows`, it SHALL discard embedded execution state and restart eligible tickets from `start` using the existing destructive recovery behavior;
- with `temporal`, it SHALL rebuild the local relay projection from retained Temporal history/state and SHALL preserve existing Temporal workflow progress and Workflow IDs.

#### Scenario: Healthy database lacks a claimed run
- **WHEN** the local projection is valid and a labeled parent has no deterministic run
- **THEN** the selected executor safely checks/creates the durable run without inferring database loss

#### Scenario: Normal serve finds no database
- **WHEN** normal `serve` starts and the configured local database is absent or unusable
- **THEN** startup fails with guidance to use explicit backend-appropriate recovery rather than silently creating state

#### Scenario: Explicit goworkflows recovery
- **WHEN** `serve --recover` is invoked with `goworkflows`
- **THEN** relay-flow preserves task-system artifacts, discards embedded execution progress, and starts eligible tickets from `start` as the existing destructive recovery contract specifies

#### Scenario: Explicit Temporal projection recovery
- **WHEN** `serve --recover` is invoked with `temporal` and local SQLite state is absent or unusable
- **THEN** relay-flow rebuilds the relay projection from Temporal without resetting task-system state, closing healthy terminals, creating a second Workflow ID, or starting a replacement workflow

### Requirement: Database recovery discards previous execution progress
During `goworkflows` `serve --recover`, the system SHALL treat prior embedded SQLite execution progress as unknown and SHALL start eligible tickets from `start` after preserving task-system artifacts.

During Temporal `serve --recover`, the system SHALL treat only the local relay projection as lost. It SHALL use Temporal history, visibility, and read-only workflow state/query APIs as the source for existing execution progress. It SHALL preserve Temporal Workflow IDs, current routes, reports, timers, activity progress, task-system state, mailbox history, runner worktrees, branches, code, and healthy terminals. It SHALL not create a fresh execution for an existing Temporal workflow.

#### Scenario: Goworkflows partial transition
- **WHEN** embedded database recovery starts after a partial transition
- **THEN** relay-flow preserves task-system artifacts but makes no inference from the old embedded progress and starts from `start`

#### Scenario: Temporal projection loss during a partial transition
- **WHEN** local projection recovery starts while a Temporal workflow has a partial transition
- **THEN** relay-flow obtains the authoritative current progress from Temporal and allows that same workflow to continue the unfinished activity

#### Scenario: Temporal active execution is found
- **WHEN** projection rebuild enumerates an active Temporal `TicketWorkflow`
- **THEN** it recreates the matching `relay_runs` row and current node/visit display without resetting or duplicating the workflow

#### Scenario: Temporal history cannot be read
- **WHEN** an active Temporal execution cannot be enumerated, decoded, or queried
- **THEN** recovery fails before normal pollers start and does not mutate task-system state or create a replacement execution

#### Scenario: Temporal closed history expired
- **WHEN** a closed Temporal execution is older than namespace retention
- **THEN** relay-flow does not recreate it merely because its historical projection cannot be rebuilt

### Requirement: Completed run data follows retention
The server SHALL retain completed/canceled `relay_runs` projection rows according to `completedRunRetentionDays`, default 30 days. In `goworkflows` mode, embedded histories and matching projection rows MAY be removed by the existing retention sweep. In `temporal` mode, Temporal Server namespace retention SHALL be configured or verified at 30 days, and relay-flow SHALL not attempt direct Temporal history deletion or server-database cleanup.

Starting, running, waiting, blocked, and canceling runs SHALL remain protected from local projection cleanup. Temporal Server owns its own history cleanup and service recovery. A missing expired Temporal history SHALL not cause relay-flow to create a new workflow automatically.

#### Scenario: Goworkflows retention
- **WHEN** a completed or canceled embedded run exceeds local retention
- **THEN** its eligible embedded history and projection rows are removed according to the existing goworkflows policy

#### Scenario: Temporal namespace retention
- **WHEN** a Temporal namespace is initialized for relay-flow
- **THEN** its workflow execution retention is at least 30 days

#### Scenario: Temporal projection retention
- **WHEN** a completed or canceled Temporal run exceeds local projection retention
- **THEN** relay-flow may remove its local projection row while leaving Temporal Server to apply its namespace retention policy

#### Scenario: Waiting Temporal run exceeds retention age
- **WHEN** a Temporal workflow is waiting longer than 30 days
- **THEN** it remains active and is not removed by relay-flow's local projection cleanup

### Requirement: Run queries use a derived projection
The system SHALL maintain the existing `relay_*` projection for run list/get, ticket lookup, active workflow/repo checks, current node/visit display, error display, and cancellation lookup in both executor modes. Temporal history/state and embedded engine history remain authoritative for graph progression, accepted reports, selected routes, and activity completion.

Temporal `serve --recover` SHALL be able to rebuild the projection from Temporal through public SDK APIs. Projection loss SHALL not trigger a new workflow, a task-system reset, or a route decision.

#### Scenario: CLI lists runs
- **WHEN** `run list` is requested
- **THEN** the server queries the local relay projection rather than replaying every workflow history

#### Scenario: Temporal projection rebuild
- **WHEN** the local relay projection is rebuilt
- **THEN** `relay_runs` rows are reconstructed from retained Temporal execution metadata and authoritative workflow state

#### Scenario: Projection disagrees with Temporal
- **WHEN** a local projection value conflicts with Temporal workflow state
- **THEN** Temporal state wins and the projection is repaired without changing graph progression

#### Scenario: Processed-report projection is missing
- **WHEN** a local processed-report receipt is absent after Temporal projection recovery
- **THEN** Temporal workflow state/history prevents duplicate graph effects
