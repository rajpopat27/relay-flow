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
[ "$IMPL" = "In Progress" ] || fail "implement mailbox did not reopen"
say "implement mailbox comments (feedback present)"
VIEW=$(acli jira workitem view "$IMPL_KEY" --fields "summary,status,comment" --json)
echo "$VIEW" | jq '{status: .fields.status.name, comments: [.fields.comment.comments[]?.body] | length}'
[ "$(echo "$VIEW" | jq '.fields.comment.comments | length')" -ge 2 ] || fail "implement feedback comment missing"
run_json | jq -e '.currentNode == "implement" and .state == "waiting"' >/dev/null || fail "run did not loop to implement"
IFS=$'\t' read -r LOOP_TERMINAL LOOP_SESSION LOOP_VISIT <<<"$(runtime_row implement)"
[ "$LOOP_SESSION" = "$(cat "$E2E_ROOT/implement-session")" ] || fail "implement session was not reused"
[ "$LOOP_VISIT" != "$(cat "$E2E_ROOT/implement-visit")" ] || fail "implement revisit did not get a fresh visit ID"
[ -n "$LOOP_TERMINAL" ] || fail "implement revisit terminal ID missing"
TS=$(terminals_for_ticket)
echo "$TS" | jq -e --arg title "$T:implement" --arg handle "$LOOP_TERMINAL" '[.[] | select(.title == $title and .handle == $handle and .connected)] | length == 1' >/dev/null || fail "implement revisit terminal not active"
beat 2
