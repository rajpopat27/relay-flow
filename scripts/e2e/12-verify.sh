#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
T=$(ticket)
say "wait for implement mailbox Done + verify node active"
IMPL_KEY=""
for i in $(seq 1 36); do
  IMPL_KEY=$(acli jira workitem view "$T" --fields "subtasks" --json | jq -r '.fields.subtasks[]? | select(.summary | endswith(":implement")) | .key')
  IMPL=""
  [ -n "$IMPL_KEY" ] && IMPL=$(acli jira workitem view "$IMPL_KEY" --fields "status" --json | jq -r '.fields.status.name')
  echo "  wait $i: implement mailbox ($IMPL_KEY) status=$IMPL"
  [ "$IMPL" = "Done" ] && break
  sleep 10
done
say "mailboxes state"
for S in $(acli jira workitem view "$T" --fields "summary,status,components,assignee,labels,subtasks,comment" --json | jq -r '.fields.subtasks[]?.key'); do
  acli jira workitem view "$S" --fields "summary,status,labels,comment" --json | jq '{summary: .fields.summary, status: .fields.status.name, comments: (.fields.comment.comments | length)}'
done
beat
say "terminals for $T (expect $T:verify)"
terminals_for_ticket | jq .
beat 2
