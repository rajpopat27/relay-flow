# Relay-flow Fix Plan

Status: plugin implementation complete; cancellation/restart implementation is pending.

This document records the agreed behavior for the three reported issues. It is an implementation plan, not a replacement for the normative workflow and integration contracts. Before coding, the relevant OpenSpec/design artifacts and tests must be updated so the new behavior is explicit.

## Scope

The fixes cover:

1. Restarting a permanently canceled ticket through an explicit new attempt.
2. The missing initial prompt after a deleted terminal (deferred for now; no behavior change in this phase).
3. Replacing Question-tool approval for relay-flow HITL reports with a direct OpenCode TUI approval dialog.

The cancellation/restart implementation has not started yet. The plugin implementation is now in place, and the standalone UI proof of concept remains in `/tmp/dummy-tui`.

The initial TUI research has already been validated in that demo, so the production implementation does not need to start from zero. The dummy plugin already:

- waits for a completed assistant response/session-idle event;
- checks the assistant text against an exact `Hello` value as a simplified stand-in for report parsing;
- sends a correction prompt when the response is not exactly `Hello`;
- opens a native OpenCode TUI approval dialog only after an exact match;
- provides `Approve` and `Reject` choices; and
- logs the approved or rejected result to `/tmp/dummy-tui/report-processed.log`.

The demo was exercised in an Orca-managed OpenCode terminal: `Say Hello` produced the exact `Hello` response, opened the approval UI, and an `Approve` selection produced a `report-processed` log entry. This is only a visual/behavioral proof of concept; it does not call relay-flow or implement the final report protocol.

---

## 1. Permanent cancellation with explicit restart

### 1.1 Agreed semantics

`run cancel` remains a permanent cancellation of the current execution attempt. Normal polling must not recreate that canceled attempt, even if its SQLite projection is later removed by retention.

Restart is a separate explicit operation:

```text
relay-flow run restart --ticket <ticket-key>
```

A canceled ticket is never restarted merely because:

- the server restarts;
- the ticket is seen by a poller again;
- the workflow file is resubmitted; or
- the ticket status changes.

The explicit restart command is the operator's intent to run the ticket again.

### 1.2 Restart starts a new attempt from `start`

A restart does not resume the canceled graph cursor. It creates a fresh workflow attempt using the latest validated workflow definition and starts at the reserved `start` node.

The restart preserves task-system and runner artifacts:

- existing worktree and code;
- workflow claim labels;
- mailbox subtasks;
- mailbox descriptions;
- mailbox comments and previous summaries/feedback;
- branches and other preserved workspace artifacts.

It must not delete or rewrite prior comments to make the ticket appear unused. The new attempt can read the preserved mailbox history and code, but its durable graph progress is new.

The default recommendation is to launch fresh OpenCode sessions for the new attempt. Previous conversations remain available through the preserved task-system history if needed, while stale instructions from the canceled attempt cannot control the new attempt.

### 1.3 Logical run identity versus execution-attempt identity

The current deterministic identity is:

```text
logicalRunID = repo/workflow/ticket
```

A restart must not reuse the canceled execution identity. Reusing it would allow an old plugin or delayed report to be mistaken for a report from the new execution because report transport does not expose the internal node visit ID.

The implementation therefore needs two concepts:

```text
logicalRunID = stable repo/workflow/ticket identity
attemptID    = unique execution generation
executionID  = identity used by the durable engine and plugin transport
```

The old canceled attempt remains queryable/auditable. The new attempt receives a new execution identity in its harness environment and report payload. Node visit IDs are also freshly generated.

The exact persisted field names and API shape must be added to the run contract before implementation. The important invariant is that old and new attempts cannot share report or cancellation fencing identities.

### 1.4 Restart sequence

The intended durable sequence is:

1. Resolve the latest run for the ticket.
2. Require the previous attempt to be fully `canceled`; reject while it is still `canceling`.
3. Load the current workflow definition and validate it.
4. Read the current parent ticket state directly through the task-system adapter.
5. Create a durable restart intent/attempt identity before starting external work, so a crash between the command and run creation can be recovered idempotently.
6. Verify the ticket is still eligible for active processing.
7. Reconcile/reopen mailbox task states through an adapter-owned operation while preserving all mailbox comments and labels.
8. Close any surviving terminals for the old attempt while preserving the worktree/code.
9. Create the new durable execution with the latest workflow snapshot.
10. Process the new attempt from `start`.
11. Ensure the start task configuration, runner environment, mailboxes, and new node runtime are applied idempotently.

The new attempt must not remove the old cancellation marker. That marker belongs to the old execution. The explicit restart intent/attempt marker must be distinguishable from cancellation and must be durable in the task system or other agreed recovery record.

### 1.5 Human task-status changes

The task system owns status vocabulary and transition semantics. Core must not hardcode Jira, Beads, or another adapter's status names.

When the new restart attempt checks the parent ticket:

| Ticket state | Expected behavior |
|---|---|
| Compatible active state | Start processing and apply the configured start task configuration. |
| Human-owned incompatible state, such as Blocked or Deferred | Mark the new attempt `blocked`; do not overwrite the human state. Retry reconciliation with shared backoff. |
| Done/Closed/completed state | Remain blocked or reject restart with an actionable error; do not automatically reopen the ticket. |
| Status restored to a compatible active state | The retry succeeds automatically and the attempt continues from `start`. |
| Ticket no longer eligible for normal polling | Explicit restart may inspect it directly, but it must still pass the adapter's active-state validation. |

A task-system conflict is not a graph route and does not become a success/failure report. The attempt remains blocked until the external state becomes compatible or the operator cancels it.

### 1.6 `run get` visibility

`run get` must make a blocked restart actionable. At minimum it must expose:

```text
State: blocked
Current node: start
Last error: Human-owned ticket status "Blocked" conflicts with the workflow start transition.
Action: Move the ticket to an allowed active start status. Relay-flow will retry automatically.
Retry: <next retry time/status when available>
```

The exact status names and reason come from the task-system adapter. The projection must persist the conflict in the existing run error/retry fields, or the run contract must add a structured blocked reason if that is needed by the CLI/API.

When the status becomes compatible:

- the attempt leaves `blocked` automatically;
- the conflict error is cleared or replaced by the next current error;
- execution continues from `start`; and
- no manual resume command is required.

### 1.7 Cancellation/restart idempotency and fencing

The implementation must preserve these invariants:

- normal polling sees the old cancellation marker and does not recreate the old attempt;
- repeating `run restart` does not create two active attempts;
- a crash after restart intent but before execution creation is repaired exactly once;
- old assistant reports, approvals, and retry loops cannot advance the new attempt;
- only one active attempt may exist for the ticket;
- old runner terminals are closed before the new attempt starts;
- no rollback or deletion of human/agent work is performed.

### 1.8 Required tests

Add behavior tests for:

- restart is rejected while the previous attempt is `canceling`;
- restart uses the latest workflow snapshot;
- restart starts at `start`, not the previous node;
- worktree, mailboxes, comments, labels, and code are preserved;
- new attempt identity differs from the canceled execution identity;
- stale reports from the old attempt are harmless;
- repeated restart is idempotent;
- incompatible human ticket status produces `blocked` state;
- `run get` contains the actionable status-remediation message;
- blocked restart retries automatically with backoff;
- restoring a compatible status unblocks and continues from `start`;
- Done/Closed is not automatically reopened;
- cancellation markers remain permanent for the canceled attempt.

---

## 2. Deleted terminal and missing prompt

This issue is intentionally deferred. No mini UI change and no OpenCode resume-route patch will be implemented in this phase.

The current behavior is understood:

- relay-flow detects the missing terminal and reconciles the same node visit;
- the Go harness builds an initial prompt and passes it with `--prompt`;
- OpenCode's resumed full-TUI session route does not automatically submit that command-line prompt;
- the replacement terminal therefore starts without a new prompt.

The chosen temporary decision is to leave this behavior unchanged. Any future fix must be separately approved and tested against the real OpenCode resumed-session route. The existing relay-flow fake tests are not sufficient because they only prove that Go constructed the command, not that OpenCode submitted the prompt.

---

## 3. HITL report approval through a direct TUI dialog

### 3.1 Agreed semantics

Implementation status: the plugin now has separate server and TUI entrypoints, and the OpenCode harness configures both. The server entrypoint owns agent reports; the TUI entrypoint owns HITL report approval. The behavior is covered by `plugin/tui.test.ts` and the existing transport/parser tests.

For relay-flow HITL nodes, the Question tool must not be used for report approval.

The runtime flow becomes:

1. OpenCode produces an assistant message.
2. The plugin waits for the completed assistant turn/session-idle event.
3. The plugin parses the complete report contract.
4. Invalid or missing HITL output remains silent.
5. A valid report opens a direct TUI approval dialog.
6. `Approve` delivers the exact parsed report.
7. `Reject` discards authorization and sends no report.
8. A later report requires a new approval.

The OpenCode Question tool may remain available for unrelated agent work unless a separate decision removes it globally. Only relay-flow report approval is being changed here.

### 3.2 Approval must be tied to the exact report

The pending approval must be bound to:

```text
sessionID
assistantMessageID
reportID
runID
node
```

The plugin must not approve a different or later assistant message because an earlier message was approved.

The plugin must enforce:

- exact two choices: `Approve` and `Reject`;
- no assistant-generated approval result;
- no fuzzy text matching such as accepting arbitrary text containing "approve";
- no report delivery before the user selects `Approve`;
- no workflow transition on `Reject`;
- exact parsed JSON bytes for delivery retries.

### 3.3 TUI implementation boundary

A server/runtime plugin cannot reliably write a blocking human prompt directly into the OpenCode TUI. `console.log` is not a chat/UI protocol, and `client.session.prompt` would start another model turn.

The implementation should use a TUI plugin or an OpenCode TUI integration with:

- `ui.DialogSelect` or an equivalent native dialog;
- TUI event subscriptions;
- access to the completed assistant message;
- a direct callback for `Approve`/`Reject`;
- the existing relay-flow report transport for approved reports.

Because OpenCode separates server and TUI plugin entrypoints, the published plugin may need separate server and TUI exports. The server and TUI components must not both deliver the same report.

A practical split is:

- server plugin: session registration/title pinning and agent-node report behavior;
- TUI plugin: HITL report parsing, approval dialog, approval/rejection state, and approved report delivery.

The final ownership split must be explicit so one assistant message cannot be processed twice.

### 3.4 Invalid-output behavior

For the actual relay-flow plugin:

- agent node + invalid output: retain the current fixed report-format correction behavior;
- HITL node + invalid/missing output: remain silent;
- HITL node + valid output: show the approval dialog without an extra LLM approval turn;
- approved report rejected by relay-flow validation: show an actionable correction/review state and require a new valid report/approval;
- rejected approval: clear authorization and do not submit a failure report automatically.

The dummy demo in `/tmp/dummy-tui` uses exact `Hello` matching as a simplified stand-in for `parseReport(...).ok`. The real implementation must replace that predicate with the complete report parser and current run/node metadata.

### 3.5 Approval persistence and lifecycle

A TUI process restart must not accidentally approve a stale report. The plugin should be able to reread the latest completed assistant message and recreate the approval dialog when appropriate, while preserving report identity.

The design must decide how a rejected message is remembered across a TUI restart. Options include:

- persist the rejected assistant message ID in TUI-local plugin state; or
- only show the approval again after a new assistant message is produced.

A canceled run must clear/ignore any pending local approval. An approved report whose relay-flow server is temporarily unavailable must use the existing quiet exact-report retry loop.

### 3.6 Required tests

Add tests for:

- exact valid report opens the TUI approval;
- invalid/missing HITL output does not open a correction prompt;
- `Approve` delivers exactly one report;
- `Reject` sends no report;
- a second idle event for the same assistant message does not create another dialog;
- approval for message A cannot authorize message B;
- a changed report ID cannot reuse an old approval;
- report delivery retries exact unchanged JSON without another LLM turn;
- a stale/duplicate relay-flow acknowledgement is treated as success;
- canceled/finished runs do not deliver a pending report;
- TUI/plugin restart behavior is deterministic;
- the relay-flow HITL path contains no Question-tool dependency.

---

## 4. Implementation order

The implementation should proceed in this order:

1. Update the OpenSpec/design requirements for explicit restart, attempt identity, and status-aware blocking.
2. Add run projection/API/CLI contracts for restart and actionable blocked status.
3. Add engine-neutral restart and attempt-fencing behavior.
4. Add task-system adapter operations for restart preparation/status compatibility without hardcoding provider statuses in core.
5. Add cancellation/restart durability, idempotency, and stale-report tests.
6. Update OpenCode plugin packaging to expose the TUI entrypoint without removing unrelated Question-tool functionality.
7. Implement the TUI report gate and exact approval/rejection behavior.
8. Add TUI/plugin tests and an end-to-end demo using the same assistant-message-to-dialog path.
9. Re-run the relay-flow Go suite and plugin/OpenCode tests.

Issue 2 remains outside this implementation sequence until a separate prompt-resume decision is approved.

## 5. Explicit non-goals

- No automatic restart of canceled tickets from polling.
- No automatic reopening of Done/Closed tickets.
- No rollback or deletion of mailbox comments, labels, worktrees, branches, or code.
- No reuse of a canceled execution identity for a new attempt.
- No Question-tool approval for relay-flow HITL reports.
- No raw stdin ownership by the server plugin.
- No mini UI migration for the deleted-terminal prompt issue in this phase.
