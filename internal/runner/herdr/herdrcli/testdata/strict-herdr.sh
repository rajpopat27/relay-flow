#!/bin/sh
# Strict fake `herdr`. It accepts only the exact production command shapes and
# reproduces the observed transport contract of herdr 0.8.2:
#   success -> result envelope on stdout, exit 0 (pane run prints nothing)
#   failure -> error envelope on stderr, exit 1, empty stdout
set -eu

fail() {
  printf 'unsupported or malformed herdr invocation:' >&2
  for arg in "$@"; do
    printf ' <%s>' "$arg" >&2
  done
  printf '\n' >&2
  exit 64
}

# The wrapper supplies selector configuration through the environment rather
# than adding session/socket flags to every Herdr command.
check_selector() {
  actual_name=$1
  expected_name=$2
  eval "expected_set=\${${expected_name}+x}"
  if [ "$expected_set" = x ]; then
    eval "expected=\${${expected_name}-}"
    eval "actual=\${${actual_name}-}"
    [ "$actual" = "$expected" ] || fail "wrong $actual_name: $actual"
  else
    eval "actual_set=\${${actual_name}+x}"
    [ "$actual_set" != x ] || fail "ambient $actual_name leaked"
  fi
}

check_selector HERDR_SESSION HERDR_EXPECT_SESSION
check_selector HERDR_SOCKET_PATH HERDR_EXPECT_SOCKET_PATH

fixture_path() {
  fixture_dir=${HERDR_FIXTURE_DIR-}
  [ -n "$fixture_dir" ] || fail "HERDR_FIXTURE_DIR is unset"
  [ -f "$fixture_dir/$1" ] || fail "missing fixture: $1"
  printf '%s' "$fixture_dir/$1"
}

emit_result() { cat "$(fixture_path "$1")"; exit 0; }

# Herdr writes error envelopes to stderr and exits 1.
emit_error() { cat "$(fixture_path "$1")" >&2; exit 1; }

check_absolute_cwd() {
  case "$1" in
    /*) ;;
    *) fail "--cwd must be absolute: $1" ;;
  esac
}

check_value() {
  [ -n "$1" ] || fail "empty positional value"
  case "$1" in
    -*) fail "unexpected flag in positional value: $1" ;;
  esac
}

case "$#:${1-}:${2-}" in
  2:api:snapshot)
    emit_result snapshot.json
    ;;
  4:worktree:list)
    [ "$3" = --cwd ] || fail "$@"
    check_absolute_cwd "$4"
    case "$4" in
      *not-a-repo) emit_error error-not-git-worktree.json ;;
      *) emit_result worktree-list.json ;;
    esac
    ;;
  11:worktree:create)
    [ "$3" = --cwd ] || fail "$@"
    check_absolute_cwd "$4"
    [ "$5" = --branch ] || fail "$@"
    check_value "$6"
    [ "$7" = --base ] || fail "$@"
    check_value "$8"
    [ "$9" = --label ] || fail "$@"
    check_value "${10}"
    [ "${11}" = --no-focus ] || fail "$@"
    emit_result worktree-create.json
    ;;
  9:worktree:open)
    [ "$3" = --cwd ] || fail "$@"
    check_absolute_cwd "$4"
    [ "$5" = --branch ] || fail "$@"
    check_value "$6"
    [ "$7" = --label ] || fail "$@"
    check_value "$8"
    [ "$9" = --no-focus ] || fail "$@"
    case "$6" in
      MISSING-*) emit_error error-worktree-not-found.json ;;
    esac
    emit_result worktree-open.json
    ;;
  9:tab:create)
    [ "$3" = --workspace ] || fail "$@"
    check_value "$4"
    [ "$5" = --cwd ] || fail "$@"
    check_absolute_cwd "$6"
    [ "$7" = --label ] || fail "$@"
    check_value "$8"
    [ "$9" = --no-focus ] || fail "$@"
    emit_result tab-create.json
    ;;
  4:tab:list)
    [ "$3" = --workspace ] || fail "$@"
    check_value "$4"
    case "$4" in
      empty) emit_result empty-tabs.json ;;
      missing) emit_error error-workspace-not-found.json ;;
      *) emit_result tab-list.json ;;
    esac
    ;;
  4:pane:list)
    [ "$3" = --workspace ] || fail "$@"
    check_value "$4"
    case "$4" in
      empty) emit_result empty-panes.json ;;
      missing) emit_error error-workspace-not-found.json ;;
      *) emit_result pane-list.json ;;
    esac
    ;;
  3:pane:get)
    check_value "$3"
    case "$3" in
      malformed) emit_result malformed.json ;;
      warning) printf 'captured warning\n' >&2; emit_result pane-get.json ;;
      missing) emit_error error-pane-not-found.json ;;
      *) emit_result pane-get.json ;;
    esac
    ;;
  4:pane:process-info)
    [ "$3" = --pane ] || fail "$@"
    check_value "$4"
    case "$4" in
      shell) emit_result pane-process-info-shell.json ;;
      missing) emit_error error-pane-not-found.json ;;
      *) emit_result pane-process-info.json ;;
    esac
    ;;
  4:pane:rename)
    check_value "$3"
    check_value "$4"
    emit_result pane-rename.json
    ;;
  4:pane:run)
    # COMMAND is deliberately one logical argument: a command containing
    # spaces or newlines must arrive as the same argument the wrapper passed
    # to exec.CommandContext. Herdr prints nothing on success.
    check_value "$3"
    [ -n "$4" ] || fail "empty command"
    exit 0
    ;;
  3:pane:close)
    check_value "$3"
    case "$3" in
      missing) emit_error error-pane-not-found.json ;;
      *) emit_result pane-close.json ;;
    esac
    ;;
  3:workspace:close)
    check_value "$3"
    case "$3" in
      missing) emit_error error-workspace-not-found.json ;;
      *) emit_result workspace-close.json ;;
    esac
    ;;
  *)
    fail "$@"
    ;;
esac
