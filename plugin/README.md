# relay-flow OpenCode plugin

The plugin has two OpenCode entrypoints:

- **Server entrypoint** (`./server`, `relay-flow.ts`): registers sessions,
  pins stable terminal titles, nudges invalid agent output, and delivers agent
  reports.
- **TUI entrypoint** (`./tui`, `tui.ts`): handles relay-flow HITL reports with
  a native OpenCode approval dialog.

Both entrypoints are active only when relay-flow launches the session with
`RELAY_FLOW_*` environment variables. The plugin never calls Jira, Beads, or
any other task system directly; it sends only the documented relay-flow JSON
commands. It never writes SQLite or manages runner environments.

## Install/configure

The server entrypoint is configured in `opencode.json`:

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
`report.feedback`; they are never delivered as separate reports.

## Server entrypoint

On session creation/update, the server plugin sends `{runId, node, sessionId}`
through `relay-flow runtime-register`. Relay-flow persists that session ID;
normal execution uses the persisted ID to resume the harness session.

On `session.idle` it:

1. Reads the last completed assistant message (aborted turns are skipped).
2. Parses the complete report contract.
3. Nudges an agent node with the fixed report contract when output is invalid.
4. Delivers a valid agent report as one JSON object on `relay-flow report` stdin.
5. Remains silent for HITL output; the TUI entrypoint owns HITL approval.

## Native HITL TUI approval

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

## JSON contracts

Runtime registration:

```json
{"runId":"...","node":"review","sessionId":"..."}
```

Report delivery:

```json
{"runId":"...","node":"review","reportId":"<session>:<message>","report":{...}}
```

`reportId` comes from the harness session/message identity. `nodeVisitID` is
internal to relay-flow and is not present in either plugin payload.

## Environment

The harness injects these on launch; the plugin reads them to route reports:

- `RELAY_FLOW_HOME`
- `RELAY_FLOW_RUN_ID`
- `RELAY_FLOW_WORKFLOW`
- `RELAY_FLOW_REPO`
- `RELAY_FLOW_TICKET`
- `RELAY_FLOW_NODE`
- `RELAY_FLOW_NODE_TYPE` (`agent` or `hitl`)
- `RELAY_FLOW_NUDGE_PROMPT`
- `RELAY_FLOW_NEXT_STEPS_JSON`

## Files

- `index.ts` — pure report parser, agent nudge policy, and exact-report retry.
- `relay-flow.ts` — OpenCode server plugin entrypoint.
- `tui.ts` — OpenCode native TUI HITL approval entrypoint.

## Tests

```sh
bun test
```
