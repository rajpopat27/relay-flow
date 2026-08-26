#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
T=$(ticket)
say "wait for parent Cancelled (end node)"
for i in $(seq 1 24); do
  PSTAT=$(acli jira workitem view "$T" --fields "summary,status,components,assignee,labels,subtasks,comment" --json | jq -r '.fields.status.name')
  echo "  wait $i: parent=$PSTAT"
  [ "$PSTAT" = "Cancelled" ] && break
  sleep 10
done
[ "$PSTAT" = "Cancelled" ] || fail "parent did not reach Cancelled"
say "final parent state"
PARENT=$(acli jira workitem view "$T" --fields "summary,status,components,assignee,labels,subtasks,comment" --json)
echo "$PARENT" | jq '{status: .fields.status.name, labels: .fields.labels}'
echo "$PARENT" | jq -e --arg label "wf:$WORKFLOW_NAME" '.fields.status.name == "Cancelled" and ((.fields.labels // []) | index($label) != null)' >/dev/null || fail "final parent state mismatch"
say "final mailboxes"
COUNT=0
for S in $(echo "$PARENT" | jq -r '.fields.subtasks[]?.key'); do
  VIEW=$(acli jira workitem view "$S" --fields "summary,status,labels,comment" --json)
  echo "$VIEW" | jq '{summary: .fields.summary, status: .fields.status.name}'
  echo "$VIEW" | jq -e '.fields.status.name == "Done"' >/dev/null || fail "mailbox $S is not Done"
  COUNT=$((COUNT + 1))
done
[ "$COUNT" -eq 3 ] || fail "expected three final mailboxes"
say "relay-flow run list (completed)"
run_json | jq -e '.state == "completed" and .finishedAt != null' >/dev/null || fail "run is not completed"
[ "$(terminals_for_ticket | jq 'length')" -eq 0 ] || fail "run-owned terminals remain after cleanup"
beat
say "serve.log evidence (claim / errors)"
grep -iE "claim|ensure|error|report|signal" "$E2E_ROOT/serve.log" || true
beat 2
