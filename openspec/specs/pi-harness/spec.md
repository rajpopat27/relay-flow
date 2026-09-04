# pi-harness Specification

## Purpose
TBD - created by archiving change relay-flow-pi-harness. Update Purpose after archive.
## Requirements
### Requirement: Pi is a selectable machine-scoped harness

The system SHALL register a built-in harness named `pi` through the existing harness factory. Machine configuration SHALL select it with `harnessPlugin: pi`; workflows SHALL NOT select a harness.

#### Scenario: Interactive init lists Pi

- **WHEN** `relay-flow init` displays the existing harness selection
- **THEN** `pi` is one of the registered harness options

#### Scenario: Non-interactive init selects Pi

- **WHEN** `relay-flow init --harness-plugin pi` is run with valid task and runner selections
- **THEN** init writes `harnessPlugin: pi` using the existing command path

#### Scenario: Unknown Pi configuration fails through the normal factory path

- **WHEN** the Pi harness configuration contains an unknown field
- **THEN** Pi harness construction fails strict validation before serve starts

### Requirement: Pi uses the existing harness contract

The Pi implementation SHALL implement the current `harness.Harness` methods without changing their signatures. It SHALL return structured `runner.Command` values and SHALL NOT manipulate runner environments or terminals directly.

#### Scenario: Pi builds an opaque runner command

- **WHEN** the durable interpreter calls `BuildCommand` with a valid `harness.LaunchSpec`
- **THEN** the Pi harness returns an executable, argument list, and environment map through `runner.Command`

#### Scenario: Pi does not discover sessions by title

- **WHEN** normal execution has a persisted Pi session ID
- **THEN** the Pi harness uses that ID for resume and does not list or rediscover sessions by title

#### Scenario: Pi setup does not modify the code repository

- **WHEN** the repo service calls `SetupRepo` for the Pi harness
- **THEN** the Pi harness does not write OpenCode configuration or other repository files

### Requirement: Pi harness configuration is minimal and strict

The Pi harness root `harnessConfig` SHALL accept only the `initial` and `feedback` template strings. Omitted values SHALL use the documented task-system-neutral defaults. The Pi harness SHALL not define a `hitl` prompt template because HITL approval is performed by direct Pi UI. Unknown fields and explicit invalid values SHALL be rejected through strict decoding.

#### Scenario: Pi prompt defaults

- **WHEN** `harnessConfig` omits `initial` and `feedback`
- **THEN** Pi uses the standard initial mailbox prompt and feedback prompt defaults without adding an OpenCode Question-tool instruction

#### Scenario: Unsupported Pi harness field

- **WHEN** Pi `harnessConfig` contains a field such as `hitl`, `agent`, or `model`
- **THEN** strict Pi harness construction rejects it rather than silently ignoring it

### Requirement: Pi node launches remain interactive

Pi node commands SHALL launch the default interactive TUI. The command SHALL NOT use print mode, JSON mode, or RPC mode. The runner terminal SHALL provide a TTY for both standard streams. When a repository role is selected, the command SHALL append that role's prompt file with `--append-system-prompt` before the final positional prompt.

#### Scenario: Fresh default Pi node launch

- **WHEN** a node with `agent: default` has no persisted session ID
- **THEN** the command is equivalent to `pi --name <ticket>:<node> <prompt>` and omits `--session-id`; Pi 0.84.1's rejected bare `--` terminator is not used

#### Scenario: Fresh repository-role Pi node launch

- **WHEN** a node with `agent: coder` has no persisted session ID and the registered repository contains `.pi/roles/coder.md`
- **THEN** the command includes `--append-system-prompt <registered-repository>/.pi/roles/coder.md`, uses `--name <ticket>:<node>`, and supplies the prompt as the final positional argument

#### Scenario: Resumed default Pi node launch

- **WHEN** a node with `agent: default` has persisted session ID `session-123`
- **THEN** the command includes `--session-id session-123`, uses `--name <ticket>:<node>`, and supplies the prompt as the final positional argument without a bare `--` terminator

#### Scenario: Resumed repository-role Pi node launch

- **WHEN** a node with `agent: reviewer` has persisted session ID `session-123` and the registered repository contains `.pi/roles/reviewer.md`
- **THEN** the command includes the repository role prompt with `--append-system-prompt`, includes `--session-id session-123`, and supplies the prompt as the final positional argument without a bare `--` terminator

#### Scenario: Prompt beginning with a dash

- **WHEN** the rendered prompt begins with `-`
- **THEN** the harness preserves it as one final positional argv value and does not insert Pi 0.84.1's rejected bare `--` terminator

#### Scenario: Pi stays alive after the initial response

- **WHEN** a fresh node command is executed in an Orca terminal and the initial assistant response settles
- **THEN** the Pi process remains alive waiting for interactive input

#### Scenario: Non-interactive execution is not accepted as success

- **WHEN** the command is run without TTY stdin or stdout
- **THEN** the integration test detects that Pi would select print mode and the harness implementation does not claim that as an interactive session

### Requirement: Pi launch environment is passed through the existing runner boundary

The Pi harness SHALL return the existing relay-flow environment contract unchanged. The command's working directory SHALL be the runner's ticket environment/worktree, not the registered repository path. The rendered Pi prompt SHALL be one positional argv value; Pi 0.84.1's rejected bare `--` terminator SHALL NOT be used. Pi's interactive stdin/stdout SHALL remain attached to the runner terminal. The separate `runtime-register` and `report` subprocesses SHALL receive their JSON objects through stdin.

The harness environment SHALL include:

```text
RELAY_FLOW_HOME
RELAY_FLOW_RUN_ID
RELAY_FLOW_WORKFLOW
RELAY_FLOW_REPO
RELAY_FLOW_TICKET
RELAY_FLOW_NODE
RELAY_FLOW_NODE_TYPE
RELAY_FLOW_NUDGE_PROMPT
RELAY_FLOW_NEXT_STEPS_JSON
```

It SHALL NOT include `RELAY_FLOW_NODE_VISIT_ID`.

#### Scenario: Pi receives the ticket worktree cwd

- **WHEN** the runner creates a Pi terminal for a ticket-scoped environment
- **THEN** the real Pi process runs in that environment's worktree while the registered repository path remains only the environment source

#### Scenario: Relay-flow metadata is complete

- **WHEN** the Pi harness builds a node command
- **THEN** all listed `RELAY_FLOW_*` values are present with their `LaunchSpec` values and no internal visit ID is exposed

#### Scenario: Pi prompt and report stdin remain separate

- **WHEN** a multiline node prompt and a multiline report are processed
- **THEN** the prompt is passed as one final positional argv value without a bare `--` terminator, while the report is passed as one JSON object on the separate `relay-flow report` stdin

### Requirement: Pi uses user-owned agent configuration and repository roles

The Pi harness SHALL use Pi's single built-in coding agent with the user's normal Pi model, provider, tools, extensions, and settings. The workflow `agent` value `default` SHALL select that built-in agent. A non-default workflow agent value SHALL select a repository-owned role prompt at `.pi/roles/<agent>.md` under the registered repository path; the harness SHALL require that file to be a readable, non-empty regular file and SHALL pass it to Pi with `--append-system-prompt`. Agent values SHALL NOT be interpreted as model IDs or OpenCode-style `--agent` options. Because Pi has no native named-agent listing command, filesystem role validation is the existence check.

#### Scenario: Default Pi agent in tests

- **WHEN** a Pi test workflow uses `agent: default`
- **THEN** Pi launch succeeds using the single built-in Pi coding agent, and `default` remains available in launch metadata/prompt data

#### Scenario: Existing repository role

- **WHEN** a Pi workflow uses `agent: coder` and the registered repository contains a readable non-empty `.pi/roles/coder.md`
- **THEN** validation succeeds and the launch command passes that role file with `--append-system-prompt`

#### Scenario: Missing repository role

- **WHEN** a Pi workflow uses a non-default agent and `.pi/roles/<agent>.md` is absent, empty, non-regular, or unreadable
- **THEN** validation fails before the node is launched and identifies the expected role file

#### Scenario: Unsafe role value

- **WHEN** a Pi workflow agent value contains path separators or path traversal
- **THEN** validation fails without resolving a file outside `.pi/roles`

#### Scenario: Normal user Pi configuration

- **WHEN** the user has configured a normal Pi model and credentials
- **THEN** the Pi harness launches that configuration without overriding it with a relay-flow-selected model

#### Scenario: Pi executable is unavailable

- **WHEN** a valid default agent or repository role is validated and the real `pi` executable cannot be found on `PATH`
- **THEN** validation fails before the node is launched

### Requirement: Pi runtime metadata is validated without silent defaults

The Pi extension SHALL activate only for a relay-flow-launched session. If relay-flow launch metadata is partially present, the extension SHALL require non-empty `RELAY_FLOW_RUN_ID`, `RELAY_FLOW_TICKET`, `RELAY_FLOW_NODE`, and a `RELAY_FLOW_NODE_TYPE` of exactly `agent` or `hitl`; it SHALL fail closed and log an error rather than defaulting an invalid or missing node type to `agent`. `RELAY_FLOW_HOME` SHALL be used for plugin logging and report transport; the Pi harness always supplies it in production. The remaining listed `RELAY_FLOW_*` values SHALL be forwarded by the harness unchanged.

#### Scenario: Non-relay Pi session

- **WHEN** `RELAY_FLOW_RUN_ID` and `RELAY_FLOW_NODE` are absent
- **THEN** the extension loads no relay-flow handlers and performs no relay-flow action

#### Scenario: Partial relay-flow metadata

- **WHEN** relay-flow identity is present but ticket or a valid node type is missing
- **THEN** the extension does not register or submit reports and records the configuration error without nudging the session

### Requirement: Pi runtime registration persists the real session

The Pi runtime extension SHALL register the actual Pi session ID through the existing `runtime-register` command. Registration SHALL use exactly `{runId, node, sessionId}` and SHALL NOT contain `nodeVisitID`.

#### Scenario: Pi session starts

- **WHEN** a relay-flow-launched Pi session emits `session_start`
- **THEN** the extension sends `{runId, node, sessionId}` to relay-flow

#### Scenario: Registration transport fails

- **WHEN** the relay-flow server is temporarily unavailable during `session_start`
- **THEN** the extension records the failure without crashing Pi and retries registration on a later runtime event

#### Scenario: Session name is stable

- **WHEN** the Pi extension starts for ticket `PAY-101` and node `implement`
- **THEN** it sets the Pi session name to `PAY-101:implement` when the name is not already set

### Requirement: Pi reports use stable session-entry identity

The Pi extension SHALL process completed assistant output only after `agent_settled`. It SHALL select the latest branch entry with `type: "message"` and `message.role: "assistant"`, ignore `stopReason: "aborted"` and `stopReason: "error"`, and parse text content blocks from other finalized assistant messages. It SHALL derive `reportId` from the Pi session ID and that stable assistant session-entry `id`, then send the existing `{runId, node, reportId, report}` envelope.

#### Scenario: Valid Pi agent report

- **WHEN** a completed assistant message contains the full report contract at an agent node
- **THEN** the extension parses it with the shared parser and submits one JSON report

#### Scenario: Pi report contains multiline fields

- **WHEN** summary or feedback fields contain multiline text
- **THEN** the extension sends the unchanged parsed report through JSON stdin without shell interpolation

#### Scenario: Duplicate settled event

- **WHEN** Pi emits duplicate settled events for the same assistant session entry
- **THEN** the extension does not open a second delivery or submit a second report

#### Scenario: Aborted assistant turn

- **WHEN** the latest assistant message is aborted or has an error completion
- **THEN** the extension does not parse, nudge, or submit it as a completed report

### Requirement: Pi agent nodes correct invalid output

For an agent node, invalid or missing completed output SHALL cause the Pi extension to send the existing fixed complete-contract correction through `pi.sendUserMessage`. The correction SHALL not depend on the workflow's custom `nudgePrompt`.

#### Scenario: Invalid agent output

- **WHEN** an agent node settles after ordinary prose without the complete report contract
- **THEN** Pi receives one fixed correction containing every required report label

#### Scenario: Invalid output is not repeatedly corrected

- **WHEN** the same invalid assistant session entry is observed more than once
- **THEN** the extension sends at most one correction for that entry

### Requirement: Pi HITL nodes use direct approval UI

For a HITL node, the Pi extension SHALL remain silent for invalid or missing output. For a valid report, it SHALL ask the user directly through `ctx.ui.select` with the fixed title `Approve relay-flow report for <ticket>:<node>` and exactly `Approve` and `Reject` choices. It SHALL not require the assistant to call a Question tool.

#### Scenario: Missing HITL output

- **WHEN** a HITL session settles with no completed assistant message
- **THEN** the extension performs no nudge, report delivery, or task-system mutation

#### Scenario: Invalid HITL output

- **WHEN** a HITL session settles with an invalid report
- **THEN** the extension remains silent and submits nothing

#### Scenario: Valid HITL output requests approval

- **WHEN** a HITL session settles with a valid report
- **THEN** the extension opens a direct Pi UI selection with exactly `Approve` and `Reject`

#### Scenario: User approves

- **WHEN** the user selects `Approve`
- **THEN** the extension submits the exact parsed report through the existing report retry path

#### Scenario: User rejects

- **WHEN** the user selects `Reject` or cancels the selector
- **THEN** the extension submits nothing, does not convert the decision into workflow failure, and leaves the durable run waiting

#### Scenario: A rejected or cancelled selection is never re-asked

- **WHEN** further settled events occur for the same assistant output after `Reject` or cancellation
- **THEN** the extension SHALL NOT re-open the selector for that output, SHALL NOT send the assistant any prompt, and SHALL open a selector again only for a later assistant output that parses as a valid report

#### Scenario: No UI is available

- **WHEN** a Pi runtime is accidentally launched in a mode without extension UI
- **THEN** the HITL extension does not submit the report or attempt an automatic approval

### Requirement: Pi report retries preserve exact output

The Pi extension SHALL reuse the existing report retry policy and transport. It SHALL keep at most one unacknowledged report delivery per run/node, retry the exact serialized JSON, and treat duplicate/stale acknowledgements as success.

#### Scenario: Temporary server failure

- **WHEN** `relay-flow report` cannot connect to the server
- **THEN** the Pi extension retries quietly with the shared 2-second, factor-2, 20-percent-jitter, 5-minute-cap policy

#### Scenario: Server acknowledges a duplicate

- **WHEN** the server acknowledges a stale or duplicate report
- **THEN** the Pi extension stops retrying successfully

#### Scenario: Delivery does not create another LLM turn

- **WHEN** a report delivery attempt fails
- **THEN** retries do not send a new prompt to Pi or regenerate the report

### Requirement: Pi plugin packaging is unambiguous

The published `relay-flow-plugin` package SHALL retain the OpenCode entry point and declare the Pi extension entry point through a Pi `package.json` manifest. The user SHALL install the package manually in Pi's global package settings, without `-l`, before running Pi harness sessions; relay-flow SHALL not auto-install or configure it.

#### Scenario: Manually installed Pi package resolution

- **WHEN** the user installs `npm:relay-flow-plugin@<version>` through Pi's package command before running relay-flow
- **THEN** Pi resolves the package manifest and loads the Pi extension entry point for relay-flow sessions

#### Scenario: Pi launch does not auto-install the package

- **WHEN** a Pi harness command is built
- **THEN** the command does not install or configure the plugin and assumes the user has installed it manually

#### Scenario: OpenCode package resolution remains unchanged

- **WHEN** OpenCode loads `relay-flow-plugin@<version>`
- **THEN** it continues to use `relay-flow.ts` and its existing behavior

### Requirement: Pi support does not change durable core behavior

Adding Pi SHALL preserve the existing report API, runtime registration API, runner terminal title rules, durable node-visit identity, transition ordering, cancellation, and recovery semantics.

#### Scenario: Pi report advances a normal run

- **WHEN** a Pi agent or approved HITL report is accepted
- **THEN** the existing durable engine persists the report and performs the same ordered summary, feedback, mailbox completion, and next-node effects

#### Scenario: Pi node is revisited

- **WHEN** a report routes back to a prior node
- **THEN** relay-flow generates a fresh internal `nodeVisitID`, retains the stable `<ticket>:<node>` terminal/session identity, and Pi resumes or relaunches through the existing runtime path

### Requirement: Automated verification does not require Pi installation

The automated Go and TypeScript test suites SHALL run without an installed Pi executable, Orca installation, model provider, or provider credentials. Tests SHALL use a temporary mock `pi` executable where process lookup is required, existing fake runner/harness seams, and fake Pi extension contexts. Every mocked flag, argument, response, event, context method, stdin rule, cwd rule, environment value, error, and lifecycle state SHALL match the installed Pi `0.84.1` contract captured by the live preflight. Real Pi/Orca execution SHALL be required for a manual local acceptance test and SHALL not be a CI dependency. Test doubles SHALL live only in `*_test.go`, `*.test.ts`, or test-fixture files.

#### Scenario: CI validates Pi command construction

- **WHEN** the automated suite tests the Pi harness
- **THEN** it validates executable, arguments, environment, and validation behavior using mocks/fakes without launching the real Pi binary

#### Scenario: CI validates Pi runtime behavior

- **WHEN** the automated suite tests registration, report parsing, nudging, retry, and HITL approval
- **THEN** it invokes the Pi extension with a fake extension API/context and does not require the Pi TUI or Question tool

#### Scenario: Local interactive smoke test

- **WHEN** a developer completes the required local acceptance check before closing the change
- **THEN** the check uses installed Pi `0.84.1` and Orca to verify the real PTY/session behavior, while its absence does not block CI execution

### Requirement: Pi test doubles reject invented behavior

Automated Pi CLI doubles SHALL accept only the real launch contract used by the harness: executable `pi`, optional `--name <title>`, optional `--append-system-prompt <role-file>`, optional `--session-id <id>`, and the final positional prompt. They SHALL reject the unsupported bare `--` terminator, validate argument order, child cwd, and environment values, and reject other unsupported flags, commands, and mode/install behavior. The prompt is an argv value; report and registration JSON stdin framing is tested by the separate relay-flow transport fake. Automated extension doubles SHALL expose only the Pi extension API and lifecycle shapes used by version `0.84.1`, including the actual `session_start`, `agent_settled`, `sessionManager`, action, and `ctx.ui.select` shapes. Test doubles SHALL reject unsupported Question APIs and event shapes rather than silently accepting them.

#### Scenario: Invented launch flag is caught

- **WHEN** production code attempts to pass an unsupported flag such as `--agent` or `--interactive` to the strict Pi CLI double
- **THEN** the test fails instead of allowing the mock to hide the production incompatibility

#### Scenario: Invented Question API is caught

- **WHEN** production code attempts to use a nonexistent Pi Question API or event
- **THEN** the strict extension test double rejects it; direct HITL behavior must use the real `ctx.ui.select` contract

### Requirement: Production uses real Pi contracts

Production relay-flow code SHALL invoke the real `pi` executable and the real Pi extension API. Test-only mocks/fakes SHALL live only in `*_test.go`, `*.test.ts`, or test-fixture files. Production SHALL NOT select fake behavior through configuration, build tags, environment switches, test fallbacks, compatibility fallbacks, or `if test` branches.

#### Scenario: Production launch uses Pi

- **WHEN** the Pi harness creates a node command
- **THEN** the command targets the real `pi` executable and the installed manual runtime plugin, not a test executable or fake mode

#### Scenario: Test fake is unavailable in production

- **WHEN** production code is built without test files or fixtures
- **THEN** no mock Pi command, fake extension context, or test-only selection path is compiled into the runtime

### Requirement: The installed Pi contract is verified before completion

Before this change is closed, a local/manual smoke test SHALL use the installed Pi `0.84.1` CLI/API and a real command/integration to verify the behavior encoded by the implementation. Its sanitized results SHALL be recorded in `internal/harness/pi/testdata/pi-0.84.1/`, `plugin/testdata/pi-0.84.1/`, or the relevant implementation notes, and SHALL be the basis for the strict CI doubles. The smoke test SHALL cover version/help, flags and argument order, stdin, cwd, environment, TTY mode, response/error behavior, session persistence, restart/resume, and extension lifecycle. The smoke test SHALL not be required for CI execution.

#### Scenario: Live contract is captured

- **WHEN** the local/manual Pi smoke test completes
- **THEN** its captured behavior is available to implementation and test authors, and no behavior is accepted solely because a permissive mock allowed it
