#!/usr/bin/env bash
# Shared config/helpers for relay-flow section-9 e2e.
# Source this from step scripts. Everything lives under $E2E_ROOT (/tmp/relayflow-e2e).
set -euo pipefail
export GIT_PAGER=cat PAGER=cat

E2E_ROOT="${E2E_ROOT:-/tmp/relayflow-e2e}"
REPO="$E2E_ROOT/raj-test-repo"
HOME_DIR="$E2E_ROOT/home"
BIN="$E2E_ROOT/relay-flow"
WORKTREE_SRC="/home/raj/orca/workspaces/relay-flow/relay-flow-rewrite"
GIF_DIR="$E2E_ROOT/gifs"
export RELAY_FLOW_HOME="$HOME_DIR"

REPO_NAME="raj-test-repo"        # Orca DisplayName AND relay-flow repo name AND Jira component
JIRA_PROJECT="GHCOS"
JIRA_COMPONENT="raj-test-repo"
WORKFLOW_NAME="hello-flow"

# TICKET is set by 02-jira and persisted here; later steps require it.
TICKET_FILE="$E2E_ROOT/ticket"
ticket() { cat "$TICKET_FILE"; }

say()  { printf '\n$ %s\n' "$*"; }
beat() { sleep "${1:-1.2}"; }

# jq-filtered Orca terminals for our ticket
terminals_for_ticket() {
  orca-ide terminal list --json | jq --arg t "$(ticket)" \
    '[.result.terminals[] | select(.title | contains($t)) | {title, handle}]'
}

rf() { "$BIN" "$@"; }
