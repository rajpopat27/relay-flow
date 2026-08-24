#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
T=$(ticket)
H=$(cat "$E2E_ROOT/hitl-handle")
say "second pass: wait until pr-review terminal returns (implement -> verify -> pr-review)"
for i in $(seq 1 72); do
  PR=$(terminals_for_ticket | jq -r '.[] | select(.title | endswith(":pr-review")) | .handle' 2>/dev/null || true)
  PSTAT=$(acli jira workitem view "$T" --fields "summary,status,components,assignee,labels,subtasks,comment" --json | jq -r '.fields.status.name')
  echo "  wait $i: parent=$PSTAT pr-review-handle=${PR:-none}"
  [ -n "$PR" ] && [ "$PSTAT" != "Done" ] && break
  [ "$PSTAT" = "Done" ] && break
  sleep 10
done
say "human approves this time"
REPORT='STATUS: success
NEXT STEP: end
SUMMARY:
  completed: Hello world program implemented, verified, reviewed.
  notCompleted: None
  issuesDiscovered: None
  verification: Human approved in Orca terminal.
  notes: none
FEEDBACK FOR end:
  reasonForNextStep: None
  requiredActions: None
  relevantContext: None
  expectedResult: None'
H=$(terminals_for_ticket | jq -r '.[] | select(.title | endswith(":pr-review")) | .handle')
orca-ide terminal send --terminal "$H" --text "$REPORT"
sleep 1
orca-ide terminal send --terminal "$H" --key enter
beat 2
