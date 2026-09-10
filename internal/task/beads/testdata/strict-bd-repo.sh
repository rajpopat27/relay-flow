#!/bin/sh
set -eu

fail() {
  printf 'unsupported or malformed bd invocation: %s\n' "$*" >&2
  exit 64
}

[ -n "${BD_LOG-}" ] || fail "BD_LOG is missing"
[ -n "${BEADS_DIR-}" ] || fail "BEADS_DIR is missing"
[ "${BEADS_DB+x}" != x ] || fail "ambient BEADS_DB leaked"
[ "${BD_DB+x}" != x ] || fail "ambient BD_DB leaked"
printf '%s|%s|%s\n' "$PWD" "$BEADS_DIR" "$*" >> "$BD_LOG"

if [ "$#" -eq 6 ] && [ "$1" = list ] && [ "$2" = --ready ] && [ "$3" = --limit ] && [ "$4" = 1 ] && [ "$5" = --no-parent ] && [ "$6" = --json ]; then
  printf '[]\n'
  exit 0
fi
if [ "$#" -eq 6 ] && [ "$1" = list ] && [ "$2" = --ready ] && [ "$3" = --no-parent ] && [ "$4" = --limit ] && [ "$5" = 0 ] && [ "$6" = --json ]; then
  printf '[]\n'
  exit 0
fi
if [ "$#" -eq 9 ] && [ "$1" = list ] && [ "$2" = --no-parent ] && [ "$3" = --status ] && [ "$4" = open,in_progress,blocked,deferred ] && [ "$5" = --label-pattern ] && [ "$6" = 'wf:*' ] && [ "$7" = --limit ] && [ "$8" = 0 ] && [ "$9" = --json ]; then
  printf '[]\n'
  exit 0
fi
fail "$@"
