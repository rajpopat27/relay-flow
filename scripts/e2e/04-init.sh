#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
say "relay-flow init --task-plugin jira --runner-plugin orca --harness-plugin opencode"
rm -rf "$HOME_DIR"; mkdir -p "$HOME_DIR"
: "${JIRA_EMAIL:?set JIRA_EMAIL}" "${JIRA_API_TOKEN:?set JIRA_API_TOKEN}"
rf init --task-plugin jira --runner-plugin orca --harness-plugin opencode
say "relay-flow task auth --site ... --email ... --token ..."
rf task auth --site "https://wkengineering.atlassian.net" --email "$JIRA_EMAIL" --token "$JIRA_API_TOKEN"
beat
say "cat \$RELAY_FLOW_HOME/config.yaml"
cat "$HOME_DIR/config.yaml"
beat
say "ls -la \$RELAY_FLOW_HOME (state.db exists, perms 0700)"
ls -la "$HOME_DIR"
[ "$(stat -c %a "$HOME_DIR")" = "700" ] || fail "RELAY_FLOW_HOME mode is not 0700"
for F in config.yaml credentials.yaml state.db; do
  require_file "$HOME_DIR/$F"
  [ "$(stat -c %a "$HOME_DIR/$F")" = "600" ] || fail "$F mode is not 0600"
done
grep -Fxq 'taskPlugin: jira' "$HOME_DIR/config.yaml" || fail "jira task plugin missing"
grep -Fxq 'runnerPlugin: orca' "$HOME_DIR/config.yaml" || fail "orca runner plugin missing"
grep -Fxq 'harnessPlugin: opencode' "$HOME_DIR/config.yaml" || fail "opencode harness plugin missing"
grep -Fxq 'keepSessionsAlive: true' "$HOME_DIR/config.yaml" || fail "keepSessionsAlive default missing"
grep -Fq 'relay_runs' < <(sqlite3 "$HOME_DIR/state.db" '.tables') || fail "relay_runs schema missing"
grep -Fq 'relay_node_runtime' < <(sqlite3 "$HOME_DIR/state.db" '.tables') || fail "relay_node_runtime schema missing"
beat 2
