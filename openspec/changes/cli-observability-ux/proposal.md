## Why

The current relay-flow CLI exposes useful workflow and run data, but its human output is either a bare name/tabular row or raw JSON. A single `run get` does not provide the at-a-glance execution view operators need to understand status, timing, progress, retries, and the executed path. The CLI also lacks scoped command help and a version command, making discovery and troubleshooting harder.

This change gives relay-flow an operator-facing inspection experience modeled on the structure of `argo get`: metadata first, conditions and timing next, then an aligned tree of executed steps. It also makes workflow/run listing useful while preserving stable machine-readable output for scripts.

## What Changes

- Add an Argo-shaped human-readable `relay-flow run get --ticket <key>` view:
  - flat run metadata;
  - conditions;
  - created/started/finished timestamps;
  - duration, progress, and resource-duration summary;
  - an aligned step tree with status marks, duration, message, and runner runtime columns.
- Record/query a derived per-run step timeline so the CLI renders the actual executed path, including branches, loops, retries, and failures without inferring history from the static graph.
- Improve `workflow list` and `run list` with aligned human tables, status summaries, filters, empty states, retry/error summaries, and actionable next-command hints.
- Improve `workflow get` with a readable workflow metadata view and static graph/route tree. Static workflow nodes are shown as configured/valid; execution marks remain the responsibility of `run get`.
- Define consistent terminal status marks, color behavior, plain-output behavior, and loading spinners. Spinners go to stderr and never corrupt JSON/stdout output.
- Add stable `--json` output for enhanced list/get queries and retain non-interactive/script-safe output.
- Add command-scoped help so `relay-flow workflow --help`, `relay-flow workflow list --help`, `relay-flow run --help`, and nested commands print only their own usage and options instead of the entire CLI surface.
- Add `relay-flow version`, `relay-flow --version`, and `relay-flow -v` with release, commit, build, Go-version, and platform information.
- Keep command exit-code semantics and the existing Unix-socket server boundary; add only the read-only query data needed for inspection.

**BREAKING** Human-readable TTY output for `workflow get` and `run get` replaces the current raw-JSON default. `--json` remains the explicit stable interface for scripts and automation.

## Capabilities

### New Capabilities

- `cli-observability`: Human-readable workflow/run listing and inspection, Argo-shaped run details, status rendering, step timelines, loading/empty/error states, scoped help, and version reporting.

### Modified Capabilities

- `workflow-repo-management`: Extend the grouped CLI command surface with version output and scoped help while preserving the existing workflow/run/repository commands and server API boundary.

## Impact

Affected areas include `cmd/relay-flow/main.go` and CLI rendering/helpers, `internal/server` routes and client methods for read-only detail queries, the run/projection query boundary, and both durable-executor projection update paths. The derived step timeline adds a relay-owned read model; it is never execution authority and is not read by workflow progression.

The change adds no external dependency, no runner/task-system behavior, no workflow YAML fields, no report-wire fields, no event bus, and no direct CLI access to SQLite or durable-engine internals. Existing JSON consumers can opt into the explicit `--json` contract, while TTY output is optimized for operators.
