## ADDED Requirements

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
The Herdr runner SHALL execute the installed `herdr` CLI rather than importing Herdr internals, using a raw Herdr socket client, reading Herdr persisted state, or adding a Herdr SDK dependency. Read/create operations SHALL parse the JSON response envelopes emitted by the CLI. Mutating operations SHALL use the documented command and flag forms and SHALL return nonzero CLI/API failures to the caller.

#### Scenario: Snapshot uses the documented command
- **WHEN** the runner discovers Herdr workspaces
- **THEN** the CLI wrapper invokes exactly `herdr api snapshot` and reads `result.snapshot`

#### Scenario: Pane lookup uses documented flags
- **WHEN** the runner checks a persisted pane
- **THEN** the CLI wrapper invokes `herdr pane get <pane_id>` and `herdr pane process-info --pane <pane_id>`

#### Scenario: Unsupported CLI invocation fails
- **WHEN** the installed Herdr executable rejects a command or flag
- **THEN** the runner returns an error and does not silently retry with an invented or alternate command shape

### Requirement: Each registered repository maps to one Herdr workspace
The Herdr runner SHALL map a registered repository to one pre-existing Herdr workspace. Workspace lookup SHALL use normalized repository paths from pane `cwd` values and SHALL use the workspace label as a tie-breaker. The adapter SHALL reject an ambiguous path match rather than choosing arbitrarily. `DiscoverRepos` SHALL derive candidates from Herdr workspace labels and pane cwd values. `ValidateRepo` and `EnsureEnvironment` SHALL return an error when the workspace is missing or ambiguous; neither operation SHALL provision or recreate a workspace. `EnsureEnvironment` SHALL return the workspace ID as the opaque `runner.Environment.ID` and the registered repository path as `runner.Environment.Path`.

#### Scenario: Existing workspace is resolved by path
- **WHEN** a registered repository has path `/work/payments` and a Herdr workspace contains a pane with cwd `/work/payments`
- **THEN** `EnsureEnvironment` returns that workspace without creating another workspace

#### Scenario: Missing workspace is reported after external deletion
- **WHEN** a previously registered repository has no unambiguous matching Herdr workspace
- **THEN** `EnsureEnvironment` returns a missing-workspace error and does not invoke `herdr workspace create`

#### Scenario: Ambiguous workspace mapping is rejected
- **WHEN** more than one Herdr workspace contains panes matching the registered repository path and no label tie-breaker is unique
- **THEN** `EnsureEnvironment` returns an error and creates no workspace

#### Scenario: Workspace discovery returns repository candidates
- **WHEN** `DiscoverRepos` reads a Herdr snapshot containing a labelled workspace and a pane cwd
- **THEN** it returns one `runner.RepoCandidate` with the workspace label as `Name` and the normalized cwd as `Path`

### Requirement: Herdr workspaces are provisioned before repository registration
A Herdr workspace for a relay-flow repository SHALL be created by the operator with the public Herdr CLI before `repo register` or normal `serve` startup. The workspace SHALL use the repository path as its cwd and an identifiable label. Relay-flow's Herdr adapter SHALL not create, recreate, or delete repository workspaces; if the workspace is missing or ambiguous, validation and environment lookup SHALL fail with an actionable error.

#### Scenario: Operator provisions a repository workspace
- **WHEN** the operator runs `herdr workspace create --cwd /work/payments --label payments --no-focus`
- **THEN** the resulting workspace can be discovered from its pane cwd and registered as relay-flow repo `payments`

#### Scenario: Missing workspace blocks registration
- **WHEN** `repo register` validates `/work/payments` and no Herdr workspace has that repository cwd
- **THEN** registration fails and directs the operator to create the workspace with the documented Herdr CLI

### Requirement: Herdr launches nodes in labelled panes
The Herdr runner SHALL create each node terminal as a tab/root pane inside the repository workspace. The tab and pane SHALL be labelled exactly `<ticket>:<node>`, and the command returned by the harness SHALL remain opaque to the runner except for transport-safe serialization. The runner SHALL pass command environment values with repeated `--env KEY=VALUE` options on tab creation and SHALL submit the executable and arguments through `herdr pane run <pane_id> <command>`.

#### Scenario: First node creates a Herdr pane
- **WHEN** ticket `PAY-101` enters node `coding`
- **THEN** the runner invokes `herdr tab create --workspace <workspace_id> --cwd <repo_path> --label PAY-101:coding --no-focus`, labels the returned pane `PAY-101:coding`, and runs the harness command in that pane

#### Scenario: Harness command and environment are preserved
- **WHEN** the harness returns executable, arguments, multiline prompt text, and `RELAY_FLOW_*` environment values
- **THEN** the runner forwards all argument values and environment values without parsing OpenCode-specific flags or dropping multiline content

#### Scenario: Failed pane setup does not leave an owned pane
- **WHEN** pane labelling or command submission fails after tab creation
- **THEN** the runner attempts to close the created pane before returning the original failure

### Requirement: Herdr uses public pane identity and detects usable runtime
The Herdr runner SHALL store the public `pane_id` in `runner.Terminal.ID` and SHALL never treat `terminal_id` as durable identity. `FindTerminal` SHALL inspect the pane and its process information and SHALL return only a live usable terminal. A pane whose runtime is absent, whose process information is unavailable, or whose foreground is only a restored shell SHALL be treated as unusable.

#### Scenario: Terminal ID changes after restart
- **WHEN** Herdr returns the same public pane ID with a new `terminal_id` after restart
- **THEN** `FindTerminal` returns the pane ID as the terminal handle and does not require the old terminal ID

#### Scenario: Missing pane is absent
- **WHEN** `herdr pane get <pane_id>` reports that the pane no longer exists
- **THEN** `FindTerminal` returns `(Terminal{}, false, nil)` and does not report a usable terminal

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

### Requirement: Herdr cleanup is ticket-scoped and preserves shared workspaces
`CloseTerminal`, `CloseTerminals`, and `CleanupRun` SHALL close only panes owned by the ticket, identified by the exact `<ticket>:` prefix on the pane label or its containing tab label, and SHALL tolerate an already-closed pane. They SHALL preserve the repository workspace and any panes not owned by that ticket. `SetEnvironmentStatus` SHALL be a successful no-op because Herdr has no shared workspace-status primitive.

#### Scenario: Cancellation closes ticket panes only
- **WHEN** cancellation requests cleanup for ticket `PAY-101` in a shared repository workspace
- **THEN** panes in tabs labelled `PAY-101:<node>` or panes labelled `PAY-101:<node>` are closed, unrelated ticket panes remain, and the workspace remains open

#### Scenario: End cleanup preserves the workspace
- **WHEN** a workflow reaches `end` with runner cleanup enabled
- **THEN** Herdr closes the run's labelled panes but does not close or delete the repository workspace

#### Scenario: Recovery closes stale panes without workspace deletion
- **WHEN** explicit database recovery calls `CloseTerminals` without SQLite runner IDs
- **THEN** the adapter discovers tab/pane labels and closes the ticket's labelled panes from Herdr state while preserving the workspace and repository files

#### Scenario: Workspace status is unsupported
- **WHEN** core calls `SetEnvironmentStatus` with an in-progress, in-review, or completed status
- **THEN** the Herdr adapter returns success without renaming or deleting the shared workspace

### Requirement: Herdr CLI tests enforce the observed contract
The Herdr CLI wrapper SHALL have tests that execute a strict fake executable named `herdr`. The fake SHALL accept only the fixed production command and flag forms, validate absolute `--cwd` values and selected Herdr environment variables, return captured-like JSON envelopes, and reject unsupported or malformed invocations. Adapter behavior tests MAY use a fake wrapper client, but every production command shape SHALL also be covered by the strict executable tests. The production factory SHALL always construct the real CLI wrapper; fake clients and fake executables SHALL be confined to tests, and a live smoke test SHALL exercise the installed Herdr CLI with a real configured harness command.

#### Scenario: Strict fake rejects invented terminal commands
- **WHEN** production code attempts `herdr terminal create` or any unsupported flag
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
- **THEN** it uses a disposable named Herdr session and the installed Herdr binary plus a real configured harness command, rather than the strict fake executable
