## ADDED Requirements

### Requirement: Cancellation remains permanent until explicit restart
A canceled execution SHALL remain terminal and SHALL NOT be recreated by normal polling, server restart, workflow replacement, ticket-status changes, or projection retention. A fresh execution SHALL be created only by an explicit `run restart` operation.

#### Scenario: Poll sees a canceled run
- **WHEN** normal polling sees a ticket whose latest attempt is canceled
- **THEN** it does not create or resume an execution

#### Scenario: Cancellation finishes after a command
- **WHEN** an operator requests restart while the previous attempt is `canceling`
- **THEN** the restart operation returns a conflict instructing the operator to wait for cancellation to finish

#### Scenario: Canceled projection is retained out
- **WHEN** a claimed ticket has no durable run row but carries the stable logical cancellation marker
- **THEN** normal polling skips run creation

### Requirement: An explicit restart creates a fresh numeric attempt
`relay-flow run restart --ticket <key>` SHALL resolve the latest ticket run and SHALL create a fresh attempt only when the previous attempt is canceled. The original attempt SHALL be attempt `1`; each explicit restart SHALL allocate the next positive numeric attempt ID from persisted run history. Attempt allocation SHALL be serialized so two concurrent restart requests cannot create the same attempt number.

#### Scenario: First explicit restart
- **WHEN** the latest attempt for a ticket is canceled and the operator invokes `run restart`
- **THEN** the system creates attempt `2` and returns its run identity

#### Scenario: Multiple explicit restarts
- **WHEN** attempts `1` and `2` are historical canceled attempts and the operator invokes restart again
- **THEN** the system creates attempt `3` rather than reusing an earlier number

#### Scenario: Restart is repeated while active
- **WHEN** the operator invokes `run restart` again while a suffixed fresh attempt is starting, running, waiting, or blocked
- **THEN** the operation returns that existing attempt and creates no second active attempt

#### Scenario: Restart is requested for a completed or active original run
- **WHEN** the latest attempt is not canceled and is not an already-created suffixed restart attempt
- **THEN** the operation returns a conflict and creates no new attempt

### Requirement: Logical and execution identities are distinct
The system SHALL retain a stable logical run identity derived from `repo/workflow/ticket` for all attempts. The first attempt SHALL use that logical value as its `runID`; an explicit restart SHALL derive a distinct execution `runID` by appending its numeric attempt suffix. `runID` SHALL be used as the durable workflow instance and report-fencing identity, while `logicalRunID` SHALL group attempts and anchor cancellation fencing.

#### Scenario: First attempt identity
- **WHEN** a ticket is first claimed
- **THEN** its run has `logicalRunId` equal to `repo/workflow/ticket`, `attemptId` equal to `1`, and `id` equal to that logical ID

#### Scenario: Fresh attempt identity
- **WHEN** attempt `2` is explicitly restarted
- **THEN** its `logicalRunId` remains unchanged, its `attemptId` is `2`, and its `id` is `<logicalRunID>~attempt~2`

#### Scenario: Normal process restart
- **WHEN** the server restarts with a healthy database
- **THEN** the current attempt keeps the same execution `runID`, attempt number, node visit, and durable graph progress

### Requirement: Restart uses the latest workflow and starts at start
A fresh attempt SHALL use the current validated workflow definition, SHALL store an immutable workflow snapshot in its durable start input, and SHALL begin at reserved `start`. It SHALL NOT resume the canceled attempt's current node, route, report, node visit, activity checkpoint, or session.

#### Scenario: Workflow changed after cancellation
- **WHEN** an operator updates and successfully resubmits a workflow after canceling its run, then invokes restart
- **THEN** the new attempt uses the updated workflow snapshot

#### Scenario: Canceled run was at a work node
- **WHEN** the canceled attempt stopped at `implement`
- **THEN** the fresh attempt applies the start lifecycle and enters the workflow's `start.onSuccess` target rather than resuming `implement`'s old visit

#### Scenario: Fresh harness runtime
- **WHEN** a fresh attempt enters its first work node
- **THEN** it has no prior persisted session binding and the harness launches a fresh session

### Requirement: Restart preserves task and runner work artifacts
A fresh attempt SHALL preserve the existing worktree, code, branches, workflow labels, mailbox subtasks, mailbox descriptions, mailbox comments, and task history. It SHALL close surviving terminals for the ticket before starting new node work, but SHALL NOT delete or roll back human or agent work.

#### Scenario: Existing ticket worktree
- **WHEN** a canceled run already has an Orca ticket worktree
- **THEN** restart reuses that environment and does not create a second worktree

#### Scenario: Existing mailboxes
- **WHEN** the canceled run has mailbox children for its work nodes
- **THEN** restart finds and reuses those children, preserving their comments, descriptions, and workflow labels

#### Scenario: Surviving terminal
- **WHEN** an old node terminal is still present during explicit restart
- **THEN** the runner closes it by ticket-scoped identity before the new attempt creates its fresh node terminal

### Requirement: Task systems own restart mailbox and status semantics
Core restart orchestration SHALL NOT contain Jira, Beads, or other provider status names. A task adapter MAY implement an optional restart-preparation operation that reopens relay-owned mailboxes while preserving comments, descriptions, and labels. Parent status compatibility SHALL remain the adapter's normal start task-config operation.

#### Scenario: Jira mailbox preparation
- **WHEN** Jira prepares an explicit restart
- **THEN** it reopens relay-owned mailboxes to its configured initial task state without changing the parent directly

#### Scenario: Beads mailbox preparation
- **WHEN** Beads prepares an explicit restart
- **THEN** it reopens relay-owned `open`/`in_progress`/`closed` mailboxes to `open` and does not reset the parent

#### Scenario: Alternative task adapter
- **WHEN** a task adapter does not need mailbox status preparation
- **THEN** core still creates the fresh attempt and uses the normal task-system boundary without requiring provider-specific code

### Requirement: Incompatible human status blocks and retries
When restart preparation or the start task configuration observes a human-owned incompatible parent or mailbox state, the new attempt SHALL enter `blocked`, SHALL preserve the external state, and SHALL retry reconciliation using the shared backoff. It SHALL continue automatically when the state becomes compatible. Manual status changes SHALL NOT select a graph route.

#### Scenario: Human blocks the parent
- **WHEN** the restarted ticket is in a human-owned `Blocked`/`Deferred`-equivalent state and the start transition cannot proceed
- **THEN** the attempt enters `blocked`, does not overwrite the parent, and records an actionable remediation error

#### Scenario: Human changes a mailbox
- **WHEN** a mailbox is in a human-owned incompatible state during restart preparation or node processing
- **THEN** the attempt enters `blocked` and does not overwrite that mailbox state

#### Scenario: Human restores the parent state
- **WHEN** the human moves the parent to a task-system-allowed active start state
- **THEN** the next durable retry clears the blocked/retry condition and the fresh attempt continues from `start` without a manual resume command

#### Scenario: Parent is Done or Closed
- **WHEN** an operator requests restart for a completed parent status
- **THEN** the attempt does not automatically reopen it and remains blocked or returns an actionable conflict until the human reopens it

### Requirement: `run get` exposes restart identity and blocked remediation
The run projection and `run get --ticket <key>` SHALL expose the current execution ID, stable logical run ID, numeric attempt ID, lifecycle state, current node, last error, and retry details when present. A blocked status error SHALL tell the operator to move the ticket to an allowed active start status or restore the task-system state, and SHALL state that relay-flow retries automatically.

#### Scenario: Fresh attempt is queried
- **WHEN** `run get` is requested after explicit restart
- **THEN** the response identifies the new suffixed `id`, unchanged `logicalRunId`, and numeric `attemptId`

#### Scenario: Restart is blocked by ticket status
- **WHEN** the current parent status conflicts with the start transition
- **THEN** `run get` shows `state: blocked`, `currentNode: start`, the conflict, the allowed-start-status action, and the next retry when known

#### Scenario: Blocked status recovers
- **WHEN** the task-system state becomes compatible and retry succeeds
- **THEN** `run get` no longer shows the stale blocked/retry error and reports the active node state

### Requirement: Old reports cannot advance a fresh attempt
Reports and approvals SHALL carry the execution `runID` for their attempt. A report for a canceled/non-current old attempt SHALL be acknowledged as an accepted duplicate or stale report without validating its payload or advancing the fresh attempt. If the old attempt projection row was retained out while a newer attempt for the same logical run remains, the server SHALL resolve the logical identity and return the same harmless duplicate acknowledgement.

#### Scenario: Old report after explicit restart
- **WHEN** a delayed report is delivered with the canceled attempt's `runID`
- **THEN** the server acknowledges it without writing comments, changing task state, or advancing the fresh attempt

#### Scenario: Old report row was retained out
- **WHEN** an old attempt's projection row is gone but a newer attempt with the same `logicalRunId` exists
- **THEN** the server acknowledges the old report as a stale duplicate without signaling the newer workflow

#### Scenario: New attempt report
- **WHEN** a valid report is delivered with the current suffixed `runID`
- **THEN** only the current attempt validates and consumes it

### Requirement: Restart is exposed through one API and CLI surface
The server SHALL expose `POST /runs/by-ticket/{key}/restart` and SHALL return the new/current `run.Run` in the standard JSON envelope. The CLI SHALL expose `relay-flow run restart --ticket <key>` and SHALL print the returned run data. Existing `run get --ticket <key>` SHALL return the latest attempt.

#### Scenario: Restart succeeds
- **WHEN** the CLI invokes `run restart --ticket PAY-101` for a canceled run
- **THEN** it exits successfully and prints the new run's ID, logical ID, attempt ID, and state

#### Scenario: Restart conflicts
- **WHEN** restart is requested while canceling or for a non-canceled original run
- **THEN** the API returns HTTP 409 with a human-readable conflict and the CLI exits with the operation-failure code

#### Scenario: Latest attempt query
- **WHEN** `run get --ticket PAY-101` is requested after restart
- **THEN** it returns the newest attempt rather than the historical canceled attempt
