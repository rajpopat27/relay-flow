# Add Herdr as a runner plugin

## Why

Relay-flow currently has an extensible runner registry but only ships an Orca runner. Herdr provides a simpler server-owned terminal runtime in which a repository can map to one persistent workspace and workflow nodes can run in labelled panes, so users should be able to select Herdr without changing task-system, harness, routing, or durable-execution code.

Herdr's public runtime identity differs from Orca's: `terminal_id` is regenerated after a Herdr restart, while the public `pane_id` is the durable automation handle. The integration therefore needs an adapter that follows the real Herdr CLI contract and tests its command shapes against strict mocks derived from the actual CLI.

## What Changes

- Add a statically registered `herdr` runner plugin beside the existing `orca` runner.
- Implement a Herdr CLI wrapper under `internal/runner/herdr/herdrclicli` using only documented `herdr` commands and JSON responses.
- Map one registered repository to one Herdr workspace, with workspace lookup based on the repository path and workspace label.
- Run each ticket/node in a Herdr tab/root pane labelled exactly `<ticket>:<node>`.
- Persist Herdr's public `pane_id` as the runner terminal handle; never persist the ephemeral `terminal_id`.
- Reconcile pane liveness through `pane get` and `pane process-info`, and use snapshot/pane labels to recover from missing or ambiguous creation acknowledgements.
- Close ticket-owned panes during cancellation, explicit recovery, and runner cleanup while preserving the shared repository workspace.
- Treat Herdr's lack of a workspace-status API as a runner-local no-op for the existing `SetEnvironmentStatus` method.
- Add a live Herdr CLI preflight and captured response fixtures before adapter implementation.
- Add strict fake-`herdr` executable tests that reject unsupported commands, flags, argument ordering, absolute `--cwd` values, and environment selection.
- Add only the minimum composition-root blank-import wiring required for the plugin to appear in runner selection; all adapter code, CLI wrappers, and adapter tests remain under `internal/runner`.
- **BREAKING** Clarify the runner contract so a runner may own an environment per ticket (Orca) or share one environment per repository (Herdr), while cleanup remains scoped to runner-owned resources for the individual run.

## Capabilities

### New Capabilities

- `herdr-runner`: Run relay-flow harness commands in Herdr workspaces and panes through the real Herdr CLI.

### Modified Capabilities

- `integration-contracts`: Extend runner environment ownership and terminal-handle semantics for repository-scoped Herdr workspaces and public pane IDs; generalize runner-derived repository candidates without changing repo registration behavior.

## Impact

The implementation adds `internal/runner/herdr` and `internal/runner/herdr/herdrclicli`, including strict CLI-contract fixtures and adapter tests. `cmd/relay-flow/main.go` and `cmd/relay-flow/serve.go` receive only static blank imports so the existing name-based registry can construct `herdr`.

The durable executor, task system, harness, workflow graph, report transport, SQLite schema, and runner interface remain unchanged. No Herdr Go dependency, raw Herdr socket client, direct Herdr storage access, or Herdr source-tree change is introduced. Runtime operation requires an installed, reachable Herdr CLI/server and an existing unambiguous workspace both when the repo is registered and when the server starts; the adapter does not provision missing workspaces.
