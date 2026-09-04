# relay-flow runtime plugin

The runtime half of the OpenCode and Pi harness contracts. Both entry points
watch for completed agent output, parse the structured report, and deliver it to
the running relay-flow server via `relay-flow report` (one JSON object on stdin,
retried with the shared backoff until acknowledged).

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

Every visit (agent or HITL) submits one report with the same fixed labels:

```
STATUS: success | failure
NEXT STEP: <one configured route for that status>

SUMMARY:
COMPLETED: ...
COMMITS: <commit IDs or None>
NOT COMPLETED: ... | None
ISSUES DISCOVERED: ... | None
VERIFICATION: ...
NOTES: ... | None

FEEDBACK:
REASON FOR NEXT STEP: ...
REQUIRED ACTIONS: ...
RELEVANT CONTEXT: ...
EXPECTED RESULT: ...
```

`None` is the literal marker for an intentionally empty section. When
`NEXT STEP` is `end`, every FEEDBACK field must be `None` and no feedback
comment is written. The parsed JSON contains both `report.summary` and
`report.feedback`; they are never delivered as separate reports. Task-system
comment templates later render these as `summaryReport` on the current mailbox
and `feedbackReport` on only the selected next mailbox.

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
   - **hitl + invalid/missing without approval** → stays silent.
   - **hitl + valid without approval** → requests Question-tool approval.
   - **hitl + invalid after approval** → requests a corrected report.
   - **valid and authorized** → reports.
4. Delivers the report as one JSON object on `relay-flow report` stdin:

   ```json
   {"runId":"...","node":"coding","reportId":"<session>:<message>","report":{...}}
   ```

   `reportId` is derived from the harness session/message identity. The plugin
   retries the exact parsed report with the shared backoff
   (initial 2s, factor 2, jitter 0.2, max 5m) until acknowledged. A
   duplicate/stale ack is treated as success; at most one retry loop runs
   per node visit.

For OpenCode HITL nodes, the existing Question-tool approval behavior remains
unchanged. The OpenCode entry point is `relay-flow.ts` and is still selected
through the package `main` field.

### Native HITL TUI approval

The TUI entrypoint subscribes to completed assistant messages/session-idle
updates for a relay-flow HITL session. Invalid or missing HITL output remains
silent. A valid report opens a native `DialogSelect` with exactly:

- **Approve** — sends the exact parsed report to `relay-flow report`.
- **Reject** — sends nothing and does not advance the workflow.

The dialog preview includes the complete parsed report. No OpenCode Question
tool call or assistant-generated approval is involved. The approval is bound to
`sessionID:assistantMessageID`, so duplicate idle/message events cannot open a
second dialog for the same report.

After an explicit approval, report delivery retries the exact unchanged JSON
until relay-flow acknowledges it. A duplicate or stale acknowledgement is
success. Debug outcomes are written to `$RELAY_FLOW_HOME/plugin.log` when the
configured relay-flow home is available.

### Pi runtime

The Pi entry point is `pi.ts`, selected by the package manifest's
`pi.extensions` entry. Pi nodes must run in Pi's interactive TUI through the
runner-provided PTY. The harness command uses `pi --name <ticket>:<node>
[--append-system-prompt <role-file>] [--session-id <id>] <prompt>`; it does not
use print mode, JSON/RPC mode, or an extension-install flag. `default` uses
Pi's built-in coding agent without a role file. A non-default workflow agent
name such as `coder` or `reviewer` must have a readable `.pi/roles/<agent>.md`
file in the registered repository; relay-flow verifies that file and passes it
to Pi. Pi 0.84.1 rejects a bare `--`, so the prompt is one positional argv
value, while registration and reports use the shared `relay-flow` stdin
transport.

Pi agent nodes send the fixed complete-report correction through
`pi.sendUserMessage()` when output is invalid. Pi HITL nodes stay silent for
invalid output and use the host UI directly for valid output:

```text
Approve relay-flow report for <ticket>:<node>
  Approve
  Reject
```

`ctx.ui.select()` is a direct Pi UI interaction, not an LLM Question-tool
call. Approve submits the parsed report; Reject or Escape submits nothing and
leaves the durable run waiting. Report retries use the same shared transport
and never create another Pi turn.

## Environment

The harness injects these on launch; the plugin reads them to route reports:

- `RELAY_FLOW_RUN_ID`
- `RELAY_FLOW_WORKFLOW`
- `RELAY_FLOW_REPO`
- `RELAY_FLOW_TICKET`
- `RELAY_FLOW_NODE`
- `RELAY_FLOW_NODE_TYPE` (`agent` or `hitl` — drives the nudge policy)
- `RELAY_FLOW_NUDGE_PROMPT`
- `RELAY_FLOW_NEXT_STEPS_JSON`

## Files

- `index.ts` — shared report parsing, nudge policy, and retry helpers.
- `transport.ts` — shared argv-only `relay-flow` subprocess transport.
- `relay-flow.ts` — OpenCode entry point (`main`).
- `pi.ts` — Pi interactive extension entry point (`pi.extensions`).

## Tests

```sh
bun test
```
