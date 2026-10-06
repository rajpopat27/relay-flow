## MODIFIED Requirements

### Requirement: Normal serve requires valid durable state

Normal `serve` SHALL acquire the single-process flock, require a valid
initialized SQLite database, construct local selected plugins, complete the
basic machine prerequisite phase, load and locally integrity-check stored
workflows, start durable workers and Repo Pollers, and then serve the Unix
socket. It SHALL refuse to silently create missing execution state. The
prerequisite phase SHALL finish before workers, polling, explicit recovery
side effects or readiness. Full repository/workflow/task-setting/agent
validation SHALL remain at registration/submission rather than being repeated
for every accepted workflow at startup. Repo polling and repeated `EnsureRun`
SHALL reconcile only active runs whose current terminal is missing.

#### Scenario: Second server starts
- **WHEN** another relay-flow server holds the flock
- **THEN** startup fails before workers or pollers start

#### Scenario: Database is missing
- **WHEN** normal serve cannot find the initialized database
- **THEN** startup fails and directs the operator to explicit recovery when appropriate

#### Scenario: Required basic dependency fails
- **WHEN** selected task connectivity, runner executable/runtime, or harness executable probing fails
- **THEN** startup fails before workers, pollers, recovery effects or socket readiness, even without registered repos

#### Scenario: Stored workflow-specific validation is unavailable
- **WHEN** basic machine prerequisites pass but a stored workflow is malformed, changed, missing an accepted hash, or cannot bind locally
- **THEN** that workflow remains isolated from routing and available for inspection while management and unrelated accepted workflows remain available

#### Scenario: Active run exists at startup
- **WHEN** durable workers start with an active run waiting for a report
- **THEN** the first repo poll ensures the run and relaunches its current visit only if the terminal is missing

### Requirement: Serve supports detached startup

Plain `relay-flow serve` and `rf serve` SHALL spawn a detached child, wait until
the Unix socket responds after prerequisite success, print `Relay-flow server
started`, and return only after readiness. `serve --background` SHALL remain an
equivalent compatibility spelling. `serve --foreground` SHALL run the server
in the current process until stop or signal. The detached child SHALL run the
foreground implementation without recursively selecting detached mode,
preserve `--debug` and `--recover`, inherit the process environment, and use
the relay-flow state root as its working directory. Dependency failures SHALL
exit nonzero without success publication and SHALL identify the safe selected
dependency/operation and server-log location. Missing executable, launch,
authentication/permission, connection, malformed response and timeout failures
SHALL be distinguishable. Detached error enrichment SHALL use only bounded
startup-failure records from the current attempt and launched child; stale,
unrelated or unavailable records SHALL NOT be replayed. No additional health
endpoint or client-side executable lookup SHALL stand in for the server's own
prerequisites. Stop SHALL work through the existing API for either serve mode.

#### Scenario: Default server becomes ready
- **WHEN** serve or rf serve succeeds
- **THEN** the command returns only after prerequisites pass and the server responds over the Unix socket

#### Scenario: Foreground server remains blocking
- **WHEN** serve --foreground succeeds
- **THEN** the command remains running until stop or signal

#### Scenario: Background compatibility spelling becomes ready
- **WHEN** serve --background succeeds
- **THEN** the command returns only after the prerequisite-gated server responds

#### Scenario: Conflicting modes are rejected
- **WHEN** serve --foreground --background is supplied
- **THEN** the command exits 2 before starting a child or acquiring server lifecycle state

#### Scenario: Detached startup fails
- **WHEN** the detached child exits or does not become ready before the startup timeout
- **THEN** the command fails and identifies server.log without publishing readiness

#### Scenario: Detached dependency failure is reported
- **WHEN** the detached child fails a required dependency check
- **THEN** the caller receives the child's safe dependency/operation diagnostic and log location, not a successful startup message or another attempt's error

#### Scenario: Detached server is independent of caller directory
- **WHEN** serve starts from a temporary or deleted caller working directory
- **THEN** the detached child uses the durable relay-flow state root as its working directory

### Requirement: Workflow definitions are persisted atomically

`workflow submit --file` SHALL strictly parse and validate the workflow,
validate every referenced repo and effective task configuration, validate
repo accessibility/task access/agents through the selected adapters, then
atomically store the exact submitted YAML and its accepted SHA-256 hash and
update in-memory bindings. A failed validation or write SHALL leave the
previous accepted YAML, hash and bindings unchanged. Basic startup health
SHALL NOT bypass these submission checks.

#### Scenario: New workflow submission
- **WHEN** a valid workflow with a new name is submitted
- **THEN** its exact YAML/hash pair is stored and relevant repo bindings are rebuilt

#### Scenario: Candidate environment validation fails
- **WHEN** a replacement fails full task, runner repo or harness agent validation despite healthy basic prerequisites
- **THEN** submission fails and the prior accepted YAML/hash and routing bindings remain unchanged

#### Scenario: Write fails
- **WHEN** atomic file replacement fails
- **THEN** the previous accepted workflow file and in-memory definition remain active

#### Scenario: Server starts
- **WHEN** normal serve starts with valid machine config, durable state and basic prerequisites
- **THEN** stored workflow integrity and local structure are checked and only trusted, locally bindable definitions publish routes before Repo Pollers begin, without repeating full submission preflight

## ADDED Requirements

### Requirement: Machine prerequisite budgets are bounded and independent of workflows

The prerequisite phase SHALL have one ten-second aggregate deadline and a
five-second ceiling for each external probe, without retries/backoff,
per-workflow/node duplication, workflow-ticket polling, history scans, agent
launches or service auto-start/install. Only selected plugins SHALL be checked.
Shared task connections SHALL be deduplicated by the owning adapter. These
budgets SHALL cover prerequisites, not total database-opening or replay time.
Zero repos and empty responding runner runtimes SHALL be valid. With no
explicit Beads workspace, local bd availability SHALL still be checked and
connection verification SHALL be reported deferred to registration; configured
connections SHALL NOT be silently skipped.

#### Scenario: Multiple connections consume the shared budget
- **WHEN** distinct task connections and the runner together would exceed ten seconds
- **THEN** later probes receive only the aggregate's remaining time and startup fails on timeout

#### Scenario: Guided registration starts a server
- **WHEN** first-run repository setup encounters a startup prerequisite failure
- **THEN** it propagates the failure rather than attempting registration, modifying environments or launching another server

#### Scenario: Guided registration reuses a ready server
- **WHEN** the existing server is already ready
- **THEN** onboarding reuses it without reinterpreting the client's PATH as the running server's environment
