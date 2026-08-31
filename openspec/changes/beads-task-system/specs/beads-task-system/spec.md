# Beads task-system plugin

## ADDED Requirements

### Requirement: Beads is a selectable machine task plugin

The machine SHALL be able to select `beads` as its task plugin. The Beads factory SHALL use the existing `task.System` contract, SHALL be statically registered, and SHALL create one task-system instance for each registered repository. Runner and harness plugin selection SHALL remain independent.

#### Scenario: Beads is selected at startup

- **WHEN** machine configuration selects `taskPlugin: beads` and the Beads repo configuration is valid
- **THEN** startup constructs the Beads task system for every registered repository and starts the existing Repo Poller group

#### Scenario: Beads authentication is requested

- **WHEN** the selected task plugin is Beads and `task auth` is invoked
- **THEN** the Beads adapter performs no task-system credential flow and does not create relay-flow credentials

### Requirement: Beads workspace scope is explicit per repository

Every registered Beads repository SHALL provide a non-empty repo-scoped `taskConfig.beadsDir`. The adapter SHALL canonicalize that directory and use it as its opaque task scope key. Registration SHALL reject a second repository resolving to the same Beads workspace. The code repository `config.Repo.Path` SHALL remain separate from `beadsDir`.

#### Scenario: Two repositories use independent workspaces

- **WHEN** two registered repositories provide different canonical `beadsDir` values
- **THEN** both registrations succeed and each repository receives an independent Beads task system and Repo Poller

#### Scenario: Two repositories share a workspace

- **WHEN** a second repository resolves to an already-registered canonical `beadsDir`
- **THEN** registration fails before a duplicate poller is created

### Requirement: Beads commands use the registered repository and workspace

Every Beads CLI invocation SHALL execute with the registered code-repository path as the child process working directory and the configured workspace as `BEADS_DIR`. The adapter SHALL NOT change the relay-flow process working directory. The adapter SHALL use `bd` JSON output and SHALL NOT read Beads JSONL exports, Dolt tables, or Beads internal packages.

#### Scenario: External workspace is used

- **WHEN** a registered repo has `path: /work/payments` and `beadsDir: /var/lib/beads/payments/.beads`
- **THEN** Beads commands run with `/work/payments` as `cmd.Dir` and `/var/lib/beads/payments/.beads` as `BEADS_DIR`

#### Scenario: Ambient workspace variables exist

- **WHEN** the relay-flow parent process contains unrelated Beads selector variables
- **THEN** the adapter's child environment explicitly selects the configured workspace rather than silently using the ambient workspace

### Requirement: Beads uses the shared task configuration vocabulary

The Beads adapter SHALL retain the existing task configuration field names
`filters`, `templates`, and `transitionTo`. `transitionTo` SHALL use the
existing `parentStatus` and `taskStatus` members. The adapter SHALL accept an
optional top-level `assignee` as the default assignee filter, with an explicit
`filters.assignees` value taking precedence in the same manner as Jira.
`beadsDir` SHALL remain required at registered-repository scope as the
Beads-specific physical workspace key. The adapter SHALL reject Jira-only
`project` and `component` fields and SHALL not support the Beads-only
`status.parent` or `status.mailbox` vocabulary.

Status values SHALL remain adapter-specific rather than being silently
translated: Beads uses values such as `in_progress` and `closed`, while Jira
uses values such as `In Progress` and `Done`. Arbitrary Jira status values
MUST NOT be accepted as Beads values.

#### Scenario: Shared lifecycle configuration is accepted

- **WHEN** Beads configuration uses `transitionTo.parentStatus` or `transitionTo.taskStatus` together with the existing `filters` or `templates` fields
- **THEN** the adapter accepts those shared field names and applies the configured values at their supported scopes

#### Scenario: Unsupported provider-specific fields are rejected

- **WHEN** Beads configuration contains `project`, `component`, `status.parent`, or `status.mailbox`
- **THEN** configuration validation rejects it rather than silently ignoring it

#### Scenario: Provider status names are not translated

- **WHEN** a Beads configuration supplies a lifecycle status
- **THEN** the adapter interprets it as a Beads-native value such as `in_progress` or `closed` and does not silently translate arbitrary Jira values such as `In Progress` or `Done`

### Requirement: Polling returns only top-level active parents

The Beads task system SHALL read ready work and claimed active work separately, merge results by issue ID, normalize parent issues into `task.Ticket`, and exclude every issue with a non-empty parent relationship. It SHALL not query Beads separately for each workflow.

#### Scenario: Ready parent is returned

- **WHEN** a top-level Beads issue is ready and matches a workflow filter
- **THEN** the repository poll includes it as a normalized parent ticket

#### Scenario: Mailbox child appears in a CLI result

- **WHEN** a Beads list command returns an issue with a non-empty parent field
- **THEN** the adapter excludes it from the parent ticket batch even if `--no-parent` was supplied

#### Scenario: Ready and claimed queries overlap

- **WHEN** the same parent appears in both ready and claimed result sets
- **THEN** the adapter returns one ticket keyed by the Beads issue ID

### Requirement: Workflow ownership uses permanent labels

After routing an unclaimed ticket, the Beads adapter SHALL add exactly the workflow ownership label `wf:<workflow>` without changing status or assignee. It SHALL NOT use a Beads claim command that selects work before relay-flow resolves the workflow.

#### Scenario: Ticket is claimed for a workflow

- **WHEN** routing resolves parent `demo-a1b2` to workflow `implementation`
- **THEN** the adapter calls `bd update demo-a1b2 --add-label wf:implementation --json` before durable run creation

#### Scenario: Existing workflow claim is present

- **WHEN** a parent already has a `wf:<workflow>` label
- **THEN** the router uses the existing claim and the adapter does not add a second claim

### Requirement: Mailboxes are reusable child issues

The Beads task system SHALL represent each agent or HITL node as one reusable child issue under the parent. Mailbox identity SHALL be the stable title `<parent-id>:<node>`. `EnsureMailboxes` SHALL find existing children, update their description and workflow label, create only missing children, and reject duplicate children with the same stable title.

#### Scenario: Missing mailbox is created

- **WHEN** a parent lacks the mailbox for node `implement`
- **THEN** the adapter creates one child with title `<parent-id>:implement`, the node description, and `wf:<workflow>`

#### Scenario: Mailbox already exists

- **WHEN** a parent already has the stable mailbox child
- **THEN** the adapter reuses it, reconciles its description/label, and does not create another child

#### Scenario: Node is revisited

- **WHEN** a workflow returns to a node
- **THEN** the adapter reuses the original mailbox child rather than creating a new subtask

### Requirement: Beads status changes use read-before-write reconciliation

For each Beads status transition with an expected source status and a target status, the adapter SHALL first read the issue with `bd show`. If the current status is already the target, it SHALL succeed without another status update. If the current status is neither the expected source nor the target, it SHALL return a conflict so the durable run blocks/retries. If the current status is the expected source, it SHALL issue an unconditional `bd update --status <target>`. The adapter SHALL accept a status change occurring between the read and write as a Beads-specific last-writer-wins race. Manual status changes SHALL NOT select a graph route.

#### Scenario: Expected state is observed

- **WHEN** a mailbox is `in_progress` and the desired transition is to `closed`
- **THEN** the adapter reads the state and issues an unconditional update to `closed`

#### Scenario: Incompatible state is observed

- **WHEN** a mailbox is `in_review` but the activity expects `in_progress` before closing it
- **THEN** the adapter returns a conflict, the run enters blocked/retry behavior, and the adapter does not issue the status update

#### Scenario: Target state is already present

- **WHEN** a retried activity reads that the mailbox is already `closed`
- **THEN** the adapter succeeds without issuing another status update

#### Scenario: State changes after the read

- **WHEN** the adapter reads the expected source status and another writer changes the status before the unconditional update
- **THEN** the Beads update follows last-writer-wins behavior and the race is accepted by this integration

### Requirement: Comments and task text remain idempotent

`HasComment` SHALL find stable markers in Beads comments. `Comment` SHALL read comments before writing, SHALL avoid a duplicate marker, and SHALL send multiline content through stdin. Beads task text SHALL render mailbox descriptions, summaries, and feedback using the adapter-owned templates.

#### Scenario: Summary is written to the current mailbox

- **WHEN** a node report is accepted
- **THEN** the summary comment is written only to the current node mailbox with a stable marker

#### Scenario: Feedback is written to the selected mailbox

- **WHEN** a report selects a non-end next node
- **THEN** feedback is written only to that next node's mailbox

#### Scenario: End is selected

- **WHEN** a report selects `end`
- **THEN** no feedback comment is written and no end mailbox is created

#### Scenario: Comment activity is retried

- **WHEN** a marked comment activity is retried
- **THEN** the adapter detects the marker and does not write a duplicate comment

### Requirement: Recovery preserves Beads work and rolls forward

Explicit database-loss recovery SHALL use the Beads adapter to rediscover parent/mailbox issues, create only missing mailboxes, reset parent/mailbox state according to Beads policy, preserve comments/labels/descriptions/history, and never delete or compensate Beads work. Recovery SHALL start fresh durable runs at `start` and SHALL not resume old routes, reports, visits, or activity checkpoints.

#### Scenario: Recovery finds an existing mailbox

- **WHEN** recovery discovers a parent with an existing node mailbox
- **THEN** it reuses the mailbox and preserves its comments, labels, description, and Beads history

#### Scenario: Recovery finds a missing mailbox

- **WHEN** recovery discovers a parent missing a required node mailbox
- **THEN** it creates only that missing child mailbox

### Requirement: Dolt server mode remains owned by Beads

The adapter SHALL work with an embedded Beads workspace or a server-backed workspace selected by the configured Beads metadata. Relay-flow SHALL NOT start `dolt sql-server`, manage `bd serve`, or access Beads storage directly as part of normal operation.

#### Scenario: Server-backed workspace is configured

- **WHEN** `beadsDir` points to a Beads workspace configured for an external Dolt server
- **THEN** relay-flow invokes `bd` with that workspace and leaves server lifecycle to the external Beads/Dolt setup
