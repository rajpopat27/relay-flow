#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
say "relay-flow repo register --name $REPO_NAME --path $REPO --set project=$JIRA_PROJECT --set component=$JIRA_COMPONENT"
rf repo register --name "$REPO_NAME" --path "$REPO" \
  --set "project=$JIRA_PROJECT" --set "component=$JIRA_COMPONENT"
beat
say "relay-flow repo get --name $REPO_NAME"
rf repo get --name "$REPO_NAME"
beat 2
