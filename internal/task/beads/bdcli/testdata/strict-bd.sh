#!/bin/sh
set -eu

fail() {
  printf 'unsupported or malformed bd invocation: %s\n' "$*" >&2
  exit 64
}

[ "$PWD" = "$BD_EXPECT_CWD" ] || fail "wrong cwd: $PWD"
[ "${BEADS_DIR-}" = "$BD_EXPECT_BEADS_DIR" ] || fail "wrong BEADS_DIR: ${BEADS_DIR-}"
[ "${BEADS_DB+x}" != x ] || fail "ambient BEADS_DB leaked"
[ "${BD_DB+x}" != x ] || fail "ambient BD_DB leaked"

fixture=
case "$#:$1" in
  2:array)
    [ "$2" = --json ] || fail "$@"
    fixture=array.json
    ;;
  3:object)
    [ "$2" = demo-1 ] && [ "$3" = --json ] || fail "$@"
    fixture=object.json
    ;;
  2:empty)
    [ "$2" = --json ] || fail "$@"
    fixture=empty.json
    ;;
  2:stdin)
    [ "$2" = --json ] || fail "$@"
    expected=$(mktemp)
    actual=$(mktemp)
    trap 'rm -f "$expected" "$actual"' EXIT
    printf 'first line\nsecond line\n\nfinal line\n' > "$expected"
    cat > "$actual"
    cmp -s "$expected" "$actual" || fail "stdin did not round-trip"
    printf '{"stdin":"accepted"}\n'
    exit 0
    ;;
  1:fail)
    printf 'partial stdout\n'
    printf 'failure detail\n' >&2
    exit 23
    ;;
  2:malformed)
    [ "$2" = --json ] || fail "$@"
    printf 'not JSON at all\n'
    exit 0
    ;;
  2:stderr)
    [ "$2" = --json ] || fail "$@"
    printf 'informational warning\n' >&2
    fixture=object.json
    ;;
  2:info)
    [ "$2" = --json ] || fail "$@"
    printf 'informational output\n'
    fixture=object.json
    ;;
  6:list)
    if [ "$2" = --ready ] && [ "$3" = --limit ] && [ "$4" = 1 ] && [ "$5" = --no-parent ] && [ "$6" = --json ]; then
      fixture=empty.json
    elif [ "$2" = --ready ] && [ "$3" = --no-parent ] && [ "$4" = --limit ] && [ "$5" = 0 ] && [ "$6" = --json ]; then
      fixture=ready.json
    else
      fail "$@"
    fi
    ;;
  9:list)
    [ "$2" = --no-parent ] && [ "$3" = --status ] && [ "$4" = open,in_progress,blocked,deferred ] && \
      [ "$5" = --label-pattern ] && [ "$6" = 'wf:*' ] && [ "$7" = --limit ] && [ "$8" = 0 ] && [ "$9" = --json ] || fail "$@"
    fixture=claimed.json
    ;;
  7:list)
    [ "$2" = --parent ] && [ "$3" = demo-parent ] && [ "$4" = --all ] && [ "$5" = --limit ] && [ "$6" = 0 ] && [ "$7" = --json ] || fail "$@"
    fixture=children.json
    ;;
  3:show)
    [ "$2" = demo-parent ] && [ "$3" = --json ] || fail "$@"
    fixture=show.json
    ;;
  3:comments)
    [ "$2" = demo-parent ] && [ "$3" = --json ] || fail "$@"
    fixture=comments.json
    ;;
  11:create)
    [ "$2" = demo-parent:implement ] && [ "$3" = --type ] && [ "$4" = task ] && \
      [ "$5" = --parent ] && [ "$6" = demo-parent ] && [ "$7" = --no-inherit-labels ] && \
      [ "$8" = --labels ] && [ "$9" = wf:implementation ] && [ "${10}" = --stdin ] && [ "${11}" = --json ] || fail "$@"
    expected=$(mktemp)
    actual=$(mktemp)
    trap 'rm -f "$expected" "$actual"' EXIT
    printf 'mailbox description\nsecond line\n' > "$expected"
    cat > "$actual"
    cmp -s "$expected" "$actual" || fail "create stdin did not round-trip"
    fixture=created.json
    ;;
  5:update)
    if [ "$2" = demo-parent.1 ] && [ "$3" = --add-label ] && [ "$4" = wf:implementation ] && [ "$5" = --json ]; then
      fixture=updated.json
    elif [ "$2" = demo-parent.1 ] && [ "$3" = --status ] && [ "$4" = in_progress ] && [ "$5" = --json ]; then
      fixture=updated.json
    elif [ "$2" = demo-parent ] && [ "$3" = --add-label ] && [ "$4" = wf:implementation ] && [ "$5" = --json ]; then
      fixture=updated.json
    else
      fail "$@"
    fi
    ;;
  6:update)
    if [ "$2" = demo-parent.1 ] && [ "$3" = --description=- ] && [ "$4" = --add-label ] && [ "$5" = wf:implementation ] && [ "$6" = --json ]; then
      expected=$(mktemp)
      actual=$(mktemp)
      trap 'rm -f "$expected" "$actual"' EXIT
      printf 'reconciled description\nwith two lines\n' > "$expected"
      cat > "$actual"
      cmp -s "$expected" "$actual" || fail "description stdin did not round-trip"
      fixture=updated.json
    else
      fail "$@"
    fi
    ;;
  7:update)
    if [ "$2" = demo-parent ] && [ "$3" = --status ] && [ "$4" = open ] && [ "$5" = --defer ] && [ -z "$6" ] && [ "$7" = --json ]; then
      fixture=updated.json
    else
      fail "$@"
    fi
    ;;
  4:comment)
    [ "$2" = demo-parent.1 ] && [ "$3" = --stdin ] && [ "$4" = --json ] || fail "$@"
    expected=$(mktemp)
    actual=$(mktemp)
    trap 'rm -f "$expected" "$actual"' EXIT
    printf 'summary line\n\nsecond line\n' > "$expected"
    cat > "$actual"
    cmp -s "$expected" "$actual" || fail "comment stdin did not round-trip"
    fixture=commented.json
    ;;
  *)
    fail "$@"
    ;;
esac

cat "$BD_FIXTURE_DIR/$fixture"
