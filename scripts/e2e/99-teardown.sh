#!/usr/bin/env bash
# Teardown: stop serve safely and remove all generated E2E state.
source "$(dirname "$0")/lib.sh"
stop_serve
rm -rf "$HOME_DIR" "$E2E_ROOT/serve.pid" "$E2E_ROOT/serve.log" "$TICKET_FILE" \
  "$REPO" "$GIF_DIR" \
  "$E2E_ROOT/hitl-handle" "$E2E_ROOT/hitl-session" "$E2E_ROOT/implement-session" "$E2E_ROOT/implement-visit"
echo "teardown done; generated E2E repo and GIFs removed"
