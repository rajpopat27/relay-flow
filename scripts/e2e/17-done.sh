#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
T=$(ticket)
say "wait for parent Done (end node)"
for i in $(seq 1 24); do
  PSTAT=$(acli jira workitem view "$T" --fields "summary,status,components,assignee,labels,subtasks,comment" --json | jq -r '.fields.status.name')
  echo "  wait $i: parent=$PSTAT"
  [ "$PSTAT" = "Done" ] && break
  sleep 10
done
say "final parent state"
acli jira workitem view "$T" --fields "summary,status,components,assignee,labels,subtasks,comment" --json | jq '{status: .fields.status.name, labels: .fields.labels}'
say "final mailboxes"
for S in $(acli jira workitem view "$T" --fields "summary,status,components,assignee,labels,subtasks,comment" --json | jq -r '.fields.subtasks[]?.key'); do
  acli jira workitem view "$S" --fields "summary,status,labels,comment" --json | jq '{summary: .fields.summary, status: .fields.status.name}'
done
say "relay-flow run list (completed)"
rf run list
beat
say "serve.log evidence (claim / errors)"
grep -iE "claim|ensure|error|report|signal" "$E2E_ROOT/serve.log" | tail -20
beat 2
