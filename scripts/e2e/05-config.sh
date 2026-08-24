#!/usr/bin/env bash
# Adds root taskConfig (jira assignee) + pollIntervalSeconds to machine config after init.
source "$(dirname "$0")/lib.sh"
CFG="$HOME_DIR/config.yaml"
say "append taskConfig + pollIntervalSeconds to config.yaml"
cat >> "$CFG" <<'YAML'
pollIntervalSeconds: 15
taskConfig:
  assignee: raj.popat@wolterskluwer.com
YAML
say "cat config.yaml"
cat "$CFG"
beat 2
