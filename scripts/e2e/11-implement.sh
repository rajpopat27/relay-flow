#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
T=$(ticket)
say "orca visual-layout tabs filtered by $T (expect exactly $T:implement)"
for i in $(seq 1 12); do
  TS=$(terminals_for_ticket)
  echo "$TS" | jq .
  [ "$(echo "$TS" | jq --arg title "$T:implement" '[.[] | select(.title == $title and .connected)] | length')" -eq 1 ] && break
  sleep 5
done
[ "$(echo "$TS" | jq --arg title "$T:implement" '[.[] | select(.title == $title and .connected)] | length')" -eq 1 ] || fail "stable implement tab was not found"
beat
H=$(echo "$TS" | jq -r --arg title "$T:implement" '.[] | select(.title == $title) | .handle')
say "orca terminal show $H (agent activity preview)"
SHOW=$(orca-ide terminal show --terminal "$H" --json)
echo "$SHOW" | jq '.result.terminal | {title, connected, writable, preview}'
echo "$SHOW" | jq -e '.result.terminal.connected and .result.terminal.writable and ((.result.terminal.title == "opencode") or (.result.terminal.title | startswith("OC |")))' >/dev/null || fail "terminal returned to a shell or is unusable"
ROW=""
for _ in $(seq 1 30); do
  ROW=$(runtime_row implement)
  [ -n "$(printf '%s' "$ROW" | cut -f2)" ] && break
  sleep 2
done
IFS=$'\t' read -r TERMINAL_ID SESSION_ID VISIT_ID <<<"$ROW"
[ "$TERMINAL_ID" = "$H" ] || fail "persisted terminal ID does not match Orca handle"
[ -n "$SESSION_ID" ] || fail "OpenCode session was not registered"
[ -n "$VISIT_ID" ] || fail "implement visit ID missing"
RUN_VISIT=$(run_json | jq -r '.currentNodeVisitId')
[ "$RUN_VISIT" = "$VISIT_ID" ] || fail "runtime visit does not match current run visit"
ACTIVE=false
for _ in $(seq 1 30); do
  for ENV in /proc/[0-9]*/environ; do
    [ -r "$ENV" ] || continue
    if tr '\0' '\n' < "$ENV" 2>/dev/null | grep -Fxq "RELAY_FLOW_TICKET=$T" &&
       tr '\0' '\n' < "$ENV" 2>/dev/null | grep -Fxq 'RELAY_FLOW_NODE=implement' &&
       tr '\0' ' ' < "${ENV%/environ}/cmdline" 2>/dev/null | grep -q 'opencode'; then
      ACTIVE=true
      break
    fi
  done
  [ "$ACTIVE" = true ] && break
  sleep 2
done
[ "$ACTIVE" = true ] || fail "no active OpenCode process found for implement"
printf '%s\n' "$SESSION_ID" > "$E2E_ROOT/implement-session"
printf '%s\n' "$VISIT_ID" > "$E2E_ROOT/implement-visit"
beat 2
