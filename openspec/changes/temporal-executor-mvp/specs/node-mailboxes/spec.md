## MODIFIED Requirements

### Requirement: Recovery reuses mailboxes
During `goworkflows` `serve --recover`, relay-flow SHALL call `EnsureMailboxes` to find existing children and create only missing ones, then SHALL reset mailbox task state using the task adapter while preserving descriptions, comments, labels, and code artifacts. During Temporal `serve --recover`, relay-flow SHALL rebuild only its local SQLite projection from Temporal and SHALL NOT reset, complete, recreate, or otherwise mutate mailbox tasks; existing Temporal workflow activities and the normal task-system contract remain responsible for mailbox reconciliation.

#### Scenario: Goworkflows recovery with existing mailboxes
- **WHEN** embedded database recovery finds a parent with all workflow mailboxes
- **THEN** `EnsureMailboxes` finds them, `ResetForRecovery` resets their fresh-run task state, and existing mailbox comments remain

#### Scenario: Goworkflows recovery with a missing mailbox
- **WHEN** embedded database recovery finds a parent missing one node mailbox
- **THEN** `EnsureMailboxes` creates only that mailbox before reset and restart from `start`

#### Scenario: Temporal projection recovery with existing mailboxes
- **WHEN** Temporal projection recovery finds an active workflow with existing mailbox tasks
- **THEN** relay-flow leaves mailbox statuses, descriptions, comments, labels, and task-system history unchanged

#### Scenario: Temporal projection recovery with a missing mailbox
- **WHEN** Temporal projection recovery finds a mailbox absent from the task system
- **THEN** projection recovery does not create or reset a mailbox and does not start a replacement Temporal workflow
