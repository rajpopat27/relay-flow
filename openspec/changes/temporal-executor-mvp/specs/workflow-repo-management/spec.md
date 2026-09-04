## MODIFIED Requirements

### Requirement: Machine config stores global settings and registered repos
Machine config at `~/.relay-flow/config.yaml` SHALL store machine-wide polling and retention settings, the selected task, runner, harness, and durable-executor plugin names/configuration, Temporal address/namespace when Temporal is selected, and registered repo name/path/task config. It SHALL be written atomically with owner-only permissions. `executorPlugin` SHALL default to `goworkflows` when omitted. The local state database SHALL also contain one singleton `relay_executor_identity` record matching the initialized executor and, for Temporal, its address and namespace; this record SHALL be verified before workers start.

#### Scenario: Valid embedded machine config
- **WHEN** machine config is loaded without `executorPlugin`
- **THEN** relay-flow strictly decodes it and selects `goworkflows` without requiring Temporal settings

#### Scenario: Valid Temporal machine config
- **WHEN** machine config selects `temporal`
- **THEN** it strictly decodes `executorPlugin`, `temporalAddress`, and `temporalNamespace` and requires a non-empty namespace

#### Scenario: Unknown machine field
- **WHEN** machine config contains an unknown root or repo field
- **THEN** loading fails with a named validation error

### Requirement: Initialization selects plugins and initializes state
`relay-flow init` SHALL select and store task, runner, harness, and durable-executor plugin names and initialize the local SQLite state required by the selected executor. The durable executor SHALL default to `goworkflows`. Its interactive selection SHALL present a selection titled exactly `Select executor`, with `goworkflows` as the default option; non-interactive init SHALL accept an equivalent executor flag.

When `temporal` is selected, init SHALL ask for the Temporal server address and namespace/team name, default the address to `localhost:7233`, require an explicit namespace, connect through the public Temporal Go SDK, and create the namespace when absent or verify it when present. A newly created namespace SHALL use workflow-execution retention of at least `max(30 days, completedRunRetentionDays)`; the MVP default is 30 days. Init SHALL not import or start Temporal Server. When `goworkflows` is selected, init SHALL not contact Temporal.

The executor, Temporal address, and Temporal namespace SHALL be immutable for an initialized relay-flow home. `init --force` SHALL reject any attempt to change them and SHALL preserve existing state. The singleton `relay_executor_identity` record SHALL be created in a SQLite transaction during initialization and SHALL be compared with machine configuration at serve startup. If a process crash leaves config and identity temporarily mismatched, serve SHALL refuse to start and rerunning init with the same values SHALL complete the initialization; different values remain rejected. There SHALL be no engine migration, automatic backend selection, or fallback.

#### Scenario: First embedded initialization
- **WHEN** the user completes init with the default executor
- **THEN** relay-flow writes `executorPlugin: goworkflows`, initializes the embedded-engine SQLite state, and does not contact Temporal

#### Scenario: First Temporal initialization
- **WHEN** the user selects Temporal and enters an address and namespace/team name
- **THEN** relay-flow creates or verifies that namespace with retention of at least `max(30 days, completedRunRetentionDays)`, writes the values to machine config, and initializes the Temporal-mode relay projection

#### Scenario: Temporal namespace is missing
- **WHEN** the selected Temporal namespace does not exist and registration is permitted
- **THEN** init registers it with at least `max(30 days, completedRunRetentionDays)` retention before reporting success

#### Scenario: Existing namespace is unsuitable
- **WHEN** the selected registered namespace is in an invalid state or has less than `max(30 days, completedRunRetentionDays)` retention
- **THEN** init fails with an actionable error, reports no success, and does not replace an existing valid machine configuration

#### Scenario: Existing executor is changed
- **WHEN** `init --force` attempts to change an initialized executor
- **THEN** initialization fails without changing config, SQLite state, workflows, logs, or task-system state

#### Scenario: Existing Temporal identity is changed
- **WHEN** `init --force` attempts to change the Temporal address or namespace of a Temporal installation
- **THEN** initialization fails without changing the durable configuration or local state

#### Scenario: Initialization is interrupted
- **WHEN** a process stops between atomic config writing and the SQLite identity transaction
- **THEN** the next serve refuses to start on the mismatch and rerunning init with the same values completes initialization without selecting a different executor

#### Scenario: Temporal configuration is requested for embedded mode
- **WHEN** `goworkflows` is selected
- **THEN** Temporal address/namespace are not required, Temporal-only flags are rejected, and no Temporal namespace operation occurs

#### Scenario: Legacy state has no executor identity
- **WHEN** an existing local database has no `relay_executor_identity` row and machine configuration omits `executorPlugin`
- **THEN** relay-flow accepts it only as a legacy `goworkflows` installation and records no Temporal identity

### Requirement: Command surface is grouped by resource
The supported command surface SHALL include:

```text
relay-flow init [--force] [--executor-plugin <goworkflows|temporal>]
                   [--temporal-address <host:port>]
                   [--temporal-namespace <name>]
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

`serve --recover` SHALL use the selected backend's fixed recovery semantics: destructive from-`start` recovery for `goworkflows`, non-destructive SQLite projection rebuild for `temporal`. There SHALL be no separate engine-switch command, no migration command, and no command that starts or stops Temporal Server.

#### Scenario: Temporal init flags are complete
- **WHEN** non-interactive init selects Temporal
- **THEN** `--executor-plugin temporal` plus required `--temporal-namespace` and optional `--temporal-address` provide the same values as the interactive flow, with `localhost:7233` as the address default

#### Scenario: Engine switch command is requested
- **WHEN** a user attempts to change executors after initialization
- **THEN** the command fails rather than migrating, copying, or falling back to the other engine

### Requirement: Normal serve requires valid durable state
Normal `serve` SHALL acquire the single-process flock, require a valid initialized local SQLite database, validate the selected executor and its configuration, validate plugins/repos/workflows/connectivity/agents, start the selected durable worker, start Repo Pollers, and then serve the Unix socket. It SHALL refuse to silently create missing execution state.

For `goworkflows`, normal startup resumes unfinished embedded runs. For `temporal`, normal startup connects to the configured namespace and resumes unfinished Temporal workflows; it SHALL not infer Temporal history loss from a missing local projection row. A Temporal projection rebuild SHALL occur only under explicit `serve --recover`, and the Temporal engine SHALL complete that rebuild before normal pollers begin.

#### Scenario: Embedded server starts
- **WHEN** an initialized home selects `goworkflows` and has a valid SQLite database
- **THEN** serve starts the embedded workers and pollers without contacting Temporal

#### Scenario: Temporal server starts
- **WHEN** an initialized home selects `temporal` and the configured namespace/address are valid
- **THEN** serve starts the Temporal worker for that namespace and the pollers using the local relay projection

#### Scenario: Temporal namespace is unavailable
- **WHEN** Temporal startup cannot connect to the configured address or namespace
- **THEN** serve fails without falling back to `goworkflows` or mutating task-system state

#### Scenario: Database is missing
- **WHEN** normal serve cannot find the initialized local database
- **THEN** startup fails and directs the operator to explicit backend-appropriate recovery rather than silently creating it

#### Scenario: Executor configuration does not match the initialized home
- **WHEN** serve detects a changed executor or Temporal identity for an existing home
- **THEN** startup fails before workers or pollers start and does not migrate state

#### Scenario: Temporal recovery is selected
- **WHEN** `serve --recover` is used with the Temporal executor
- **THEN** the Temporal engine rebuilds the local projection before normal pollers start, without invoking the goworkflows task-system reset path

#### Scenario: Agent validation fails
- **WHEN** a workflow references an unavailable configured harness agent
- **THEN** startup fails before any tickets are polled or claimed

### Requirement: Graceful shutdown is bounded
Shutdown SHALL stop accepting commands and new polling work, stop the selected durable worker, allow currently running calls up to 30 seconds to return, and close the Unix socket and local projection database. Already-running external activities SHALL NOT be assumed interruptible. Temporal client/worker shutdown SHALL occur before closing the local projection database.

#### Scenario: Stop during a Temporal activity
- **WHEN** stop is requested while a Temporal activity is running
- **THEN** relay-flow stops new polling, attempts bounded worker shutdown, closes the client after worker shutdown, and leaves Temporal history available for the next start

#### Scenario: Stop during an embedded activity
- **WHEN** stop is requested while a go-workflows activity is running
- **THEN** relay-flow preserves embedded durable state and leaves unfinished work recoverable on the next start

### Requirement: Completed data is retained by global policy
Local relay projection cleanup SHALL run once at startup, after Temporal projection rebuild when recovery is requested and before normal pollers, and SHALL remove only completed or canceled projection rows older than `completedRunRetentionDays`, default 30 days. In `goworkflows` mode, embedded engine history follows the existing retention policy. In `temporal` mode, Temporal namespace retention SHALL be configured or verified at least `max(30 days, completedRunRetentionDays)` and Temporal Server SHALL own history deletion and recovery. Relay-flow SHALL not access Temporal Server storage or claim exact per-workflow history deletion.

#### Scenario: Temporal namespace retention
- **WHEN** a Temporal namespace is created for relay-flow
- **THEN** its workflow-execution retention is configured to at least `max(30 days, completedRunRetentionDays)`

#### Scenario: Temporal projection cleanup
- **WHEN** a completed Temporal run exceeds local projection retention
- **THEN** its local relay projection may be removed while Temporal Server retains or expires its history according to namespace policy

#### Scenario: Active Temporal run reaches retention age
- **WHEN** a Temporal run is starting, running, waiting, blocked, or canceling for longer than 30 days
- **THEN** relay-flow does not remove its local projection or cause Temporal to close it

#### Scenario: Temporal history expires
- **WHEN** a closed Temporal history exceeds namespace retention
- **THEN** relay-flow does not recreate a workflow or reset its task-system artifacts merely because that history is no longer available
