#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
T=$(ticket)
say "wait for HITL: terminal $T:pr-review present and waiting"
for i in $(seq 1 36); do
  H=$(terminals_for_ticket | jq -r --arg title "$T:pr-review" '.[] | select(.title == $title and .connected) | .handle' 2>/dev/null || true)
  [ -n "$H" ] && break
  sleep 10
done
[ -n "$H" ] || fail "pr-review HITL terminal did not appear"
echo "handle=$H"
echo "$H" > "$E2E_ROOT/hitl-handle"
beat
say "orca terminal read $H (should show session waiting for human input; no nudge)"
orca-ide terminal wait --terminal "$H" --for tui-idle --timeout-ms 120000 --json >/dev/null || fail "HITL terminal did not become idle"
READ=$(orca-ide terminal read --terminal "$H" --limit 200 --json)
echo "$READ" | jq '.result | {nextCursor, lines}'
IFS=$'\t' read -r HITL_TERMINAL HITL_SESSION HITL_VISIT <<<"$(runtime_row pr-review)"
[ "$HITL_TERMINAL" = "$H" ] && [ -n "$HITL_SESSION" ] && [ -n "$HITL_VISIT" ] || fail "HITL runtime registration incomplete"
run_json | jq -e '.currentNode == "pr-review" and .state == "waiting"' >/dev/null || fail "run is not waiting at HITL"
for _ in $(seq 1 30); do
  grep -Fq "msg=\"hitl silent\"" "$HOME_DIR/plugin.log" && grep -Fq "nodeVisitId=\"$HITL_VISIT\"" "$HOME_DIR/plugin.log" && break
  sleep 1
done
grep -Fq "msg=\"hitl silent\"" "$HOME_DIR/plugin.log" &&
  grep -Fq "nodeVisitId=\"$HITL_VISIT\"" "$HOME_DIR/plugin.log" || fail "no silent-HITL evidence in plugin.log"
printf '%s\n' "$HITL_SESSION" > "$E2E_ROOT/hitl-session"
beat 2
