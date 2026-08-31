---
name: gitnexus-debugging
description: Use when tracing an error, unexpected behavior, regression, or call path.
---

# Debugging with GitNexus

Use the installed `gitnexus` CLI directly from the repository root.

## Workflow

1. Check freshness with `gitnexus status`; if stale, run `gitnexus analyze --index-only`.
2. Find related flows: `gitnexus query "error or symptom" --repo /absolute/repo/path`.
3. Inspect a suspect: `gitnexus context SymbolName --repo /absolute/repo/path`.
4. Trace a call path: `gitnexus trace SourceSymbol TargetSymbol --repo /absolute/repo/path`.
5. Read the identified source and tests to confirm the root cause.

Use `gitnexus cypher '<query>' --repo /absolute/repo/path` only when `query`, `context`, and `trace` cannot answer the question. For statement-level control/data flow, re-index with `--pdg` and use `gitnexus impact SymbolName --mode pdg --line <line>`.
