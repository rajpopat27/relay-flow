#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
say "git init $REPO + initial commit + copy opencode plugin"
rm -rf "$REPO"
git init -q "$REPO"
cd "$REPO"
echo "# relay-flow e2e test repo" > README.md
git add . && git commit -qm "init"
say "git --no-pager log --oneline"
git --no-pager log --oneline
beat
say "mkdir -p .opencode/plugins && cp plugin/index.ts"
mkdir -p .opencode/plugins
cp "$WORKTREE_SRC/plugin/index.ts" .opencode/plugins/relay-flow.ts
git add . && git commit -qm "add relay-flow opencode plugin"
say "ls -la .opencode/plugins/"
ls -la .opencode/plugins/
beat 2
