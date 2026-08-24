#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
T=$(ticket)
say "orca terminal list filtered by $T (expect $T:implement)"
for i in $(seq 1 12); do
  TS=$(terminals_for_ticket)
  echo "$TS" | jq .
  [ "$(echo "$TS" | jq 'length')" -ge 1 ] && break
  sleep 5
done
beat
H=$(terminals_for_ticket | jq -r '.[0].handle // empty')
say "orca terminal show $H (agent activity preview)"
[ -n "$H" ] && orca-ide terminal show --terminal "$H" --json | jq '.result | {title, preview}'
beat 2
