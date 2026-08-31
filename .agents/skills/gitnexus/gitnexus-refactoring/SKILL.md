---
name: gitnexus-refactoring
description: Use for safe renames, extraction, moves, splits, or structural code changes.
---

# Refactoring with GitNexus

Use the installed `gitnexus` CLI directly from the repository root.

## Workflow

1. Map dependants: `gitnexus impact SymbolName --direction upstream --repo /absolute/repo/path`.
2. Inspect references and dependencies: `gitnexus context SymbolName --repo /absolute/repo/path`.
3. Find participating flows: `gitnexus query "SymbolName responsibility" --repo /absolute/repo/path`.
4. Make scoped edits in dependency order: contracts, implementations, callers, tests.
5. Verify scope: `gitnexus detect-changes --scope unstaged --repo /absolute/repo/path`.
6. Run tests for affected flows.

The installed CLI has no rename command. Never use broad find-and-replace; use `context` and `impact`, inspect every reference, then edit deliberately.
