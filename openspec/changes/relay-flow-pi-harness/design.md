# Design: Pi harness

## Context

Relay-flow already separates the launch-time harness adapter from the runtime plugin:

```text
workflow interpreter
  -> harness.LaunchSpec
  -> harness.BuildCommand
  -> runner.EnsureTerminal
  -> runtime plugin
  -> relay-flow report
```

The current implementation in `internal/harness/opencode` and `plugin/relay-flow.ts` provides the reference behavior. The Pi source at `/home/raj/raj/pi` exposes a compatible command-line session model and a richer extension API.

Relevant existing relay-flow surfaces:

- `internal/harness/harness.go`: `SetupRepo`, `ValidateAgent`, `FindSession`, `RenderPrompt`, and `BuildCommand`.
- `internal/harness/factory.go`: package-local named factories and duplicate-registration protection.
- `internal/execution/goworkflows/activities.go`: persisted terminal/session IDs, prompt rendering, and opaque command execution.
- `internal/execution/goworkflows/interpreter.go`: generic node execution and report waits.
- `internal/run/run.go`: generic runtime session registration and report values.
- `plugin/index.ts`: report parsing and exact-report retry.
- `plugin/relay-flow.ts`: OpenCode-specific event and process transport wrapper.

Relevant Pi source behavior:

- `packages/coding-agent/src/main.ts` selects interactive mode only when both stdin and stdout are TTYs and no print/JSON/RPC mode is selected.
- `packages/coding-agent/src/cli/args.ts` supports `--session-id`, `--name`, `--extension`, and `--` for positional prompts.
- `packages/coding-agent/src/core/session-manager.ts` persists explicit session IDs in the session header and resolves them within the current project session directory.
- `packages/coding-agent/src/core/extensions/types.ts` exposes `ctx.ui.select`, `ctx.ui.confirm`, `ctx.ui.custom`, `ctx.sessionManager`, and `pi.sendUserMessage`.
- `packages/coding-agent/src/core/agent-session.ts` emits `agent_settled` after retries, compaction, and queued messages have finished.
- `packages/coding-agent/examples/extensions/question.ts` is an optional LLM-callable example tool, not a built-in Pi Question API.

## KISS/YAGNI guardrails

- Add only the Pi Go adapter, Pi runtime extension, package manifest entry, static wiring, focused tests, and user-facing documentation required by this change.
- Do not add a Pi SDK host process, named-agent/profile registry, model-selection layer, auto-installer, new server endpoint, approval field, SQLite table, event bus, or compatibility fallback.
- Keep model credentials, provider choice, tools, and other Pi runtime policy in the user's existing Pi configuration.
- Treat the completed source investigation below as the research preflight; complete the required live contract capture before declaring production implementation ready. Do not repeat source research unless the tested Pi version or APIs change.
- Keep CI independent of local tools: automated tests use a strict temporary `pi` executable mock only where process lookup/validation requires it, fake runner/harness boundaries, and fake Pi extension contexts. Every mocked flag, argument, field, event, stdin rule, cwd rule, environment value, response shape, and error must come from the installed Pi `0.84.1` CLI/API or the inspected source checkout; no Pi binary, Orca installation, model provider, or credentials are required in CI.
- Production code always invokes the real `pi` CLI and real Pi extension API. Test fakes/mocks exist only in Go `*_test.go`, TypeScript `*.test.ts`, or test-fixture files. Do not add fake-selection configuration, build tags, test fallbacks, compatibility fallbacks, or `if test` production paths.

## Pi source checkout and contract inventory

The inspected Pi source is cloned at:

```text
source checkout:
  path:    /home/raj/raj/pi
  origin:  https://github.com/earendil-works/pi.git
  branch:  main
  commit:  853a80d26c90a14c1886f0ebb8ffaae133ca2185
  source:  package version 0.84.4
installed runtime:
  command: /home/linuxbrew/.linuxbrew/Cellar/pi-coding-agent/0.84.1/bin/pi
  launcher: /home/linuxbrew/.linuxbrew/Cellar/pi-coding-agent/0.84.1/libexec/bin/pi
  package: /home/linuxbrew/.linuxbrew/Cellar/pi-coding-agent/0.84.1/libexec/lib/node_modules/@earendil-works/pi-coding-agent
  docs:    /home/linuxbrew/.linuxbrew/Cellar/pi-coding-agent/0.84.1/libexec/lib/node_modules/@earendil-works/pi-coding-agent/docs
  version: 0.84.1
```

When implementation needs to confirm a Pi behavior, check the installed `0.84.1` package, dist declarations/source maps, and docs first, then use this checkout for source context. Do not use a newer source-checkout behavior unless it is also present in the installed runtime contract. The relevant source files are:

```text
/home/raj/raj/pi/packages/coding-agent/src/main.ts
/home/raj/raj/pi/packages/coding-agent/src/cli/args.ts
/home/raj/raj/pi/packages/coding-agent/src/core/extensions/types.ts
/home/raj/raj/pi/packages/coding-agent/src/core/extensions/runner.ts
/home/raj/raj/pi/packages/coding-agent/src/core/agent-session.ts
/home/raj/raj/pi/packages/coding-agent/src/core/session-manager.ts
/home/raj/raj/pi/packages/coding-agent/src/modes/interactive/interactive-mode.ts
/home/raj/raj/pi/packages/coding-agent/examples/extensions/question.ts
```

The installed-runtime contracts to mock are:

1. **CLI command contract:** the harness returns `pi`, optional `--name <title>`, optional `--session-id <id>`, `--`, and the prompt. A strict command fake must reject `--agent`, `--interactive`, `--report`, invented `question` flags, and any unsupported mode flag. It must validate argument order and must not pretend that a non-TTY process is interactive.
2. **Executable availability:** production `ValidateAgent` accepts only the logical `default` agent and checks that the real `pi` executable is discoverable on `PATH`; it does not call an invented `pi agent list`. The unavailable case is tested with a temporary executable/PATH fixture.
3. **Extension lifecycle:** the fake event registrar exposes the actual `session_start` and `agent_settled` events. It does not invent `session.idle` or `question.*` events.
4. **Session context:** the fake `ctx.sessionManager.getSessionId()` and `getBranch()` return the actual Pi-shaped session data: `SessionEntry` message entries with stable entry IDs and assistant messages containing `content` blocks plus `stopReason`.
5. **Extension UI:** the fake `ctx.ui.select(title, options, opts?)` returns `string | undefined` and records the exact `Approve`/`Reject` options. It must not expose a nonexistent Question API.
6. **Extension actions:** the fake `pi` records `setSessionName()` and `sendUserMessage()`. It does not implement extra agent/profile/model behavior that the production Pi extension does not use.
7. **Report transport:** the existing strict relay-flow subprocess fake captures the actual `runtime-register`/`report` argv and stdin JSON. It validates stdin framing, command cwd, and environment. It is not a substitute for Pi behavior and must not add Pi-specific commands.
8. **TTY boundary:** no CI fake claims to prove TTY behavior; only the real local smoke test can verify that the Orca terminal gives Pi TTY stdin/stdout and that the process remains alive.

All automated doubles are contract checks, not permissive simulators. If a behavior is not present in the installed Pi 0.84.1 CLI/API or in the inspected source checkout, it must not be added to a mock. No behavior may be tested only through a permissive mock.

## Required real-contract capture before implementation

Before production implementation is considered ready, perform the live contract check with the installed Pi `0.84.1` runtime. Capture sanitized command output/API observations as test fixtures or implementation notes under the exact locations `internal/harness/pi/testdata/pi-0.84.1/` and `plugin/testdata/pi-0.84.1/` when the data belongs to those packages. Verify and record:

- `pi --version` and `pi --help`, including accepted option names and argument ordering;
- the absence of a named-agent listing command and the absence of unsupported flags such as `--agent` and `--interactive`;
- fresh and resumed commands using `--name`, `--session-id`, `--`, and a multiline prompt;
- stdin/stdout TTY detection and the fact that non-TTY execution selects print mode;
- child working directory and inherited/relay-flow environment values;
- session JSONL creation, stable session ID, session name, and `--session-id` restart/resume behavior;
- actual extension loading, `session_start`, `agent_settled`, `ctx.sessionManager`, `pi.setSessionName`, `pi.sendUserMessage`, and `ctx.ui.select` behavior;
- actual process exit/error behavior for unavailable Pi, invalid flags, invalid session IDs, and transport failures;
- the exact manual global package-install prerequisite and the package manifest entry that loads `pi.ts`.

This live check is local/manual because CI has no Pi installation. Its results define the strict automated fakes; the fakes must not become a replacement for the real check. The live check is required for completing the change even though it is not run by CI.

## Decisions

### 1. Reuse the existing harness interface

Do not add methods to `harness.Harness`. Add `internal/harness/pi` with an `init()` registration:

```go
harness.Register("pi", harness.Factory{...})
```

The Pi adapter's strict root `harnessConfig` contains only optional `initial` and `feedback` template strings. Its defaults are the same task-system-neutral templates used by the current OpenCode harness. Pi has no `hitl` harness prompt because HITL approval is performed by `ctx.ui.select`; an unknown `hitl` key is rejected by strict decoding.

```go
type Config struct {
    Initial  string `yaml:"initial"`
    Feedback string `yaml:"feedback"`
}
```

Default values:

```text
initial:
  Task system: {{taskSystem}}
  Use the {{taskSystem}} tools to read the parent ticket {{ticket}}.

  Your mailbox is {{mailbox}}. Read its description and comments for node instructions and feedback.

feedback:
  New feedback was added to the comments section of your mailbox subtask {{mailbox}}. Read it.
```

The adapter implements all current methods:

- `SetupRepo`: no-op because the user manually installs the runtime package before using the Pi harness.
- `ValidateAgent`: require the logical label `default` and call the real executable lookup for `pi`. It does not execute a nonexistent agent-list command and does not validate model/provider credentials.
- `FindSession`: return no result. Normal execution uses the persisted session ID directly, matching the current OpenCode direct-ID path.
- `RenderPrompt`: render the Pi `initial`/`feedback` templates and node nudge exactly as the existing harness contract requires. Do not append OpenCode Question-tool instructions.
- `BuildCommand`: return the interactive Pi command and required environment.

### 2. Use Pi's single built-in agent

Pi has no built-in equivalent to OpenCode's `build`/`plan` agent registry, and `pi --list-models` lists models rather than agents. This change does not invent named profiles.

Relay-flow represents Pi's single built-in coding agent with the logical workflow value `agent: default`. `ValidateAgent` accepts only that value after verifying the `pi` executable is available. Every node uses the user's normal Pi model, provider, tools, extensions, and settings. Generic existing harness fakes may use arbitrary labels, but Pi-specific workflows and tests use `default`.

This keeps model/agent policy user-owned while giving workflow validation one simple, typo-resistant agent value.

### 3. Launch only interactive Pi sessions

The command builder uses this shape:

```text
pi --name <ticket>:<node> \
   [--session-id <persisted-session-id>] \
   -- <rendered-prompt>
```

Rules:

- `--session-id` is included only when `LaunchSpec.ResumeID` is non-empty.
- Use `--session-id`, not `--session`; Pi's `--session` path/partial-ID behavior can select or fork a session.
- Place the prompt after `--` so a prompt beginning with `-` cannot be interpreted as a CLI option.
- Never pass `-p`, `--print`, `--mode json`, `--mode rpc`, or `--no-session`.
- Preserve the existing relay-flow environment contract exactly:
  - `RELAY_FLOW_HOME`
  - `RELAY_FLOW_RUN_ID`
  - `RELAY_FLOW_WORKFLOW`
  - `RELAY_FLOW_REPO`
  - `RELAY_FLOW_TICKET`
  - `RELAY_FLOW_NODE`
  - `RELAY_FLOW_NODE_TYPE`
  - `RELAY_FLOW_NUDGE_PROMPT`
  - `RELAY_FLOW_NEXT_STEPS_JSON`
  - do not add `RELAY_FLOW_NODE_VISIT_ID`.
- The rendered prompt is an argv value; Pi's stdin remains the interactive terminal. The separate `relay-flow report`/`runtime-register` processes receive JSON through their own stdin.
- The child Pi process runs in the runner's ticket-environment/worktree cwd supplied by Orca, not in the registered repository path used to construct that environment.
- The relay-flow Pi plugin is installed manually by the user in Pi's global package settings, without `-l`, before running Pi sessions, for example with `pi install npm:relay-flow-plugin@<version>`.
- The runner's stable tab title remains exactly `<ticket>:<node>`; Pi's `--name` is session metadata and does not replace runner identity.

The command is intentionally opaque to Orca. Automated tests assert the exact installed-runtime command shape with a strict fake and do not execute Pi. The fake must reject invented flags such as `--agent`, `--interactive`, `--report`, and `--mode` values not used by this launch path. A local/manual smoke test uses the existing Orca terminal creation path to supply the PTY required for Pi's interactive-mode detection.

### 4. Use one manually installed plugin package

Extend `plugin/package.json` so the existing `relay-flow-plugin` package contains both runtime entry points:

```json
{
  "main": "relay-flow.ts",
  "pi": {
    "extensions": ["./pi.ts"]
  }
}
```

Add `pi.ts` to the package `files` list. OpenCode continues to load `relay-flow.ts`; the user manually installs the package in Pi with `pi install npm:relay-flow-plugin@<version>`, and Pi resolves `pi.ts` through the manifest. If `pi.ts` imports `ExtensionAPI` types, declare `@earendil-works/pi-coding-agent` as an unbundled `peerDependency` according to Pi's package rules; do not bundle a second Pi runtime.

Relay-flow does not install or configure the package automatically in this change. The Pi launch command therefore does not include `--extension`, avoiding duplicate loading when the user has installed the package globally.

The Pi entry SHALL share `plugin/index.ts` for parsing and delivery. Move the current Node `spawn()` transport from `plugin/relay-flow.ts` into a small `plugin/transport.ts` module and use it from both wrappers. The transport SHALL invoke the real `relay-flow` executable with argv `runtime-register` or `report`, write exactly one JSON object to child stdin, and treat a zero exit as acknowledgement, including stale/duplicate acknowledgement. Do not use `pi.exec()` for report transport: its implementation ignores child stdin, while report JSON must be written to stdin without a shell.

### 5. Register the Pi session at session start

The Pi extension reads relay-flow metadata from `process.env` and is a no-op when no relay-flow identity is present.

If any relay-flow identity value is present, the extension requires every harness metadata key below to be present; an empty `RELAY_FLOW_NUDGE_PROMPT` is allowed, and `RELAY_FLOW_NEXT_STEPS_JSON` must contain valid JSON. Invalid or partial metadata fails closed and is logged rather than defaulting to an agent node or processing a report.

The harness always supplies:

```text
RELAY_FLOW_HOME
RELAY_FLOW_RUN_ID
RELAY_FLOW_WORKFLOW
RELAY_FLOW_REPO
RELAY_FLOW_TICKET
RELAY_FLOW_NODE
RELAY_FLOW_NODE_TYPE   # exactly agent or hitl
RELAY_FLOW_NUDGE_PROMPT
RELAY_FLOW_NEXT_STEPS_JSON
```

On `session_start`:

1. If both `RELAY_FLOW_RUN_ID` and `RELAY_FLOW_NODE` are absent, return without registering handlers or performing relay-flow work.
2. Validate the complete metadata contract and require a node type exactly equal to `agent` or `hitl`.
3. Read `ctx.sessionManager.getSessionId()`.
4. Send the existing runtime registration payload:

```json
{"runId":"...","node":"...","sessionId":"..."}
```

5. Set the Pi session name to `<ticket>:<node>` when it is not already that value.

Registration is retried on a later settled event if the initial transport fails. Registration contains no `nodeVisitID`.

### 6. Process reports on `agent_settled`

The Pi extension uses `agent_settled`, not streaming or early message events. It walks `getBranch()` from the leaf toward the root and selects the first matching assistant message entry on the active branch:

```text
ctx.sessionManager.getBranch()
```

It accepts only a finalized assistant message whose `stopReason` is not `aborted` or `error`, concatenates its `type: "text"` content blocks in order, and passes the result to the shared `parseReport` function. It does not parse thinking blocks, tool calls, or an assistant message from an abandoned branch.

Pi assistant messages do not expose an OpenCode-style assistant message ID. Use the stable session entry ID as the Pi message identity:

```text
reportId = <sessionId>:<assistant-session-entry-id>
```

This identity is stable across retries and session reloads.

### 7. Use direct Pi UI for HITL approval

For an agent node:

- invalid/missing report → send the existing fixed complete-contract correction with `pi.sendUserMessage()`;
- valid report → submit through the existing report transport.

For a HITL node:

- invalid/missing report → remain silent;
- valid report → call `ctx.ui.select()` with exactly `Approve` and `Reject` options;
- `Approve` → submit the exact parsed report;
- `Reject` or Escape → submit nothing and leave the durable run waiting.

The selector is a host UI interaction, not an LLM tool call. Use `ctx.ui.select` with the fixed title `Approve relay-flow report for <ticket>:<node>` and exactly the options `Approve` and `Reject`; the current assistant report is already visible in Pi's transcript. Automated tests provide the actual Pi extension-context shape with a fake `ctx.ui.select` implementation and do not start Pi. They must not invent or depend on `question.*` events or a built-in Question tool. It works in Pi TUI mode because `InteractiveMode` installs the extension UI context before the initial prompt runs. Do not add a custom modal in this change.

The extension captures the report/session-entry identity before opening the selector and guards it against duplicate settled events. It consumes approval after one submission attempt; approval state is ephemeral and is not written to SQLite or a new session entry. Delivery retries continue quietly through `deliverReport` without blocking Pi's ability to remain interactive; they do not create another LLM turn. A transport failure is logged and retried, while an invalid/missing runtime metadata error fails closed.

### 8. Keep durable report behavior unchanged

The Pi plugin sends the same JSON envelope:

```json
{"runId":"...","node":"...","reportId":"...","report":{...}}
```

It uses the existing `relay-flow report` subprocess transport and shared backoff. The server continues to validate the report, persist the signal, acknowledge after persistence, and deduplicate by report ID. No Pi code accesses SQLite or the task system.

### 9. Keep init generic

Add blank imports for `internal/harness/pi` in both command entrypoints. The existing `harness.Names()` calls then expose `pi` automatically:

- interactive init gets Pi in the existing `Select harness` selection;
- `--harness-plugin pi` works through the existing flag;
- `harness.Defaults("pi")` supplies any initial/feedback template defaults;
- init does not install the Pi package or ask for an agent/model;
- no Pi-specific init branch or prompt is added.

## Alternatives Rejected

| Alternative | Why rejected |
|---|---|
| Add a new harness interface method | The existing methods already cover launch, prompt, validation, and session registration behavior. |
| Use `pi -p` | Print mode exits after one response and cannot support persistent HITL/interactive sessions. |
| Use Pi RPC mode | It is headless JSONL transport, not the requested interactive terminal session. |
| Ask the LLM to call a Question tool | Pi extensions can ask users directly through `ctx.ui.select`, eliminating an unnecessary model turn and tool-following dependency. |
| Add named Pi agents now | Pi has no core named-agent registry; its single built-in agent is represented as `default` for this change. |
| Treat workflow `agent` as a model ID | It would break existing `build`/`plan` workflow labels and change the workflow contract. |
| Persist Pi session file paths in relay-flow | The existing runtime registration/session-ID contract is sufficient while the runner environment/cwd remains stable. |
| Auto-install or configure the Pi package | Plugin installation is intentionally manual for now, matching the current OpenCode deployment workflow. |
| Require Pi or Orca in CI | CI uses strict installed-runtime Pi 0.84.1 contract fakes and fake boundaries; the required real interactive execution is a local/manual acceptance check, not a CI dependency. |
| Use a shell pipeline for report JSON | It violates the existing JSON-stdin transport requirement and is unsafe for multiline report content. |
