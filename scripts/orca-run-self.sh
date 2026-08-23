#!/usr/bin/env bash
# orca-run-self.sh <objective-name>
# Creates a Run for the CALLING terminal, binds it, proves the bind, prints the Run ID.
# Deterministic: no caller-supplied ID, no shell capture of the wrong field.
# Usage: orca-run-self.sh implementer   -> prints nothing but the bound run id on stdout.
set -euo pipefail

OBJ="${1:?usage: orca-run-self.sh <objective-name>}"
ORCA="${ORCA_CLI_COMMAND:-orca-ide}"

# 1. create; pull result.run.id (NOT top-level .id)
RUN_ID="$("$ORCA" orchestration run-create --objective "$OBJ" --json \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["run"]["id"])')"

# 2. bind this terminal to it
"$ORCA" orchestration run-use --id "$RUN_ID" --json >/dev/null

# 3. prove the bind: coordinator_handle must be non-null
COORD="$("$ORCA" orchestration run-show --id "$RUN_ID" --json \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["run"].get("coordinator_handle") or "")')"

if [ -z "$COORD" ]; then
  echo "ERROR: run $RUN_ID created but NOT bound (coordinator_handle null)" >&2
  exit 1
fi

# stdout carries ONLY the run id so callers can capture it safely
printf '%s\n' "$RUN_ID"
