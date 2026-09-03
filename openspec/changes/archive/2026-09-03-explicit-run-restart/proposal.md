# Explicit restart for permanently canceled runs

## Why

A canceled relay-flow run is intentionally terminal, but the current command
surface has no way to start that ticket again without risking automatic
recreation, stale durable state, or stale agent reports affecting the new
execution. Operators need an explicit, auditable restart operation that uses
the latest workflow while preserving the ticket's work and human history.

The behavior has now been implemented and exercised end-to-end against a real
Beads workspace, Orca runner, OpenCode terminal, and durable relay-flow server.
This change records the contract and repeatable proof so future changes do not
have to rediscover the restart behavior.

## What Changes

- Add `relay-flow run restart --ticket <key>` and its Unix-socket API route.
- Keep cancellation permanent; polling, server restart, workflow replacement,
  and ticket-status changes never restart a canceled attempt automatically.
- Create a fresh numeric execution attempt from the reserved `start` node
  using the latest validated workflow snapshot.
- Preserve the worktree, code, branches, workflow labels, mailbox subtasks,
  mailbox comments, descriptions, and task history.
- Close surviving terminals for the ticket before launching the fresh attempt.
- Use a new execution/report-fencing ID while retaining a stable logical
  repo/workflow/ticket identity.
- Expose logical run ID, numeric attempt ID, lifecycle state, and actionable
  blocked-status errors through `run get` and the run projection.
- Keep a restart blocked when a human-owned parent or mailbox status is
  incompatible; never overwrite the human's status, and retry automatically
  after the status is restored.
- Implement adapter-owned restart mailbox preparation for both Jira and Beads
  without adding provider-specific status rules to core orchestration.
- Acknowledge stale reports from prior attempts safely without advancing the
  new attempt.
- Add the reproducible `/tmp/dummy-tui` Beads/Orca/OpenCode E2E record with
  commands, observed output, logs, and cleanup steps.

## Capabilities

### New Capabilities

- `explicit-run-restart`: Explicit fresh-attempt restart for permanently
  canceled ticket runs, including numeric attempt identity, status-aware
  blocking, adapter-owned mailbox preparation, and stale-report fencing.

### Modified Capabilities

None. The existing archived rewrite specifications remain historical; this
follow-up adds the explicit restart contract as a new capability.

## Impact

- `internal/run`: logical/attempt identity and restart orchestration.
- `internal/execution/goworkflows`: persisted attempt projection fields,
  fresh-attempt startup, mailbox preparation, stale-report handling, and
  actionable blocked errors.
- `internal/server` and `cmd/relay-flow`: restart API and CLI command.
- `internal/task`: optional restart-preparation boundary.
- `internal/task/jira` and `internal/task/beads`: provider-owned mailbox
  reopening and human-state conflict behavior.
- `internal/identity`: numeric attempt suffixes and logical-ID extraction.
- Existing runner, harness, and task interfaces remain replaceable; core does
  not import Jira or Beads implementations.
- No new runtime dependency is introduced.
- The related native HITL TUI plugin is already committed separately in
  `15a5ae6`; this change focuses on explicit run restart and its E2E proof.
