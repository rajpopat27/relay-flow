#!/usr/bin/env bash
# Teardown: stop serve safely and remove temporary run state. Does NOT delete the git repo or GIFs.
source "$(dirname "$0")/lib.sh"
stop_serve
rm -rf "$HOME_DIR" "$E2E_ROOT/serve.pid" "$E2E_ROOT/serve.log" "$TICKET_FILE" \
  "$E2E_ROOT/hitl-handle" "$E2E_ROOT/hitl-session" "$E2E_ROOT/implement-session" "$E2E_ROOT/implement-visit"
echo "teardown done; repo at $REPO untouched; gifs in $GIF_DIR"
