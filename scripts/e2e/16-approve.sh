#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
T=$(ticket)
say "second pass: wait until pr-review terminal returns (implement -> verify -> pr-review)"
for i in $(seq 1 72); do
  PR=$(terminals_for_ticket | jq -r --arg title "$T:pr-review" '.[] | select(.title == $title and .connected) | .handle' 2>/dev/null || true)
  PSTAT=$(acli jira workitem view "$T" --fields "summary,status,components,assignee,labels,subtasks,comment" --json | jq -r '.fields.status.name')
  echo "  wait $i: parent=$PSTAT pr-review-handle=${PR:-none}"
  [ -n "$PR" ] && [ "$PSTAT" != "Done" ] && break
  [ "$PSTAT" = "Done" ] && break
  sleep 10
done
[ -n "$PR" ] || fail "second pr-review terminal did not appear"
run_json | jq -e '.currentNode == "pr-review" and .state == "waiting"' >/dev/null || fail "run is not waiting at second HITL pass"
IFS=$'\t' read -r PR_TERMINAL PR_SESSION PR_VISIT <<<"$(runtime_row pr-review)"
[ "$PR_SESSION" = "$(cat "$E2E_ROOT/hitl-session")" ] || fail "pr-review session was not reused"
say "human approves this time"
REPORT='The human reviewer approves this pass. Reply with exactly the report below and no other text.

STATUS: success
NEXT STEP: end

SUMMARY:
COMPLETED: Hello world program implemented, verified, reviewed.
COMMITS: None
NOT COMPLETED: None
ISSUES DISCOVERED: None
VERIFICATION: Human approved in Orca terminal.
NOTES: None

FEEDBACK:
REASON FOR NEXT STEP: None
REQUIRED ACTIONS: None
RELEVANT CONTEXT: None
EXPECTED RESULT: None'
orca-ide terminal send --terminal "$PR" --text "$REPORT" --enter --json >/dev/null
beat 2
