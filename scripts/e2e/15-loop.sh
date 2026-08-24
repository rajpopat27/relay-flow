#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
T=$(ticket)
say "wait: loop back to implement (feedback on implement mailbox, status reopened)"
IMPL_KEY=""
for i in $(seq 1 36); do
  IMPL_KEY=$(acli jira workitem view "$T" --fields "subtasks" --json | jq -r '.fields.subtasks[]? | select(.summary | endswith(":implement")) | .key')
  IMPL=""
  [ -n "$IMPL_KEY" ] && IMPL=$(acli jira workitem view "$IMPL_KEY" --fields "status" --json | jq -r '.fields.status.name')
  echo "  wait $i: implement ($IMPL_KEY) = $IMPL"
  [ "$IMPL" = "In Progress" ] && break
  sleep 10
done
say "implement mailbox comments (feedback present)"
acli jira workitem view "$IMPL_KEY" --fields "summary,status,comment" --json | jq '{status: .fields.status.name, comments: [.fields.comment.comments[]?.body] | length}'
beat 2
