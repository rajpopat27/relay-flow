#!/usr/bin/env bash
# Adds root taskConfig (jira assignee) + pollIntervalSeconds to machine config after init.
source "$(dirname "$0")/lib.sh"
CFG="$HOME_DIR/config.yaml"
say "append taskConfig + pollIntervalSeconds to config.yaml"
cat >> "$CFG" <<'YAML'
pollIntervalSeconds: 15
taskConfig:
  assignee: test.user@example.com
YAML
say "cat config.yaml"
cat "$CFG"
grep -Fxq 'pollIntervalSeconds: 15' "$CFG" || fail "poll interval is not 15"
grep -Fxq '  assignee: test.user@example.com' "$CFG" || fail "Jira assignee missing"
grep -Fxq 'keepSessionsAlive: true' "$CFG" || fail "keepSessionsAlive must default true"
grep -Fxq 'keepTerminalsAlive: true' "$CFG" || fail "keepTerminalsAlive must default true"
beat 2
