#!/usr/bin/env bash
# Teardown: stop serve, remove run state; optionally reset Jira ticket. Does NOT delete the git repo.
source "$(dirname "$0")/lib.sh"
[ -f "$E2E_ROOT/serve.pid" ] && kill "$(cat "$E2E_ROOT/serve.pid")" 2>/dev/null || true
rm -rf "$HOME_DIR" "$E2E_ROOT/serve.pid" "$E2E_ROOT/serve.log" "$TICKET_FILE" "$E2E_ROOT/hitl-handle"
echo "teardown done; repo at $REPO untouched; gifs in $GIF_DIR"
