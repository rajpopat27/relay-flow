#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
say "relay-flow serve --debug > serve.log 2>&1 &"
nohup relay-flow serve --debug > "$E2E_ROOT/serve.log" 2>&1 &
echo $! > "$E2E_ROOT/serve.pid"
beat 3
say "ls -la \$RELAY_FLOW_HOME (socket + lock)"
ls -la "$HOME_DIR"
beat
say "relay-flow workflow list (proves socket up)"
rf workflow list
beat 2
