# herdr-runner Specification

## Purpose
TBD - created by archiving change herdr-runner-support. Update Purpose after archive.
## Requirements
### Requirement: Herdr is a selectable runner plugin
The system SHALL register a runner named `herdr` through the existing runner factory registry. The Herdr adapter SHALL implement the existing `runner.Runner` interface without adding Herdr-specific fields or methods to common runner values. The production composition root SHALL statically import the adapter so `herdr` appears in runner selection and can be constructed from machine-scoped runner configuration.

#### Scenario: Herdr appears in runner selection
- **WHEN** the runner registry is queried after built-in adapters are loaded
- **THEN** `runner.Names()` includes `herdr` and `runner.New("herdr", ...)` constructs the Herdr adapter

#### Scenario: Unknown Herdr configuration is rejected
- **WHEN** `runner.New("herdr", ...)` receives an unknown Herdr runnerConfig field
- **THEN** strict adapter-owned configuration validation returns an error before any Herdr CLI call

### Requirement: Herdr CLI selection is adapter-owned
The Herdr runner configuration SHALL accept only the optional fields `session` and `socketPath`. The production executable name SHALL be fixed to `herdr`; when `session` or `socketPath` is set, the adapter SHALL pass it through the corresponding documented Herdr environment selector. These values SHALL be machine-level runner configuration and SHALL NOT be requested by `repo register`. No configuration value SHALL select a fake executable.

#### Scenario: Default Herdr CLI is used
- **WHEN** `runnerConfig` omits `session` and `socketPath`
- **THEN** the production adapter invokes the `herdr` executable using Herdr's normal session resolution

#### Scenario: Named Herdr session is selected
- **WHEN** `runnerConfig` sets `session: relay-flow`
- **THEN** the adapter invokes the CLI with `HERDR_SESSION=relay-flow` and does not ask `repo register` for a session value

#### Scenario: Explicit socket is selected
- **WHEN** `runnerConfig` sets `socketPath: /tmp/herdr.sock`
- **THEN** the adapter invokes the CLI with `HERDR_SOCKET_PATH=/tmp/herdr.sock`

### Requirement: Herdr runner operations use the public Herdr CLI
The Herdr runner SHALL execute the installed `herdr` CLI rather than importing Herdr internals, using a raw Herdr socket client, reading Herdr persisted state, or adding a Herdr SDK dependency. The wrapper SHALL follow the observed transport contract: successful commands return a `result` envelope on stdout with exit status 0, and failures return an `{"error":{"code","message"}}` envelope on stderr with a nonzero exit status. There is no `ok` field. The wrapper SHALL map the documented error codes `pane_not_found`, `workspace_not_found`, `worktree_not_found`, and `not_git_worktree` to typed errors and SHALL return every other failure unchanged.

#### Scenario: Snapshot uses the documented command
- **WHEN** the runner discovers Herdr workspaces
- **THEN** the CLI wrapper invokes exactly `herdr api snapshot` and reads `result.snapshot`

#### Scenario: Pane lookup uses documented flags
- **WHEN** the runner checks a persisted pane
- **THEN** the CLI wrapper invokes `herdr pane get <pane_id>` and `herdr pane process-info --pane <pane_id>`

#### Scenario: Error envelopes are read from stderr
- **WHEN** a Herdr command fails with a JSON error envelope on stderr and a nonzero exit status
- **THEN** the wrapper returns the typed error for the reported code and preserves Herdr's own message

#### Scenario: Unsupported CLI invocation fails
- **WHEN** the installed Herdr executable rejects a command or flag
- **THEN** the runner returns an error and does not silently retry with an invented or alternate command shape

### Requirement: Each ticket maps to its own Herdr Git worktree workspace
The Herdr runner SHALL give every ticket its own Git worktree, opened by Herdr as its own workspace, so concurrent tickets in one repository never share a checkout. `EnsureEnvironment` SHALL open the existing ticket-branch checkout and SHALL create it only when no checkout exists. The ticket branch name SHALL be the ticket key. New checkouts SHALL be based on the repository's origin branch, resolved as `origin/HEAD`, then `origin/main`, then `origin/master`, falling back to local `main` or `master` only when the repository has no `origin` remote; when none resolves, the runner SHALL return an actionable error. An existing ticket branch SHALL be reused as-is so previous agent commits are never discarded. `EnsureEnvironment` SHALL return the open workspace ID as `runner.Environment.ID` and the worktree checkout path as `runner.Environment.Path`. The workspace ID SHALL be treated as Herdr's current handle, never as durable identity; the durable identity is the ticket branch and its checkout.

`DiscoverRepos` SHALL derive candidates from the Git identity Herdr reports for each open workspace, deduplicated by repository root. `ValidateRepo` SHALL verify the registered path is a repository root Herdr can manage and SHALL create nothing.

#### Scenario: Existing ticket worktree is reused
- **WHEN** ticket `PAY-101` already has a checkout for branch `PAY-101`
- **THEN** `EnsureEnvironment` opens that checkout and creates no second worktree

#### Scenario: First visit creates the ticket worktree
- **WHEN** ticket `PAY-101` has no checkout and the repository resolves `origin/HEAD` to `origin/main`
- **THEN** the runner creates branch `PAY-101` from `origin/main` as its own worktree workspace and returns the checkout path as the environment path

#### Scenario: Existing branch keeps prior work
- **WHEN** branch `PAY-101` already exists with agent commits but its checkout was removed
- **THEN** the recreated worktree checks out the existing branch and no commit is discarded

#### Scenario: No origin branch is resolvable
- **WHEN** a repository has an `origin` remote but no `origin/HEAD`, `origin/main`, or `origin/master`
- **THEN** `EnsureEnvironment` returns an actionable error naming the repository and the `git remote set-head` remedy

#### Scenario: Repository candidates are deduplicated
- **WHEN** `DiscoverRepos` reads a snapshot containing a source checkout workspace and several ticket worktree workspaces of the same repository
- **THEN** it returns one `runner.RepoCandidate` per repository root

#### Scenario: Registration validates the repository root
- **WHEN** `repo register` validates a path that is inside a repository rather than its root
- **THEN** validation fails with an error naming the repository root to register, and no workspace or worktree is created

### Requirement: Herdr registration requires no operator provisioning
Registering a repository for the Herdr runner SHALL require no pre-created Herdr workspace. `ValidateRepo` SHALL succeed against a Git repository root even when the Herdr session has nothing open, and worktrees and workspaces SHALL be created lazily when a ticket first needs one.

#### Scenario: Registration succeeds with an empty Herdr session
- **WHEN** `repo register --path /work/payments` runs and no Herdr workspace is open
- **THEN** validation succeeds and no Herdr workspace, worktree, or branch is created

#### Scenario: Non-repository path is rejected
- **WHEN** `repo register` validates a path that is not inside a Git work tree
- **THEN** registration fails with Herdr's reported reason

### Requirement: Herdr launches nodes in labelled panes
The Herdr runner SHALL create each node terminal as a tab/root pane inside that ticket's worktree workspace, with the tab cwd set to the worktree checkout. The tab and pane SHALL be labelled exactly `<ticket>:<node>`, and the command returned by the harness SHALL remain opaque to the runner except for transport-safe serialization. The runner SHALL render command environment values into the launched command line rather than binding them to the pane, so a reused pane can never inherit a previous run's environment, and SHALL submit the command through `herdr pane run <pane_id> <command>`.

#### Scenario: First node creates a Herdr pane
- **WHEN** ticket `PAY-101` enters node `coding`
- **THEN** the runner invokes `herdr tab create --workspace <workspace_id> --cwd <worktree_path> --label PAY-101:coding --no-focus`, labels the returned pane `PAY-101:coding`, and runs the harness command in that pane

#### Scenario: Harness command and environment are preserved
- **WHEN** the harness returns executable, arguments, multiline prompt text, and `RELAY_FLOW_*` environment values
- **THEN** the runner forwards all argument values and environment values without parsing OpenCode-specific flags or dropping multiline content

#### Scenario: Reused pane runs the current run identity
- **WHEN** a pane left by an earlier run is adopted for a new run of the same ticket and node
- **THEN** the relaunched command carries the current run's `RELAY_FLOW_*` values and never the previous run's values

#### Scenario: Failed pane setup does not leave an owned pane
- **WHEN** pane labelling or command submission fails after tab creation
- **THEN** the runner attempts to close the created pane before returning the original failure

### Requirement: Herdr uses public pane identity and detects usable runtime
The Herdr runner SHALL store the public `pane_id` in `runner.Terminal.ID` and SHALL never treat `terminal_id` as durable identity. `FindTerminal` SHALL inspect the pane and its process information and SHALL return only a live usable terminal. A pane whose runtime is absent, whose process information is unavailable, or whose foreground is only a restored shell SHALL be treated as unusable.

#### Scenario: Terminal ID changes after restart
- **WHEN** Herdr returns the same public pane ID with a new `terminal_id` after restart
- **THEN** `FindTerminal` returns the pane ID as the terminal handle and does not require the old terminal ID

#### Scenario: Missing pane is absent
- **WHEN** `herdr pane get <pane_id>` reports `pane_not_found`
- **THEN** `FindTerminal` returns `(Terminal{}, false, nil)` and does not report a usable terminal

#### Scenario: Transport failure is not treated as a missing pane
- **WHEN** a pane lookup fails for any reason other than `pane_not_found`
- **THEN** `FindTerminal` returns the error so the activity can retry instead of replacing a live pane

#### Scenario: Restored shell is not an active agent terminal
- **WHEN** a pane exists but `pane process-info` shows only the pane shell in the foreground
- **THEN** `FindTerminal` reports the pane as unusable so durable reconciliation can relaunch the harness

#### Scenario: Live agent process is reusable
- **WHEN** a labelled pane has a live foreground agent process
- **THEN** `FindTerminal` returns the public pane ID and `EnsureTerminal` does not create a second pane

### Requirement: Herdr reconciles pane creation and report prompts by stable labels
`EnsureTerminal` SHALL perform find-before-create. When a stored pane handle is absent or unusable, the adapter SHALL search the selected workspace's tab list and pane list for the exact `<ticket>:<node>` label before creating a replacement. The tab label SHALL be used as the recovery marker before `pane rename` has completed; the pane label SHALL be used thereafter. `SendTerminal` SHALL submit prompt text to the selected pane through the public Herdr pane-input command. The runner SHALL not include `nodeVisitID`, workflow name, or agent name in the pane label.

#### Scenario: Lost create acknowledgement is recovered
- **WHEN** a tab was created and relay-flow lost the create response before the pane label was applied or its handle was persisted
- **THEN** the next `EnsureTerminal` finds the exact `<ticket>:<node>` tab or pane label and does not create a duplicate

#### Scenario: Revisit sends feedback to a live pane
- **WHEN** a node is revisited and its labelled pane is live
- **THEN** `SendTerminal` invokes `herdr pane run <pane_id> <feedback>` and no new pane is created

#### Scenario: Unusable stored pane is replaced
- **WHEN** a stored pane exists but is not usable
- **THEN** the runner closes that pane, creates a new labelled pane, and returns the new public pane ID

### Requirement: Herdr cleanup is ticket-scoped and preserves the worktree
`CloseTerminal`, `CloseTerminals`, and `CleanupRun` SHALL close only panes owned by the ticket, identified by the exact `<ticket>:` prefix on the pane label or its containing tab label, and SHALL tolerate an already-closed pane. `CleanupRun` SHALL additionally close the ticket's Herdr workspace and SHALL NOT remove the Git worktree, its branch, or its files. Cleanup SHALL roll forward: an absent repository, checkout, or workspace SHALL be reported as success rather than failing recovery. `SetEnvironmentStatus` SHALL be a successful no-op because Herdr has no workspace-status primitive.

#### Scenario: Cancellation closes ticket panes only
- **WHEN** cancellation requests cleanup for ticket `PAY-101`
- **THEN** panes in tabs labelled `PAY-101:<node>` or panes labelled `PAY-101:<node>` are closed, unrelated panes remain, and the workspace remains open

#### Scenario: End cleanup preserves the worktree
- **WHEN** a workflow reaches `end` with runner cleanup enabled
- **THEN** Herdr closes the run's labelled panes and the ticket workspace while the worktree checkout, its branch, and its files remain on disk

#### Scenario: Recovery rolls forward when the environment is gone
- **WHEN** cleanup or explicit database recovery runs after the ticket checkout or workspace was removed externally
- **THEN** the adapter reports success without creating or reopening anything

#### Scenario: Workspace status is unsupported
- **WHEN** core calls `SetEnvironmentStatus` with an in-progress, in-review, or completed status
- **THEN** the Herdr adapter returns success without renaming, closing, or deleting any workspace

### Requirement: Herdr CLI tests enforce the observed contract
The Herdr CLI wrapper SHALL have tests that execute a strict fake executable named `herdr`. The fake SHALL accept only the fixed production command and flag forms, validate absolute `--cwd` values and selected Herdr environment variables, and reproduce the observed transport contract, including error envelopes on stderr with nonzero exit statuses. Response fixtures SHALL be captured from the installed Herdr CLI rather than hand-written. Adapter behavior tests MAY use a fake wrapper client, but every production command shape SHALL also be covered by the strict executable tests. The production factory SHALL always construct the real CLI wrapper; fake clients and fake executables SHALL be confined to tests. A live test SHALL drive the production CLI wrapper itself against the installed Herdr binary in a disposable session, and SHALL be skipped when Herdr is unavailable so default CI stays green.

#### Scenario: Strict fake rejects invented terminal commands
- **WHEN** production code attempts `herdr terminal create`, `herdr workspace create`, `herdr worktree remove`, or any unsupported flag
- **THEN** the strict fake exits nonzero and the test fails

#### Scenario: Strict fake verifies pane and snapshot response shapes
- **WHEN** the wrapper calls tab creation, tab list, pane lookup, pane list, snapshot, or process-info
- **THEN** the fixture contains the real `result.root_pane`, `result.tab`, `result.tabs`, `result.panes`, `result.pane`, `result.snapshot`, or `result.process_info` location used by the adapter

#### Scenario: Strict fake verifies command environment
- **WHEN** the wrapper launches a Herdr command for a registered repository
- **THEN** the fake observes the absolute repository paths in Herdr's `--cwd` arguments and the configured Herdr session/socket selector rather than ambient unrelated values

#### Scenario: Live CLI contract is recorded before implementation
- **WHEN** the Herdr runner change begins implementation
- **THEN** a disposable Herdr session/workspace has first been exercised with the installed CLI and its version, accepted flags, response envelopes, pane restart behavior, and process-info behavior have been recorded

#### Scenario: Production never selects a fake
- **WHEN** the configured runner is `herdr`
- **THEN** the factory executes the installed `herdr` binary for pane/workspace inspection and no test fake, fake-selection flag, environment switch, or compatibility fallback is reachable

#### Scenario: Live smoke uses the installed CLI
- **WHEN** post-implementation live verification runs
- **THEN** it drives the production Go CLI wrapper against the installed Herdr binary in a disposable named session, rather than the strict fake executable or hand-run CLI commands
