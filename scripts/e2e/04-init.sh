#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
say "relay-flow init --task-plugin jira --runner-plugin orca --harness-plugin opencode"
rm -rf "$HOME_DIR"; mkdir -p "$HOME_DIR"
rf init --task-plugin jira --runner-plugin orca --harness-plugin opencode
beat
say "cat \$RELAY_FLOW_HOME/config.yaml"
cat "$HOME_DIR/config.yaml"
beat
say "ls -la \$RELAY_FLOW_HOME (state.db exists, perms 0700)"
ls -la "$HOME_DIR"
beat 2
