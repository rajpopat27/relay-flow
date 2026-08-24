#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
say "orca repo add --path $REPO (DisplayName must equal $REPO_NAME)"
orca-ide repo add --path "$REPO" --json | jq '{id: .result.id, displayName: .result.displayName, path: .result.path}'
beat
say "orca repo list --json (find our repo)"
orca-ide repo list --json | jq --arg p "$REPO" '[.. | objects | select(.path? == $p) | {displayName, path}]'
beat 2
