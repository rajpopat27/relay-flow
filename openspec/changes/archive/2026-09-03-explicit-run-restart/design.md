# Design: explicit restart for permanently canceled runs

## Context

Relay-flow's deterministic run identity is derived from repository, workflow,
and ticket. `run cancel` is a permanent stop: the durable workflow cancels,
closes runner terminals while preserving the worktree, writes a stable task-
system cancellation marker, and finishes in `canceled`. Normal polling must
therefore not recreate that execution after a process restart or projection
retention.

The missing operation was an intentional fresh attempt. The operation spans the
Run Manager, durable engine, run projection, Unix-socket server, CLI, task
adapters, runner, and harness launch metadata. It must not make Jira, Beads,
Orca, or OpenCode details part of core run orchestration.

The implementation is a follow-up to the completed native TUI plugin change
(`15a5ae6`). The plugin's report `runId` is the execution-attempt identity, so
explicit restart must fence a new attempt from old plugin retries and old
OpenCode sessions.

## Goals / Non-Goals

**Goals:**

- Provide an explicit `run restart` operation for a canceled ticket.
- Start the fresh attempt at `start` using the latest validated workflow.
- Keep cancellation permanent and prevent poll-driven recreation.
- Preserve code, worktrees, branches, labels, mailboxes, comments, and
  descriptions.
- Close stale run terminals before the new attempt starts and launch a fresh
  harness session.
- Use numeric durable attempt IDs (`1`, `2`, `3`, ...) and fence report/durable
  execution IDs between attempts.
- Keep human-owned task statuses authoritative: incompatible status blocks and
  retries instead of being overwritten.
- Make blocked status and remediation visible through `run get`.
- Implement mailbox reopening behind an optional task adapter capability for
  both Jira and Beads.
- Make delayed reports from old attempts harmless and acknowledged.
- Prove the behavior with unit, integration, race, and repeatable real-tool E2E
  coverage.

**Non-Goals:**

- Automatically restarting canceled tickets during polling.
- Resuming a canceled graph cursor or reusing its OpenCode session.
- Automatically reopening Done/Closed parent tickets.
- Rolling back comments, labels, worktrees, branches, or code.
- Adding a manual pause/resume state machine.
- Teaching core Jira or Beads status names.
- Adding a runner/harness/task-system implementation to the report plugin.
- Fixing the separate deleted-terminal initial-prompt issue.
- Adding a UUID attempt ID; numeric attempts are the contract.

## Decisions

### 1. Keep a stable logical identity and allocate numeric attempts

`logicalRunID` remains the delimiter-safe deterministic value:

```text
<repo>/<workflow>/<ticket>
```

The original execution is attempt `1` and keeps the existing `runID` for
compatibility. Explicit restart allocates the next numeric `attemptID` from
persisted `relay_runs` history while holding the shared lifecycle mutex. The
execution ID is derived as:

```text
attempt 1: <logicalRunID>
attempt 2: <logicalRunID>~attempt~2
attempt 3: <logicalRunID>~attempt~3
```

`runID` is the durable go-workflows instance ID and the report transport fence.
`logicalRunID` groups attempts and anchors the permanent task-system
cancellation marker. `attemptID` is an operator-visible numeric generation.

The first-attempt ID stays deterministic so existing claim-before-run and
healthy restart behavior does not change. A repeated restart command sees the
active suffixed attempt and returns it instead of allocating another number.

**Alternative rejected:** use a random UUID for each attempt. A UUID is not
needed for a single-machine ordered restart generation and is less useful in
`run get` output. Numeric allocation is sufficient because the lifecycle gate
serializes allocation and prior attempts are persisted.

### 2. Add restart to the Run Manager, not to adapters

`RunManager.RestartByTicket` resolves the latest run, validates that it is
fully `canceled`, resolves the current registered repository and current
workflow definition, allocates the next attempt, and calls the existing
engine-neutral `Executor.EnsureRun` with a new `run.Start` snapshot.

The manager owns no Jira, Beads, runner, or harness code. It only uses the
existing repo/workflow registries and `Executor`/`RunQueries` boundaries.

Rules:

- `canceling` returns a conflict explaining that cancellation must finish.
- A completed or active original attempt returns a conflict.
- An already-active suffixed attempt is returned idempotently.
- A removed repository/workflow or workflow no longer bound to the repository
  returns a conflict.
- The latest workflow value is copied into the new `run.Start` snapshot.

**Alternative rejected:** add a Jira/Beads-specific restart command. That
would make core behavior provider-dependent and would not work for another
task system.

### 3. Let the durable engine perform fresh-attempt preparation

The engine still uses one generic `TicketWorkflow` interpreter. When
`start.AttemptID > 1`, it schedules one durable `PrepareRestart` activity after
mailbox discovery and before validation/start processing. That activity:

1. invokes the optional `task.RestartPreparer` capability to reopen relay-owned
   mailbox state;
2. closes all run-owned terminals for the ticket through `runner.Runner` while
   preserving the ticket environment/worktree; and
3. retries both external effects through the existing roll-forward retry loop.

The new run's runtime table is empty, so node launch has no prior session ID and
uses a fresh harness session. The runner's stable ticket identity lets Orca
reuse the existing ticket worktree while closing the prior node terminals.

**Alternative rejected:** have `RunManager` call Jira, Beads, or the runner
synchronously. That would bypass durable activity checkpoints and make a crash
between cleanup calls unrecoverable.

### 4. Use an optional task-system restart capability

The existing `task.System` contract remains the common parent/mailbox/query
boundary. Add the narrow optional capability:

```go
type RestartPreparer interface {
    PrepareRestart(ctx context.Context, parent TicketRef, mailboxes []Mailbox) error
}
```

Core type-asserts this capability; an adapter that does not need mailbox
reopening can be a no-op without changing core. Initial adapters implement it:

- Jira transitions existing mailboxes to `To Do`; the parent is not changed.
  An unavailable/manual transition is returned as a retryable conflict.
- Beads reconciles each mailbox to `open`, allowing only relay-owned
  `open`/`in_progress`/`closed` source states. `blocked`, `deferred`, and
  `hooked` remain human-owned conflicts. The parent is not changed.

The normal `start` `ApplyTaskConfig` operation remains responsible for parent
status compatibility. Thus a manually blocked/deferred/closed parent enters
`blocked` without being reset by restart.

**Alternative rejected:** reuse `ResetForRecovery`. Database-loss recovery is
explicitly destructive to execution state and has different parent reset
semantics; using it for a normal restart would overwrite a human parent state
in Beads.

### 5. Keep status vocabulary inside adapters and expose a core action

Jira and Beads continue to own their status names and source-state checks. The
shared durable retry loop classifies `retry.ConflictError` and adds an
adapter-neutral actionable suffix:

- start conflict: `Move ticket <key> to an allowed active start status;
  relay-flow will retry automatically`;
- node/mailbox conflict: `Restore the task-system state required for node
  <node>; relay-flow will retry automatically`.

The projection stores this full text in `last_error`; retry details retain the
latest conflict and next retry time. `run get` therefore shows `state:
blocked`, the current node, the human-readable conflict, and the retry data.
When the adapter succeeds, the retry projection is cleared and the workflow
continues automatically.

**Alternative rejected:** put Jira status names in core or automatically set a
status from `run restart`. That would overwrite human decisions and break
Beads/alternative task systems.

### 6. Fence stale reports by execution ID and logical-ID fallback

Each attempt's `runID` is sent through the existing plugin environment and
report JSON. A report for an old retained attempt is handled by the existing
canceled/non-current duplicate path.

If an old attempt projection row has been retained out while a newer attempt
with the same `logical_run_id` remains, `Engine.SubmitReport` resolves the
logical ID from the attempt suffix and returns an accepted duplicate before
validating or signaling the payload. The old report cannot advance the new
workflow.

The cancellation marker uses the stable logical ID, so normal polling remains
blocked after any canceled attempt. Explicit restart deliberately bypasses
that marker because it is an operator-authorized operation.

### 7. Expose one HTTP route and one CLI command

The server adds:

```text
POST /runs/by-ticket/{key}/restart
```

The handler calls `Deps.RestartRun` and returns the new `run.Run` in the normal
JSON envelope. The client and CLI add:

```text
relay-flow run restart --ticket <key>
```

The command prints the new run JSON, including `id`, `logicalRunId`, and
`attemptId`. Existing `run get --ticket <key>` returns the newest attempt and
its blocked/error/retry fields.

The server handler contains no graph, SQLite, Jira, Beads, runner, or harness
logic. Composition wiring supplies the Run Manager.

### 8. Preserve normal polling and recovery rules

`RunManager.EnsureRun` scans the projection for the newest active attempt and
ensures that execution ID. It does not check the cancellation marker for an
active attempt. If only a canceled/completed attempt exists, normal polling
does nothing. If the projection is missing entirely, a claimed parent is
checked against the stable logical cancellation marker before a first attempt
can be recreated.

Normal healthy-database server restarts continue the same attempt. Explicit
`serve --recover` remains a separate database-loss operation and is not changed
by this capability.

## Risks / Trade-offs

- **Projection allocation race** → serialize `max(attemptID)+1` under the
  shared lifecycle gate; the engine inserts the new attempt before creating its
  workflow instance, so a crash is repaired by later `EnsureRun`.
- **Older projection schema** → add nullable logical/attempt columns and
  backfill existing rows as logical ID + attempt 1. No task-system migration is
  performed.
- **Status changes during restart preparation** → adapters read/reconcile
  through their existing status policies; conflicts block and retry, and the
  parent is never blindly reset.
- **Duplicate external cleanup after an ambiguous response** → runner and
  task operations remain idempotent; rare duplicate close attempts are
  accepted.
- **Old plugin retries after a new attempt** → old `runID` remains fenced;
  logical-ID fallback acknowledges stale reports only as duplicates and never
  signals the new instance.
- **OpenCode launch race** → the new attempt has no persisted session ID, so
  the harness requests a fresh launch. The separate missing-initial-prompt
  issue remains explicitly out of scope.
- **Alternative task adapter without restart preparation** → the capability is
  optional; core still closes runner terminals and executes the fresh run, while
  adapters that need mailbox reopening implement the capability.
- **A long-lived blocked run can hide an operator action** → `run get` exposes
  state, conflict, next retry, and the exact status remediation.

## Migration Plan

1. Deploy the binary containing the new projection columns and restart route.
2. On first engine open, `relay_runs` migration backfills old rows as attempt 1
   and logical ID equal to the old deterministic ID.
3. Existing canceled runs remain canceled and their stable cancellation
   markers remain authoritative.
4. To retry one, run:

   ```sh
   relay-flow run restart --ticket <key>
   ```

5. Inspect the returned run and then:

   ```sh
   relay-flow run get --ticket <key>
   ```

6. If status is blocked, restore the task to an adapter-allowed active start
   status. The durable retry continues automatically.
7. Rollback is operational: stop the server, preserve/backup
   `~/.relay-flow`, and restore the prior binary. No external task/runner work
   is automatically reversed.

The implementation was verified in a disposable Beads workspace using the
real `bd` CLI, Orca worktree/terminal service, OpenCode launch path, and the
current relay-flow binary. The exact record is in `e2e-verification.md`.

## Open Questions

None for this capability. The deleted-terminal prompt issue and any future
manual pause/resume operation remain separate changes.
