#!/usr/bin/env bash
# One-time setup: build + install relay-flow so the plugin's `relay-flow report` resolves on PATH.
source "$(dirname "$0")/lib.sh"
say "go install ./cmd/relay-flow (lands in $(go env GOPATH)/bin, on PATH)"
cd "$WORKTREE_SRC"
GOFLAGS=-buildvcs=false go install ./cmd/relay-flow
beat
say "which relay-flow && relay-flow --help"
which relay-flow
relay-flow --help 2>&1 | head -3
beat
say "keep e2e binary copy in sync at $BIN"
cp "$(which relay-flow)" "$BIN"
beat 2
