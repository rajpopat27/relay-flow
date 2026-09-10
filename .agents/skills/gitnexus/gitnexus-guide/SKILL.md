---
name: gitnexus-guide
description: Use when explaining GitNexus commands or choosing the correct code-intelligence workflow.
---

# GitNexus Guide

Use the installed `gitnexus` CLI directly. Do not use GitNexus MCP tools, `npx`, or Node wrappers.

## Command Selection

- `gitnexus status`: check index freshness.
- `gitnexus analyze --index-only`: create or refresh the index without generating files.
- `gitnexus query "concept"`: find related execution flows and symbols.
- `gitnexus context SymbolName`: inspect callers, callees, and participating flows.
- `gitnexus impact SymbolName --direction upstream`: find dependants before editing.
- `gitnexus impact SymbolName --direction downstream`: inspect dependencies.
- `gitnexus trace Source Target`: find the shortest call path.
- `gitnexus detect-changes --scope staged`: inspect staged change impact before committing.
- `gitnexus detect-changes --scope compare --base-ref main`: assess a branch.
- `gitnexus check --cycles`: detect circular imports.
- `gitnexus cypher '<query>'`: run a raw graph query as a last resort.
- `gitnexus list`, `doctor`, `clean`, `wiki`: maintain and inspect GitNexus itself.

When several repositories share a name, pass `--repo /absolute/repo/path`. Run `gitnexus <command> --help` for exact flags.
