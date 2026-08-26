#!/usr/bin/env bash
# One-time setup: build + install relay-flow so the plugin's `relay-flow report` resolves on PATH.
source "$(dirname "$0")/lib.sh"
GOBIN=$(go env GOBIN)
[ -n "$GOBIN" ] || GOBIN="$(go env GOPATH)/bin"
say "go install ./cmd/relay-flow (lands in $GOBIN, on PATH)"
cd "$WORKTREE_SRC"
GOFLAGS=-buildvcs=false go install ./cmd/relay-flow
beat
say "command -v relay-flow"
BIN=$(command -v relay-flow) || fail "relay-flow is not on PATH"
echo "$BIN"
[ "$BIN" = "$GOBIN/relay-flow" ] || fail "PATH resolves $BIN, expected $GOBIN/relay-flow"
[ -x "$BIN" ] || fail "installed binary is not executable"
[[ "$BIN" != "$E2E_ROOT"/* ]] || fail "binary must not be copied under E2E_ROOT"
beat 2
