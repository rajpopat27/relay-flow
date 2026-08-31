---
name: gitnexus-exploring
description: Use to understand architecture, execution flows, callers, callees, or unfamiliar code.
---

# Exploring with GitNexus

Use the installed `gitnexus` CLI directly from the repository root.

## Workflow

1. Check freshness: `gitnexus status`.
2. If stale: `gitnexus analyze --index-only`.
3. Find relevant flows: `gitnexus query "concept" --repo /absolute/repo/path`.
4. Inspect key symbols: `gitnexus context SymbolName --repo /absolute/repo/path`.
5. Trace relationships when needed: `gitnexus trace SourceSymbol TargetSymbol --repo /absolute/repo/path`.
6. Read the returned source files for implementation details.

Use `--content` with `query` or `context` only when source snippets are useful. Pass `--file` or `--uid` to disambiguate common symbol names.
