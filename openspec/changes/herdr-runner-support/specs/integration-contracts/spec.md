## MODIFIED Requirements

### Requirement: Task plugins declare required repo keys explicitly
Every task factory SHALL return the YAML keys required for repo registration and SHALL derive an opaque canonical task-scope key from root and repo task config. `repo register` SHALL collect or deterministically derive required values, SHALL reject a task-scope key already used by another repo, and SHALL NOT depend on reflection or separate prompt metadata. For task systems that derive a component or equivalent value from a runner candidate, that value SHALL be derived from the configured runner candidate's stable name rather than prompted.

#### Scenario: Jira repo registration from Orca candidates
- **WHEN** the Jira factory declares `project` and `component` and the configured runner returns an Orca repository candidate
- **THEN** repo registration asks for project once and derives the component from the candidate name before saving

#### Scenario: Jira repo registration from Herdr candidates
- **WHEN** the Jira factory declares `project` and `component` and the configured runner returns a Herdr workspace candidate
- **THEN** repo registration asks for project once and derives the component from the Herdr workspace candidate name before saving

#### Scenario: Required value is absent
- **WHEN** the user omits a required repo key
- **THEN** registration fails before machine config changes

#### Scenario: Task scope is already registered
- **WHEN** a second repo resolves to the same task-system physical scope as an existing repo
- **THEN** registration fails before creating a duplicate poller

### Requirement: Runner executes harnesses in runner-owned environments
The runner SHALL discover and validate repos, ensure the execution environment required by its ownership model, find live terminals, close selected/run terminals while preserving any shared environment, start a terminal with a structured command, and clean runner-owned resources when requested. All node agents in a run SHALL share that run's environment. A runner MAY use one ticket-scoped environment per run, as Orca does, or one repository-scoped environment shared by runs, as Herdr does. The runner SHALL NOT parse workflow routes, reports, or harness-specific command syntax.

#### Scenario: Orca starts OpenCode
- **WHEN** the harness returns an OpenCode command for a node
- **THEN** the Orca runner creates or reuses the ticket worktree and starts that command in the node terminal

#### Scenario: Herdr starts OpenCode
- **WHEN** the harness returns an OpenCode command for a node and the configured runner is Herdr
- **THEN** the Herdr runner starts that command in a pane inside the repository's mapped Herdr workspace

#### Scenario: Successive nodes share files
- **WHEN** a run moves from exploration to coding
- **THEN** both node terminals execute in the same runner environment while retaining separate harness sessions

#### Scenario: Multiple Herdr runs share a repository workspace
- **WHEN** two tickets for the same registered repository are active at the same time
- **THEN** both runs use the one mapped Herdr workspace while their ticket/node panes remain separately identifiable

#### Scenario: Runner recovers without SQLite
- **WHEN** explicit database recovery calls `CloseTerminals` using repo/workflow/ticket values
- **THEN** the runner locates and closes stale run terminals without requiring persisted runner IDs and preserves the environment/workspace and repository code

#### Scenario: End cleanup is requested
- **WHEN** a workflow reaches end with runner cleanup enabled
- **THEN** the runner closes the run's terminals and removes only other run-environment resources it owns; a repository-scoped runner preserves its shared repository workspace

### Requirement: Runner terminal titles are stable and minimal
Runner terminal titles SHALL contain only `<ticket>:<node>`. They SHALL NOT contain `nodeVisitID`, workflow name, agent name, or other changing metadata. The `runner.Terminal.ID` field SHALL contain an adapter-owned opaque handle whose durability follows the runner contract; for Herdr this handle SHALL be the public `pane_id`, not the ephemeral `terminal_id`. Terminal identity SHALL be ticket scoped, and terminal lookup SHALL return only a live usable terminal.

#### Scenario: Node is first processed
- **WHEN** ticket `PAY-101` enters coding
- **THEN** the runner uses terminal title `PAY-101:coding`

#### Scenario: Coding is revisited
- **WHEN** the same ticket returns to coding with a new node visit ID
- **THEN** the previous coding terminal is closed or reconciled, a terminal with title `PAY-101:coding` is started with stable run/node metadata, and any resumable harness session may continue

#### Scenario: Same visit is reconciled
- **WHEN** reconciliation checks the current visit and its live terminal exists
- **THEN** the runner reuses that terminal without closing or renaming it

#### Scenario: Found terminal is dead
- **WHEN** lookup finds a stale/non-usable terminal record
- **THEN** the runner treats it as absent and starts a usable terminal

#### Scenario: Herdr terminal runtime is restarted
- **WHEN** Herdr returns the same public pane ID with a newly generated terminal ID after restart
- **THEN** the runner continues to identify the pane with its public pane ID and does not require the old terminal ID

#### Scenario: Herdr pane is restored as a shell
- **WHEN** Herdr restores a labelled pane but its foreground process is only the pane shell
- **THEN** the runner treats the pane as unusable for the harness and creates or relaunches a usable node terminal
