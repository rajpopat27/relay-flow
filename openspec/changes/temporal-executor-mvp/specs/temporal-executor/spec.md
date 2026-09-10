## ADDED Requirements

### Requirement: Temporal execution uses the external server
The `temporal` executor SHALL use the public Temporal Go SDK to connect to the configured external Temporal Server. It SHALL NOT import `go.temporal.io/server`, build the Temporal Server repository, query or modify the server's underlying database, or start Temporal as a relay-flow child process.

#### Scenario: Temporal executor starts
- **WHEN** an initialized relay-flow installation selects `executorPlugin: temporal`
- **THEN** relay-flow connects through the SDK to the configured address and namespace and does not open or inspect Temporal Server storage

#### Scenario: Temporal server is unavailable
- **WHEN** the configured Temporal address cannot be reached during startup
- **THEN** relay-flow fails startup with an actionable error and does not silently fall back to `goworkflows`

### Requirement: Temporal configuration is collected during initialization
When Temporal is selected, `relay-flow init` SHALL present a selection titled exactly `Select executor`, then ask for or accept non-interactive values for the Temporal server address and namespace/team name. The address SHALL default to `localhost:7233`; the namespace SHALL be explicit and SHALL NOT silently default to `default`. Init SHALL create a missing namespace or verify an existing registered namespace before writing the completed configuration. Temporal-only flags supplied with `goworkflows` are invalid rather than ignored. In a non-interactive invocation, selecting `temporal` requires `--temporal-namespace`; no Temporal answers are read from the legacy task/runner/harness selection lines. Machine loading SHALL apply the same conditional validation: Temporal requires a non-empty namespace and defaults an omitted address to `localhost:7233`; embedded mode rejects non-empty Temporal fields.

#### Scenario: Interactive Temporal initialization
- **WHEN** the user selects `temporal` during init
- **THEN** init asks for the Temporal server address and namespace/team name, creates or verifies that namespace, and stores both values in machine configuration

#### Scenario: Non-interactive Temporal initialization
- **WHEN** init is given the Temporal executor, address, and namespace flags
- **THEN** it performs the same validation and namespace setup without an interactive prompt

#### Scenario: Embedded initialization
- **WHEN** the user selects the default `goworkflows` executor
- **THEN** init does not contact Temporal or require Temporal address/namespace values

#### Scenario: Missing namespace
- **WHEN** the requested Temporal namespace does not exist and the server permits namespace registration
- **THEN** init registers it with retention of at least `max(30 days, completedRunRetentionDays)` before reporting success

#### Scenario: Existing namespace has insufficient retention
- **WHEN** an existing namespace has less than `max(30 days, completedRunRetentionDays)` of workflow retention
- **THEN** init fails with an actionable retention error and does not silently lower the relay-flow retention policy

#### Scenario: Namespace creation is not permitted
- **WHEN** the requested namespace is absent and the Temporal Server rejects namespace registration
- **THEN** init fails without writing configuration or reporting a ready relay-flow installation

#### Scenario: Temporal-only flags select goworkflows
- **WHEN** init selects `goworkflows` while Temporal address or namespace flags are supplied
- **THEN** init rejects the invocation without contacting Temporal or writing partial configuration

#### Scenario: Embedded config contains Temporal fields
- **WHEN** machine config omits `executorPlugin` or selects `goworkflows` while Temporal address or namespace is non-empty
- **THEN** machine loading rejects the configuration before startup

#### Scenario: Temporal address is omitted
- **WHEN** machine config selects `temporal` and omits `temporalAddress`
- **THEN** machine loading uses `localhost:7233` and still requires an explicit namespace

#### Scenario: Relay-flow restarts
- **WHEN** the same initialized relay-flow home starts again
- **THEN** it uses the same configured Temporal address and namespace and does not generate or select another namespace

### Requirement: One initialized installation has one immutable executor
An initialized relay-flow home SHALL select exactly one executor. The executor SHALL default to `goworkflows` when omitted for backward compatibility. Supported commands SHALL reject changing the executor after initialization, and relay-flow SHALL NOT auto-detect, migrate, or fall back between executors. Temporal address and namespace SHALL remain bound to a Temporal-initialized home. A singleton relay-owned `relay_executor_identity` record SHALL persist the selected executor and, for Temporal, its address and namespace; it SHALL not contain workflow state. The identity record SHALL be created atomically with initialization and verified before worker startup.

#### Scenario: Default executor
- **WHEN** a legacy machine configuration has no `executorPlugin`
- **THEN** relay-flow treats it as `goworkflows`

#### Scenario: Executor change is requested
- **WHEN** `init --force` attempts to change an initialized home from `goworkflows` to `temporal` or the reverse
- **THEN** init rejects the change without modifying configuration, SQLite state, workflows, or task-system state

#### Scenario: Backend failure does not fall back
- **WHEN** the selected executor fails to start
- **THEN** relay-flow reports that executor's failure and does not start the other executor

#### Scenario: Identity mismatch
- **WHEN** the configured executor or Temporal address/namespace differs from the initialized installation-identity record
- **THEN** startup fails before workers or pollers start and does not migrate, combine, or reinterpret state

### Requirement: Executor identity is persisted and verified
The local database SHALL contain one singleton `relay_executor_identity` record containing the selected executor and, for Temporal, the configured address and namespace. For `goworkflows`, Temporal address/namespace values in the record SHALL be empty; for `temporal`, both SHALL be non-empty and match machine configuration byte-for-byte. Initialization SHALL create the record atomically with the selected projection state. Serve SHALL verify the record against machine configuration before starting workers or pollers. A legacy database without the record SHALL be accepted only as a legacy `goworkflows` installation when configuration also selects `goworkflows`; it SHALL not be adopted as a Temporal installation.

#### Scenario: Identity matches
- **WHEN** the configured executor and Temporal identity match the singleton record
- **THEN** startup continues to backend construction and validation

#### Scenario: Identity mismatches
- **WHEN** the configured executor, Temporal address, or Temporal namespace differs from the record
- **THEN** startup fails before workers or pollers start and does not migrate, merge, or reinterpret state

#### Scenario: Temporal recovery recreates local state
- **WHEN** Temporal projection recovery creates a replacement local database
- **THEN** it writes the identity record from the existing Temporal configuration before workers/pollers become ready

### Requirement: Temporal uses one bounded worker and fixed task queue
The Temporal executor SHALL create one client-bound normal aggregate worker for the fixed task queue `relay-flow`, register one generic `TicketWorkflow`, and register the activity implementation. Workflow-task execution SHALL be limited to 10 concurrent tasks, activity execution SHALL be limited to 20 concurrent tasks, and each activity attempt SHALL have a fixed five-minute start-to-close timeout with native activity retry maximum one. Waiting workflows SHALL consume no permanent relay-flow goroutine or activity slot. Normal workflow and activity polling SHALL each use two pollers. A temporary workflow-only/query-capable worker SHALL exist only sequentially during explicit projection recovery and SHALL not execute non-local activities; the relay-flow workflow shall use no local activities during recovery.

#### Scenario: Worker registration
- **WHEN** the Temporal executor starts successfully
- **THEN** the worker has one registered generic workflow and the relay-flow activity methods, and polls only the configured namespace/task queue

#### Scenario: Activity concurrency
- **WHEN** more than 20 activity tasks are ready
- **THEN** no more than 20 activity executions run concurrently in that serve process

#### Scenario: Waiting report
- **WHEN** a ticket workflow waits for an agent or HITL report
- **THEN** Temporal retains the wait and the worker does not reserve an activity execution slot for that ticket

#### Scenario: Worker fatal error
- **WHEN** the Temporal worker reports a fatal error after startup
- **THEN** relay-flow requests serve shutdown and does not continue polling without the selected durable worker

#### Scenario: Activity exceeds attempt timeout
- **WHEN** a Temporal activity attempt runs longer than five minutes
- **THEN** Temporal times out that attempt, the workflow's typed retry loop handles the failure, and the activity is not retried by a second native attempt

### Requirement: Temporal workflow values are serializable and engine-independent
Only serializable value structs SHALL cross the Temporal workflow/activity boundary. Workflow code SHALL NOT persist interfaces, clients, database handles, functions, task systems, runners, harnesses, or Temporal SDK objects in workflow input/state. The immutable `run.Start` workflow snapshot SHALL be persisted as the Temporal workflow input.

#### Scenario: Workflow starts with a snapshot
- **WHEN** the Run Manager starts a ticket through the Temporal executor
- **THEN** the Temporal workflow input contains the immutable run and workflow value snapshot and no live dependency object

#### Scenario: Non-serializable dependency is encountered
- **WHEN** implementation would need to pass a client or interface into a workflow/activity value
- **THEN** the implementation keeps that dependency on the worker's registered activity struct and passes only serializable values

### Requirement: Temporal preserves graph and activity ordering
The Temporal interpreter SHALL preserve the existing serial graph and ordered effect semantics. It SHALL persist/consume the report and selected route in Temporal history before scheduling summary, selected feedback, mailbox completion, next-node task configuration, and next-node terminal work. It SHALL use Temporal replay-safe side effects, timers, signal channels/selectors, and workflow time.

#### Scenario: Report selects a next node
- **WHEN** a current node receives a valid report selecting a work node
- **THEN** Temporal records the signal/selected route and executes summary, selected feedback, completion, next-node configuration, and next-node setup in the existing order

#### Scenario: Activity is retried
- **WHEN** a task-system, runner, harness, or projection activity fails transiently
- **THEN** the Temporal workflow uses the shared durable backoff policy and resumes the same selected route without rollback

#### Scenario: Workflow code replays
- **WHEN** a Temporal worker restarts and replays an open workflow
- **THEN** visit IDs, timers, signal handling, and route decisions remain deterministic and no external effect is re-run merely because replay occurred

### Requirement: Temporal report delivery is durable and fenced
The Temporal executor SHALL validate reports against the immutable workflow snapshot, send them on the fixed Temporal signal name `report` to the current Temporal Workflow ID, and acknowledge a new report only after Temporal accepts the signal. Reconciliation SHALL use the fixed signal name `reconcile`. It SHALL retain the existing JSON wire keys and shall not expose `nodeVisitID` to the harness. Temporal workflow state/history SHALL remain the final deduplication and stale-visit fence; the local `relay_processed_reports` table SHALL be used only as a derived fast path.

#### Scenario: Valid report
- **WHEN** a report targets the current run/node and passes snapshot validation
- **THEN** relay-flow signals the Temporal workflow and returns an acknowledgement only after the signal RPC succeeds

#### Scenario: Stale report
- **WHEN** a report signal contains an old node visit or consumed report ID
- **THEN** the workflow ignores it without writing comments, changing task state, or selecting another route

#### Scenario: Projection receipt is missing
- **WHEN** the local processed-report projection row is absent but Temporal history says the report was consumed
- **THEN** Temporal-side deduplication prevents a repeated graph effect

### Requirement: Temporal cancellation ends as canceled
The Temporal executor SHALL request workflow cancellation with the supplied reason, stop scheduling normal work, configure normal activities with `WaitForCancellation: true`, wait for already-running activities according to the five-minute attempt timeout and context behavior, run disconnected cleanup activities, and return a Temporal cancellation result rather than accidentally completing the workflow. Runner cleanup SHALL preserve worktrees/code according to the existing runner contract.

#### Scenario: Cancel while waiting
- **WHEN** a workflow waiting for a report is canceled
- **THEN** disconnected cleanup closes run-owned terminals, writes the cancellation comment, updates the projection, and Temporal records the workflow as canceled

#### Scenario: Cancel during activity
- **WHEN** cancellation is requested while an activity is already executing
- **THEN** the workflow does not run rollback and waits according to the activity cancellation policy before cleanup

### Requirement: Temporal honors relay-flow execution-attempt fencing
The Temporal executor SHALL use the `run.Start.ID` value as the exact Temporal Workflow ID. `ALLOW_DUPLICATE_FAILED_ONLY` SHALL be a server-side guard only; relay-flow SHALL never intentionally start a second execution with the same Workflow ID. Explicit restart attempts created by the Run Manager SHALL use their distinct attempt-suffixed application IDs. A restart SHALL never reuse a canceled execution's Workflow ID, and reports, cancellation requests, and reconcile signals carrying an old attempt ID SHALL not affect a newer attempt.

#### Scenario: Original attempt starts
- **WHEN** the Run Manager creates the first attempt
- **THEN** the Temporal Workflow ID equals the deterministic application run ID

#### Scenario: Explicit restart starts
- **WHEN** the Run Manager creates a second attempt
- **THEN** Temporal starts it with the distinct attempt-suffixed application ID and the old execution remains fenced

#### Scenario: Old report arrives
- **WHEN** a report for an old attempt arrives after a newer attempt exists
- **THEN** the Temporal executor acknowledges/ignores it for the old execution and it cannot advance the newer attempt

### Requirement: SQLite is only a relay projection in Temporal mode
In Temporal mode, `state.db` SHALL contain only the relay-flow projection tables, the singleton `relay_executor_identity` installation marker, and the SQLite support required to serve those tables. It SHALL NOT contain Temporal workflow history, Temporal task queues, Temporal timers, or a second execution state machine. Loss or corruption of this local projection SHALL not change Temporal workflow execution or cause task-system reset.

#### Scenario: Temporal mode opens SQLite
- **WHEN** a Temporal executor starts
- **THEN** it opens the local database only for relay-flow projection/identity data and uses Temporal Server for workflow execution state

#### Scenario: Local projection is unavailable
- **WHEN** relay-flow cannot query its SQLite projection in Temporal mode
- **THEN** it reports/retries the local query operation or enters explicit projection recovery and does not start a replacement Temporal workflow

#### Scenario: Projection disagrees with Temporal
- **WHEN** a relay projection value disagrees with Temporal state
- **THEN** Temporal state wins and the projection is repaired without changing graph progression

### Requirement: Temporal workflow snapshots and internal state are recoverable after relay restart
The Temporal executor SHALL be able to retrieve an existing run's immutable `run.Start` snapshot and authoritative current state from Temporal history/state after the relay-flow process restarts. It SHALL NOT require the current workflow file or the in-memory snapshot cache to validate reports for an active run. The workflow SHALL expose the fixed internal read-only `relay-flow/run-state-v1` and `relay-flow/report-state-v1` query contracts defined by the design; query handlers SHALL not perform activities or mutate external state. `run-state-v1` SHALL return current run state and serializable runtime bindings; `report-state-v1` SHALL accept a report ID and return consumed status plus current node/visit/state. Query results SHALL be advisory and the workflow SHALL recheck identity when consuming a signal.

#### Scenario: Report after relay restart
- **WHEN** relay-flow receives a report after its in-memory cache was lost
- **THEN** it loads the original snapshot from the Temporal workflow-start history and validates the report against that snapshot

#### Scenario: Workflow file was replaced after completion
- **WHEN** an old completed run is inspected after its workflow file has changed
- **THEN** the executor uses the run's stored Temporal snapshot rather than the current file

#### Scenario: Projection rebuild queries an active workflow
- **WHEN** projection recovery needs the current node, visit, state, or runtime binding for an open Temporal workflow
- **THEN** the recovery-only workflow worker answers the internal read-only query without executing non-local activities or mutating task-system state

#### Scenario: Report fencing queries an active workflow
- **WHEN** a report arrives and local projection data is missing or stale
- **THEN** the report-state query returns current node/visit and consumed-report status without mutating workflow or external state, and workflow-side validation remains final

#### Scenario: Query races with workflow close
- **WHEN** the report-state query observes an open run but the workflow closes before the signal is sent
- **THEN** relay-flow rechecks the execution and acknowledges a confirmed closed execution as a duplicate rather than retrying forever or starting another workflow

### Requirement: Temporal projection recovery is non-destructive
When `serve --recover` is used with the Temporal executor, relay-flow SHALL rebuild its local `relay_*` projection from retained Temporal metadata/history/state, whether the existing local projection is missing, unusable, or explicitly being rebuilt. It SHALL preserve existing Temporal Workflow IDs and progress and SHALL NOT reset task-system state or create a new Temporal execution. Recovery SHALL use a temporary workflow-only/query-capable worker for open-workflow queries, shall not execute non-local activities during reconstruction, and shall start the normal aggregate worker only after projection reconstruction succeeds.

#### Scenario: Local projection is lost
- **WHEN** `state.db` is missing or unusable and `serve --recover` is selected for the Temporal executor
- **THEN** relay-flow preserves any existing `state.db`, `state.db-wal`, and `state.db-shm` siblings as unique backups, creates a fresh local projection, enumerates only retained `TicketWorkflow` executions on task queue `relay-flow` with pagination, reconciles active claimed parents by exact Workflow ID, restores run metadata/current state, applies local retention, and then starts the same Temporal workflows

#### Scenario: Active execution is rebuilt
- **WHEN** an active Temporal workflow is found during projection recovery
- **THEN** relay-flow restores its current node/visit/runtime projection and leaves its task-system state, terminal, mailbox, and selected route unchanged

#### Scenario: A valid projection is explicitly rebuilt
- **WHEN** `serve --recover` is selected for Temporal even though the local projection is readable
- **THEN** relay-flow preserves a backup, rebuilds the projection from Temporal, and still performs no task-system reset or replacement start

#### Scenario: Claimed parent has no Temporal execution
- **WHEN** recovery finds an active task-system parent with a `wf:<workflow>` claim but exact `DescribeWorkflowExecution` returns `NotFound`
- **THEN** recovery records the missing execution without starting it; normal post-recovery `EnsureRun` handles the separate claim-before-run case

#### Scenario: Projection recovery encounters an unreadable active history
- **WHEN** an active Temporal workflow cannot be read or decoded
- **THEN** recovery fails before pollers start and does not create a replacement workflow or mutate the task system

#### Scenario: Temporal server is unavailable during recovery
- **WHEN** projection recovery cannot connect to the configured Temporal namespace
- **THEN** recovery fails before pollers start, preserves the timestamped local backup, and does not mutate the task system or start replacement workflows

#### Scenario: Projection cache rebuild
- **WHEN** Temporal projection recovery reconstructs local state
- **THEN** it writes `relay_runs` and available `relay_node_runtime` bindings, leaves `relay_processed_reports` and `relay_node_sessions` empty, and relies on Temporal report state plus normal runtime registration/reconciliation for correctness

#### Scenario: Recovery worker is used
- **WHEN** Temporal projection recovery needs workflow queries before normal execution resumes
- **THEN** it uses a temporary worker configured with `LocalActivityWorkerOnly: true`, does not run non-local activities during reconstruction, stops that worker, and starts the normal worker only after reconstruction succeeds

#### Scenario: Visibility is eventually consistent
- **WHEN** Temporal Visibility enumeration does not yet show a recently started relay-flow workflow
- **THEN** recovery performs exact reconciliation for active task-system parents using their `wf:<workflow>` claims and Temporal Workflow IDs before pollers start, and never starts a duplicate execution

### Requirement: Temporal recovery does not access server storage
Projection recovery SHALL enumerate and inspect workflows only through public Temporal client/visibility/history/query APIs. It SHALL NOT inspect Temporal's persistence files, issue server-internal database queries, or attempt to restore Temporal Server state.

#### Scenario: Recovery runs against the local server
- **WHEN** projection recovery is invoked
- **THEN** all Temporal reads use the SDK and configured namespace/address, while the server's own recovery remains outside relay-flow

### Requirement: Temporal retention is namespace-owned
The Temporal namespace used by relay-flow SHALL be configured or verified with workflow-execution retention of at least `max(30 days, completedRunRetentionDays)`. For this MVP the default `completedRunRetentionDays` is 30 days. The Temporal executor SHALL run the existing one-pass local projection retention sweep at startup, after projection rebuild when recovery is requested and before normal pollers. It SHALL NOT claim exact per-workflow Temporal history deletion or implement server retention itself.

#### Scenario: Namespace is initialized
- **WHEN** Temporal init creates the namespace
- **THEN** it requests workflow retention of at least `max(30 days, completedRunRetentionDays)`

#### Scenario: Startup sees insufficient namespace retention
- **WHEN** Temporal startup finds the configured namespace retention below `max(30 days, completedRunRetentionDays)`
- **THEN** serve fails before pollers start and does not lower local retention or recreate workflows

#### Scenario: Closed history expires
- **WHEN** a closed Temporal workflow exceeds namespace retention
- **THEN** relay-flow accepts that the server may remove its history and does not recreate the workflow merely because its historical projection cannot be rebuilt

#### Scenario: Local retention sweep runs
- **WHEN** the Temporal engine starts normally or completes projection recovery
- **THEN** it performs one local projection retention sweep before normal pollers and removes only eligible completed/canceled rows

### Requirement: Temporal engine shutdown is bounded and ordered
The Temporal executor SHALL stop its worker before closing the Temporal client and local projection database. It SHALL make shutdown idempotent and respect the serve process's bounded shutdown window. Partial startup failures SHALL close resources already opened.

#### Scenario: Normal shutdown
- **WHEN** the serve process shuts down
- **THEN** no new Temporal tasks are polled, worker shutdown is attempted, the client is closed, and SQLite is closed after worker shutdown

#### Scenario: Repeated shutdown
- **WHEN** shutdown is called more than once
- **THEN** no worker/client/database close operation is performed in an unsafe repeated order

### Requirement: Temporal execution has no implicit execution retry or history compaction
The Temporal executor SHALL not configure a Temporal execution retry policy or use Continue-As-New in this MVP. Temporal's normal workflow-task retry behavior remains enabled. One supported relay-flow application run SHALL use one Temporal Workflow ID and one execution; explicit restart attempts use the Run Manager's distinct attempt-suffixed application ID. History compaction, Continue-As-New, and workflow-code versioning require a separate approved design.

#### Scenario: Workflow task fails
- **WHEN** a Temporal workflow task fails or is replayed
- **THEN** Temporal retries the workflow task using its normal behavior without starting a second application execution

#### Scenario: Multiple executions share an ID
- **WHEN** Temporal Visibility contains unsupported multiple executions for one relay-flow Workflow ID
- **THEN** recovery selects the running execution or latest retained execution, records an actionable inconsistency, and does not merge histories or create another execution

### Requirement: Temporal-specific forbidden behavior remains absent
The Temporal implementation SHALL NOT add Temporal-specific logic to task routing, task adapters, runner adapters, harness plugins, HTTP handlers, or plugin transport. It SHALL NOT use Temporal as an arbitrary application database, add engine switching, add fallback behavior, add per-ticket workers, or add rollback/compensation.

#### Scenario: Alternative task system
- **WHEN** a non-Jira task adapter is used with the Temporal executor
- **THEN** it receives the same engine-neutral task activities and no Temporal-specific task code is required

#### Scenario: Plugin report delivery
- **WHEN** a runtime plugin submits a report
- **THEN** it uses the existing JSON stdin/Unix-socket path and never connects to Temporal or SQLite directly
