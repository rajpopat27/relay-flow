#!/usr/bin/env bash
# Shared config/helpers for relay-flow section-9 e2e.
# Source this from step scripts. Everything lives under $E2E_ROOT (/tmp/relayflow-e2e).
set -euo pipefail
export GIT_PAGER=cat PAGER=cat

E2E_ROOT="${E2E_ROOT:-/tmp/relayflow-e2e}"
REPO="$E2E_ROOT/raj-test-repo"
HOME_DIR="$E2E_ROOT/home"
WORKTREE_SRC="/home/raj/orca/workspaces/relay-flow/relay-flow-rewrite"
GIF_DIR="$E2E_ROOT/gifs"
export RELAY_FLOW_HOME="$HOME_DIR"

REPO_NAME="raj-test-repo"        # Orca DisplayName AND relay-flow repo name AND Jira component
JIRA_PROJECT="GHCOS"
JIRA_COMPONENT="raj-test-repo"
WORKFLOW_NAME="helloFlow"

# TICKET is set by 02-jira and persisted here; later steps require it.
TICKET_FILE="$E2E_ROOT/ticket"
ticket() { cat "$TICKET_FILE"; }

say()  { printf '\n$ %s\n' "$*"; }
beat() { sleep "${1:-1.2}"; }

# Orca terminals keyed by stable visual-layout tab title, never mutable pane title.
terminals_for_ticket() {
  orca-ide terminal list --include-visual-layouts --json | jq --arg prefix "$(ticket):" '
    def leaves:
      if type != "object" then empty
      elif .type == "terminal" then .
      else (.children[]? | leaves), (.first? | leaves), (.second? | leaves)
      end;
    [.result.visualLayouts[]?.root.tabs[]?
      | select(.title | startswith($prefix))
      | . as $tab
      | ($tab.panes | leaves)
      | {title: $tab.title, handle: .handle, connected: (.connected == true)}]'
}

rf() { relay-flow "$@"; }

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

require_file() { [ -f "$1" ] || fail "missing file: $1"; }

stop_serve() {
  if [ -S "$HOME_DIR/server.sock" ]; then
    RELAY_FLOW_HOME="$HOME_DIR" relay-flow stop || fail "relay-flow stop failed"
    for _ in $(seq 1 30); do
      [ ! -S "$HOME_DIR/server.sock" ] && return 0
      sleep 1
    done
    fail "server socket remained after stop"
  fi
}

run_json() { rf run get --ticket "$(ticket)"; }

runtime_row() {
  local node="$1" run_id
  run_id=$(run_json | jq -r '.id')
  sqlite3 -tabs "$HOME_DIR/state.db" \
    "SELECT COALESCE(terminal_id,''), COALESCE(session_id,''), node_visit_id FROM relay_node_runtime WHERE run_id = '$run_id' AND node = '$node';"
}
