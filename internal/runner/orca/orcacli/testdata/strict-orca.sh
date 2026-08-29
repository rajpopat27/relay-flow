#!/bin/sh
set -eu

fixture=
if [ "$#" -eq 3 ] && [ "$1" = repo ] && [ "$2" = list ] && [ "$3" = --json ]; then
  fixture=repo-list.json
elif [ "$#" -eq 3 ] && [ "$1" = worktree ] && [ "$2" = list ] && [ "$3" = --json ]; then
  fixture=worktree-list.json
elif [ "$#" -eq 11 ] && [ "$1" = worktree ] && [ "$2" = create ] && [ "$3" = --name ] && [ "$4" = PAY-101 ] && [ "$5" = --repo ] && [ "$6" = id:repo-1 ] && [ "$7" = --parent-worktree ] && [ "$8" = worktree:wt-main ] && [ "$9" = --base-branch ] && [ "${10}" = origin/alice/PAY-101 ] && [ "${11}" = --json ]; then
  fixture=worktree-create.json
elif [ "$#" -eq 7 ] && [ "$1" = worktree ] && [ "$2" = set ] && [ "$3" = --worktree ] && [ "$4" = id:wt-PAY-101 ] && [ "$5" = --workspace-status ] && [ "$6" = in-review ] && [ "$7" = --json ]; then
  fixture=worktree-create.json
elif [ "$#" -eq 5 ] && [ "$1" = worktree ] && [ "$2" = rm ] && [ "$3" = --worktree ] && [ "$4" = id:wt-PAY-101 ] && [ "$5" = --json ]; then
  fixture=worktree-remove.json
elif [ "$#" -eq 6 ] && [ "$1" = terminal ] && [ "$2" = list ] && [ "$3" = --worktree ] && [ "$4" = id:wt-PAY-101 ] && [ "$5" = --include-visual-layouts ] && [ "$6" = --json ]; then
  fixture=terminal-list.json
elif [ "$#" -eq 5 ] && [ "$1" = terminal ] && [ "$2" = show ] && [ "$3" = --terminal ] && [ "$4" = term-1 ] && [ "$5" = --json ]; then
  fixture=terminal-show.json
elif [ "$#" -eq 8 ] && [ "$1" = terminal ] && [ "$2" = send ] && [ "$3" = --terminal ] && [ "$4" = term-1 ] && [ "$5" = --text ] && [ "$6" = 'hello' ] && [ "$7" = --enter ] && [ "$8" = --json ]; then
  fixture=terminal-send.json
elif [ "$#" -eq 9 ] && [ "$1" = terminal ] && [ "$2" = create ] && [ "$3" = --worktree ] && [ "$4" = name:PAY-101 ] && [ "$5" = --title ] && [ "$6" = PAY-101:implement ] && [ "$7" = --command ] && [ "$8" = 'echo hello' ] && [ "$9" = --json ]; then
  fixture=terminal-create.json
elif [ "$#" -eq 5 ] && [ "$1" = terminal ] && [ "$2" = close ] && [ "$3" = --terminal ] && [ "$4" = term-1 ] && [ "$5" = --json ]; then
  fixture=terminal-close.json
else
  printf 'unsupported or malformed orca invocation:' >&2
  printf ' <%s>' "$@" >&2
  printf '\n' >&2
  exit 64
fi

cat "$ORCA_FIXTURE_DIR/$fixture"
