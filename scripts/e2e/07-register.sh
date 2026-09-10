#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
say "relay-flow repo register --name $REPO_NAME --path $REPO --set project=$JIRA_PROJECT --set statusDefaults.start='To Do' --set statusDefaults.work='In Progress' --set statusDefaults.end='Done'"
rf repo register --name "$REPO_NAME" --path "$REPO" \
  --set "project=$JIRA_PROJECT" \
  --set 'statusDefaults.start=To Do' \
  --set 'statusDefaults.work=In Progress' \
  --set 'statusDefaults.end=Done'
beat
say "relay-flow repo get --name $REPO_NAME"
INFO=$(rf repo get --name "$REPO_NAME")
echo "$INFO" | jq .
echo "$INFO" | jq -e --arg n "$REPO_NAME" --arg p "$REPO" --arg project "$JIRA_PROJECT" --arg component "$REPO_NAME" '
  .name == $n and .path == $p and .taskConfig.project == $project and .taskConfig.component == $component and
  .taskConfig.statusDefaults.start == "To Do" and
  .taskConfig.statusDefaults.work == "In Progress" and
  .taskConfig.statusDefaults.end == "Done"' >/dev/null || fail "registered repo mismatch"
beat 2
