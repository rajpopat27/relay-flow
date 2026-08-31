# Design: Herdr runner plugin

## Context

Relay-flow already has a machine-scoped runner registry and a concrete Orca adapter. The runner contract separates repository discovery, execution environments, terminal lifecycle, command execution, and cleanup from the durable workflow interpreter. `internal/runner/orca/orcacli` also establishes the intended external-integration pattern: a narrow client wrapper around a real executable plus adapter-level behavior tests.

Herdr is a local/server-owned terminal runtime rather than a worktree manager. Its public model is a workspace containing tabs and panes. Creating a workspace creates an initial tab/root pane; creating a tab creates a root pane; commands are sent to panes. A live check with the installed `herdr 0.8.2` CLI (protocol 20) confirmed that `workspace create`, `tab create`, `pane list`, `pane get`, `pane process-info`, `pane rename`, `pane run`, and `pane close` use the command forms recorded in `herdr-cli-research.md`. Herdr exposes the current `terminal_id` on pane responses, but that ID belongs to the current runtime and changes after a server restart. The public `pane_id` is the stable automation handle. Herdr has no general public terminal-list, terminal-health, or terminal-recreate command.

This change adds Herdr as a selectable runner while keeping task-system, harness, routing, durable execution, and SQLite code unchanged. The implementation is intentionally constrained to `internal/runner` plus the two static blank-import lines needed to register the plugin in the production composition roots.

## Goals / Non-Goals

**Goals:**

- Register `herdr` through the existing name-based runner factory.
- Use the installed `herdr` CLI itself, with the documented commands and flags, as the only production transport.
- Map one registered repository to one Herdr workspace.
- Run each ticket/node in a separately labelled Herdr pane while keeping all nodes for a run in the same repository workspace.
- Persist public `pane_id` values as runner terminal handles and tolerate Herdr-generated `terminal_id` changes.
- Detect usable agent panes through public pane and process-info commands.
- Recover missing or stale panes using stable labels and close/create operations.
- Preserve shared repository workspaces during cancellation, recovery, and `cleanupRunnerOnEnd` cleanup.
- Validate the CLI wrapper against strict fake executables whose accepted argv and response fixtures are based on live Herdr CLI behavior.

**Non-Goals:**

- Do not import Herdr Rust/Go internals or add a Herdr SDK dependency.
- Do not call Herdr's private socket protocol directly in this change.
- Do not modify the Herdr source tree or add a Herdr-side API.
- Do not create a new runner interface, runner-specific core abstraction, or runner enum.
- Do not persist Herdr workspace IDs or terminal IDs in relay-flow machine configuration.
- Do not manage Herdr server/session lifecycle beyond invoking its documented CLI.
- Do not provision or recreate Herdr workspaces from the adapter; workspace creation is an operator setup step.
- Do not use Herdr's native `agent start` flow; the selected harness continues to return an opaque `runner.Command`.
- Do not change task-system, workflow, report, durable-engine, or harness behavior.
- Do not close a repository workspace when cleaning one ticket run.
- Do not add a Herdr-specific platform gate or new OS-specific runtime behavior; inherit the platforms already supported by relay-flow and the installed Herdr CLI.

## Decisions

### 1. Use the public Herdr CLI only

The production client executes `herdr` as a subprocess. It uses the exact public command shapes recorded in `herdr-cli-research.md`; it does not construct requests for Herdr's private newline-delimited socket protocol. This keeps the integration aligned with the user-visible and cross-version-supported interface and mirrors the existing Orca CLI wrapper.

The client captures stdout and stderr separately, parses JSON responses for read/create operations, and returns command/API errors without embedding command payloads in info-level logs. Adapter-owned Herdr runner configuration has only these optional fields: `session` (sets `HERDR_SESSION`) and `socketPath` (sets `HERDR_SOCKET_PATH`). The executable name is fixed to `herdr`; when a selector is omitted, the normal Herdr CLI resolution is inherited. These are machine-level runner settings, never `repo register` inputs, and there is no configuration path for selecting a fake executable.

**Rejected:** importing Herdr internals, adding a Go socket client, or using undocumented terminal APIs. Those options would create coupling to Herdr implementation details and violate the CLI-only boundary for this first integration.

### 2. Keep the common Runner interface unchanged

`internal/runner/herdr` implements the current `runner.Runner` methods. The existing factory registry remains the extension point. `runner.Terminal.ID` is already an opaque string, so the Herdr adapter stores a public pane ID there. `runner.Environment.ID` stores a Herdr workspace ID only in durable runtime state, where it is treated as an external opaque handle; repository configuration continues to contain only path and task config.

The two composition roots add blank imports for `internal/runner/herdr`. No other production package imports Herdr-specific types.

**Rejected:** adding a `RunnerType` enum or Herdr fields to common structs. That would make every consumer know about one concrete runner and would widen the high-blast-radius interface unnecessarily.

### 3. One repository maps to one shared workspace

The Herdr adapter uses the registered repository name and path to locate a workspace. It reads `api snapshot`, groups panes by workspace, and matches a normalized pane `cwd` to the registered repository path. An exact workspace label match is a tie-breaker. Operators provision a workspace with the registered repo name as its label and `--no-focus` so setup does not steal focus; workspace provisioning is not an adapter side effect.

`ValidateRepo` requires the local path and an unambiguous matching Herdr workspace to exist; it performs no creation side effect during repo registration. `EnsureEnvironment` rechecks the mapping and returns an error when the workspace is missing or ambiguous. The operator must recreate a deleted workspace with `workspace create --cwd ... --label ... --no-focus` before registering the repo again or restarting relay-flow. This keeps both registration and startup validation deterministic.

The adapter serializes workspace lookup operations within one runner instance. It reports multiple path matches as an error rather than guessing which user workspace owns the repository.

**Rejected:** persisting workspace IDs in `config.yaml` (IDs are adapter-owned and may be invalid after external session changes), matching only labels (labels can be renamed or collide), or creating a new workspace per ticket (contradicts the requested repository mapping and prevents shared files).

### 4. Create node terminals as labelled tabs/root panes

Herdr has no public terminal creation command. `CreateTerminal` therefore performs:

1. `tab create --workspace <workspace_id> --cwd <repo_path> --label <ticket>:<node> --no-focus` with the command environment as repeated `--env KEY=VALUE` flags;
2. `pane rename <pane_id> <ticket>:<node>` to make the pane label explicit and recoverable;
3. `pane run <pane_id> <command>` to submit the opaque harness command.

The tab label is intentionally set before the pane label so a lost acknowledgement between steps 1 and 2 can be recovered through `tab list --workspace` plus `pane list --workspace`.

The adapter renders only the command transport representation required by Herdr's CLI. It does not inspect or reinterpret harness arguments such as `--session`, `--prompt`, or `--agent`. Environment values are passed during tab creation, while executable and arguments are rendered as one POSIX-shell-quoted command line for `pane run`. The exact quoting behavior is covered by strict CLI tests with spaces, quotes, and multiline values.

If rename or command submission fails after tab creation, the adapter closes the created pane before returning the error. `CreateTerminal` itself does not perform title discovery; `EnsureTerminal` owns find-before-create behavior.

**Rejected:** using Herdr `agent start` (it would make the runner understand harness/agent semantics), relying only on tab labels (pane listing does not expose the containing tab label), or inventing `herdr terminal create` flags.

### 5. Treat pane IDs as durable handles and terminal IDs as ephemeral

`FindTerminal` uses a stored pane ID with `pane get`, then calls `pane process-info --pane <pane_id>`. A pane is usable only when its current runtime exists and process information shows a live foreground process rather than merely a restored shell. The returned `runner.Terminal.ID` remains the public pane ID; the current `terminal_id` is discarded.

When no stored ID is available, the adapter scans the selected workspace's tab list and pane list for the exact `<ticket>:<node>` label. The tab label is the first recovery marker because a newly created root pane has no pane label until `pane rename` completes; the pane label is the marker after rename. This handles a lost create acknowledgement at every step without blindly creating a second pane. When a stored pane exists but is unusable, `EnsureTerminal` closes it and creates a replacement so a fresh environment containing the required `RELAY_FLOW_*` variables is used.

A new node visit that finds a live pane follows the existing core behavior: the runner sends the rendered feedback prompt to that pane. A same-visit retry does not send a prompt. A Herdr restart that restores a shell is reconciled as an unusable agent pane and relaunched with the same durable OpenCode session ID when one exists.

**Rejected:** persisting `terminal_id`, declaring every pane with a shell usable, or requiring a Herdr terminal-recreate endpoint that does not exist.

### 6. Scope cleanup to ticket panes, never the shared workspace

`CloseTerminals` reads the repository workspace's tabs and panes and closes only panes owned by the ticket: a pane label or its containing tab label must start with `<ticket>:`. It uses `tab list --workspace` together with `pane list --workspace` so it can clean a pane even when a crash occurred before `pane rename` applied the pane label. It ignores missing panes so cancellation and explicit recovery are idempotent. `CleanupRun` invokes the same ticket-scoped cleanup and deliberately leaves the Herdr workspace and its neutral/root panes in place.

This is the Herdr interpretation of the common runner cleanup contract: the workspace is repository-owned/shared, while labelled tabs/panes are run-owned. It prevents one completed or canceled ticket from disrupting other active tickets in the repository.

`SetEnvironmentStatus` is a no-op because Herdr has no shared workspace-status primitive. Run state remains visible through relay-flow's projection and the task system; the adapter does not encode a misleading status in workspace labels or metadata.

### 7. Keep tests tied to the real CLI contract

Before implementation, a disposable Herdr session/workspace is exercised with the installed CLI. The preflight records the Herdr version, accepted flags, output envelopes, environment propagation, pane labels, restart behavior, and process-info behavior.

The `herdrclicli` tests install a strict executable named `herdr` earlier on `PATH`. The script accepts only the fixed production argv forms, validates absolute `--cwd` values and selected Herdr environment variables, returns captured-like JSON envelopes, and exits nonzero for unsupported/malformed invocations. It must reject any accidental invented command or flag. The strict fake checks the wrapper's chosen argument order; it does not claim that Herdr itself rejects other option orderings.

Adapter tests may use a fake `herdrclicli.Client` whose values and behavior are the same fields exercised by those CLI fixtures. This separates CLI-shape verification from adapter decision testing without allowing a dummy fake to hide a bad production command. The fake exists only in `_test.go`; the production factory always constructs the real CLI client and has no fake-selection path. A mandatory live smoke test invokes the installed Herdr binary and a real configured harness command after implementation.

**Rejected:** tests that only call a permissive fake client, hard-code response shapes not observed from Herdr, add a fake-selection configuration/fallback, or add a second test-only production constructor.

## Risks / Trade-offs

- **Herdr has no general terminal health/recreate API** → use `pane get` plus `pane process-info`; close and create a pane when the runtime is unusable. Keep strict liveness tests for live command, restored shell, and missing pane cases.
- **WorkspaceInfo does not expose cwd directly** → use snapshot pane cwd values and require an unambiguous path match. Fail rather than attach to an ambiguous workspace.
- **`pane run` accepts shell text rather than structured argv** → pass env through repeated `--env` flags and use quoting appropriate for the shell used by the existing relay-flow runtime. The adapter remains CLI-only and does not add a separate OS-specific transport or platform gate.
- **A Herdr restart produces fresh shells and terminal IDs** → store pane IDs only, recognize shell-only panes as unusable, and relaunch through normal durable reconcile.
- **Several runs share one workspace** → use exact ticket/node labels for ownership and never delete the workspace during run cleanup.
- **Herdr CLI versions may change** → run the live preflight first, capture real fixtures, and fail explicitly on unsupported command shapes; do not add compatibility fallbacks.

## Migration Plan

No data migration is required. Existing `runnerPlugin: orca` configurations and Orca worktrees remain unchanged.

For Herdr:

1. Install and start the desired Herdr session/server.
2. Create one Herdr workspace per repository with the repository path as its cwd and an identifiable label.
3. Select `herdr` during `relay-flow init`, or set `runnerPlugin: herdr` in a new machine configuration.
4. Register repositories using the Herdr-discovered workspace name/path.
5. Submit workflows normally.

Rollback is selecting `orca` again and registering Orca repositories. Herdr-created workspaces and panes are not automatically removed because relay-flow does not own the shared workspace lifecycle.

## Runtime Scope

There are no unresolved runtime choices for this change:

- Repo registration and normal startup require an existing, unambiguous Herdr workspace; registration and the adapter do not create one.
- If a workspace is deleted, the operator recreates it with the documented Herdr CLI before registering the repo again or restarting relay-flow.
- Cleanup closes ticket-labelled panes and never closes the shared repository workspace.
- `SetEnvironmentStatus` is a successful no-op.
- Platform behavior is inherited from the current relay-flow runtime and the installed Herdr CLI; this change adds no platform-specific branch or Herdr platform restriction.
