## MODIFIED Requirements

### Requirement: Equivalent CLI aliases

The supported CLI SHALL be available as both `relay-flow` and `rf`. The two
names SHALL use the same `RELAY_FLOW_HOME`, Unix socket, lock, database, logs,
exit codes, command parser, and server lifecycle. `rf` SHALL be an alias, not a
separate configuration or daemon.

#### Scenario: Alias version output

- **WHEN** `relay-flow version` and `rf version` are invoked
- **THEN** both commands print matching version/build information and exit 0

#### Scenario: Alias stops a canonical server

- **WHEN** a server is started with one executable name and stopped with the
  other
- **THEN** the same Unix-socket server shuts down successfully

### Requirement: Serve startup modes

Plain `relay-flow serve` and `rf serve` SHALL start a detached child, wait for
Unix-socket readiness, print `Relay-flow server started`, and return success
only after readiness. `serve --background` SHALL remain an equivalent
compatibility spelling. `serve --foreground` SHALL remain blocking until
`stop` or signal. `--foreground` and `--background` together SHALL fail with
CLI usage exit code 2 before starting a child or acquiring server lifecycle
state. Readiness and child startup failures SHALL identify `server.log`.

The detached child SHALL run the existing foreground server implementation
without recursively selecting detached mode. It SHALL receive `--recover` and
`--debug` exactly once when requested, and SHALL not receive either user-facing
mode flag.

#### Scenario: Default detached startup

- **WHEN** `rf serve` succeeds
- **THEN** it returns after the Unix-socket API responds and prints the ready
  message

#### Scenario: Foreground startup

- **WHEN** `relay-flow serve --foreground` starts successfully
- **THEN** the command remains running until the server is stopped or signaled

#### Scenario: Conflicting modes

- **WHEN** `serve --foreground --background` is supplied
- **THEN** the command exits 2 without starting a child

### Requirement: Stable daemon working directory

Detached children SHALL use the durable relay-flow state root as their process
working directory rather than inheriting the caller's directory. Real
foreground server processes SHALL likewise anchor inherited external runner
CLI calls at that durable root. Starting from a temporary or deleted caller
directory SHALL not cause later runner/Orca CLI calls to fail due to
`process.cwd` or `ENOENT` errors. Task-system adapters SHALL continue to set
adapter-owned child directories explicitly and relay-flow SHALL not add an
adapter-level `os.Chdir`.

#### Scenario: Deleted caller directory

- **WHEN** a server is started from a temporary caller directory and that
directory is deleted after startup
- **THEN** the running server keeps a valid durable working directory and
  remains stoppable
