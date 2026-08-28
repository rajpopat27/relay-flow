## ADDED Requirements

### Requirement: Machine config stores global settings and registered repos
Machine config at `~/.relay-flow/config.yaml` SHALL store machine-wide polling and retention settings, selected plugin names/config, and registered repo name/path/task config. It SHALL be written atomically with owner-only permissions.

#### Scenario: Valid machine config
- **WHEN** machine config is loaded
- **THEN** plugin selections, global config, and repo registrations are strictly decoded

#### Scenario: Unknown machine field
- **WHEN** machine config contains an unknown root or repo field
- **THEN** loading fails with a named validation error

### Requirement: Filesystem layout and permissions are fixed
relay-flow SHALL use owner-only directory `~/.relay-flow` with mode `0700`. It SHALL use `config.yaml`, `state.db`, `server.sock`, `server.lock`, `server.log`, `plugin.log`, and `workflows/<name>.yaml`. Config, database, lock, and log files SHALL use mode `0600`; workflow files SHALL use `0644`; the Unix socket SHALL use `0600`.

#### Scenario: Initialization creates root layout
- **WHEN** relay-flow initializes on a new machine
- **THEN** it creates the root directory and database/config files with the specified paths and permissions

#### Scenario: Workflow is submitted
- **WHEN** a workflow is stored
- **THEN** its path is `~/.relay-flow/workflows/<name>.yaml` with mode `0644`

#### Scenario: Server listens
- **WHEN** serve starts successfully
- **THEN** it creates `~/.relay-flow/server.sock` with mode `0600` and uses `server.lock` for single-process ownership

### Requirement: Config and workflow writes are atomically replaced
Machine-config and workflow-file writes SHALL use `github.com/google/renameio/v2` so readers observe either the old complete file or the new complete file. A failed replacement SHALL leave the previous file usable.

#### Scenario: Process fails during workflow write
- **WHEN** workflow replacement fails before atomic rename
- **THEN** the previous workflow file remains complete and active

### Requirement: Global defaults are deterministic
`pollIntervalSeconds` SHALL default to 15 and SHALL be positive. `completedRunRetentionDays` SHALL default to 30 and SHALL be positive. Both settings SHALL apply machine-wide; per-repo and per-workflow overrides SHALL be rejected.

#### Scenario: Defaults omitted
- **WHEN** both global settings are absent
- **THEN** the server uses a 15-second poll interval and 30-day completed-run retention

#### Scenario: Invalid interval
- **WHEN** `pollIntervalSeconds` is zero or negative
- **THEN** machine config validation fails

#### Scenario: Workflow attempts poll override
- **WHEN** workflow YAML contains `pollIntervalSeconds`
- **THEN** strict workflow validation rejects it

### Requirement: Initialization selects plugins and initializes state
`relay-flow init` SHALL select and store task, runner, and harness plugin names and initialize SQLite. Its interactive titles SHALL be `Select task system`, `Select runner`, and `Select harness`; a plugin type with one registered option SHALL be selected automatically. It SHALL print the selected values and `Relay-flow initialized`. Without `--force`, existing machine config or database SHALL cause refusal. `init --force` SHALL refuse while the server is running or any run is nonterminal, and otherwise SHALL preserve `state.db`, completed history, workflows, logs, repo registrations, and machine settings while updating plugin selections. It SHALL NOT require repo registration or prompt for optional global plugin config.

#### Scenario: First initialization
- **WHEN** the user completes plugin selection
- **THEN** relay-flow writes machine config and initializes its database

#### Scenario: No repos selected
- **WHEN** initialization completes without registered repos
- **THEN** initialization succeeds and repos can be registered separately

#### Scenario: Initialization is rerun with existing database
- **WHEN** relay-flow is initialized again without `--force` after runs have been recorded
- **THEN** initialization fails without changing machine config or execution history

#### Scenario: Forced initialization updates a safe stopped instance
- **WHEN** `init --force` runs while the server is stopped and all recorded runs are terminal
- **THEN** plugin selections are updated without recreating the database or changing history, workflows, logs, repos, or other machine settings

#### Scenario: Forced initialization is unsafe
- **WHEN** `init --force` runs while the server is running or a run is nonterminal
- **THEN** initialization fails without changing configuration or durable state

### Requirement: Repos are registered independently
Interactive `relay-flow repo register` SHALL use the configured runner to discover repos and a `charmbracelet/huh` multi-select titled `Select repositories`. It SHALL ask for the Jira project once, SHALL derive each component from the selected Orca repo name, and SHALL never prompt for component. It SHALL register selected repos sequentially through the existing API using each Orca name/path and the shared project; no batch endpoint or rollback SHALL be used. A failure SHALL identify the failed repo and retain prior successful registrations. Non-interactive registration SHALL remain supported with stable `--name`/`--path`, but component SHALL be derived from `--name` and SHALL NOT be accepted as an override. Registration SHALL validate runner and task-system connectivity, reject a canonical task-system scope already assigned to another repo, and atomically persist each repo entry.

#### Scenario: Successful registration
- **WHEN** repo name/path and all required task values are valid
- **THEN** the repo becomes available for workflow submission and polling

#### Scenario: User searches discovered repos
- **WHEN** the runner discovers many repos during interactive registration
- **THEN** a `charmbracelet/huh` multi-select titled `Select repositories` lets the user use Space to select repos and Enter to confirm

#### Scenario: Multiple Jira repos are registered
- **WHEN** the user selects multiple Orca repos and enters one Jira project
- **THEN** each repo is registered sequentially with that project and a component equal to its Orca repo name

#### Scenario: A later registration fails
- **WHEN** one selected repo fails after earlier selected repos were registered
- **THEN** the failure identifies that repo and the earlier registrations remain

#### Scenario: Duplicate repo name
- **WHEN** the chosen name is already registered
- **THEN** registration fails without changing machine config

#### Scenario: Duplicate canonical path
- **WHEN** another registered repo uses the same canonical path
- **THEN** registration fails

#### Scenario: Duplicate task-system scope
- **WHEN** another repo already maps to the same task-system physical isolation, such as the same Jira project/component
- **THEN** registration fails to preserve one-to-one repo mapping

#### Scenario: Runner internal ID changes
- **WHEN** a runner's internal workspace identifier differs later
- **THEN** the runner resolves it from stored repo name/path rather than a hand-written persisted ID

### Requirement: Repo removal protects references
`repo remove` SHALL reject removal while any stored workflow references the repo or any active run uses it. Successful removal SHALL stop its Repo Poller and atomically remove its registration.

#### Scenario: Workflow references repo
- **WHEN** a stored workflow lists the repo
- **THEN** removal fails and identifies the referencing workflow

#### Scenario: Repo is unused
- **WHEN** no stored workflow or active run references the repo
- **THEN** the Repo Poller stops and the registration is removed

### Requirement: Workflow definitions are persisted atomically
`workflow submit --file` SHALL strictly parse and validate the workflow, validate every referenced repo and task configuration, then atomically store it at `~/.relay-flow/workflows/<name>.yaml` and update in-memory bindings. A failed validation or write SHALL leave the existing file and bindings unchanged.

#### Scenario: New workflow submission
- **WHEN** a valid workflow with a new name is submitted
- **THEN** its file is stored and relevant repo bindings are rebuilt

#### Scenario: Write fails
- **WHEN** atomic file replacement fails
- **THEN** the previous workflow file and in-memory definition remain active

#### Scenario: Server starts
- **WHEN** normal serve starts with valid machine config and database
- **THEN** all stored workflow files are loaded, validated, and bound before Repo Pollers begin

### Requirement: Workflow replacement has no concurrent versions
Submitting an existing workflow name SHALL replace it only when that workflow has no active, waiting, blocked, starting, running, or canceling run. Run claim/creation and workflow replacement/removal SHALL share a lifecycle gate so a run cannot begin with an old definition during replacement. Existing runs SHALL use their immutable workflow snapshot. The system SHALL NOT expose workflow version selection.

#### Scenario: Replacement with active run
- **WHEN** any active run uses the workflow
- **THEN** submission is rejected and the stored/in-memory definition remains unchanged

#### Scenario: Replacement after completion
- **WHEN** all runs of the workflow are completed or canceled
- **THEN** a valid submission atomically replaces the definition for future runs

#### Scenario: Run creation races with replacement
- **WHEN** a matching ticket is being claimed while the workflow is submitted for replacement
- **THEN** the lifecycle gate serializes the operations so the run is created from one complete definition and replacement observes that run as active

### Requirement: Workflow removal protects active runs
`workflow remove` SHALL reject removal while the workflow has an active run. Successful removal SHALL delete the stored file and rebuild repo bindings.

#### Scenario: Remove active workflow
- **WHEN** a workflow has a waiting HITL run
- **THEN** removal is rejected

#### Scenario: Remove inactive workflow
- **WHEN** the workflow has no active runs
- **THEN** its file and repo bindings are removed

### Requirement: One Unix-socket API serves CLI and future UI
The server SHALL expose workflow, repo, run, report, stop, and discovery operations over HTTP on the same-user Unix socket. CLI commands SHALL call this API rather than implementing separate state-changing behavior.

#### Scenario: CLI submits workflow
- **WHEN** the user runs workflow submit
- **THEN** the CLI sends the workflow bytes to the server API and displays the server result

#### Scenario: Future UI lists runs
- **WHEN** a local UI requests run data
- **THEN** it can call the same run-list API used by the CLI

### Requirement: API responses and status codes are stable
Every server response SHALL be JSON. Success SHALL use `{"ok":true,"data":...}`. Failure SHALL use `{"ok":false,"error":{"code":"<lowerCamel>","message":"..."}}`. Malformed input SHALL return HTTP 400, missing resources 404, lifecycle/state conflicts 409, unsupported methods 405, and unexpected server failures 500.

#### Scenario: Workflow replacement conflicts with active run
- **WHEN** workflow submit attempts replacement during an active run
- **THEN** the API returns HTTP 409 with lower-camel error code and human-readable message

#### Scenario: Request JSON is malformed
- **WHEN** an endpoint receives invalid JSON
- **THEN** it returns HTTP 400 using the standard error envelope

#### Scenario: Workflow list succeeds
- **WHEN** workflows are listed
- **THEN** the API returns HTTP 200 with the standard success envelope

### Requirement: CLI exit codes are stable
CLI commands SHALL exit 0 on success, 2 for command/flag usage errors, and 1 for validation, server, or operation failures. CLI stderr SHALL contain the human-readable error message; machine-readable endpoint data SHALL remain JSON.

#### Scenario: Unknown flag
- **WHEN** a command receives an unsupported flag
- **THEN** it prints usage and exits 2

#### Scenario: Server rejects operation
- **WHEN** the API returns a validation or conflict error
- **THEN** the CLI prints the message to stderr and exits 1

### Requirement: Command surface is grouped by resource
The supported command surface SHALL include:

```text
relay-flow init [--force]
relay-flow serve [--recover] [--background]
relay-flow stop
relay-flow report

relay-flow workflow submit --file <path>
relay-flow workflow remove --name <name>
relay-flow workflow list
relay-flow workflow get --name <name>

relay-flow repo register
relay-flow repo remove --name <name>
relay-flow repo list
relay-flow repo get --name <name>

relay-flow run list
relay-flow run get --ticket <key>
relay-flow run cancel --ticket <key>
```

There SHALL be no separate workflow update command; submit performs create-or-replace subject to active-run restrictions.

#### Scenario: Existing workflow is submitted
- **WHEN** the workflow name exists and has no active runs
- **THEN** submit replaces it without requiring an update command

#### Scenario: Stop command is used
- **WHEN** `relay-flow stop` is invoked
- **THEN** the server stops accepting new work, performs bounded shutdown, and exits

### Requirement: Normal serve requires valid durable state
Normal `serve` SHALL acquire the single-process flock, require a valid initialized SQLite database, validate plugins/repos/workflows/connectivity/agents, start durable workers, start Repo Pollers, and then serve the Unix socket. It SHALL refuse to silently create missing execution state. Repo polling and repeated `EnsureRun` SHALL reconcile only active runs whose current terminal is missing.

#### Scenario: Second server starts
- **WHEN** another relay-flow server holds the flock
- **THEN** startup fails before workers or pollers start

#### Scenario: Database is missing
- **WHEN** normal serve cannot find the initialized database
- **THEN** startup fails and directs the operator to explicit recovery when appropriate

#### Scenario: Agent validation fails
- **WHEN** a workflow references an unavailable configured harness agent
- **THEN** startup fails before any tickets are polled or claimed

#### Scenario: Active run exists at startup
- **WHEN** durable workers start with an active run waiting for a report
- **THEN** the first repo poll ensures the run and relaunches its current visit only if the terminal is missing

### Requirement: Serve supports detached startup
Plain `relay-flow serve` SHALL remain a blocking foreground command. `serve --background` SHALL spawn a detached child running foreground serve without the background flag, preserve `--debug` and `--recover`, wait until the Unix socket responds, and print `Relay-flow server started`. Startup failure or timeout SHALL fail and identify `server.log`. `relay-flow stop` SHALL stop either foreground or background servers through the existing API.

#### Scenario: Background server becomes ready
- **WHEN** `serve --background` succeeds
- **THEN** the command returns only after the server responds over the Unix socket

#### Scenario: Background startup fails
- **WHEN** the detached child exits or does not become ready before the startup timeout
- **THEN** the command fails and points the user to `server.log`

### Requirement: Graceful shutdown is bounded
Shutdown SHALL stop accepting commands and new polling work, cancel worker polling, allow currently running calls up to 30 seconds to return, and close the socket and database. Already-running external activities SHALL NOT be assumed interruptible.

#### Scenario: Stop during an activity
- **WHEN** stop is requested while an activity is running
- **THEN** relay-flow waits up to 30 seconds and leaves durable state recoverable on the next start

### Requirement: Standard flag parsing remains the CLI parser
The CLI SHALL use Go's standard `flag` package for command and option parsing. `charmbracelet/huh` SHALL be limited to interactive init/repo-registration forms and searchable selections.

#### Scenario: Non-interactive workflow command
- **WHEN** workflow submit/list/get/remove is invoked
- **THEN** standard flag parsing handles its arguments without initializing a TUI framework

### Requirement: Completed data is retained by global policy
Retention cleanup SHALL remove only completed or canceled workflow histories and matching run-projection rows older than `completedRunRetentionDays`. Cleanup SHALL preserve starting, running, waiting, blocked, and canceling runs. Canceled parents SHALL retain their permanent task-system cancellation markers.

#### Scenario: Retention runs
- **WHEN** cleanup evaluates completed and waiting runs
- **THEN** only completed runs older than the configured period are removed

#### Scenario: Canceled run reaches retention
- **WHEN** a canceled run is older than the configured period
- **THEN** its history and projection row are removed while its parent cancellation marker remains
