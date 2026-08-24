#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
T=$(ticket)
say "wait for HITL: terminal $T:pr-review present and waiting"
for i in $(seq 1 36); do
  H=$(terminals_for_ticket | jq -r '.[] | select(.title | endswith(":pr-review")) | .handle' 2>/dev/null || true)
  [ -n "$H" ] && break
  sleep 10
done
echo "handle=$H"
echo "$H" > "$E2E_ROOT/hitl-handle"
beat
say "orca terminal read $H (should show session waiting for human input; no nudge)"
orca-ide terminal read --terminal "$H" 2>&1 | tail -30
beat 2
