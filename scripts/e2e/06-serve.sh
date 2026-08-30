#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
say "relay-flow serve --debug > serve.log 2>&1 &"
nohup relay-flow serve --debug > "$E2E_ROOT/serve.log" 2>&1 &
echo $! > "$E2E_ROOT/serve.pid"
beat 3
say "ls -la \$RELAY_FLOW_HOME (socket + lock)"
ls -la "$HOME_DIR"
[ -S "$HOME_DIR/server.sock" ] || fail "server socket missing"
[ "$(stat -c %a "$HOME_DIR/server.sock")" = "600" ] || fail "server socket mode is not 0600"
kill -0 "$(cat "$E2E_ROOT/serve.pid")" 2>/dev/null || fail "serve process exited"
require_file "$E2E_ROOT/serve.log"
require_file "$HOME_DIR/server.log"
require_file "$HOME_DIR/server.lock"
require_file "$HOME_DIR/plugin.log"
[ -d "$HOME_DIR/workflows" ] || fail "workflows directory missing"
beat
say "relay-flow workflow list (proves socket up)"
rf workflow list
beat 2
