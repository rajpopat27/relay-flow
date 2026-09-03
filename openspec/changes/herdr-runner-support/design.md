# Design: Herdr runner plugin

## Context

Relay-flow already has a machine-scoped runner registry and a concrete Orca adapter. The runner contract separates repository discovery, execution environments, terminal lifecycle, command execution, and cleanup from the durable workflow interpreter. `internal/runner/orca/orcacli` also establishes the intended external-integration pattern: a narrow client wrapper around a real executable plus adapter-level behavior tests.

Herdr is a local/server-owned terminal runtime whose public model is a workspace containing tabs and panes, and it manages Git worktrees natively: `worktree create` makes a linked checkout and opens it as its own workspace, `worktree open` reopens an existing checkout, and `worktree list` reports every checkout of a repository with the workspace currently holding it. Every workspace also reports the Git identity (`repo_root`, `repo_name`, `checkout_path`) of its checkout. A live check with the installed `herdr 0.8.2` CLI (protocol 20) confirmed these command forms and those of `tab create`, `tab list`, `pane list`, `pane get`, `pane process-info`, `pane rename`, `pane run`, `pane close`, and `workspace close`, as recorded in `herdr-cli-research.md`. The observed transport contract is a `result` envelope on stdout with exit 0 for success and an `{"error":{"code","message"}}` envelope on stderr with exit 1 for failure; there is no `ok` field. Herdr exposes the current `terminal_id` on pane responses, but that ID belongs to the current runtime and changes after a server restart. The public `pane_id` is the stable automation handle. Herdr has no general public terminal-list, terminal-health, or terminal-recreate command.

This change adds Herdr as a selectable runner while keeping task-system, harness, routing, durable execution, and SQLite code unchanged. The implementation is intentionally constrained to `internal/runner` plus the two static blank-import lines needed to register the plugin in the production composition roots.

## Goals / Non-Goals

**Goals:**

- Register `herdr` through the existing name-based runner factory.
- Use the installed `herdr` CLI itself, with the documented commands and flags, as the only production transport.
- Give every ticket its own Herdr-managed Git worktree workspace, mirroring the isolation the Orca runner provides.
- Run each node of a ticket in a separately labelled pane inside that ticket's worktree workspace.
- Persist public `pane_id` values as runner terminal handles and tolerate Herdr-generated `terminal_id` changes.
- Detect usable agent panes through public pane and process-info commands.
- Recover missing or stale panes using stable labels and close/create operations.
- Preserve the Git worktree, its branch, and its files during cancellation, recovery, and `cleanupRunnerOnEnd` cleanup.
- Validate the CLI wrapper against strict fake executables whose accepted argv and response fixtures are based on live Herdr CLI behavior.

**Non-Goals:**

- Do not import Herdr Rust/Go internals or add a Herdr SDK dependency.
- Do not call Herdr's private socket protocol directly in this change.
- Do not modify the Herdr source tree or add a Herdr-side API.
- Do not create a new runner interface, runner-specific core abstraction, or runner enum.
- Do not persist Herdr workspace IDs or terminal IDs in relay-flow machine configuration.
- Do not manage Herdr server/session lifecycle beyond invoking its documented CLI.
- Do not require operator-provisioned Herdr workspaces; ticket worktrees and workspaces are created lazily by the adapter.
- Do not remove Git worktrees, branches, or files during cleanup.
- Do not use Herdr's native `agent start` flow; the selected harness continues to return an opaque `runner.Command`.
- Do not change task-system, workflow, report, durable-engine, or harness behavior.
- Do not close or modify the repository's own source checkout workspace when cleaning one ticket run.
- Do not add a Herdr-specific platform gate or new OS-specific runtime behavior; inherit the platforms already supported by relay-flow and the installed Herdr CLI.

## Decisions

### 1. Use the public Herdr CLI only

The production client executes `herdr` as a subprocess. It uses the exact public command shapes recorded in `herdr-cli-research.md`; it does not construct requests for Herdr's private newline-delimited socket protocol. This keeps the integration aligned with the user-visible and cross-version-supported interface and mirrors the existing Orca CLI wrapper.

The client captures stdout and stderr separately, parses the `result` envelope from stdout, and parses Herdr's JSON error envelope from stderr. The documented codes `pane_not_found`, `workspace_not_found`, `worktree_not_found`, and `not_git_worktree` map to typed sentinel errors so the adapter can distinguish an absent resource from a transport failure; every other failure is returned unchanged. Errors never embed command payloads in info-level logs. Adapter-owned Herdr runner configuration has only these optional fields: `session` (sets `HERDR_SESSION`) and `socketPath` (sets `HERDR_SOCKET_PATH`). The executable name is fixed to `herdr`; when a selector is omitted, the normal Herdr CLI resolution is inherited. These are machine-level runner settings, never `repo register` inputs, and there is no configuration path for selecting a fake executable.

**Rejected:** importing Herdr internals, adding a Go socket client, or using undocumented terminal APIs. Those options would create coupling to Herdr implementation details and violate the CLI-only boundary for this first integration.

### 1a. Freeze the internal CLI-wrapper contract before tests

The Go method names and signatures are part of this design so tests do not invent an API for code that has not been written yet. The contract is internal to the Herdr runner package and is not a new core seam:

```go
type Client interface {
    Snapshot(ctx context.Context) (Snapshot, error)
    WorktreeList(ctx context.Context, repoPath string) (WorktreeListing, error)
    WorktreeCreate(ctx context.Context, repoPath, branch, base, label string) (Workspace, error)
    WorktreeOpen(ctx context.Context, repoPath, branch, label string) (Workspace, error)
    CreateTab(ctx context.Context, workspaceID, cwd, label string) (Tab, Pane, error)
    ListTabs(ctx context.Context, workspaceID string) ([]Tab, error)
    ListPanes(ctx context.Context, workspaceID string) ([]Pane, error)
    GetPane(ctx context.Context, paneID string) (Pane, error)
    ProcessInfo(ctx context.Context, paneID string) (ProcessInfo, error)
    RenamePane(ctx context.Context, paneID, label string) error
    RunPane(ctx context.Context, paneID, command string) error
    ClosePane(ctx context.Context, paneID string) error
    CloseWorkspace(ctx context.Context, workspaceID string) error
}
```

The methods map one-to-one to verified public commands: `worktree list/create/open`, `tab create`, `tab list`, `api snapshot`, the `pane` commands, and `workspace close`. There is intentionally no `CreateWorkspace` (worktree creation opens the workspace) and no `WorktreeRemove` (cleanup never removes a checkout).

### 1b. Freeze the Herdr adapter construction contract

The adapter owns the machine-level Herdr runner configuration:

```go
type Config struct {
    Session    string `yaml:"session,omitempty"`
    SocketPath string `yaml:"socketPath,omitempty"`
}

func New(raw config.RawValues) (runner.Runner, error)   // production factory
func newAdapter(cli herdrcli.Client) *adapter           // same-package tests
```

`New` decodes `raw` with `config.DecodeStrict` (wrapping failures as `herdr runnerConfig: ...`) before constructing `herdrcli.New(...)`, so invalid configuration never reaches Herdr. Registration is `runner.Register("herdr", New)`. `Config` is consumed when building the client and is not stored on the adapter. `newAdapter` is intentionally unexported: there is no exported constructor accepting a `Client`, no test-only production constructor, and no fake-selection path.

### 2. Keep the common Runner interface unchanged

`internal/runner/herdr` implements the current `runner.Runner` methods. The existing factory registry remains the extension point. `runner.Terminal.ID` is already an opaque string, so the Herdr adapter stores a public pane ID there. `runner.Environment.ID` stores a Herdr workspace ID only in durable runtime state, where it is treated as an external opaque handle; repository configuration continues to contain only path and task config.

The two composition roots add blank imports for `internal/runner/herdr`. No other production package imports Herdr-specific types.

**Rejected:** adding a `RunnerType` enum or Herdr fields to common structs. That would make every consumer know about one concrete runner and would widen the high-blast-radius interface unnecessarily.

### 3. One ticket maps to one Herdr Git worktree workspace

This mirrors the Orca runner: an environment is a ticket-scoped checkout, not a shared directory. `EnsureEnvironment` calls `worktree open --cwd <repo> --branch <ticket> --label <ticket> --no-focus`, and on `worktree_not_found` calls `worktree create` with the same branch plus `--base <origin branch>`. Herdr checks out an existing branch as-is, so a re-created checkout never discards previous agent commits, and `--base` only matters for a brand-new branch.

The branch name is the ticket key, matching Orca's ticket-named worktrees. The base is always the repository's origin branch, resolved as `origin/HEAD`, then `origin/main`, then `origin/master`; a repository with no `origin` remote falls back to local `main` or `master`, and an unresolvable base is an actionable error rather than a guess.

`Environment.ID` is the currently open workspace ID and `Environment.Path` is the worktree checkout. Herdr assigns a new workspace ID when a closed checkout is reopened, so the workspace ID is a current handle only; the durable identity is the ticket branch and its checkout. Since `EnsureEnvironment` runs before every terminal operation, the adapter always holds a current ID.

`DiscoverRepos` reads the `worktree` block each workspace reports and deduplicates by `repo_root`, so both source and ticket workspaces resolve to one repository candidate. `ValidateRepo` calls `worktree list --cwd <path>`, which works even when Herdr has nothing open, and requires the registered path to be the reported `repo_root`. Registration therefore needs no operator-provisioned workspace and creates nothing.

**Rejected:** running every ticket in the shared repository checkout (concurrent tickets would fight over one working tree and branch), persisting workspace IDs in `config.yaml`, requiring the operator to pre-create workspaces, and matching repositories by pane `cwd` (a pane that `cd`s into a subdirectory produced bogus candidates).

### 4. Create node terminals as labelled tabs/root panes

Herdr has no public terminal creation command. `CreateTerminal` therefore performs:

1. `tab create --workspace <workspace_id> --cwd <worktree_path> --label <ticket>:<node> --no-focus`;
2. `pane rename <pane_id> <ticket>:<node>` to make the pane label explicit and recoverable;
3. `pane run <pane_id> <command>` to submit the opaque harness command.

The tab label is intentionally set before the pane label so a lost acknowledgement between steps 1 and 2 can be recovered through `tab list --workspace` plus `pane list --workspace`.

The adapter renders only the command transport representation required by Herdr's CLI. It does not inspect or reinterpret harness arguments such as `--session`, `--prompt`, or `--agent`. Environment values, executable, and arguments are rendered together as one POSIX-shell-quoted command line for `pane run`, exactly as the Orca adapter does. Environment is deliberately bound to the launch rather than to the tab: `--env` values would persist on a pane and a pane adopted from an earlier run would then report a stale `RELAY_FLOW_RUN_ID`. The exact quoting behavior is covered by strict CLI tests with spaces, quotes, and multiline values.

If rename or command submission fails after tab creation, the adapter closes the created pane before returning the error. `CreateTerminal` itself does not perform title discovery; `EnsureTerminal` owns find-before-create behavior.

**Rejected:** using Herdr `agent start` (it would make the runner understand harness/agent semantics), relying only on tab labels (pane listing does not expose the containing tab label), or inventing `herdr terminal create` flags.

### 5. Treat pane IDs as durable handles and terminal IDs as ephemeral

`FindTerminal` uses a stored pane ID with `pane get`, then calls `pane process-info --pane <pane_id>`. A pane is usable only when its current runtime exists and process information shows a live foreground process rather than merely a restored shell. The returned `runner.Terminal.ID` remains the public pane ID; the current `terminal_id` is discarded.

When no stored ID is available, the adapter scans the selected workspace's tab list and pane list for the exact `<ticket>:<node>` label. The tab label is the first recovery marker because a newly created root pane has no pane label until `pane rename` completes; the pane label is the marker after rename. This handles a lost create acknowledgement at every step without blindly creating a second pane. When a stored pane exists but is unusable, `EnsureTerminal` closes it and creates a replacement so a fresh environment containing the required `RELAY_FLOW_*` variables is used.

A new node visit that finds a live pane follows the existing core behavior: the runner sends the rendered feedback prompt to that pane. A same-visit retry does not send a prompt. A Herdr restart that restores a shell is reconciled as an unusable agent pane and relaunched with the same durable OpenCode session ID when one exists.

**Rejected:** persisting `terminal_id`, declaring every pane with a shell usable, or requiring a Herdr terminal-recreate endpoint that does not exist.

### 6. Scope cleanup to the ticket, and never remove the worktree

`CloseTerminals` resolves the ticket's open worktree workspace through `worktree list`, then closes only panes owned by the ticket: a pane label or its containing tab label must start with `<ticket>:`. Using `tab list` together with `pane list` cleans a pane even when a crash occurred before `pane rename` applied the pane label. `CleanupRun` performs the same pane cleanup and then closes the ticket workspace with `workspace close`.

The Git worktree, its branch, and its files are deliberately preserved; this is the one intentional divergence from the Orca adapter, which deletes the ticket worktree. A later run reopens the same checkout through `worktree open`.

Cleanup rolls forward: an absent repository, missing ticket checkout, closed workspace, or already-closed pane is reported as success, so `serve --recover` never fails because someone removed a workspace by hand. Only unexpected failures propagate.

`SetEnvironmentStatus` is a no-op because Herdr has no workspace-status primitive. Run state remains visible through relay-flow's projection and the task system.

### 7. Keep tests tied to the real CLI contract

Response fixtures are captured from the installed Herdr CLI in a disposable session, never hand-written. An earlier hand-written fixture set invented an `ok` field that Herdr does not emit; the strict fake accepted it and the wrapper failed against every real command. Fixtures are therefore captured output with only paths and host identifiers sanitized.

The normal CI test suite does not require Herdr to be installed. The `herdrcli` tests install a strict executable named `herdr` earlier on `PATH`. The script accepts only the fixed production argv forms, validates absolute `--cwd` values and selected Herdr environment variables, returns captured-like JSON envelopes, and exits nonzero for unsupported/malformed invocations. It must reject any accidental invented command or flag. The strict fake checks the wrapper's chosen argument order; it does not claim that Herdr itself rejects other option orderings.

Adapter tests may use a fake `herdrcli.Client` whose values and behavior are the same fields exercised by those CLI fixtures. This separates CLI-shape verification from adapter decision testing without allowing a dummy fake to hide a bad production command. The fake exists only in `_test.go`; the production factory always constructs the real CLI client and has no fake-selection path.

A live test drives the production wrapper itself against the installed Herdr binary in a disposable session, gated by `RELAY_FLOW_HERDR_LIVE=1` so CI without Herdr stays green. Driving the Go wrapper, not hand-run CLI commands, is what catches envelope mistakes.

`e2e-test.md` is the manual whole-system procedure: a real repository, a real task system, and a real agent driven through registration, submission, handoff, and cleanup, with the expected output of every step recorded so a regression is visible by comparison.

**Rejected:** tests that only call a permissive fake client, hard-code response shapes not observed from Herdr, require the installed Herdr binary in default CI, add a fake-selection configuration/fallback, or add a second test-only production constructor.

## Risks / Trade-offs

- **Herdr has no general terminal health/recreate API** → use `pane get` plus `pane process-info`; close and create a pane when the runtime is unusable. Keep strict liveness tests for live command, restored shell, and missing pane cases.
- **Workspace IDs change when a checkout is reopened** → treat the ID as a current handle, re-resolve it in `EnsureEnvironment` before every terminal operation, and never persist it as identity.
- **`pane run` accepts shell text rather than structured argv** → render env assignments and quoted arguments into one command line, as the Orca adapter already does. The adapter remains CLI-only and adds no OS-specific transport or platform gate.
- **A Herdr restart produces fresh shells and terminal IDs** → store pane IDs only, recognize shell-only panes as unusable, and relaunch through normal durable reconcile.
- **Worktrees accumulate because cleanup never removes them** → accepted deliberately so code, branches, and uncommitted work survive; operators prune checkouts themselves.
- **Herdr CLI versions may change** → run the live preflight first, capture real fixtures, and fail explicitly on unsupported command shapes; do not add compatibility fallbacks.

## Migration Plan

No data migration is required. Existing `runnerPlugin: orca` configurations and Orca worktrees remain unchanged.

For Herdr:

1. Install and start the desired Herdr session/server.
2. Select `herdr` during `relay-flow init`, or set `runnerPlugin: herdr` in a new machine configuration.
3. Register repositories by path; no Herdr workspace needs to exist first.
4. Submit workflows normally. The first node of each ticket creates that ticket's worktree workspace.

Rollback is selecting `orca` again and registering Orca repositories. Herdr worktrees and panes are left in place because cleanup never removes checkouts.

## Runtime Scope

There are no unresolved runtime choices for this change:

- Repo registration validates a repository root through `worktree list` and creates nothing.
- Each ticket's worktree workspace is created on first use and reopened afterward.
- Cleanup closes ticket-labelled panes and the ticket workspace, and never removes the worktree, branch, or files.
- An absent repository, checkout, or workspace makes cleanup and recovery roll forward as success.
- `SetEnvironmentStatus` is a successful no-op.
- Platform behavior is inherited from the current relay-flow runtime and the installed Herdr CLI; this change adds no platform-specific branch or Herdr platform restriction.
