# durable-run-execution Specification

## Purpose
TBD - created by archiving change relay-flow-subtask-refactor. Update Purpose after archive.
## Requirements
### Requirement: Each parent ticket has one durable run
The system SHALL execute each claimed parent ticket as one durable workflow instance with a deterministic ID derived from repo, workflow, and ticket. One generic interpreter SHALL execute all validated YAML workflow definitions.

#### Scenario: First poll starts a run
- **WHEN** a claimed parent has no durable instance in a healthy database
- **THEN** the Run Manager creates one instance using the deterministic run ID and a value snapshot of the workflow

#### Scenario: Repeated start request
- **WHEN** `EnsureRun` is called again with the same deterministic run ID
- **THEN** it returns the existing run without restarting the graph

#### Scenario: Server restarts normally
- **WHEN** the server restarts with its valid SQLite database
- **THEN** unfinished durable runs resume from their recorded waits and activity progress

### Requirement: The run interprets a serial graph
The durable workflow SHALL begin at reserved `start`, follow its single entry edge, process one agent or HITL node at a time, and stop after processing reserved `end`. Every completed node visit SHALL select exactly one next node.

#### Scenario: Run begins
- **WHEN** a new durable run starts
- **THEN** it ensures mailboxes and the runner environment, confirms referenced agents were validated, applies `start` task configuration, and moves to the single `start.onSuccess` target

#### Scenario: Work node selects a previous node
- **WHEN** a valid failure report selects a configured earlier node
- **THEN** the workflow creates a new visit for that earlier node and continues serially

#### Scenario: Run reaches end
- **WHEN** a valid report selects `end`
- **THEN** the workflow applies end task configuration, performs configured runner cleanup, marks the run completed, and schedules no further agent work

### Requirement: Every node entry has a distinct visit identity
The system SHALL generate one opaque `nodeVisitID` through a replay-safe durable side effect whenever a work node is entered. Replay and normal server restart SHALL return the same ID for that visit. Returning to the node or starting a fresh run during explicit database recovery SHALL generate a different ID.

#### Scenario: Node is revisited
- **WHEN** coding routes to review and review later routes back to coding
- **THEN** the second coding visit has a different `nodeVisitID` from the first

#### Scenario: Old visit reports after revisit
- **WHEN** a report arrives for the first coding visit after the second coding visit is current
- **THEN** the server acknowledges it as an old duplicate and does not advance the run

#### Scenario: Explicit recovery restarts visit sequence
- **WHEN** database recovery creates a fresh run for a ticket that previously had node visits
- **THEN** newly generated visit IDs do not collide with stale pre-recovery visit IDs

### Requirement: Durable workflow history owns transition progress
The workflow SHALL persist the accepted structured report and selected next node before scheduling external mailbox, task-system, runner, or harness activities. External operations SHALL execute as ordered, separately durable activity checkpoints.

#### Scenario: Crash after report persistence
- **WHEN** the report and next node are persisted but the server crashes before writing comments
- **THEN** replay uses the persisted route and continues with the first unfinished activity without asking the agent to choose again

#### Scenario: Crash during a later activity
- **WHEN** summary and feedback are written but completing the current mailbox has not succeeded
- **THEN** replay retains the selected route and retries completion before processing the next node

#### Scenario: Activity result is ambiguous
- **WHEN** an external provider applies an operation but its acknowledgement is lost
- **THEN** the adapter performs its idempotency/reconciliation check and may tolerate a rare duplicate comment without changing the route twice

### Requirement: Runtime failures roll forward
The system SHALL retry runtime task-system, runner, harness, and projection failures indefinitely using the shared backoff policy. It SHALL NOT automatically undo completed external work. Known invalid configuration, credentials, permissions, connectivity, repo mappings, and agents SHALL be validated before workers and Repo Pollers start.

#### Scenario: Jira is temporarily unavailable
- **WHEN** a Jira activity fails because Jira REST API is temporarily unavailable
- **THEN** the activity retries with durable backoff and the run continues automatically when Jira recovers

#### Scenario: Credentials become invalid during a run
- **WHEN** credentials worked at startup but later stop working
- **THEN** the run remains active, exposes the current error, and retries until credentials recover or the run is canceled

#### Scenario: Startup credentials are invalid
- **WHEN** task-system credentials fail startup validation
- **THEN** the server does not start workers or Repo Pollers

### Requirement: Manual task-system conflicts block without blind overwrite
When an activity detects that a human changed task-system state incompatibly, the run SHALL enter `blocked`, SHALL expose the conflict, and SHALL retry reconciliation with backoff. It SHALL continue automatically when the external state becomes compatible. Manual status changes SHALL NOT select a graph route.

#### Scenario: Human moves a mailbox unexpectedly
- **WHEN** relay-flow expects to complete a mailbox but finds a conflicting human-selected state
- **THEN** it marks the run blocked and does not overwrite the state blindly

#### Scenario: Human restores expected state
- **WHEN** the conflicting state is restored to one compatible with the pending activity
- **THEN** the next retry completes the activity and the run leaves blocked state automatically

### Requirement: Shared workers bound concurrency
The server SHALL use one Workflow Worker object limited to 10 concurrent workflow tasks and one Activity Worker object limited to 20 concurrent activities. Waiting for a report or HITL decision SHALL consume neither an Activity Worker nor a permanent per-run goroutine.

#### Scenario: Hundreds of runs wait for agents
- **WHEN** hundreds of runs are waiting for structured reports
- **THEN** they remain durable without reserving hundreds of workers or goroutines

#### Scenario: More than twenty activities are ready
- **WHEN** more than twenty external activities are ready
- **THEN** no more than twenty execute concurrently

### Requirement: Existing runs reconcile their current terminal
When `EnsureRun` finds an existing active run, it SHALL check the current ticket/node terminal by stable title and SHALL send a durable reconciliation request only when the terminal is absent or unusable. While waiting for a report, the workflow SHALL handle that request by relaunching the same visit with the same `nodeVisitID`.

#### Scenario: Current terminal remains live
- **WHEN** a repo poll ensures an active run whose current terminal is live
- **THEN** no reconciliation signal is sent and the terminal/workflow wait remain unchanged

#### Scenario: Current terminal died
- **WHEN** a repo poll ensures an active run whose current terminal is absent or unusable
- **THEN** reconciliation relaunches the harness for the same node visit and mailbox without selecting a new route

#### Scenario: HITL terminal is idle
- **WHEN** reconciliation finds a live idle HITL terminal
- **THEN** it leaves the terminal untouched and sends no nudge

### Requirement: Backoff is consistent across runtime environments
The system SHALL use exponential backoff with a 2-second initial delay, factor 2, 20-percent jitter, and a 5-minute maximum delay. Go polling and durable workflow timers SHALL use the shared `BackoffPolicy`; the TypeScript harness plugin SHALL mirror the same constants.

#### Scenario: Repeated transient failures
- **WHEN** the same runtime operation fails repeatedly
- **THEN** retry delays grow exponentially with jitter and do not exceed five minutes

#### Scenario: Server restarts during durable backoff
- **WHEN** the server crashes while a durable retry timer is pending
- **THEN** the workflow restores the timer from history and does not reset the transition decision

### Requirement: Cancellation stops without rollback
`run cancel` SHALL resolve the active run by ticket, cancel the durable workflow context, prevent later normal activities, wait for an already-running activity to return, close run-owned runner terminals while preserving the workspace/code, and post a parent cancellation comment with stable marker `<runID>:cancellation`. It SHALL leave mailbox statuses and history unchanged.

#### Scenario: Cancel while waiting for report
- **WHEN** a run is waiting for an agent or HITL report and cancellation is requested
- **THEN** the wait is canceled, runner resources are closed, a parent cancellation comment is attempted, and no node is rolled back

#### Scenario: Cancel during an activity
- **WHEN** cancellation occurs while an external activity is already running
- **THEN** the activity is allowed to return before cancellation cleanup executes

#### Scenario: Cancellation comment acknowledgement is ambiguous
- **WHEN** the task system may have accepted the cancellation comment but returns an error
- **THEN** the adapter checks the stable marker before retrying and rare duplicates remain acceptable

### Requirement: End cleanup is explicit
The workflow field `cleanupRunnerOnEnd` SHALL control whether reaching `end` closes all run-owned runner terminals and other runner-owned execution resources. When enabled, it SHALL take priority over machine-level terminal retention. The lifecycle node name `end` SHALL remain distinct from runner terminals.

#### Scenario: Cleanup enabled
- **WHEN** a run reaches `end` with `cleanupRunnerOnEnd: true`
- **THEN** the runner closes the run-owned terminals/resources after applying end task configuration

#### Scenario: Cleanup disabled
- **WHEN** a run reaches `end` with cleanup disabled
- **THEN** the run completes without closing runner resources

### Requirement: Database lifecycle distinguishes normal and disaster recovery
`relay-flow init` SHALL initialize the SQLite database. Normal `serve` SHALL require a valid existing database and SHALL refuse to start when it is missing or unusable. A missing deterministic run in a healthy database SHALL be treated as claim-before-run recovery. Only `serve --recover` SHALL create fresh execution state after database loss.

#### Scenario: Healthy database lacks a claimed run
- **WHEN** the database is valid and a labeled parent has no deterministic run
- **THEN** the Run Manager safely creates the run from `start`

#### Scenario: Normal serve finds no database
- **WHEN** normal `serve` starts and the configured database is absent
- **THEN** startup fails with guidance to use explicit recovery rather than silently recreating state

#### Scenario: Explicit database recovery
- **WHEN** `serve --recover` is invoked after database loss
- **THEN** fresh execution state is created, active labeled parents are discovered, stale runner terminals are closed while workspaces/code are preserved, existing mailboxes are ensured and reset, and fresh runs start at `start`

#### Scenario: Recovery preserves work artifacts
- **WHEN** database recovery resets a run
- **THEN** mailbox comments, workflow labels, worktrees, branches, and code are preserved while repeated LLM work is accepted

#### Scenario: Recovery sees a canceled parent
- **WHEN** an active-status labeled parent contains the stable cancellation marker
- **THEN** database recovery does not create a fresh run for that parent

### Requirement: Database recovery discards previous execution progress
During `serve --recover`, the system SHALL treat all prior SQLite run, node, visit, report, route, timer, and activity progress as unknown. It SHALL NOT resume any previous node or activity. For each non-canceled active labeled parent, it SHALL create a new durable run using the deterministic repo/workflow/ticket run ID, generate fresh random durable node visit IDs, and process from `start`.

#### Scenario: Previous database had a partial transition
- **WHEN** recovery starts for a ticket that may previously have written some mailbox comments or statuses
- **THEN** relay-flow preserves task-system artifacts but makes no inference from old execution progress and starts at `start`

#### Scenario: Deterministic run identity repeats
- **WHEN** a recovered ticket uses the same repo, workflow, and ticket as before database loss
- **THEN** the new durable run naturally has the same logical `runId` while all node visit IDs are fresh

### Requirement: Completed run data follows retention
The server SHALL remove completed or canceled durable histories and matching run-projection rows after `completedRunRetentionDays`, default 30. It SHALL NOT remove starting, running, waiting, blocked, or canceling runs through retention cleanup. The task-system cancellation marker SHALL prevent a cleaned canceled run from being recreated.

#### Scenario: Completed run exceeds retention
- **WHEN** a completed run is older than the configured retention period
- **THEN** its engine history and matching run-projection row are eligible for cleanup

#### Scenario: Waiting run exceeds retention age
- **WHEN** a HITL run has waited longer than the retention period
- **THEN** retention cleanup leaves it intact because it is not completed

#### Scenario: Canceled run exceeds retention
- **WHEN** a canceled run is older than the configured retention period
- **THEN** its history and projection row are eligible for cleanup while the permanent parent cancellation marker prevents restart

### Requirement: Run queries use a derived projection
The system SHALL maintain a `relay_runs` projection for run list/get, ticket lookup, active workflow/repo checks, current node/visit display, error display, and cancellation lookup. Durable workflow history SHALL remain authoritative for accepted reports, selected routes, and activity completion. Projection updates SHALL be idempotent durable activities.

#### Scenario: CLI lists runs
- **WHEN** `run list` is requested
- **THEN** the server queries the run projection rather than replaying every workflow history

#### Scenario: Projection update is interrupted
- **WHEN** the server crashes while updating the run projection
- **THEN** durable replay retries the idempotent projection activity without changing graph progression or repeating task-system work
