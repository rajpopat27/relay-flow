#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
say "acli jira workitem create --project $JIRA_PROJECT --type Task"
OUT=$(acli jira workitem create --project "$JIRA_PROJECT" --type Task \
  --summary "e2e: hello world program in python" \
  --description "create a hello world program in python" \
  --assignee "@me" --json)
echo "$OUT" | jq .
KEY=$(echo "$OUT" | jq -r '.key // .Key // empty')
[ -n "$KEY" ] || { echo "no key in output"; exit 1; }
echo "$KEY" > "$TICKET_FILE"
beat

say "WAIT: human sets component '$JIRA_COMPONENT' on $KEY in Jira UI, then runs 02b"
echo "ticket key saved to $TICKET_FILE"
beat 2
