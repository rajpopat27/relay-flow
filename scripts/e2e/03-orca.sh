#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
say "orca repo add --path $REPO (DisplayName must equal $REPO_NAME)"
ADD=$(orca-ide repo add --path "$REPO" --json)
echo "$ADD" | jq '.result.repo | {id, displayName, path}'
echo "$ADD" | jq -e --arg p "$REPO" --arg n "$REPO_NAME" '.result.repo.path == $p and .result.repo.displayName == $n' >/dev/null || fail "Orca repo-add result mismatch"
beat
say "orca repo list --json (find our repo)"
LIST=$(orca-ide repo list --json)
echo "$LIST" | jq --arg p "$REPO" '[.result.repos[] | select(.path == $p) | {displayName, path}]'
echo "$LIST" | jq -e --arg p "$REPO" --arg n "$REPO_NAME" '[.result.repos[] | select(.path == $p and .displayName == $n)] | length == 1' >/dev/null || fail "Orca repo list does not contain one exact project"
beat 2
