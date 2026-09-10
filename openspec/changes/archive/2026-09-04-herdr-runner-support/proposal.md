# Add Herdr as a runner plugin

## Why

Relay-flow currently has an extensible runner registry but only ships an Orca runner. Herdr provides a server-owned terminal runtime that also manages Git worktrees natively, so users should be able to select Herdr and get the same per-ticket isolation Orca provides without changing task-system, harness, routing, or durable-execution code.

Herdr's public runtime identity differs from Orca's: `terminal_id` is regenerated after a Herdr restart, while the public `pane_id` is the durable automation handle. The integration therefore needs an adapter that follows the real Herdr CLI contract and tests its command shapes against strict fakes whose fixtures are captured from the actual CLI.

## What Changes

- Add a statically registered `herdr` runner plugin beside the existing `orca` runner.
- Implement a Herdr CLI wrapper under `internal/runner/herdr/herdrcli` using only documented `herdr` commands and JSON responses.
- Map each ticket to its own Herdr-managed Git worktree workspace, created from the repository's origin branch and named by the ticket key.
- Run each node of a ticket in a Herdr tab/root pane labelled exactly `<ticket>:<node>` inside that ticket's worktree.
- Persist Herdr's public `pane_id` as the runner terminal handle; never persist the ephemeral `terminal_id` or a Herdr workspace ID.
- Reconcile pane liveness through `pane get` and `pane process-info`, and use tab/pane labels to recover from lost creation acknowledgements.
- Close ticket-owned panes and the ticket workspace during cancellation, explicit recovery, and runner cleanup while preserving the worktree, its branch, and its files.
- Map Herdr's documented error codes to typed errors so an absent resource is distinguished from a transport failure and cleanup rolls forward.
- Treat Herdr's lack of a workspace-status API as a runner-local no-op for the existing `SetEnvironmentStatus` method.
- Add a live Herdr CLI preflight, captured response fixtures, and a live test that drives the production wrapper against the installed Herdr binary.
- Add strict fake-`herdr` executable tests that reject unsupported commands, flags, argument ordering, relative `--cwd` values, and ambient environment selection.
- Add only the minimum composition-root blank-import wiring required for the plugin to appear in runner selection; all adapter code, CLI wrappers, and adapter tests remain under `internal/runner`.
- **BREAKING** Clarify the runner contract so runner-owned environment identity may be a ticket worktree (Orca) or a ticket worktree workspace (Herdr), while cleanup remains scoped to runner-owned resources for the individual run.

## Capabilities

### New Capabilities

- `herdr-runner`: Run relay-flow harness commands in per-ticket Herdr worktree workspaces and labelled panes through the real Herdr CLI.

### Modified Capabilities

- `integration-contracts`: Extend runner environment ownership and terminal-handle semantics for Herdr worktree workspaces and public pane IDs; generalize runner-derived repository candidates without changing repo registration behavior.

## Impact

The implementation adds `internal/runner/herdr` and `internal/runner/herdr/herdrcli`, including strict CLI-contract fixtures and adapter tests. `cmd/relay-flow/main.go` and `cmd/relay-flow/serve.go` receive only static blank imports so the existing name-based registry can construct `herdr`.

The durable executor, task system, harness, workflow graph, report transport, SQLite schema, and runner interface remain unchanged. No Herdr Go dependency, raw Herdr socket client, direct Herdr storage access, or Herdr source-tree change is introduced. Runtime operation requires only an installed, reachable Herdr CLI/server: repositories are registered by path, and ticket worktrees and workspaces are created on first use.
