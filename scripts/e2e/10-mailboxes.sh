#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
T=$(ticket)
say "acli subtasks of $T (expect $T-impl mailboxes: implement/verify/pr-review)"
for i in $(seq 1 12); do
  PARENT=$(acli jira workitem view "$T" --fields "summary,status,components,assignee,labels,subtasks,comment" --json)
  COUNT=$(echo "$PARENT" | jq '(.fields.subtasks // []) | length')
  echo "  check $i: subtask-count=$COUNT"
  [ "$COUNT" -eq 3 ] && break
  sleep 5
done
[ "$COUNT" -eq 3 ] || fail "expected exactly three mailbox subtasks"
DETAILS='[]'
for S in $(echo "$PARENT" | jq -r '.fields.subtasks[]?.key'); do
  VIEW=$(acli jira workitem view "$S" --fields "summary,status,labels,comment" --json)
  echo "$VIEW" | jq '{key, summary: .fields.summary, status: .fields.status.name, labels: .fields.labels}'
  DETAILS=$(jq -cn --argjson all "$DETAILS" --argjson one "$VIEW" '$all + [$one]')
done
echo "$DETAILS" | jq -e --arg t "$T" --arg label "wf:$WORKFLOW_NAME" '
  ([.[].fields.summary] | sort) == ([($t+":implement"),($t+":pr-review"),($t+":verify")] | sort) and
  all(.[]; (.fields.labels // []) | index($label) != null)' >/dev/null || fail "mailbox titles or labels mismatch"
beat 2
