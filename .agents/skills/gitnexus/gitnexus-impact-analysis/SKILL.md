---
name: gitnexus-impact-analysis
description: Use before symbol edits or when asked what depends on code and what may break.
---

# Impact Analysis with GitNexus

Use the installed `gitnexus` CLI directly from the repository root.

## Before Editing

1. Run `gitnexus impact SymbolName --direction upstream --repo /absolute/repo/path`.
2. Review direct callers first, then affected processes and transitive dependants.
3. Warn the user before proceeding when risk is HIGH or CRITICAL.

Use `--file`, `--kind`, or `--uid` when a symbol is ambiguous. Use `--include-tests` when test dependencies matter.

## After Editing

- Unstaged changes: `gitnexus detect-changes --scope unstaged --repo /absolute/repo/path`
- Staged changes: `gitnexus detect-changes --scope staged --repo /absolute/repo/path`
- Branch review: `gitnexus detect-changes --scope compare --base-ref main --repo /absolute/repo/path`

Run tests for the affected processes. Never commit without staged change detection.
