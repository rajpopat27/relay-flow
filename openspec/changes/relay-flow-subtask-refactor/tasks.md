# Tasks

## Guidelines (binding for every task)

- **Source of truth:** `docs/structs-methods-interfaces.md`, `docs/feature-tracker.md`, this change's `proposal.md`, `design.md`, and `specs/*/spec.md`. If anything is ambiguous, contradictory, or unfamiliar, these documents are the final decision-makers — re-read them before acting, and if they genuinely don't answer it, STOP and ask the user instead of guessing.
- **Exactness:** match the structs, methods, interfaces, names, and wire keys in `docs/structs-methods-interfaces.md` exactly. Never edit the two `docs/` files.
- **KISS/YAGNI:** implement only what each task says. No speculative infrastructure, compatibility layers, fallbacks, migrations, or extra abstractions. This is a beta breaking rewrite — deleting old code is correct.
- **Unstable state is expected:** after section 1 the build fails; after section 3 tests are red until section 4. Never reorder tasks or "fix" unrelated red to go green early.

This is a breaking clean replacement of the current per-workflow, in-memory daemon. The design (design.md), specs (specs/), and docs (`docs/structs-methods-interfaces.md`, `docs/feature-tracker.md`) already fix the contracts, struct/method signatures, package layout, and every rejected alternative. Follow them exactly. Do not add fallbacks, compatibility layers, migrations, or infrastructure they do not name. Almost every decision is already recorded; ask the user only when the source-of-truth documents genuinely conflict or omit a required behavior.

Ordering is: removal → tests (behavior from specs) → implementation → verification. Removal tasks come first so no fallback code survives; tests encode spec behavior before coding; implementation makes them pass; verification runs last.

## 1. Remove legacy code (breaking, no fallback)

- [ ] 1.1 Delete the per-workflow daemon and its tests: remove `internal/daemon/` entirely (daemon.go, daemon_test.go). The daemon's PollLoop/PollOnce/dispatch/nudge logic is replaced by Repo Pollers, the Ticket Router, the Run Manager, and the durable interpreter.
- [ ] 1.2 Delete the legacy Jira/tasks service: remove `internal/tasks/` entirely (tasks.go, tasks_test.go, `internal/tasks/jira/`). Its poll/status/report orchestration is replaced by the repo-bound `task.System` contract and ordered durable activities.
- [ ] 1.3 Delete the legacy config package and its tests: remove `internal/config/` (machine.go, schema.go, schema_test.go, demo_test.go). Its workflow schema/validation moves to `internal/workflow`; machine values/I/O move to the new `internal/config`.
- [ ] 1.4 Delete the legacy discovery package: remove `internal/discovery/` (discovery.go, discovery_test.go). Repo discovery/validation moves to `internal/repo` via the runner.
- [ ] 1.5 Delete the legacy runner orchestration: remove `internal/runner/` (runner.go, runner_test.go, `internal/runner/orca/`, including its README.md). Spawn/buildCommand is replaced by `harness.BuildCommand` plus `runner.EnsureTerminal`.
- [ ] 1.6 Delete the legacy OpenCode helper: remove `internal/opencode/`. Its existence check and launch behavior move under `internal/harness/opencode`.
- [ ] 1.7 Delete the legacy plugin report path: remove `plugin/report-status.ts`. Report delivery is the JSON `relay-flow report` command; no plugin writes to SQLite and no flag-based report survives.
- [ ] 1.8 Remove the legacy server and client: delete `internal/server/` (server.go, client.go, server_test.go) so the new server is rebuilt on the new services, not patched.
- [ ] 1.9 Remove the legacy entrypoint: delete `cmd/relay-flow/main.go` contents that wire the old daemon/config/server; the file is rewritten later as a thin command parser only.
- [ ] 1.10 Delete the top-level CLI wrappers: remove `internal/acli/` and `internal/orcacli/`. Their replacements are created later as `internal/task/jira/acli` and `internal/runner/orca/orcacli`; nothing references them after 1.1–1.9, so delete them outright.
- [ ] 1.11 After removals the module is intentionally broken: `go build ./...` MUST fail until section 4 rebuilds the code. Do not add stub or compatibility shims to make it pass early.

## 2. Foundations and toolchain

- [ ] 2.1 Upgrade the Go toolchain to `1.24.6` in `go.mod` and CI/release config; verify `go build` and release packaging work on the new toolchain.
- [ ] 2.2 Record the exact dependency versions to be used, but do NOT add them to `go.mod` yet (unused requires break `go mod tidy`/`go build`): the pins are `github.com/cschleiden/go-workflows` exactly `v1.4.2` (with its embedded `modernc.org/sqlite` backend), `github.com/google/renameio/v2`, and `github.com/charmbracelet/huh`. Add no merge, CLI-framework, ORM, validation, UUID, DI, or state-machine libraries. These enters `go.mod` in section 4 at these exact versions when first imported (go.sum pins them then).
- [ ] 2.3 Complete the go-workflows compatibility spike before the main rewrite: a small throwaway program/test covering SQLite startup, explicit instance IDs, duplicate start handling, separate Workflow/Activity workers, signals, durable timers, disconnected-context cancellation cleanup, and history inspection. Confirm each behavior passes, then delete the spike code — no written record is kept. If v1.4.2 fails any required behavior, stop and revise the design; do not silently switch versions.
- [ ] 2.4 Create `internal/paths`: `Paths` struct, `ForUserHome`, and `Ensure` as documented in docs/structs-methods-interfaces.md. All artifacts live under `~/.relay-flow` (`0700`): `config.yaml`, `state.db`, `server.sock`, `server.lock`, `server.log`, `plugin.log`, `workflows/<name>.yaml`.
- [ ] 2.5 Create `internal/identity`: `RunID` and `NodeVisitID`, `NewRunID(repo, workflow, ticket)` with deterministic delimiter-safe encoding, `NewNodeVisitID()`. Both are opaque to consumers; this package imports no other relay-flow package.
- [ ] 2.6 Create `internal/retry`: `Kind`, `Failure`, `BackoffPolicy`, `DefaultBackoffPolicy` (initial 2s, factor 2, jitter 0.2, max 5m), `ConflictError`, `Classify`, `BackoffPolicy.Delay(attempt, random)`, and `Do`. Backoff calculation is shared; adapters only supply their waiting mechanism.

## 3. Behavior tests first (from specs, before implementation)

Write these tests against the contracts in docs/structs-methods-interfaces.md and the scenario language in specs/. They are written before the implementation exists, so they MUST fail to compile or run until section 4 lands; that is expected. Do not add stubs or shims to turn them green early. Keep tests scoped to behavior, not internals.

### Workflow definition and reporting (specs/workflow-definition, specs/structured-node-reporting)

- [ ] 3.1 `internal/workflow` parse/validate tests: strict root schema, reserved `start`/`end` rules, work-node rules (agent+description required, routes for every permitted outcome), unique repos, route targets must exist and cannot target `start`, nudge template variables validated at submission, and rejection of unknown fields.
- [ ] 3.2 Report contract tests: every section required, `None` means intentionally empty, `STATUS` is success|failure, `NEXT STEP` must name exactly one configured route for that status, and when `NEXT STEP` is `end` all feedback fields must be `None`.
- [ ] 3.3 `Workflow.ValidateReport` tests: pure, called for both agent and HITL nodes, rejects a next step not configured for the reported status.
- [ ] 3.4 Task-config merge tests in `internal/config`: `Merge` applies root→repo→workflow→node precedence, maps merge recursively, later scalar/list replaces, omitted keys inherit, explicit YAML `null` rejected.
- [ ] 3.5 Jira transition defaults tests: omitted transitions default to parent `In Progress` at `start`, mailbox `In Progress` at work nodes, parent `Done` at `end`, and an omitted work-node parent transition leaves the parent unchanged.

### Integration contracts (specs/integration-contracts)

- [ ] 3.6 Task-system contract tests (fake adapter): `Poll` returns only active parents (never mailbox subtasks); `EnsureMailboxes` finds existing, creates only missing, returns the complete node→mailbox map; `ApplyTaskConfig`, `CompleteMailbox`, `HasComment`, `Comment`, `ResetForRecovery` are separate primitives; `CompleteMailbox` performs no comment/routing/runner work.
- [ ] 3.7 Runner contract tests (fake runner): terminal title is exactly `<ticket>:<node>` and never contains `nodeVisitID`, workflow, or agent; `FindTerminal` returns only a live usable terminal; `CloseTerminals` preserves the environment; `CleanupRun` removes all runner-owned run resources; `EnsureEnvironment`/`EnsureTerminal` are idempotent.
- [ ] 3.8 Harness contract tests (fake harness): `ValidateAgent`, `FindSession` by stable title, and `BuildCommand` returns a structured `runner.Command` with env `RELAY_FLOW_RUN_ID`, `RELAY_FLOW_NODE_VISIT_ID`, `RELAY_FLOW_WORKFLOW`, `RELAY_FLOW_REPO`, `RELAY_FLOW_TICKET`, `RELAY_FLOW_NODE`, `RELAY_FLOW_NODE_TYPE`, `RELAY_FLOW_NUDGE_PROMPT`, `RELAY_FLOW_NEXT_STEPS_JSON`; the harness never manipulates runner state.
- [ ] 3.9 Plugin selection tests: one task/runner/harness plugin per machine selected at root; workflows cannot override plugin types; unknown configured plugin name returns an error listing registered names; duplicate factory registration panics.

### Repo workflow routing (specs/repo-workflow-routing)

- [ ] 3.10 Repo Poller tests: one poller per registered repo, all pollers use root `pollIntervalSeconds` (default 15), a semaphore caps concurrent polls at 10, pollers only fetch batches and call the batch handler (no matching/claiming).
- [ ] 3.11 Ticket Router tests (pure): multiple `wf:*` claims → `InvalidClaimError`; exactly one claim resolves directly without re-running filters and an unknown/unregistered claim → `InvalidClaimError`; zero filter matches → `ErrNoMatch`; one match → that workflow; multiple matches → `AmbiguousError` with no mutation.
- [ ] 3.12 Claim ordering tests: unassigned ticket is claimed with `wf:<name>` before `Executor.EnsureRun` is called; an already-claimed ticket skips claiming and only ensures the run; claim failure prevents run creation.
- [ ] 3.13 Filter tests: adapter compiles workflow `taskConfig.filters` into matchers over normalized ticket fields; matching is in-memory against one repo poll batch (no per-workflow re-query).

### Durable run execution (specs/durable-run-execution)

- [ ] 3.14 Run identity tests: run ID is deterministic `repo/workflow/ticket`; `EnsureRun` with the same ID returns the existing run without restarting; every work-node entry generates a fresh `nodeVisitID` that is stable across replay and changes on revisit and on fresh runs after recovery.
- [ ] 3.15 Serial graph tests: run begins at `start`, follows its single `onSuccess` edge, processes one node at a time, revisits create new visits, and a report selecting `end` applies end config, performs configured runner cleanup, and completes the run.
- [ ] 3.16 Transition ordering tests: after a report is accepted, effects execute in this exact order — persist report+selected route, write SUMMARY to current mailbox, write FEEDBACK to selected next mailbox (skipped when next is `end`), `CompleteMailbox` current, `ApplyTaskConfig` next, then start/reconcile next terminal. Each is a separate durable checkpoint.
- [ ] 3.17 Roll-forward recovery tests: crash after report persistence resumes the persisted route without re-asking the agent; crash mid-transition retries only unfinished activities; no rollback/compensation ever runs.
- [ ] 3.18 Conflict/blocked tests: a manual task-system change marks the run blocked, retries with the shared backoff, and continues automatically when state becomes compatible; no blind overwrite and no manual resume command.
- [ ] 3.19 Cancellation tests: `CancelByTicket` resolves the active run, stops scheduling new activities, waits for any running activity, closes runner terminals while preserving workspace/code, posts one parent comment with marker `runID:cancellation`, leaves mailbox history/statuses unchanged, and marks the run canceled.
- [ ] 3.20 Report delivery/dedup tests: acknowledgement happens only after the signal is durably persisted; only the first report for a current visit is consumed; a report for a non-current visit is acknowledged as an old duplicate with no repeated graph effects; no dedup table/hash is used.
- [ ] 3.20a Terminal reconcile tests: repeated `EnsureRun` on an active run checks the current `<ticket>:<node>` terminal by stable title; when that terminal is missing or unusable the workflow relaunches the same visit with the same `nodeVisitID`; when it is live no signal is sent. HITL reconcile restores a missing session but never nudges an idle live session.
- [ ] 3.20b Report ack semantics tests: the ack payload is `{"accepted":bool,"duplicate":bool}`; the `report` CLI exits 0 on any ack, exits 1 on server/validation failure, and never exits non-zero for a stale/duplicate report.
- [ ] 3.21 Database lifecycle tests: normal `serve` requires a valid existing database and refuses to start if missing/unusable; with a healthy DB a labeled ticket with a missing run is treated as claim-before-run and created; database loss is never inferred from a missing run.
- [ ] 3.22 `serve --recover` tests: explicit destructive mode only; creates fresh state, closes surviving run-owned terminals (preserving worktrees/code), `EnsureMailboxes` finds existing and creates missing, resets mailbox tasks to To Do, preserves comments/labels, creates fresh deterministic runs, generates fresh visit IDs, starts every run at `start`, and skips parents carrying the cancellation marker.
- [ ] 3.23 Retention tests: completed/canceled durable histories and `relay_runs` rows are removed after `completedRunRetentionDays` (default 30); starting/running/waiting/blocked/canceling runs are never removed; task-system markers remain.
- [ ] 3.24 Run projection tests: `relay_runs` is a derived read model only (idempotent update activities), supports `GetRun`, `FindRunByTicket`, `ListRuns` with filter, `HasActiveWorkflow`, `HasActiveRepo`; it is not a second authority for routes/reports.

### Node mailboxes (specs/node-mailboxes)

- [ ] 3.25 Mailbox lifecycle tests: one reusable mailbox per agent/HITL node keyed by stable `<ticket>:<node>` title; `start`/`end` get none; revisits reuse the same mailbox; each ensured mailbox carries the `wf:<name>` label and its description is the node's work description; summary goes to the current node's mailbox; feedback goes only to the selected next node's mailbox.
- [ ] 3.26 End/mailbox tests: when next step is `end` no feedback comment is written and `end` has no mailbox; manual mailbox status changes never route the graph; HITL uses the same mailbox lifecycle; recovery reuses mailboxes.

### Workflow repo management (specs/workflow-repo-management)

- [ ] 3.27 Machine config tests: `LoadMachine`/`SaveMachine`, defaults (`pollIntervalSeconds` 15, `completedRunRetentionDays` 30, both must be positive when set), fixed filesystem layout and permissions (config/db/lock/log `0600`, socket `0600`, workflows `0644`, root `0700`).
- [ ] 3.28 Atomic write tests: `WriteAtomic` creates a sibling temp, writes+fsyncs, sets mode, renames over destination, and fsyncs the parent directory; workflow submit and config save use it.
- [ ] 3.29 Init tests: `relay-flow init` selects the three plugin names, writes root config, initializes SQLite, and refuses to overwrite existing config/history.
- [ ] 3.30 Repo registration tests: `repo register` discovers/selects a runner repo, collects the task factory's `RequiredRepoKeys`, validates runner repo + task connectivity, rejects duplicate names/paths/task scopes, and atomically saves; repo entries hold only `path` and `taskConfig`.
- [ ] 3.31 Repo removal tests: removal is rejected while a stored workflow references the repo or an active run uses it.
- [ ] 3.32 Workflow storage/service tests: `workflow submit` creates or replaces atomically, replacement/removal rejected while any run of that workflow is active, validation completes before file replacement, no versioning, and `Repo.Workflows` bindings are rebuilt on submit/remove/startup.
- [ ] 3.33 Server API tests: Unix-socket JSON API, success `{"ok":true,"data":...}` and error `{"ok":false,"error":{"code":...,"message":...}}`, HTTP 400/404/405/409/500 mapping, and CLI exit codes 0 (success), 2 (usage), 1 (server/validation/operation).
- [ ] 3.34 Command surface tests: `init`, `serve [--recover]`, `stop`, `report`, `workflow submit|remove|list|get`, `repo register|remove|list|get`, `run list|get|cancel`; `report` reads one JSON object from stdin; standard-library `flag` remains the parser.
- [ ] 3.35 Shutdown tests: stop accepting requests/polls immediately, cancel worker polling, wait up to 30s for running calls, then close socket and database; durable unfinished work resumes on next normal start.

### Runtime harness plugin (TypeScript)

- [ ] 3.36 Plugin parse tests (bun test or vitest): the plugin reads the last completed assistant message on idle and parses the complete report contract (STATUS / NEXT STEP / all SUMMARY and FEEDBACK sections); missing or malformed sections are treated as invalid.
- [ ] 3.37 Plugin nudge-policy tests: an agent node with invalid/missing output sends the rendered nudge through OpenCode's session API; a HITL node with invalid/missing output stays silent; a HITL node with valid output reports normally.
- [ ] 3.38 Plugin report-retry tests: the plugin sends `{runId, nodeVisitId, report}` as one JSON object via `relay-flow report` stdin, retries the exact unchanged parsed report with the shared backoff constants (initial 2s, factor 2, jitter 0.2, max 5m) mirrored in TypeScript until acknowledged, runs only one retry loop per node visit, and treats a duplicate/stale ack as success without resubmitting.

## 4. Core implementation (make section-3 tests pass)

Implement in dependency order per docs/structs-methods-interfaces.md. Use concrete structs; interfaces only at replaceable boundaries (task, runner, harness, executor, and small consumer query needs). Pass serializable values across durable boundaries.

- [ ] 4.1 `internal/config`: `RawValues`, `Machine`, `Repo`, `LoadMachine`, `SaveMachine`, `Merge`, `DecodeStrict`, `WriteAtomic` (renameio). Note: the package-local named factory maps belong to the `task`, `runner`, and `harness` packages themselves (duplicate registration panics; unknown name errors list registered names) — config only stores and decodes values, it does not own the registries.
- [ ] 4.2 `internal/workflow`: `NodeType`, `Outcome`, `Workflow`, `Node`, `Route`, `Summary`, `Feedback`, `Report`, `NudgeTemplateData`; `Parse`, `Validate`, `StartTarget`, `Routes`, `ValidateReport`, `RenderNudge`; `Store` (LoadAll/Get/Put/Remove), `Registry`, and `Service` (Submit/Remove/Get/List) with the `ActiveRuns`/`RepoLookup` consumer interfaces.
- [ ] 4.3 `internal/task`: values (`Ticket`, `TicketRef`, `Mailbox`, `MailboxSpec`, `Target`), the `System` interface, `RepoSpec`, `Factory`, and `Register`/`New`/`RequiredRepoKeys`/`TaskScopeKey`/`Names`.
- [ ] 4.4 `internal/runner`: values (`RepoCandidate`, `Environment`, `Terminal`, `Command`, `RunSpec`), the `Runner` interface, and `Factory`/`Register`/`New`/`Names`.
- [ ] 4.5 `internal/harness`: `Session`, `LaunchSpec`, the `Harness` interface, and `Factory`/`Register`/`New`/`Names`.
- [ ] 4.6 `internal/repo`: `WorkflowBinding`, `Info`, `Repo`, `Registry` (with `BindWorkflows`), `Service` (Discover/RequiredRepoKeys/Register/Remove/Get/List), `RepoPoller`, and `PollerGroup` (max 10 concurrent polls).
- [ ] 4.7 `internal/router`: `ErrNoMatch`, `AmbiguousError`, `InvalidClaimError`, and pure `ResolveWorkflow` implementing the exact routing order from design decision 6.
- [ ] 4.8 `internal/run`: IDs/state aliases, `Start`, `Work`, `NodeWork`, `CommentWork`, `Run`, `ReportRequest`, `ReportAck`, `Filter`, the `Executor` and `RunQueries` interfaces, and `RunManager` (`EnsureRun`, `CancelByTicket`).
- [ ] 4.9 `internal/execution/goworkflows`: `Engine`, `Activities`, `RunProjection`, the `relay_runs` schema and indexes, one generic `TicketWorkflow` interpreter, one Workflow Worker (max 10) and one Activity Worker (max 20), `nodeVisitID` generated once per node entry as a durable replay-safe side effect (random, never derived from run/node/sequence, so explicit database-loss recovery generates fresh non-colliding IDs), the ordered transition activities, durable signal-based report handling, private typed retry loops using `BackoffPolicy.Delay` with replay-safe jitter, cancellation cleanup on a disconnected context, and `New`/`Start`/`Shutdown` lifecycle. Keep all go-workflows types inside this package. When this package first imports go-workflows, add it at exactly `v1.4.2` (run `go mod tidy` afterward); likewise `renameio/v2` and `huh` enter `go.mod` at their pinned versions from task 2.2 when first imported by config/cmd.
- [ ] 4.10 Engine report path: `Engine.SubmitReport` resolves the instance by `runID`, acknowledges non-current visits as old duplicates, validates the current visit's report, signals `report/<nodeVisitID>`, and acknowledges only after the signal is durably persisted. Reconcile signal: repeated `EnsureRun` checks the current ticket/node terminal by stable title and signals the workflow to relaunch the same visit (same `nodeVisitID`) only when that terminal is missing/unusable.
- [ ] 4.11 `internal/task/jira`: the Jira `Config` (assignee/project/component/filters/transitionTo), `CompileFilter` matchers, parent-only `Poll`, `Claim` via `wf:<name>`, `EnsureMailboxes` (title `<ticket>:<node>`, description from the node, `wf:<name>` label on every mailbox, find existing and create only missing), `ApplyTaskConfig` with the deterministic transition defaults, `CompleteMailbox`, marker-checked `HasComment`/`Comment`, and `ResetForRecovery` (the core operation is adapter-agnostic; the Jira adapter implements it by transitioning mailboxes to `To Do`). Move the ACLI wrapper to `internal/task/jira/acli` and keep it fakeable.
- [ ] 4.12 `internal/runner/orca`: implement `Runner` against Orca (environment per run, terminals titled `<ticket>:<node>`, find-before-create, close-terminals vs cleanup-run). Move the Orca CLI wrapper to `internal/runner/orca/orcacli` and keep it fakeable.
- [ ] 4.13 `internal/harness/opencode`: implement `Harness` (ValidateAgent, FindSession by title, BuildCommand returning the structured command with the required `RELAY_FLOW_*` env). The OpenCode runtime plugin owns message parsing, title pinning, nudge-on-invalid for agent nodes, silence for HITL, and exact-report JSON retry with the shared backoff constants mirrored in TypeScript.
- [ ] 4.14 `internal/server`: Unix-socket HTTP handlers and `Client` for stop, workflows, repos (discover/task-fields/register/list/get/remove), reports, and runs (list/get-by-ticket/cancel), with the documented routes, JSON envelope, status codes, and no Jira/Orca/graph/SQLite logic in handlers.
- [ ] 4.15 `cmd/relay-flow/main.go`: thin command parsing only (standard `flag`), wiring `init`, `serve [--recover]`, `stop`, `report`, and the workflow/repo/run subcommands to the server client; `huh` is used only for interactive repo selection during `repo register`.

## 5. Startup wiring and lifecycle

- [ ] 5.1 Implement the explicit composition root in serve: load machine config → select task/runner/harness factories → construct shared runner/harness → load repos and one `task.System` per repo → load workflow files → validate each workflow against every referenced repo task system → bind workflows+matchers to repos → open the go-workflows SQLite engine and workers → construct the Run Manager → start the Repo Poller group → start the Unix-socket server.
- [ ] 5.2 Before workers/pollers start, validate task-system/runner/harness config, credentials, permissions, connectivity, registered repos, and every configured agent; fail fast on known permanent errors.
- [ ] 5.3 Implement the polling callback exactly as documented (`handleBatch`): resolve via router, ignore `ErrNoMatch`, log other routing errors without mutating, and call `RunManager.EnsureRun`.
- [ ] 5.4 Implement the shared lifecycle gate so run claiming/creation and workflow replacement/removal cannot interleave a definition swap with a run start.
- [ ] 5.5 Implement graceful shutdown (section 3.35 behavior) and normal-start refusal when the database is missing/unusable.
- [ ] 5.6 Implement `serve --recover` exactly as decision 22 / spec: poll active labeled parents (excluding completed-through-`end` and cancellation-marked), close stale terminals preserving code, `EnsureMailboxes`, reset mailboxes to To Do, create fresh deterministic runs, start each at `start`. Never run automatically.

## 6. Verification

- [ ] 6.1 Run the full suite: `go test ./...` and the TypeScript plugin tests (bun test / vitest) pass, including all section-3 behavior tests and the crash-boundary, duplicate-report, ambiguous-comment, blocked-state, cancellation, restart, terminal-reconcile, and database-loss recovery tests.
- [ ] 6.2 Build and smoke the binary: `go build ./...`, `relay-flow init`, `serve`, `stop`, and one end-to-end ticket through `start` → a work node → `end`. Test seam (define before implementation): the smoke test runs in Go at the startup-wiring level with fake task/runner/harness factories registered for the test binary — the shipped binary only constructs real plugins from config, so do not add a fake-plugin config option to the production CLI. Confirm terminal titles, mailbox reuse, ordered transition effects, and the JSON report path.
- [ ] 6.3 Cross-check cross-artifact consistency before declaring done: lifecycle nodes are `start`/`end` (`terminal` = runner terminal only), the cleanup field is `cleanupRunnerOnEnd`, terminal titles stay stable while `nodeVisitID` changes, JSON wire keys are `runId`/`nodeVisitId`, harness metadata env names match decision 19, fresh visit/run identity after `--recover`, and routes cannot target `start`.
- [ ] 6.4 Confirm no legacy code remains: `internal/daemon`, old `internal/config`, old `internal/server`, old `internal/tasks`, old `internal/runner`, `internal/discovery`, `internal/opencode`, top-level `internal/acli`/`internal/orcacli`, and `plugin/report-status.ts` are gone; no fallback or compatibility shims were introduced.
- [ ] 6.5 `docs/structs-methods-interfaces.md` and `docs/feature-tracker.md` are normative references — do NOT rewrite them. Only publish clean-replacement notes for the new root/workflow YAML (e.g. README); write no migration tooling.
