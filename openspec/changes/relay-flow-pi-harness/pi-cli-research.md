# Pi CLI/API research record

## Status

Static investigation and the required live contract capture of the relay-flow
integration and installed Pi runtime are complete for Pi `0.84.1`. The newer
source checkout is retained for source context, but the installed
CLI/package/API is authoritative for implementation and mocks. The separate
Orca terminal smoke test remains a required local acceptance step in Task 5.1.

The remaining real-world check is the Orca terminal smoke test in Task 5.1,
covering the installed Pi PTY/lifecycle path. It is not a CI dependency because
CI does not have Pi or model-provider credentials installed.

## Runtime locations

```text
Source checkout:
  path:    /home/raj/raj/pi
  origin:  https://github.com/earendil-works/pi.git
  branch:  main
  commit:  853a80d26c90a14c1886f0ebb8ffaae133ca2185
  source version: 0.84.4

Installed runtime:
  command wrapper: /home/linuxbrew/.linuxbrew/Cellar/pi-coding-agent/0.84.1/bin/pi
  launcher:        /home/linuxbrew/.linuxbrew/Cellar/pi-coding-agent/0.84.1/libexec/bin/pi
  package:         /home/linuxbrew/.linuxbrew/Cellar/pi-coding-agent/0.84.1/libexec/lib/node_modules/@earendil-works/pi-coding-agent
  declarations:    /home/linuxbrew/.linuxbrew/Cellar/pi-coding-agent/0.84.1/libexec/lib/node_modules/@earendil-works/pi-coding-agent/dist
  docs:            /home/linuxbrew/.linuxbrew/Cellar/pi-coding-agent/0.84.1/libexec/lib/node_modules/@earendil-works/pi-coding-agent/docs
```

The installed `bin/pi` wrapper sets `PI_SKIP_VERSION_CHECK=1` and executes the installed `libexec/bin/pi` launcher.

## Verified installed CLI behavior

The following was verified against the installed command:

```text
pi --version
→ 0.84.1
```

`pi --help` documents these relevant options:

```text
--name, -n <name>
--session-id <id>
--session <path|id>
--extension, -e <path>
--mode <text|json|rpc>
--print, -p
--
```

The help output does not advertise a bare `--` terminator. The CLI has no `pi agent list` command and no `--agent` or `--interactive` flag. `pi --list-models` lists provider/model combinations; it does not list named agents. Pi has one built-in coding-agent runtime, represented in relay-flow workflows as `agent: default`.

The harness launch contract therefore uses only:

```text
pi --name <ticket>:<node> [--session-id <id>] <prompt>
```

The prompt is a positional argv value; it is not sent to Pi over stdin. A bare `--` is rejected by the installed 0.84.1 runtime (`Error: Unknown option: --`). `--session-id` is the exact project-session ID option used for persisted resume. `--session` is not used because it accepts a path/partial ID and may trigger session selection/fork behavior.

Pi chooses interactive mode only when both stdin and stdout are TTYs and neither print nor another explicit non-interactive mode is selected. Without a TTY, the CLI resolves to print mode. A real PTY is therefore supplied by the existing Orca terminal in the manual acceptance test; CI command tests must not pretend to prove this.

## Verified installed extension/API behavior

The installed package declarations and docs expose:

```text
ExtensionAPI.on("session_start", handler)
ExtensionAPI.on("agent_settled", handler)
ExtensionAPI.setSessionName(name)
ExtensionAPI.sendUserMessage(content, options?)
ExtensionContext.sessionManager.getSessionId()
ExtensionContext.sessionManager.getBranch()
ExtensionContext.ui.select(title, options, opts?)
```

The relevant actual types are:

```text
ctx.ui.select(
  title: string,
  options: string[],
  opts?: ExtensionUIDialogOptions,
): Promise<string | undefined>
```

`agent_settled` has no message payload. The extension must read the active branch from the session manager. `getBranch()` returns entries in root-to-leaf order, so the extension searches from the end toward the beginning.

A session message entry has a stable entry `id`, `parentId`, timestamp, and a `message`. An assistant message has `role: "assistant"`, an array of content blocks, and a `stopReason`. The plugin extracts only `type: "text"` content blocks. It ignores assistant messages with `stopReason: "aborted"` or `stopReason: "error"`.

Pi does not expose a built-in Question API or `question.*` lifecycle events. `examples/extensions/question.ts` is an optional LLM-callable example tool. Relay-flow HITL approval uses the real extension UI selector instead.

## Manual plugin installation

The relay-flow Pi extension is part of the published `relay-flow-plugin` package. The user installs it manually in Pi's global settings, without `-l`:

```sh
pi install npm:relay-flow-plugin@<relay-flow-plugin-version>
```

The package retains the OpenCode `main: relay-flow.ts` entry and adds a Pi manifest entry:

```json
{
  "pi": {
    "extensions": ["./pi.ts"]
  }
}
```

Relay-flow does not install or configure this package automatically. The Pi harness `SetupRepo` operation is a no-op.

## Strict test-double contract

Automated tests must use test-only doubles that mirror the verified behavior above:

- a temporary executable on `PATH` only for the real `pi` availability lookup;
- a strict runner command capture that validates `pi`, `--name`, optional `--session-id`, positional prompt argv ordering, and the relay-flow environment; it rejects the unsupported bare `--`;
- a fake extension registrar that accepts only `session_start` and `agent_settled` handlers;
- a fake session manager with real `getSessionId()`/`getBranch()` shapes and stable session-entry IDs;
- a fake `ctx.ui.select()` returning `string | undefined` and recording exactly `Approve`/`Reject`;
- a fake `pi` object recording only `setSessionName()` and `sendUserMessage()`;
- the existing strict relay-flow subprocess fake for `runtime-register`/`report` JSON stdin.

The Pi CLI fake must reject invented flags, commands, stdin prompt behavior, cwd, or environment. The extension fake must reject invented Question APIs/events or extra context methods. No fake may accept a command/API shape that the installed Pi runtime does not provide.

## Required live capture before closing the change

Use the installed Pi `0.84.1` runtime and record sanitized results in:

```text
internal/harness/pi/testdata/pi-0.84.1/
plugin/testdata/pi-0.84.1/
```

Capture:

- version/help and invalid-flag errors;
- fresh/resumed argv, multiline prompt handling, child cwd, environment, stdin/stdout TTY state, and process lifetime;
- session JSONL creation, session name, stable session ID, and restart with `--session-id`;
- extension loading and the actual `session_start`/`agent_settled` event/context behavior;
- direct `ctx.ui.select()` behavior for `Approve`, `Reject`, and cancellation;
- report transport errors and server-unavailable behavior without secrets.

These results define the strict CI fakes. They do not get replaced by the fakes.

## Live capture (2026-08-31)

The smoke/acceptance target for this change is the installed Pi `0.84.1`
binary at `/home/linuxbrew/.linuxbrew/Cellar/pi-coding-agent/0.84.1/bin/pi`.
Sanitized command and lifecycle fixtures are stored under
`internal/harness/pi/testdata/pi-0.84.1/` and
`plugin/testdata/pi-0.84.1/`; no credentials or raw terminal control output
are stored.

An initial pass was run with `--offline` and no credentials; it could not
exercise a real model turn. It was superseded by the full live run recorded in
the next section. Sanitized fixtures are in `plugin/testdata/pi-0.84.1/`. The
first pass confirmed:

- `--name`, `--session-id`, and positional multiline prompts are accepted;
- a bare `--` is rejected with `Error: Unknown option: --`;
- `--agent` and `--interactive` are rejected as unknown options;
- non-TTY execution resolves to print mode and reports missing credentials
  without entering the TUI;
- a PTY launch resolves to `tui`, has `stdin`/`stdout` attached to a TTY, sets
  the requested session name, and remains alive until Ctrl-C;
- `session_start` runs in both print and TUI modes, with
  `getSessionId()`, `getBranch()`, `setSessionName()`, and `sendUserMessage()`
  available; TUI mode reports `hasUI: true` and `ctx.ui.select()` accepts the
  title plus exactly the supplied options;
- a subsequent PTY launch using the same `--session-id` reports the same
  session ID and emits the normal resume lifecycle event.

## Full live run with a real provider (2026-08-31, tasks 5.1-5.3)

Executed end to end against installed Pi `0.84.1` with a real provider
(`github-copilot/claude-haiku-4.5`, thinking off, tools disabled), a PTY from
`script(1)`, an isolated `--session-dir`, and `RELAY_FLOW_HOME` pointed at a
temporary directory. The relay-flow binary was built to a scratch path
(`go build -o /tmp/.../bin/relay-flow`) and reached only through a logging
shim on `PATH`; the installed relay-flow binary was never used or replaced.
No relay-flow server was running, so every transport call failed and retried,
which is what made the retry contract observable.

Deviations from the production launch, all observability-only: the smoke runs
added `--session-dir`, `--model`, `--thinking`, `-nt`, and `-e` (production
relies on a globally installed package instead of `-e`).

Observed:

- interactive launch resolved to `mode: tui`, `hasUI: true`, both streams TTY,
  child cwd equal to the launch directory, and all nine `RELAY_FLOW_*`
  variables visible to the extension;
- the Pi process stayed alive after the response settled (observed 65s+) and
  after the HITL selector was dismissed; it exited only when the smoke test
  killed it;
- the same command with redirected streams resolved to `mode: print`,
  `hasUI: false`, and exited after one response - the reason the runner PTY is
  mandatory. `ctx.ui.select` is still `typeof "function"` in print mode, so
  `ctx.hasUI` is the only meaningful UI guard;
- `--name` set both the session name and the terminal title
  (`π - PAY-101:review - project`); `pi.getSessionName()` already returned it at
  `session_start`, so the extension's rename is a no-op;
- session JSONL was written per session; `--session-id` reattached to the exact
  same file and ID, the branch contained the prior entries with their original
  IDs, and the model answered a follow-up from the earlier turn
  ("I output STATUS: success.");
- `agent_settled` carried a branch containing non-message entries
  (`session_info`, `model_change`, `thinking_level_change`) alongside
  `type: "message"` entries with stable IDs and `stopReason: "stop"`;
- the real `plugin/pi.ts` sent `runtime-register` with
  `{runId, node, sessionId}`, retried it on the next settle after failure, and
  sent `report` with `reportId = <sessionId>:<entryId>`;
- every report retry wrote byte-identical stdin (4 report IDs, 4-6 attempts
  each, one unique payload per report ID);
- an invalid assistant message on an agent node produced exactly one
  `pi.sendUserMessage` correction, after which the model emitted a valid report
  that was delivered;
- on a HITL node the extension rendered
  `Approve relay-flow report for PAY-101:review` with exactly `Approve` and
  `Reject`; Escape submitted nothing, and a later settled turn did not reopen
  the selector or send any prompt; `Approve` delivered exactly one report.
