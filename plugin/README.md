# relay-flow opencode plugin

The runtime half of the harness contract. Watches for a completed agent reply,
parses the structured report, applies the agent/HITL nudge policy, and delivers
the report to the running relay-flow server via `relay-flow report` (one JSON
object on stdin, retried with the shared backoff until acknowledged).

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

## Structured report

Every visit (agent or HITL) ends with the same contract:

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
comment is written.

## What the plugin does

On session creation/update, the plugin sends `{runId, node, sessionId}` through
`relay-flow runtime-register`. Relay-flow persists that session ID; normal
execution uses the persisted ID to resume the harness session.

On `session.idle`:

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

- `index.ts` — the plugin (parse, nudge policy, report retry).

## Tests

```sh
bun test
```
