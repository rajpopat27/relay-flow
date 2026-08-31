#!/bin/sh
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
# than adding session/socket flags to every Herdr command.  The HERDR_EXPECT_*
# variables are test controls inherited by this executable; they make both
# configured values and accidental ambient selector leakage observable.
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

emit_fixture() {
  fixture=$1
  fixture_dir=${HERDR_FIXTURE_DIR-}
  [ -n "$fixture_dir" ] || fail "HERDR_FIXTURE_DIR is unset"
  [ -f "$fixture_dir/$fixture" ] || fail "missing fixture: $fixture"
  cat "$fixture_dir/$fixture"
}

check_absolute_cwd() {
  cwd=$1
  case "$cwd" in
    /*) ;;
    *) fail "--cwd must be absolute: $cwd" ;;
  esac
  if [ "${HERDR_EXPECT_CWD+x}" = x ] && [ "$cwd" != "$HERDR_EXPECT_CWD" ]; then
    fail "wrong --cwd: $cwd"
  fi
}

check_value() {
  value=$1
  [ -n "$value" ] || fail "empty positional value"
  case "$value" in
    -*) fail "unexpected flag in positional value: $value" ;;
  esac
}

check_env_assignment() {
  assignment=$1
  case "$assignment" in
    *=*) ;;
    *) fail "malformed --env value: $assignment" ;;
  esac
  key=${assignment%%=*}
  case "$key" in
    ''|[0-9]*|*[!A-Za-z0-9_]* ) fail "malformed environment key: $key" ;;
  esac
}

fixture=
case "$#:${1-}:${2-}" in
  2:api:snapshot)
    fixture=snapshot.json
    ;;
  4:tab:list)
    [ "$3" = --workspace ] || fail "$@"
    check_value "$4"
    if [ "$4" = empty ]; then
      fixture=empty-tabs.json
    else
      fixture=tab-list.json
    fi
    ;;
  4:pane:list)
    [ "$3" = --workspace ] || fail "$@"
    check_value "$4"
    if [ "$4" = empty ]; then
      fixture=empty-panes.json
    else
      fixture=pane-list.json
    fi
    ;;
  3:pane:get)
    check_value "$3"
    case "$3" in
      malformed)
        fixture=malformed.json
        ;;
      stderr)
        printf 'captured warning\n' >&2
        fixture=pane-get.json
        ;;
      error)
        emit_fixture error-pane.json
        exit 23
        ;;
      *)
        fixture=pane-get.json
        ;;
    esac
    ;;
  4:pane:process-info)
    [ "$3" = --pane ] || fail "$@"
    check_value "$4"
    fixture=pane-process-info.json
    ;;
  4:pane:rename)
    check_value "$3"
    check_value "$4"
    ;;
  4:pane:run)
    # COMMAND is deliberately one logical argument.  A command containing
    # spaces or newlines must arrive here as the same argument the wrapper
    # passed to exec.CommandContext.
    check_value "$3"
    [ -n "$4" ] || fail "empty command"
    ;;
  3:pane:close)
    check_value "$3"
    ;;
  *)
    if [ "${1-}" = tab ] && [ "${2-}" = create ]; then
      [ "$#" -ge 9 ] || fail "$@"
      [ "$3" = --workspace ] || fail "$@"
      check_value "$4"
      [ "$5" = --cwd ] || fail "$@"
      check_absolute_cwd "$6"
      [ "$7" = --label ] || fail "$@"
      check_value "$8"
      [ "$9" = --no-focus ] || fail "$@"
      shift 9
      actual_envs=
      while [ "$#" -gt 0 ]; do
        [ "$1" = --env ] || fail "$@"
        [ "$#" -ge 2 ] || fail "$@"
        check_env_assignment "$2"
        if [ -n "$actual_envs" ]; then
          actual_envs="$actual_envs;$2"
        else
          actual_envs=$2
        fi
        shift 2
      done
      if [ "${HERDR_EXPECT_ENVS+x}" = x ] && [ "$actual_envs" != "$HERDR_EXPECT_ENVS" ]; then
        fail "wrong --env sequence: $actual_envs"
      fi
      fixture=tab-create.json
    else
      fail "$@"
    fi
    ;;
esac

case "$fixture" in
  '')
    # Herdr's pane mutations return success without a response envelope.
    exit 0
    ;;
  *)
    emit_fixture "$fixture"
    ;;
esac
