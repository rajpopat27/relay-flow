#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
T=$(ticket)
say "acli subtasks of $T (expect $T-impl mailboxes: implement/verify/pr-review)"
for i in $(seq 1 6); do
  SUB=$(acli jira workitem view "$T" --fields "summary,status,components,assignee,labels,subtasks,comment" --json | jq -r '[.fields.subtasks[]?.key] | join(",")')
  echo "  check $i: subtasks=$SUB"
  [ -n "$SUB" ] && [ "$SUB" != "" ] && break
  sleep 5
done
for S in $(acli jira workitem view "$T" --fields "summary,status,components,assignee,labels,subtasks,comment" --json | jq -r '.fields.subtasks[]?.key'); do
  acli jira workitem view "$S" --fields "summary,status,labels,comment" --json | jq '{key, summary: .fields.summary, status: .fields.status.name, labels: .fields.labels}'
done
beat 2
