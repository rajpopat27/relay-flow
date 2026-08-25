#!/usr/bin/env bash
# One-time setup: build + install relay-flow so the plugin's `relay-flow report` resolves on PATH.
source "$(dirname "$0")/lib.sh"
say "go install ./cmd/relay-flow (lands in $(go env GOPATH)/bin, on PATH)"
cd "$WORKTREE_SRC"
GOFLAGS=-buildvcs=false go install ./cmd/relay-flow
beat
say "command -v relay-flow"
command -v relay-flow
beat 2
