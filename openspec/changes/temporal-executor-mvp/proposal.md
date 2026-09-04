## Why

Relay-flow currently has a durable-execution boundary, but its only implementation is the embedded `go-workflows` engine backed by SQLite. The MVP needs to run the same ticket graph through the already available local Temporal Server while preserving the existing engine-neutral contracts, task-system behavior, runner behavior, report wire format, and KISS/YAGNI constraints.

Temporal Server is an external service and will own Temporal workflow history, timers, activity queues, replay, and recovery. Relay-flow will retain its small `relay_*` SQLite projection for fast run queries and local metadata, but that projection must never become the execution authority.

## What Changes

- Add a selectable `temporal` durable executor beside the existing `goworkflows` executor.
- Keep `goworkflows` as the default executor.
- Select the executor once during `relay-flow init`; executor switching is not supported, including through `init --force`.
- When Temporal is selected during init, ask for the Temporal server address and namespace/team name, create or verify that namespace, and configure 30-day namespace retention.
- Use the local Temporal Server only for this MVP. Do not add Temporal Cloud configuration.
- Add `internal/execution/temporal` implementing the existing `run.Executor` and `run.RunQueries` boundaries.
- Keep Temporal Server external; import only the Temporal Go SDK, never `go.temporal.io/server` or the server's internal database.
- Use Temporal workflow history as the authority for graph progression, accepted reports, selected routes, timers, retries, and cancellation.
- Retain the existing `relay_runs`, `relay_node_runtime`, `relay_processed_reports`, and `relay_node_sessions` tables where they are useful. In Temporal mode they are a derived read model/cache, not workflow state.
- Add only the small installation-identity marker needed to prevent an initialized home from switching executor or Temporal durable identity; this marker is not workflow state or a query projection.
- Add explicit Temporal projection rebuild behavior for `serve --recover`: use a temporary workflow-query-only worker, rebuild local projections from Temporal history/state, and never reset tickets, mailboxes, terminals, or workflow progress.
- Preserve the existing destructive from-`start` recovery behavior for `goworkflows`.
- Adapt worker registration, futures, timers, selectors, side effects, cancellation, activity options, error classification, workflow snapshot loading, and report signaling to the Temporal SDK rather than copying `go-workflows` APIs literally.
- Make startup wiring backend-neutral while preserving one relay-flow process, one Temporal namespace, one Temporal task queue, and bounded worker concurrency.
- Pin `go.temporal.io/sdk v1.48.0` for normal builds; any local SDK source replacement is development-only and must not make the committed module path machine-specific.

**BREAKING** The machine configuration and init flow gain executor and Temporal settings. Temporal-mode recovery semantics differ from embedded SQLite-mode recovery. Existing configs without an executor value default to `goworkflows`.

## Capabilities

### New Capabilities

- `temporal-executor`: Run relay-flow workflows through an external Temporal Server, retain a derived SQLite projection, and rebuild that projection from Temporal when local SQLite state is lost.

### Modified Capabilities

- `durable-run-execution`: Support both the embedded `goworkflows` executor and the Temporal executor, with backend-specific restart and recovery semantics.
- `workflow-repo-management`: Add immutable executor selection, Temporal address/namespace initialization, namespace creation/validation, and 30-day Temporal retention.
- `structured-node-reporting`: Make report acknowledgement/deduplication refer to the selected durable executor, with Temporal workflow history as authority in Temporal mode.
- `node-mailboxes`: Make explicit recovery backend-specific so Temporal projection recovery does not reset mailbox state.

## Impact

Affected code includes `go.mod`, machine configuration and init parsing, `cmd/relay-flow/serve.go`, `cmd/relay-flow/main.go`, shared SQLite projection/database lifecycle code, and a new `internal/execution/temporal` package. Existing `internal/execution/goworkflows` behavior remains available as the default backend.

The Temporal implementation will use `go.temporal.io/sdk/client`, `worker`, and `workflow` against `localhost:7233`. The Temporal Server repository at `/home/raj/raj/temporal` is reference/runtime infrastructure only and is not a Go dependency. The existing Unix socket API, task system, runner, harness, workflow YAML, report JSON, mailbox lifecycle, terminal titles, and no-compensation policy remain unchanged.

The local SQLite database is not removed in this change. It remains required for the current relay-flow query/projection path unless a later change replaces those queries with Temporal-native visibility and query handlers. Losing that projection must never cause a second Temporal workflow or a task-system reset. A small singleton installation-identity record prevents supported commands from changing the selected executor or Temporal address/namespace for an existing home.
