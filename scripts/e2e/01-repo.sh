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
say "install plugin: entry in plugins/, core in .opencode/lib/ (plugins/ dir loads every .ts as a plugin)"
mkdir -p .opencode/plugins .opencode/lib
cp "$WORKTREE_SRC/plugin/index.ts" .opencode/lib/relay-flow-core.ts
sed 's|"./index"|"../lib/relay-flow-core"|g' "$WORKTREE_SRC/plugin/relay-flow.ts" > .opencode/plugins/relay-flow.ts
git add . && git commit -qm "add relay-flow opencode plugin"
say "ls .opencode/plugins/ .opencode/lib/"
ls -la .opencode/plugins/ .opencode/lib/
beat 2
