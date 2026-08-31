---
name: gitnexus-cli
description: Use for GitNexus index maintenance, status checks, diagnostics, repository listing, cleanup, or wiki generation.
---

# GitNexus CLI

Use the installed `gitnexus` executable directly from the repository root. Never use GitNexus MCP tools, `npx`, or `node .gitnexus/run.cjs`.

## Commands

- Check index health: `gitnexus status`
- Build or refresh without modifying agent files: `gitnexus analyze --index-only`
- Include control/data-flow analysis when required: `gitnexus analyze --index-only --pdg`
- List indexed repositories: `gitnexus list`
- Diagnose installation/runtime support: `gitnexus doctor`
- Remove the current index: `gitnexus clean`
- Generate documentation: `gitnexus wiki`
- Inspect command flags: `gitnexus <command> --help`

Run `analyze` only when the index is absent or stale. Prefer `--index-only` so GitNexus does not regenerate repository instructions or skills.
