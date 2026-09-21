#!/usr/bin/env bash
# One-time setup: build + install relay-flow so the plugin's `relay-flow report` resolves on PATH.
source "$(dirname "$0")/lib.sh"
GOBIN=$(go env GOBIN)
[ -n "$GOBIN" ] || GOBIN="$(go env GOPATH)/bin"
say "go install ./cmd/relay-flow ./cmd/rf (lands in $GOBIN, on PATH)"
cd "$WORKTREE_SRC"
GOFLAGS=-buildvcs=false go install ./cmd/relay-flow ./cmd/rf
beat
say "command -v relay-flow and rf"
BIN=$(command -v relay-flow) || fail "relay-flow is not on PATH"
RF_BIN=$(command -v rf) || fail "rf is not on PATH"
echo "$BIN"
echo "$RF_BIN"
[ "$BIN" = "$GOBIN/relay-flow" ] || fail "PATH resolves $BIN, expected $GOBIN/relay-flow"
[ "$RF_BIN" = "$GOBIN/rf" ] || fail "PATH resolves $RF_BIN, expected $GOBIN/rf"
[ -x "$BIN" ] || fail "installed relay-flow binary is not executable"
[ -x "$RF_BIN" ] || fail "installed rf binary is not executable"
[[ "$BIN" != "$E2E_ROOT"/* ]] || fail "binary must not be copied under E2E_ROOT"
[[ "$RF_BIN" != "$E2E_ROOT"/* ]] || fail "rf binary must not be copied under E2E_ROOT"
[ "$(relay-flow version)" = "$(rf version)" ] || fail "relay-flow and rf version output differs"
beat 2
