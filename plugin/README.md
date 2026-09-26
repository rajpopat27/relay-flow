# relay-flow runtime plugin

The runtime half of the OpenCode and Pi harness contracts. Both entry points
watch for completed agent output, parse the structured report, and deliver it to
the running relay-flow server via `relay-flow report` (one JSON object on stdin,
retried with the shared backoff for temporary failures).

The plugin never calls the task system directly, never writes SQLite, and never
manages runner environments.

The plugin has two JSON contracts with relay-flow:

- runtime registration: `{runId, node, sessionId}`
- report delivery: `{runId, node, reportId, report}`

It derives `reportId` from the harness session/message identity. `nodeVisitID`
is internal to relay-flow and is not present in either payload.

## Install

Add `"relay-flow-plugin"` to the `plugin` array in your repo's `opencode.json`:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "plugin": ["relay-flow-plugin"]
}
```

The TUI entrypoint is configured in `.opencode/tui.json`:

```json
{
  "$schema": "https://opencode.ai/tui.json",
  "plugin": ["relay-flow-plugin"]
}
```

The OpenCode harness adds both entries to a registered repository. The package
manifest exposes `./server` and `./tui` separately because OpenCode requires
server and TUI modules to be target-exclusive.

For Pi, install the published package manually in Pi's global package settings
before starting relay-flow sessions:

```sh
pi install npm:relay-flow-plugin@<version>
```

Relay-flow does not install or configure the package automatically. The Pi
extension is loaded from the package's `pi.extensions` manifest entry.

The package has one manual installation strategy: install this published
package globally in Pi once, then use the normal interactive Pi command for
relay-flow nodes. Do not add `-e`/`--extension` to the relay-flow launch
command; global package loading supplies `pi.ts` and avoids duplicate loading.

## Structured report

Every visit (agent or HITL) submits one report with four fixed fields:

```
STATUS: success | failure
NEXT STEP: <one valid route>
SUMMARY: <concise result>
FEEDBACK: <concise handoff, or None when NEXT STEP is end>
```

`None` is required for `FEEDBACK` when `NEXT STEP` is `end`; no feedback
comment is written. `RELAY_FLOW_REPORT_FORMAT` supplies the same canonical
format to the runtime plugin for schema correction prompts. The plugin parses
only the four-field schema, then normalizes the concise fields into the existing
`report.summary` and `report.feedback` JSON objects, using `None` for unused
subsections. Relay-flow validates routes and end feedback on the server; the
plugin does not pre-validate those semantics. Task-system comment templates
render `summaryReport` on the current mailbox and `feedbackReport` on only
the selected next mailbox.

## What the plugin does

On session creation/update, the plugin sends `{runId, node, sessionId}` through
`relay-flow runtime-register`. Relay-flow persists that session ID; normal
execution uses the persisted ID to resume the harness session.

### OpenCode runtime

On OpenCode `session.idle`:

1. Reads the last completed assistant message (aborted turns are skipped).
2. Parses the report contract above.
3. Applies the nudge policy:
   - **agent + invalid/missing** → sends a fixed correction containing the
     exact report contract through OpenCode's session API.
   - **hitl + partial report-shaped invalid** → sends that same fixed
     correction once through OpenCode's session API; approval is not opened
     for the invalid message.
   - **hitl + missing/empty output** → stays silent and keeps waiting.
   - **hitl + valid** → left to the TUI entrypoint, which owns the native
     Approve/Reject dialog.
   - **valid and authorized** → reports.
4. Delivers the report as one JSON object on `relay-flow report` stdin:

   ```json
   {"runId":"...","node":"coding","reportId":"<session>:<message>","report":{...}}
   ```

   `reportId` is derived from the harness session/message identity. The plugin
   retries the exact parsed report in the background for temporary failures
   with the shared backoff (initial 2s, factor 2, jitter 0.2, max 5m).
   A duplicate/stale ack is success; at most one delivery runs per run/node.
   An HTTP `invalidReport` error stops that delivery after one attempt,
   releases its in-flight slot, and sends the server's exact validation
   message as a correction prompt. The assistant's next message yields a new
   `reportId` and can be delivered without restarting the plugin. The CLI
   exposes this code/message as JSON on stderr while successful report JSON
   on stdin stays unchanged.

For OpenCode HITL nodes, approval belongs to the TUI entrypoint below; the
server plugin never uses OpenCode's Question tool for relay-flow approval. The
OpenCode entry point is `relay-flow.ts` and is still selected through the
package `main` field.

### Native HITL TUI approval

The TUI entrypoint subscribes to completed assistant messages/session-idle
updates for a relay-flow HITL session. Invalid or missing HITL output opens no
dialog here (the server plugin sends the correction for partial
report-shaped output). A valid report — including one produced after a
correction — opens a
native `DialogSelect` with exactly:

- **Approve** — sends the exact parsed report to `relay-flow report`.
- **Reject** — sends nothing and does not advance the workflow.

The dialog preview includes the complete parsed report. No OpenCode Question
tool call or assistant-generated approval is involved. The approval is bound to
`sessionID:assistantMessageID`, so duplicate idle/message events cannot open a
second dialog for the same report.

After an explicit approval, temporary delivery failures retry the exact
unchanged JSON in the background; a duplicate or stale acknowledgement is
success. A permanent server validation error is sent unchanged to the HITL
agent and the corrected assistant message needs fresh native approval, even
when automatic failure routing was enabled for the rejected report. Debug
outcomes are written to `$RELAY_FLOW_HOME/plugin.log` when the configured
relay-flow home is available.

### Pi runtime

The Pi entry point is `pi.ts`, selected by the package manifest's
`pi.extensions` entry. Pi nodes must run in Pi's interactive TUI through the
runner-provided PTY. The harness command uses `pi --name <ticket>:<node>
[--prompt-template .pi/prompts/<agent>.md] [--session-id <id>] [/<agent> ]<prompt>`;
it does not use print mode, JSON/RPC mode, or an extension-install flag.
`default` uses Pi's built-in coding agent without a prompt-template resource. A
non-default workflow agent name such as `coder` or `reviewer` selects the
project-owned `.pi/prompts/<agent>.md` file through Pi's native prompt-template
loader; relay-flow passes the rendered task/feedback text as the `/<agent>`
command arguments and does not validate or register template files. Pi 0.84.1
rejects a bare `--`, so the prompt is one positional argv value, while
registration and reports use the shared `relay-flow` stdin transport.

Pi agent nodes send the fixed four-field correction through
`pi.sendUserMessage()` when output is invalid. Pi HITL nodes send that same
correction once when output contains at least two distinct report labels,
stay silent for ordinary, missing, empty, or aborted output, and use the host
UI directly for valid output:

```text
Approve relay-flow report for <ticket>:<node>
  Approve
  Reject
```

`ctx.ui.select()` is a direct Pi UI interaction, not an LLM Question-tool
call. Approve submits the parsed report; Reject or Escape submits nothing and
leaves the durable run waiting. Report retries use the same shared transport
and run in the background: `agent_settled` returns without waiting for backoff,
so Pi stays interactive. A permanent `invalidReport` sends the server's exact
message to the agent once; a corrected assistant entry has a new `reportId`.
At a HITL node the corrected entry must receive a new `Approve` decision,
even if the rejected failure report used automatic routing.

## Environment

The harness injects these on launch; the plugin reads them to route reports:

- `RELAY_FLOW_RUN_ID`
- `RELAY_FLOW_WORKFLOW`
- `RELAY_FLOW_REPO`
- `RELAY_FLOW_TICKET`
- `RELAY_FLOW_NODE`
- `RELAY_FLOW_NODE_TYPE` (`agent` or `hitl` — drives the nudge policy)
- `RELAY_FLOW_AUTO_REJECT` (`true` enables direct delivery of valid HITL failures; absent/false keeps approval)
- `RELAY_FLOW_NUDGE_PROMPT`
- `RELAY_FLOW_NEXT_STEPS_JSON`
- `RELAY_FLOW_REPORT_FORMAT` (the canonical four-field report contract)

## Files

- `index.ts` — shared report parsing, nudge policy, and retry helpers.
- `transport.ts` — shared argv-only `relay-flow` subprocess transport.
- `relay-flow.ts` — OpenCode entry point (`main`).
- `pi.ts` — Pi interactive extension entry point (`pi.extensions`).

## Tests

```sh
bun test
```
