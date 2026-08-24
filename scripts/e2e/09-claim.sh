#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
T=$(ticket)
say "wait for poll to claim $T (pollInterval 15s, allow up to 60s)"
for i in $(seq 1 12); do
  LABELS=$(acli jira workitem view "$T" --fields "summary,status,components,assignee,labels,subtasks,comment" --json | jq -r '[.fields.labels[]?] | join(",")')
  echo "  poll $i: labels=$LABELS"
  [[ "$LABELS" == *"wf:$WORKFLOW_NAME"* ]] && break
  sleep 5
done
beat
say "acli view $T: status + wf label"
acli jira workitem view "$T" --fields "summary,status,components,assignee,labels,subtasks,comment" --json | jq '{status: .fields.status.name, labels: .fields.labels}'
beat
say "relay-flow run list"
rf run list
beat 2
