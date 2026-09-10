## Context

The current relay-flow implementation has a durable execution boundary in `internal/run`, with `internal/execution/goworkflows` as the embedded implementation. The embedded engine stores workflow history, queues, timers, and worker state in `state.db`; relay-flow also stores the `relay_*` read model in that file. The current `run.Executor` contract already names Temporal as a permitted replacement, but `cmd/relay-flow/main.go` and `cmd/relay-flow/serve.go` currently depend directly on concrete `goworkflows.Engine` helpers.

The local Temporal Server is available at `localhost:7233`. The Temporal Server repository is a separate service implementation and is not an application dependency. Relay-flow will run one Temporal client and one normal worker for the configured namespace and task queue. During explicit Temporal projection recovery only, a temporary workflow-query-only worker is started sequentially, with non-local activities disabled; it is stopped before the normal worker starts. Temporal Server owns the durable workflow history and its own service recovery. Relay-flow owns only its process, task-system effects, runner effects, harness integration, and the local relay query projection.

This change supports both executors in one binary, but never in one initialized relay-flow installation at the same time. The executor is selected during initialization and is immutable afterward. Existing machine configurations without `executorPlugin` are interpreted as `goworkflows`.

## Goals / Non-Goals

**Goals:**

- Keep `goworkflows` as the default durable executor.
- Add a Temporal executor that implements the existing `run.Executor` and `run.RunQueries` boundaries.
- Use the external local Temporal Server at the configured address and namespace.
- Ask for and create/verify a namespace during Temporal initialization.
- Preserve the existing `relay_*` SQLite projection because current API queries and runtime metadata use it; it remains required by the current run-query API in this MVP.
- Make Temporal history authoritative for graph state, reports, routes, timers, activity progress, and cancellation.
- Rebuild the local projection from Temporal on explicit Temporal recovery without restarting tickets or mutating task-system state.
- Keep the existing `goworkflows` destructive recovery behavior for the embedded backend.
- Keep the current task, runner, harness, report, mailbox, terminal-title, retry, and no-compensation contracts.
- Keep all Temporal-specific types and APIs inside `internal/execution/temporal`.
- Make startup and server composition backend-neutral.

**Non-Goals:**

- Do not import `go.temporal.io/server`, `/home/raj/raj/temporal`, or any Temporal Server implementation package.
- Do not connect to, inspect, modify, back up, restore, or delete Temporal Server's underlying database.
- Do not support Temporal Cloud, TLS, API keys, or remote deployment configuration in this MVP.
- Do not switch executors after initialization, automatically migrate state, or fall back from one executor to another.
- Do not use Temporal as a general-purpose relational/KV database. Only serializable workflow inputs/state, activity results, history, and Temporal visibility/query metadata are allowed.
- Do not delete the existing `relay_runs`, `relay_node_runtime`, `relay_processed_reports`, or `relay_node_sessions` tables in this change.
- Do not treat SQLite projections as graph authority in Temporal mode.
- Do not reset Jira/task-system state, close active terminals, or create a new workflow merely because the local projection is rebuilt.
- Do not create one worker, queue, namespace, or long-lived process per ticket or YAML workflow.
- Do not implement a custom state machine, event bus, generic dependency-injection framework, compatibility layer, or migration framework.
- Do not change report JSON keys, introduce `nodeVisitID` into plugin transport, or allow plugins to access SQLite.
- Do not add workflow-version selection, manual pause/resume, rollback, or compensation.

## Decisions

### 1. Support two executors, with one immutable choice per installation

The existing `run.Executor`/`run.RunQueries` interfaces remain the replacement boundary. The binary contains both `goworkflows` and `temporal` packages. `executorPlugin` is selected during `init`, defaults to `goworkflows`, and is passed to the composition root for construction.

The supported initialization choices are:

```text
goworkflows  embedded go-workflows history and queues in state.db
temporal     external Temporal history and queues; relay_* SQLite projection
```

`init --force` SHALL reject a different executor from the one already initialized. It SHALL also reject changing the Temporal address or namespace for a Temporal installation because those values identify the durable execution service. A singleton installation-identity record in the local state database records the selected executor and, for Temporal, its address and namespace; init writes it atomically with the selected local projection, and serve verifies it before starting any worker. There is no engine-switch command, automatic migration, or fallback. To use another executor, an operator creates a separate relay-flow home and initializes it explicitly; existing state is not imported.

The identity record is a single relay-owned metadata row, for example:

```sql
CREATE TABLE IF NOT EXISTS relay_executor_identity (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    executor_plugin TEXT NOT NULL,
    temporal_address TEXT,
    temporal_namespace TEXT
);
```

It is not a run, route, report, or general data store. For `goworkflows`, the Temporal address/namespace columns are empty; for `temporal`, both are non-empty and must match machine configuration byte-for-byte. A legacy database without this row is accepted only as `goworkflows` when the machine config also selects `goworkflows`; the first successful initialization/startup writes the matching legacy `goworkflows` identity. It cannot be claimed as an initialized Temporal home without a new home and explicit Temporal initialization. If configuration and the marker disagree, startup fails before workers/pollers and never migrates or combines state. Namespace uniqueness across different relay-flow homes is an operator configuration rule; the local flock does not provide a distributed cross-host namespace lock, and this MVP adds no distributed ownership service.

**Rejected:** selecting an executor per workflow, changing executors when the server starts, probing one backend and silently falling back to another, or migrating execution state between engines. These choices make deterministic workflow identity and recovery ambiguous.

### 2. Temporal configuration is collected during init

Machine configuration adds:

```go
ExecutorPlugin    string `yaml:"executorPlugin"`
TemporalAddress   string `yaml:"temporalAddress,omitempty"`
TemporalNamespace string `yaml:"temporalNamespace,omitempty"`
```

Interactive init keeps the existing task/runner/harness selections and adds a selection titled exactly `Select executor`, with `goworkflows` as the default option. When `temporal` is selected, init asks for:

```text
Temporal server address (default localhost:7233)
Temporal namespace/team name (required)
```

Non-interactive init accepts `--executor-plugin`, `--temporal-address`, and `--temporal-namespace` with the same semantics. In a non-interactive invocation, selecting `temporal` requires `--temporal-namespace`; the address defaults to `localhost:7233` when omitted. Temporal namespace is never silently defaulted to `default`; a named namespace is required so an installation cannot accidentally share the local default namespace. Supplying Temporal-only flags while selecting `goworkflows` is an invalid invocation, not an ignored setting. A non-interactive invocation with no executor flag uses the default `goworkflows` and never reads Temporal answers from the three legacy plugin-selection lines.

Init connects through the Temporal SDK. If the namespace does not exist, it registers it with a workflow-execution retention period of `max(30 days, completedRunRetentionDays)`. If it exists, init verifies it is registered and its retention is at least that value; it does not silently alter an externally managed namespace. The namespace is not deleted by relay-flow. For this MVP the default is 30 days.

When `goworkflows` is selected, init does not contact Temporal and Temporal fields are absent/empty. Machine loading applies the same rule: a non-empty Temporal address or namespace with an embedded executor (including an omitted executor that defaults to `goworkflows`) is invalid. When `temporal` is selected, loading applies the `localhost:7233` address default and requires a non-empty namespace. Unknown executor names fail with the registered executor names. Strict machine decoding rejects unknown fields. Init writes the machine configuration and local installation identity only after all selected-plugin and Temporal namespace checks succeed. The config file and SQLite transaction cannot be one cross-store ACID commit, so a crash between them is handled explicitly: serve refuses any missing/mismatched identity before workers/pollers, and rerunning init with the same values completes the initialization; rerunning with different values is rejected. A failed init never reports success or starts serve.

**Rejected:** asking for Temporal settings when the embedded executor is selected, creating a random namespace, using `default` implicitly, changing namespaces on every process start, or letting runtime startup create a missing namespace without an explicit initialization step.

### 3. Keep SQLite as a derived relay-flow projection

The existing relay tables remain the application read model:

```text
relay_runs               run list/get, ticket lookup, active checks, display state
relay_node_runtime       terminal/session/current-visit bindings
relay_processed_reports  fast duplicate-report receipt
relay_node_sessions      registered harness sessions
```

The schema also contains one singleton installation-identity record containing the selected executor and, for Temporal, the configured address and namespace. It exists only to reject an unsupported backend/identity switch; it is not workflow state, a route record, or a user query table.

In `goworkflows` mode, `state.db` contains both go-workflows engine tables and relay tables. In Temporal mode, `state.db` contains relay-flow tables, the identity record, and the SQLite driver/schema needed by the projection. Temporal history is authoritative for the graph; SQLite projection writes are idempotent activities driven by the workflow.

The projection is intentionally not a second state machine. The workflow does not read a selected route from `relay_runs`, and a projection write failure cannot change the selected route. If the projection is unavailable, query/report paths retry or return an actionable error while Temporal remains the execution authority.

The shared projection/database lifecycle is extracted so both engines and `cmd/relay-flow/main.go` use the same schema, installation-identity check, and initialization helpers. There is one schema implementation, not copied `RunProjection` implementations that can drift. An existing database without an identity record is accepted only as a legacy `goworkflows` database; Temporal init requires a new home or an explicitly initialized Temporal projection and never imports embedded engine state.

**Rejected:** removing the projection in this change, using SQLite as a fallback execution engine in Temporal mode, or adding a second durable queue beside Temporal.

### 4. Use the public Temporal Go SDK only

The Temporal engine imports only public SDK packages:

```go
 go.temporal.io/sdk/client
 go.temporal.io/sdk/worker
 go.temporal.io/sdk/workflow
 go.temporal.io/sdk/temporal
 go.temporal.io/api/enums/v1
 go.temporal.io/api/serviceerror
 go.temporal.io/api/workflowservice/v1
```

The normal module pins an exact released SDK version. A local source replacement may be used only in an uncommitted development `go.work`/modfile; a user-specific absolute path is not committed to `go.mod`. The application does not import the Temporal Server module.

The Temporal package construction contract is:

```go
type Dependencies struct {
    Repos           *repo.Registry
    Runner          runner.Runner
    Harness         harness.Harness
    TaskSystem      string
    RetentionDays   int
    Runtime         *run.RuntimePolicy
    TemporalAddress string
    TemporalNamespace string
    Recover         bool
}

func New(path string, deps Dependencies) (*Engine, error)
func (e *Engine) Start(ctx context.Context) error
func (e *Engine) Shutdown(ctx context.Context) error
```

`Recover` is false for normal startup and true only for `serve --recover`; it is consumed by `Start` and is not persisted in workflow input. Temporal address/namespace are validated before client creation and are never read from the workflow definition.

The client is constructed with `client.Options{HostPort: address, Namespace: namespace}`. The worker uses the fixed task queue `relay-flow` and bounded execution slots:

```go
worker.Options{
    MaxConcurrentWorkflowTaskExecutionSize: 10,
    MaxConcurrentActivityExecutionSize:      20,
    MaxConcurrentWorkflowTaskPollers:        2,
    MaxConcurrentActivityTaskPollers:        2,
    WorkerStopTimeout:                       30 * time.Second,
}
```

A Temporal SDK worker is an aggregate worker that hosts workflow and activity implementations; 10 and 20 are execution limits, not ten and twenty worker objects. Poller counts remain bounded SDK settings and are not confused with execution slots.

`worker.Start()` is called only after registration. `Shutdown` stops the worker before closing the Temporal client and local SQLite connection, and is guarded against repeated calls. Worker start/fatal errors are surfaced to the serve lifecycle; a fatal worker callback requests serve shutdown rather than leaving pollers running without a durable worker. The client is always closed on partial startup failure. Normal `Start` creates one aggregate worker; only Temporal recovery uses the sequential temporary query-only worker described in Decision 9.

**Rejected:** importing server code, using raw gRPC generated APIs for normal engine behavior, using `client.Dial(address, namespace)` (not the SDK API), starting Temporal as a child process, or creating workers per run.

### 5. Keep workflow and activity implementations separate

The current `Activities` struct contains an exported `TicketWorkflow` method. Temporal activity-struct registration reflects over exported methods, so registering that struct unchanged would attempt to register the workflow as an activity and panic.

The Temporal package therefore uses:

- a package-level `TicketWorkflow(workflow.Context, run.Start) error` (or a workflow-only type with no activity registration); and
- an `Activities` struct whose exported methods are activities only.

Workflow code invokes registered activity names or nil-receiver method references. It never captures a live client, database handle, task system, runner, harness, function, or interface in a serialized workflow value. Activity inputs and outputs remain serializable value structs.

**Rejected:** registering the current mixed `Activities` struct as-is, persisting dependency objects in workflow input, or letting workflow code call task/runner/harness clients directly.

### 6. Port behavior, not go-workflows syntax

The Temporal interpreter retains the current logical order:

```text
ensure mailboxes
apply start configuration
ensure environment and validate agents
follow start edge
for each work node:
  durable visit side effect
  projection/runtime preparation
  task configuration
  runner/harness setup
  durable report/reconcile wait
  validate report and selected route
  record processed report
  summary comment
  selected-next feedback comment
  complete current mailbox
  apply next-node configuration
  start/reconcile next terminal
apply end configuration
optional runner cleanup
mark completed
```

The following are Temporal-specific translations:

- `workflow.SideEffect(...).Get(&value)` for visit IDs and replay-safe jitter;
- `workflow.NewTimer(...).Get(ctx, nil)` for durable backoff;
- `workflow.GetSignalChannel` and `workflow.Selector` for report/reconcile/cancellation waits;
- `workflow.ExecuteActivity` with `Future.Get(ctx, &result)` and explicit `ActivityOptions`;
- a Temporal `RetryPolicy` with `MaximumAttempts: 1`, because the private typed retry loop owns the common 2-second/exponential/jitter policy;
- `workflow.ActivityOptions{StartToCloseTimeout: 5 * time.Minute, WaitForCancellation: true, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}}` for every normal activity; longer external operations require a separate approved design;
- Temporal application-error type inspection for conflict classification;
- disconnected cleanup followed by a Temporal cancellation error so canceled executions are not accidentally recorded as completed.

The implementation must preserve deterministic map ordering, use `workflow.Now`, avoid native Go timers/goroutines/channels in workflow code, and use Temporal replay-safe APIs. The current `retryLoop` cannot be copied literally because its generic future and signal APIs are go-workflows-specific.

### 7. Preserve immutable workflow snapshots and report fencing

`run.Start` remains the workflow input snapshot. Temporal stores it in the workflow-start history. The Temporal engine never reloads the YAML file to validate an existing run.

After a relay-flow process restart, `SubmitReport` loads the snapshot from the Temporal `WorkflowExecutionStarted` event when it is not in the in-memory cache. It validates the report against that snapshot before signaling the workflow.

Temporal workflow state maintains the current node, current visit, and consumed report IDs. The workflow exposes an internal read-only query used only for projection rebuild and report fencing. The public report wire format remains exactly:

```json
{"runId":"...","node":"...","reportId":"...","report":{...}}
```

The internal Temporal signal names and payload are fixed and are not sent by plugins:

```go
const (
    reportSignalName    = "report"
    reconcileSignalName = "reconcile"
)

type ReportSignal struct {
    ReportID    string
    Node        string
    NodeVisitID run.NodeVisitID
    Report      workflow.Report
}
```

`nodeVisitID` remains internal. The engine obtains the current visit from projection when healthy or from the Temporal query/rebuild path. The workflow verifies node/visit/report identity before applying effects. A duplicate or stale signal may be stored by Temporal but is ignored without graph effects.

The Temporal workflow exposes these fixed internal read-only query contracts; they are not public relay-flow API endpoints:

```go
const (
    runStateQuery     = "relay-flow/run-state-v1"
    reportStateQuery  = "relay-flow/report-state-v1"
)

type NodeRuntimeBinding struct {
    Node        string
    TerminalID  string
    SessionID   string
    NodeVisitID run.NodeVisitID
}

type RunStateSnapshot struct {
    Run             run.Run
    RuntimeBindings []NodeRuntimeBinding // sorted by Node
}

type ReportStateQuery struct {
    ReportID string
}

type ReportStateSnapshot struct {
    CurrentNode        string
    CurrentNodeVisitID run.NodeVisitID
    State              run.State
    Processed          bool
}
```

The Temporal `EnsureNodeRuntime` activity returns a `NodeRuntimeBinding` value after it has reconciled the terminal/session, and the workflow stores that value in its serializable runtime map; the projection update remains a separate idempotent activity. `Run.StartedAt` in the query snapshot comes from `workflow.GetInfo(ctx).WorkflowStartTime`, and `UpdatedAt`/retry timestamps use `workflow.Now(ctx)`; projection rebuild uses Temporal Visibility start/close times for fields not present in the query. The workflow updates this snapshot deterministically at these exact points: initialize `starting` before the first activity; set `running`/current node/visit after the node runtime exists; set `waiting` before waiting for a report; add a consumed report ID before transition effects; set `blocked` and retry metadata when a conflict activity fails; update runtime bindings after terminal/session reconciliation; set `completed` after end cleanup; and set `canceled` after disconnected cancellation cleanup. It retains the consumed report-ID set for workflow-side deduplication; the query returns membership for one requested report ID rather than an unbounded report list.

`run-state-v1` returns the current serializable workflow snapshot and safe runtime bindings for projection rebuild. `report-state-v1` accepts `ReportStateQuery{ReportID}` and answers whether that report ID was consumed, returning the current node/visit and run state for signal fencing. The workflow updates these values deterministically as it advances; query handlers never perform activities or mutate external state. Query results are advisory for the race window: the workflow rechecks node, visit, run state, and report ID when it consumes the signal.

`SubmitReport` handles query/visibility races explicitly: a consumed report or closed run is acknowledged as a duplicate; an open current run is validated and signaled; a Temporal `NotFound` after a concurrent close is rechecked and acknowledged as a duplicate only when the execution is confirmed closed; other Temporal/client failures are returned so the plugin retries. A query failure is never converted into a new run or a task-system mutation.

**Rejected:** reading the current YAML for active runs, putting `nodeVisitID` in plugin JSON, allowing the projection to select a route, or treating a successful signal RPC as permission to skip workflow-side validation.

### 8. Temporal start and duplicate handling

`EnsureRun` uses the app run ID as the Temporal Workflow ID and uses the fixed task queue:

```go
client.StartWorkflowOptions{
    ID:                                  string(start.ID),
    TaskQueue:                           "relay-flow",
    WorkflowIDReusePolicy:               enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
    WorkflowExecutionErrorWhenAlreadyStarted: true,
}
```

The implementation handles these cases explicitly:

1. Verify the local installation identity and load the projection row if present.
2. Describe the Temporal Workflow ID before starting when the projection is missing or nonterminal; a `NotFound` result means no Temporal execution is present, while a running/closed result is reconciled rather than guessed.
3. If the projection row is missing and no Temporal execution exists, insert the `starting` projection row and call `ExecuteWorkflow`.
4. If the process crashes after the projection insert or after Temporal accepted the start, the next call repeats the describe/start decision and never creates a second running execution.
5. If the projection row is present and the Temporal execution is running, return `created=false`.
6. If the projection row is absent but a Temporal execution exists, recover the projection from Temporal metadata/state.
7. If projection says active but Temporal execution is closed, reconcile the terminal status or report an actionable inconsistency; do not blindly start another execution.
8. An already-started error caused by a running execution is not treated as a creation failure.
9. A completed execution is not reused under `ALLOW_DUPLICATE_FAILED_ONLY`.

The SDK-generated Temporal Run ID is not used as relay-flow's run ID. Commands target the app Workflow ID. `ALLOW_DUPLICATE_FAILED_ONLY` is only a server-side guard for an ambiguous failed start; relay-flow never intentionally starts a second execution with the same Workflow ID. The MVP does not configure a Temporal execution retry policy and does not use Continue-As-New; Temporal's normal workflow-task retry behavior remains enabled. One supported app execution maps to one Temporal Workflow ID and one Temporal execution for its lifetime; explicit restart attempts use the Run Manager's distinct attempt-suffixed application ID. A future Continue-As-New/versioning change would require a separate design decision and is out of scope.

Visibility enumeration may return multiple historical executions for a Workflow ID if an operator has created them outside the supported relay-flow path. Recovery selects the currently running execution, or the latest retained execution when none is running, and records an explicit inconsistency instead of merging histories or starting another execution.

### 9. Projection rebuild is the Temporal meaning of `serve --recover`

Normal Temporal startup requires a valid local projection database, then reconnects the worker and allows Temporal to resume open workflows. It does not infer server loss from a missing projection row.

Explicit `serve --recover` in Temporal mode is a local projection rebuild. The Temporal engine receives recovery mode through a `Recover bool` field in its Temporal-specific construction dependencies. Its `Start` method performs this sequence before returning; `serve.go` does not invoke the generic task-system destructive recovery path for Temporal. The normal Temporal worker is not considered started until reconstruction succeeds. In normal mode `Recover` is false and `Start` creates exactly one aggregate worker; in recovery mode it creates the temporary query-only worker, completes the rebuild, stops it, and then creates the normal aggregate worker.

1. acquire the relay-flow lock and stop normal polling;
2. preserve the existing local SQLite file and its `-wal`/`-shm` siblings, when present, as uniquely named timestamped backups;
3. create the relay projection schema and write the Temporal installation-identity record at the normal database path;
4. connect to the configured namespace and enumerate only relay-flow `TicketWorkflow` executions on task queue `relay-flow` through Temporal Visibility, using pagination;
5. decode each execution's immutable `run.Start` metadata from Temporal history or recorded workflow metadata;
6. poll each registered task system read-only to discover active parents carrying `wf:<workflow>` claims, derive their expected Workflow IDs, and run exact `DescribeWorkflowExecution` reconciliation for any active claimed parent not returned by Visibility;
7. start a temporary recovery-only Temporal workflow worker with `worker.Options{LocalActivityWorkerOnly: true}` that registers `TicketWorkflow`, the same activity names needed for workflow replay, and its read-only state query but does not execute non-local activities; the relay-flow workflow uses no local activities, so this worker performs no external effects; use it to query authoritative state for open executions;
8. recreate every non-expired `relay_runs` row from Temporal state; recreate `relay_node_runtime` rows from `RuntimeBindings`; leave `relay_processed_reports` and `relay_node_sessions` empty because Temporal report-state and runtime registration are their authorities; apply local retention after reconstruction. A claimed parent whose exact Workflow ID is `NotFound` is recorded as a missing execution and is not started by recovery; after recovery, normal `EnsureRun` handles that separate claim-before-run case under its normal rules;
9. rediscover current terminals by stable ticket/node title when runtime handles are missing and update the current `relay_node_runtime` binding;
10. stop the recovery-only worker, start the normal Temporal worker against the same namespace and Workflow IDs, and allow pending activities to continue only after projection reconstruction succeeds;
11. start normal pollers only after the rebuild succeeds. If any step fails, stop/close the Temporal client and local database without starting pollers; no task-system mutation or replacement workflow is attempted.

Projection rebuild SHALL NOT:

- reset parent or mailbox task status;
- delete or rewrite comments, labels, branches, worktrees, or code;
- close healthy active terminals;
- create a second Temporal execution;
- choose a new route or new `nodeVisitID`;
- query or modify the Temporal Server database;
- treat a missing optional cache row as execution loss.

The Temporal workflow's state/query and Temporal history, not the rebuilt SQLite row, decide what happens next. The rebuild enumerator filters by the exact registered workflow type `TicketWorkflow`, the fixed task queue `relay-flow`, and the configured namespace; unrelated Temporal workflows are ignored. It uses paginated Visibility results and then performs exact `DescribeWorkflowExecution` reconciliation for every active task-system parent carrying a `wf:<workflow>` claim so Visibility lag cannot cause a duplicate start. It performs a final local retention pass after reconstruction so rows already outside local retention are not resurrected. `relay_processed_reports` and `relay_node_sessions` are intentionally empty after rebuild and are repopulated only by normal report/session paths; `relay_node_runtime` is restored from `RuntimeBindings` and repaired through stable runner discovery. These tables are optimization/cache data; Temporal-side deduplication and stable runner/harness reconciliation remain correct.

If an active Temporal history cannot be read or decoded, recovery fails before pollers start and leaves task-system state untouched. If a closed history has expired under the namespace's 30-day retention, it is not reconstructed as a durable historical run; this is expected server retention behavior, not a relay-flow recovery action.

In `goworkflows` mode, `serve --recover` keeps the existing explicit destructive behavior: discard embedded execution history, preserve task artifacts, reset mailbox state through the adapter, and start eligible tickets from `start`.

**Rejected:** reusing the old go-workflows recovery routine in Temporal mode, resetting tickets because only a projection was lost, using the same Workflow ID to create a fresh Temporal run, or terminating Temporal workflows as part of ordinary projection recovery.

### 10. Namespace retention and ownership

Temporal namespace retention is configured/verified during Temporal init at least as high as the configured `completedRunRetentionDays` and never below 30 days. The Temporal engine SHALL run the existing one-pass local projection retention sweep at startup, after projection rebuild when recovery is requested and before normal pollers; it does not attempt to delete Temporal history through an internal database or unsupported API. Temporal Server owns cleanup and service recovery. For this MVP the configured/default value is 30 days.

The configured namespace is stable for the initialized relay-flow home. Init verifies the namespace retention, and every Temporal serve startup verifies it again before workers/pollers; if it has been lowered below `max(30 days, completedRunRetentionDays)`, startup fails rather than changing server policy or local retention. Namespace deletion, changing the namespace to point at another history, and retention policies shorter than 30 days are unsupported in this MVP.

### 11. Backend-neutral startup wiring

`serve.go` chooses the configured executor:

```text
missing executorPlugin → goworkflows
executorPlugin: goworkflows → goworkflows.New
executorPlugin: temporal   → temporal.New
unknown value            → startup error listing registered executors
```

Both engines receive the same task/runner/harness dependencies and implement the same server-facing methods. `serveDeps` depends on a small local lifecycle/engine interface rather than `*goworkflows.Engine`.

Shared helpers provide:

- SQLite projection initialization and `HasNonterminalRuns` checks;
- mailbox-spec rendering;
- backend-neutral engine lifecycle composition.

No Temporal-specific branch is added to task routing, task adapters, runner adapters, harness plugins, or HTTP handlers.

### 12. Dependency and toolchain policy

The application keeps the exact `go-workflows` pin and adds `go.temporal.io/sdk v1.48.0` as the initial released SDK pin. The project toolchain is raised to Go `1.26.0`; CI and release configuration must use the same toolchain. If the compatibility spike proves `v1.48.0` unsuitable, implementation stops and this change's pin/design are revised before another SDK is selected. The checked-out SDK source at `/home/raj/raj/sdk-go` may be used for a local compatibility investigation through a non-committed replacement, but the committed module cannot depend on that machine path. The installation-identity marker is the only new local metadata needed for executor immutability; it is not a migration or an execution-state store.

The Temporal Server source checkout is used only to understand the external service version/API and is never imported. Local tests connect to `localhost:7233` or use Temporal's SDK test environment; they do not build the server repository.

### 13. Testing strategy

Before the production port, add a Temporal compatibility spike using the exact selected SDK and the running local server. It must cover:

- client dialing with address and namespace;
- namespace verification;
- workflow/activity registration;
- explicit Workflow IDs and duplicate start handling;
- activity execution and max-attempt-one behavior;
- signal delivery and duplicate/stale signals;
- durable timers and replay-safe side effects;
- worker stop/reconnect with a workflow waiting for a report;
- cancellation cleanup that ends in Temporal canceled state;
- history retrieval and decoding of the `run.Start` snapshot;
- projection rebuild queries.

Temporal engine tests use fakes behind existing task/runner/harness interfaces and a real Temporal test server for persistence semantics. They do not use the Temporal Server's internal database. Default `go test ./...` remains self-contained through the SDK test environment; tests that require the external `localhost:7233` service are explicitly gated by `RELAY_FLOW_TEMPORAL_LIVE=1` and use a dedicated named namespace plus unique Workflow IDs. Existing go-workflows behavior tests remain for the default backend; shared behavior tests run against both engines where the test environment is explicitly available.

All tests use unique Workflow IDs/task queues or a dedicated test namespace. The developer's `default` namespace is not assumed to have the required retention or isolation for committed tests.

## Risks / Trade-offs

- **Projection rebuild is more work than the current go-workflows recovery path.** → Keep it explicit and backend-specific; rebuild only the read model and never reset task-system state.
- **Temporal Visibility is eventually consistent.** → Use it for enumeration, use Temporal history/query for authoritative state, and do not use visibility alone to select graph routes.
- **Temporal history can grow during indefinite retries and HITL waits.** → Keep the MVP serial and bounded in concurrency; Continue-As-New/history compaction is explicitly deferred and must be designed before very long-lived production runs.
- **Temporal SDK activity errors differ from go-workflows errors.** → Add a typed error-classification spike and inspect Temporal `ApplicationError` types.
- **Temporal cancellation can mark a workflow completed if cleanup returns nil.** → Return a Temporal cancellation error after disconnected cleanup and test final server status.
- **Temporal Server retention is namespace-wide and eventual.** → Configure/verify 30-day retention at init; relay-flow does not claim exact per-run history deletion.
- **A worker code change can break replay of open histories.** → Keep the generic workflow function stable, require replay tests, and do not introduce workflow-code changes without an explicit versioning/drain decision.
- **A local SQLite projection can be lost while Temporal remains healthy.** → Rebuild from Temporal; missing cache data must never create a second Workflow ID or mutate task state.
- **The two backends have different crash-test mechanics.** → Test embedded recovery through SQLite restart and Temporal recovery through worker/client restart against the external service.

## Migration Plan

This change does not migrate execution state between engines. Existing installations remain `goworkflows` unless newly initialized with `temporal`. Existing configurations without `executorPlugin` default to `goworkflows`.

For a new Temporal installation:

1. Ensure the local Temporal Server is running at the desired address.
2. Run `relay-flow init` and select `temporal`.
3. Enter the Temporal address and a unique namespace/team name.
4. Let init create or verify the namespace with 30-day retention.
5. Register repositories and submit workflows normally.

Normal relay-flow restart reconnects to the same Temporal namespace and resumes open workflows. `serve --recover` in Temporal mode only rebuilds local projection state. It does not restart workflow executions.

To use another executor, initialize a separate relay-flow home. There is no in-place switch, migration, fallback, or rollback path between executors.

## Open Questions

None. The following are fixed for this change:

- `goworkflows` is the default executor.
- Executor selection is immutable after init.
- Temporal address and namespace are collected during init and remain tied to that initialized home.
- Temporal is local-only at `localhost:7233` by default; Cloud is out of scope.
- Temporal namespaces use 30-day retention.
- SQLite relay projections remain in scope.
- Temporal history is execution authority.
- Temporal `serve --recover` rebuilds projection only.
- `goworkflows` `serve --recover` retains destructive from-`start` semantics.
- No direct Temporal Server database access or server-module import is allowed.
