## 0. Scope guardrails and source-of-truth alignment

- [x] 0.1 Read `proposal.md`, `design.md`, every spec under this change, `docs/structs-methods-interfaces.md`, `docs/feature-tracker.md`, and the archived go-workflows change before implementation.
- [x] 0.2 Treat the following as fixed acceptance constraints: `goworkflows` is the default; executor selection is immutable; Temporal address and namespace are initialization identity; Temporal is local-only; namespace retention is at least 30 days; SQLite relay projections remain; Temporal history owns Temporal execution; Temporal `serve --recover` rebuilds projection only; goworkflows `serve --recover` restarts from `start`.
- [x] 0.3 Add a scope audit note listing explicit prohibitions: no Temporal Server module/import, no direct Temporal database access, no Temporal Cloud, no engine switching/migration/fallback, no arbitrary Temporal database usage, no removal of relay tables, no ticket reset during Temporal projection recovery, no per-ticket workers, no custom state machine/event bus/DI, no report-wire changes, no plugin SQLite access, and no rollback/compensation.
- [x] 0.4 Verify the local reference paths and runtime assumptions without changing external repositories: SDK at `/home/raj/raj/sdk-go`, Server at `/home/raj/raj/temporal`, Temporal endpoint at `localhost:7233`, and a dedicated named namespace rather than the existing one-day-retention `default` namespace.

## 1. Toolchain and dependency preparation

- [x] 1.1 Pin and record `go.temporal.io/sdk v1.48.0` for the compatibility spike and normal module; if the spike proves it unsuitable, stop and update this change before selecting another version. Do not use an unpinned SDK checkout in the committed module.
- [x] 1.2 Raise the application toolchain to the selected minimum (currently planned as Go `1.26.0`) and update CI/release toolchain configuration consistently.
- [x] 1.3 Use the local SDK checkout only for exploratory source/compile work through an uncommitted `go.work` or modfile replacement; acceptance tests and the committed module use the pinned `v1.48.0`, and the committed `go.mod` has no machine-specific absolute replacement.
- [x] 1.4 Retain the exact `go-workflows` dependency and verify both executor packages can compile in the same module without replacing or removing the embedded backend.
- [x] 1.5 If the Temporal executor owns a SQLite projection without importing go-workflows, add and pin the SQLite driver explicitly rather than relying on an indirect engine dependency.

## 2. Compatibility spike before production implementation

- [x] 2.1 Add a Temporal compatibility spike using the exact selected SDK against a disposable namespace on the running local Temporal Server, gated by `RELAY_FLOW_TEMPORAL_LIVE=1`; default `go test ./...` must remain self-contained. Do not import or build `/home/raj/raj/temporal`.
- [x] 2.2 Prove SDK client dialing with `client.Options{HostPort, Namespace}` and verify namespace registration/description and retention of at least `max(30 days, completedRunRetentionDays)` behavior.
- [x] 2.3 Prove package-level workflow registration, activity-struct registration, worker startup, fixed task queue, two workflow pollers, two activity pollers, five-minute activity attempt timeout, and workflow/activity execution limits of 10 and 20.
- [x] 2.4 Prove explicit Workflow ID start, `WorkflowIDReusePolicy`, `WorkflowExecutionErrorWhenAlreadyStarted`, and the `created`/existing-run mapping for running, failed, canceled, and completed executions.
- [x] 2.5 Prove signal delivery, stale/duplicate signal handling, durable timers, replay-safe side effects, and workflow history retrieval/decoding of a `run.Start` snapshot.
- [x] 2.6 Prove worker/client stop and reconnect: an open workflow waiting for a report must resume from Temporal history after the relay worker process is restarted.
- [x] 2.7 Prove cancellation cleanup with disconnected workflow context, `WaitForCancellation`, running-activity cancellation behavior, five-minute attempt timeout, and final Temporal status `canceled` rather than `completed`.
- [x] 2.8 Prove Temporal activity error serialization/classification for transient and `retry.ConflictError` failures, including the Temporal `ApplicationError` type observed by workflow code.
- [x] 2.9 Prove the fixed `relay-flow/run-state-v1` and `relay-flow/report-state-v1` internal queries and exact serializable response shapes needed to rebuild current node, visit, state, retry status, runtime bindings, and processed-report identity after local projection loss; queries must be read-only and advisory.
- [x] 2.10 Keep the spike as a permanent regression test/record, with unique IDs, a dedicated named namespace, explicit live-test gating, and no dependency on the user's default namespace or a Temporal CLI installation.

## 3. Engine-neutral projection and configuration tests first

- [x] 3.1 Add tests proving the existing `relay_runs`, `relay_node_runtime`, `relay_processed_reports`, and `relay_node_sessions` schema and query behavior are shared by both executors.
- [x] 3.2 Add tests for the singleton `relay_executor_identity` record, including SQLite transaction initialization, legacy missing-marker handling as goworkflows-only, mismatch rejection, crash/mismatch recovery by rerunning identical init values, and preservation during projection rebuild.
- [x] 3.3 Extract one shared projection/database lifecycle implementation; do not maintain copied projection schemas in `goworkflows` and `temporal`.
- [x] 3.4 Add tests proving Temporal projection writes are derived/idempotent and never select routes, reports, or graph progress.
- [x] 3.5 Add machine-config tests for `executorPlugin`, `temporalAddress`, and `temporalNamespace`, including legacy omission defaulting to `goworkflows`, `localhost:7233` address default for Temporal, required Temporal namespace, rejection of non-empty Temporal fields in embedded mode, strict unknown-field rejection, and conditional validation.
- [x] 3.6 Add init tests for default embedded selection, exact `Select executor` prompt, explicit Temporal selection, address default `localhost:7233`, required namespace/team input, required non-interactive `--temporal-namespace`, legacy three-line embedded input behavior, equivalent non-interactive flags, namespace creation/verification, and 30-day retention.
- [x] 3.7 Add init tests proving Temporal settings are not requested/contacted for embedded mode and partial config is not written when Temporal setup fails.
- [x] 3.8 Add tests proving `init --force` cannot change executor, Temporal address, or Temporal namespace and cannot migrate or fallback between engines.
- [x] 3.9 Add tests proving normal startup and `--recover` select exactly the configured executor and never run both engines for one initialized home.
- [x] 3.10 Add report-path tests proving acknowledgement and duplicate handling use the selected durable executor, with Temporal history/state remaining authoritative when local receipts are absent.
- [x] 3.11 Add mailbox recovery tests proving goworkflows recovery resets mailboxes while Temporal projection recovery does not mutate mailbox tasks or create missing mailboxes.

## 4. Temporal projection-rebuild behavior tests first

- [x] 4.1 Add a test fixture with open and retained closed Temporal `TicketWorkflow` executions containing immutable `run.Start` metadata and authoritative current state.
- [x] 4.2 Add projection-rebuild tests for a missing local database: create a fresh relay projection, enumerate only `TicketWorkflow` executions assigned to task queue `relay-flow` with pagination through public SDK APIs, reconcile active task-system parents by exact Workflow ID to cover Visibility lag, restore run rows, apply local retention, and start no replacement workflows.
- [x] 4.3 Add tests proving projection rebuild restores current node/visit/state from the fixed internal query/history contract and does not reset selected routes, report history, timers, or activity progress.
- [x] 4.4 Add tests proving projection rebuild preserves task-system state, mailbox history, labels, comments, worktrees, branches, code, and healthy runner terminals.
- [x] 4.5 Add tests proving rebuild writes `relay_runs` and available runtime bindings, leaves `relay_processed_reports`/`relay_node_sessions` empty, and missing local cache rows do not cause duplicate graph effects because Temporal state and normal runner/harness reconciliation remain authoritative.
- [x] 4.6 Add tests proving Temporal engine recovery mode starts a temporary workflow-only/query-capable worker with `LocalActivityWorkerOnly: true`, answers read-only state/report queries without running non-local activities, then stops it and starts the normal worker only after reconstruction succeeds.
- [x] 4.7 Add tests proving unreadable active Temporal history aborts recovery before pollers start without task-system mutation or replacement execution.
- [x] 4.8 Add tests proving expired closed Temporal history is not recreated, recently started workflows missed by Visibility are found by exact claimed-parent reconciliation, and no direct Temporal persistence/database operation is attempted.
- [x] 4.9 Add tests proving `serve --recover` rebuilds Temporal projection even when the local projection is readable, preserving a backup and performing no task reset or replacement start.
- [x] 4.10 Add tests proving a claimed parent with no exact Temporal Workflow ID is recorded as missing during projection recovery and is handled only by normal post-recovery claim-before-run logic.
- [x] 4.11 Add Temporal integration tests for explicit restart attempts: the new attempt uses the Run Manager's distinct attempt-suffixed Workflow ID, while stale reports/cancel/reconcile requests for the old attempt cannot affect it.

## 5. Temporal engine implementation

- [x] 5.1 Create `internal/execution/temporal` with engine-neutral `Dependencies`, Temporal client/worker lifecycle, shared projection, and the existing `run.Executor`/`run.RunQueries` methods.
- [x] 5.2 Implement Temporal client creation with `client.Options{HostPort: cfg.TemporalAddress, Namespace: cfg.TemporalNamespace}` and fixed task queue `relay-flow`.
- [x] 5.3 Implement Temporal worker registration with one package-level generic workflow and an activity-only struct; ensure `TicketWorkflow` is not accidentally registered as an activity and make runtime-setup activity results carry the serializable runtime bindings needed by `run-state-v1`.
- [x] 5.4 Implement bounded worker options, two workflow/two activity pollers, fixed five-minute activity attempt timeout, no Temporal execution retry policy, native activity retry maximum one, `WaitForCancellation`, fatal-worker shutdown signaling, idempotent worker shutdown, client close, and partial-startup cleanup.
- [x] 5.5 Port the interpreter semantically to Temporal selectors, signal channels, timers, side effects, futures, workflow time, and disconnected contexts; do not copy go-workflows generic APIs literally.
- [x] 5.6 Preserve deterministic workflow behavior: stable map ordering, no native timers/channels/goroutines in workflow code, replay-safe visit IDs/jitter, and no live dependency objects in workflow values.
- [x] 5.7 Implement Temporal application-error classification for conflict/blocking and the shared durable retry policy without leaking Temporal types through `run.Executor`.
- [x] 5.8 Implement Temporal workflow snapshot retrieval from `WorkflowExecutionStarted` history after process restart; never reload current YAML for an existing run.
- [x] 5.9 Implement Temporal `EnsureRun` with explicit Workflow ID, duplicate/existing execution handling, Visibility/Describe reconciliation, projection insertion/reconciliation, one-execution-per-supported-ID behavior, explicit restart-attempt fencing, and no implicit retry or accidental restart of closed executions.
- [x] 5.10 Implement Temporal `SubmitReport` with snapshot validation, fixed report-state query, current-state/visit fencing, concurrent-close recheck, Temporal signal persistence-before-ack, stale/duplicate acknowledgement, and Temporal-side deduplication.
- [x] 5.11 Implement Temporal `CancelRun` with reason propagation, cancellation-state projection, disconnected cleanup, final canceled execution status, and attempt-ID fencing for stale cancellation requests.
- [x] 5.12 Implement the fixed Temporal internal workflow state/query contracts (`relay-flow/run-state-v1` and `relay-flow/report-state-v1`) with exact serializable request/response shapes, deterministic run/runtime snapshot updates after each listed transition point, advisory query semantics, and report-ID membership while keeping the public report wire unchanged.
- [x] 5.13 Implement Temporal recovery mode in engine dependencies/lifecycle: start the temporary workflow-only/query-capable worker with `LocalActivityWorkerOnly: true` and non-local activities disabled during projection reconstruction, stop it, and start the normal worker only after reconstruction succeeds; normal mode starts exactly one aggregate worker.
- [x] 5.14 Implement Temporal projection rebuild using only public visibility/history/query SDK APIs, exact workflow-type/task-queue filtering, pagination, active claimed-parent reconciliation for Visibility lag, local-retention application, and no Temporal Server storage access.
- [x] 5.15 Implement Temporal namespace setup/verification during init with retention of at least `max(30 days, completedRunRetentionDays)` and actionable errors for unavailable or unsuitable namespaces.

## 6. Preserve and share the existing go-workflows executor

- [x] 6.1 Keep `internal/execution/goworkflows` behavior and its embedded SQLite engine unchanged except for extraction of shared projection/database helpers.
- [x] 6.2 Preserve existing goworkflows activity ordering, retry policy, cancellation cleanup, retention sweep, and destructive `serve --recover` semantics.
- [x] 6.3 Ensure legacy configurations without `executorPlugin` continue selecting goworkflows and do not contact Temporal.
- [x] 6.4 Run the existing goworkflows behavior suite after projection/config extraction and fix only regressions caused by the shared code move.
- [x] 6.5 Verify existing report, mailbox, task-system, runner, and harness contracts remain unchanged for goworkflows while their durable-engine acknowledgement/recovery wording is backend-neutral.

## 7. Backend-neutral startup, CLI, and recovery wiring

- [x] 7.1 Replace concrete `*goworkflows.Engine` fields in `cmd/relay-flow/serve.go` with a small backend-neutral lifecycle/query interface that both engines implement.
- [x] 7.2 Move init database/`HasNonterminalRuns` calls and mailbox-spec rendering behind shared or selected-backend-neutral helpers; remove unconditional goworkflows references from Temporal paths.
- [x] 7.3 Wire executor selection in `serve` with default goworkflows, explicit Temporal construction, unknown-name errors, and no fallback.
- [x] 7.4 Wire `serve --recover` so goworkflows uses destructive from-`start` recovery while Temporal performs non-destructive projection rebuild.
- [x] 7.5 Keep one flock, one selected executor, one Temporal namespace/task queue, shared pollers, shared Run Manager, and unchanged Unix-socket API.
- [x] 7.6 Add integration tests proving workflow submission, repo polling, run manager, report endpoint, cancellation, and query APIs work with either executor through the same engine-neutral services.
- [x] 7.7 Add tests proving the installation-identity marker rejects an executor/address/namespace change rather than migrating or combining state from both engines.

## 8. Retention and operational verification

- [x] 8.1 Configure/verify Temporal namespace retention at least `max(30 days, completedRunRetentionDays)` and remove any attempt to call go-workflows history deletion APIs from the Temporal path.
- [x] 8.2 Test the one-pass startup local projection retention for completed/canceled rows while preserving active rows and leaving Temporal Server history cleanup to the namespace policy.
- [ ] 8.3 Test normal Temporal restart, worker reconnect, report delivery after restart, activity retry, cancellation, and projection rebuild against the local server.
- [ ] 8.4 Test that Temporal Server unavailability produces an actionable failure/retry and never starts the embedded backend as fallback.
- [x] 8.5 Confirm no Temporal Cloud/TLS/API-key configuration or server lifecycle management has entered this MVP.

## 9. Final verification and scope audit

- [x] 9.1 Run `gofmt`, `go test ./...`, `go test -race ./...`, `go vet ./...`, and the plugin test suite.
- [ ] 9.2 Run the Temporal compatibility/integration tests using a dedicated namespace and unique workflow IDs; do not depend on `default` namespace retention or a manually installed Temporal CLI.
- [x] 9.3 Run `git diff --check` and GitNexus change detection; verify only the approved config, shared projection, executor, wiring, test, and documentation files changed.
- [x] 9.4 Audit the final diff for forbidden behavior: server-module import, direct Temporal DB access, Cloud code, engine switch/migration/fallback, arbitrary Temporal storage, deleted relay tables, Temporal task reset during projection recovery, per-ticket workers, custom queue/state machine/event bus/DI, report-wire changes, plugin SQLite access, and rollback/compensation.
- [x] 9.5 Verify the final implementation still has `goworkflows` as the default and that a Temporal installation asks for address/namespace during init, creates/verifies its named namespace, and uses 30-day retention.
- [x] 9.6 Verify Temporal `serve --recover` rebuilds only the local projection and never restarts a ticket or creates a second Temporal workflow.
