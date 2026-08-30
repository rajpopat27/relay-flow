#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
T=$(ticket)
say "wait for implement mailbox Done + verify node active"
IMPL_KEY=""
for i in $(seq 1 36); do
  IMPL_KEY=$(acli jira workitem view "$T" --fields "subtasks" --json | jq -r '.fields.subtasks[]? | select(.fields.summary | endswith(":implement")) | .key')
  IMPL=""
  [ -n "$IMPL_KEY" ] && IMPL=$(acli jira workitem view "$IMPL_KEY" --fields "status" --json | jq -r '.fields.status.name')
  echo "  wait $i: implement mailbox ($IMPL_KEY) status=$IMPL"
  [ "$IMPL" = "Done" ] && break
  sleep 10
done
[ "$IMPL" = "Done" ] || fail "implement mailbox did not complete"
say "mailboxes state"
VERIFY_KEY=""
for S in $(acli jira workitem view "$T" --fields "summary,status,components,assignee,labels,subtasks,comment" --json | jq -r '.fields.subtasks[]?.key'); do
  VIEW=$(acli jira workitem view "$S" --fields "summary,status,labels,comment" --json)
  echo "$VIEW" | jq '{summary: .fields.summary, status: .fields.status.name, comments: (.fields.comment.comments | length)}'
  SUMMARY=$(echo "$VIEW" | jq -r '.fields.summary')
  [ "$SUMMARY" = "$T:verify" ] && VERIFY_KEY="$S"
  if [ "$SUMMARY" = "$T:implement" ]; then
    [ "$(echo "$VIEW" | jq '.fields.comment.comments | length')" -ge 1 ] || fail "implement summary comment missing"
  fi
done
[ -n "$VERIFY_KEY" ] || fail "verify mailbox missing"
VERIFY_VIEW=$(acli jira workitem view "$VERIFY_KEY" --fields "summary,status,labels,comment" --json)
[ "$(echo "$VERIFY_VIEW" | jq '.fields.comment.comments | length')" -ge 1 ] || fail "verify feedback comment missing"
beat
say "terminals for $T (expect $T:verify)"
TS=$(terminals_for_ticket)
echo "$TS" | jq .
VERIFY_ROW=""
for _ in $(seq 1 30); do
  VERIFY_ROW=$(runtime_row verify)
  [ -n "$VERIFY_ROW" ] && break
  sleep 2
done
IFS=$'\t' read -r VERIFY_TERMINAL VERIFY_SESSION VERIFY_VISIT <<<"$VERIFY_ROW"
[ -n "$VERIFY_TERMINAL" ] && [ -n "$VERIFY_VISIT" ] || fail "no persisted evidence that verify was entered"
CURRENT=$(run_json | jq -r '.currentNode')
case "$CURRENT" in
  verify)
    echo "$TS" | jq -e --arg title "$T:verify" '[.[] | select(.title == $title and .connected)] | length == 1' >/dev/null || fail "verify terminal is not active"
    ;;
  pr-review)
    [ "$(echo "$VERIFY_VIEW" | jq -r '.fields.status.name')" = "Done" ] || fail "run advanced without completing verify mailbox"
    ;;
  *) fail "run never transitioned through verify; current node is $CURRENT" ;;
esac
beat 2
