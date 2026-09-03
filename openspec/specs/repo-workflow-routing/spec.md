# repo-workflow-routing Specification

## Purpose
TBD - created by archiving change relay-flow-subtask-refactor. Update Purpose after archive.
## Requirements
### Requirement: Workflows target registered repos
The system SHALL require every workflow to declare one or more registered repo names. The workflow declaration SHALL be the source of truth for repo membership, and the system SHALL rebuild each repo's in-memory workflow bindings from stored workflow files at startup and after workflow submission or removal.

#### Scenario: Global workflow targets several repos
- **WHEN** a stored workflow declares three registered repos
- **THEN** the system binds the same workflow definition to all three repos without duplicating the stored workflow

#### Scenario: Workflow references an unknown repo
- **WHEN** a workflow is submitted with a repo name that is not registered
- **THEN** submission fails before the workflow file or in-memory bindings are changed

### Requirement: One Repo Poller fetches each repo
The system SHALL run one Repo Poller per registered repo. Every Repo Poller SHALL use the machine-wide `pollIntervalSeconds`, defaulting to 15 when omitted, and a shared concurrency limit SHALL allow no more than 10 task-system polls to execute simultaneously.

#### Scenario: Several workflows share one repo
- **WHEN** five workflows target the same repo
- **THEN** one Repo Poller fetches that repo's tickets once per interval and supplies the result to routing for all five workflows

#### Scenario: More than ten repos become due
- **WHEN** more than ten Repo Pollers are ready to execute
- **THEN** at most ten polls execute concurrently and the remaining polls wait for capacity

#### Scenario: Repo poll fails transiently
- **WHEN** the task-system poll returns a runtime error
- **THEN** the Repo Poller retries with the shared exponential backoff and does not stop other repos from polling

### Requirement: Polling returns active unblocked parent tickets only
The task-system polling contract SHALL return active parent tickets for one repo and SHALL NOT return mailbox subtasks as workflow-run candidates. The Jira adapter SHALL fetch every supported field required to evaluate configured workflow filters plus `issuelinks` in each paginated REST v3 search response. It SHALL exclude a candidate when any inward `Blocks` link contains a linked issue whose `statusCategory.key` is not `done`. It SHALL accept candidates with no blockers or only blockers in the `done` category, including `Cancelled`, without per-ticket link requests. Linked blockers MAY belong to another project.

#### Scenario: Parent and mailbox share a workflow label
- **WHEN** a parent ticket and its mailbox subtasks all carry `wf:basicFlow`
- **THEN** polling returns the active parent only and mailbox discovery remains the responsibility of `EnsureMailboxes`

#### Scenario: Parent completed through end
- **WHEN** the parent satisfies the task adapter's configured completed state after the workflow reaches `end`
- **THEN** the parent is excluded from normal polling

#### Scenario: Parent has a mixture of closed and open blockers
- **WHEN** Jira search returns a parent blocked by one linked issue in category `done` and another linked issue outside category `done`
- **THEN** polling excludes the parent before routing or durable run creation

#### Scenario: Every blocker is closed
- **WHEN** every inward `Blocks` issue returned with a parent has `statusCategory.key` equal to `done`
- **THEN** polling keeps the parent as a candidate without another Jira request

### Requirement: Workflow filters are structured and locally evaluable
The task plugin SHALL own the workflow filter schema and SHALL compile validated key-value filters into in-memory matchers over normalized ticket fields. Workflow configuration SHALL NOT accept arbitrary Jira JQL or another task-system query language.

#### Scenario: Jira workflow filter matches a ticket
- **WHEN** a workflow filters by supported parent statuses, issue types, labels, and assignee and a polled ticket satisfies those values
- **THEN** its compiled matcher reports a match without another Jira request

#### Scenario: Unsupported filter field is submitted
- **WHEN** workflow task configuration contains an unknown filter field
- **THEN** strict task-plugin validation rejects the workflow before it is stored

#### Scenario: Arbitrary query is submitted
- **WHEN** a workflow attempts to configure free-form JQL
- **THEN** workflow validation rejects the configuration

### Requirement: Existing workflow claims take precedence
The Ticket Router SHALL inspect all parent labels with prefix `wf:` before evaluating workflow filters. Exactly one valid claim SHALL route directly to that named workflow if the workflow is registered for the repo. The system SHALL NOT re-evaluate filters for a singly claimed ticket. More than one workflow claim SHALL be an ownership error.

#### Scenario: Claimed ticket no longer matches current filters
- **WHEN** a ticket carries `wf:basicFlow` but no longer matches that workflow's pickup filters
- **THEN** the Ticket Router still resolves `basicFlow` for crash recovery

#### Scenario: Claim names an unknown workflow
- **WHEN** a ticket carries a workflow label that has no stored workflow
- **THEN** the Ticket Router reports an invalid claim and does not mutate or start the ticket

#### Scenario: Claim names workflow not bound to repo
- **WHEN** a ticket carries a known workflow label but that workflow does not target the polled repo
- **THEN** the Ticket Router reports an invalid claim and does not start a run

#### Scenario: Ticket carries several workflow claims
- **WHEN** a parent carries two or more `wf:` labels
- **THEN** the Ticket Router reports invalid ambiguous ownership and mutates nothing

### Requirement: Unclaimed routing is unambiguous
For an unclaimed parent ticket, the Ticket Router SHALL evaluate only the workflows bound to that repo. Zero matches SHALL be ignored, exactly one match SHALL be selected, and multiple matches SHALL produce an ambiguity error without mutating the ticket.

#### Scenario: No workflow matches
- **WHEN** no bound workflow matcher accepts an unclaimed ticket
- **THEN** the system leaves the ticket unchanged

#### Scenario: Exactly one workflow matches
- **WHEN** exactly one bound workflow matcher accepts an unclaimed ticket
- **THEN** the Ticket Router returns that workflow to the Run Manager

#### Scenario: Several workflows match
- **WHEN** two or more bound workflows accept an unclaimed ticket
- **THEN** the system records an ambiguity error and adds no workflow label

### Requirement: Claim is persisted before run creation
The Run Manager SHALL add `wf:<workflow>` to the selected parent before creating the durable run. The Jira adapter SHALL use one label-add request based on the claims already inspected by the router and SHALL NOT re-read labels during claim. Assignee isolation and one server per machine are the ownership boundary; the reduced but non-zero cross-machine race is accepted. The Run Manager SHALL create or ensure the run only after the claim succeeds. Before recreating a missing claimed run, it SHALL skip a parent containing the stable cancellation marker. The deterministic run ID SHALL be derived from repo, workflow, and parent ticket. When that run already exists in SQLite, repeated polls SHALL skip the Jira cancellation-marker request but still call durable `EnsureRun` so active terminal reconciliation is preserved.

#### Scenario: Claim fails
- **WHEN** the task system fails to persist the workflow label
- **THEN** the Run Manager does not create the durable run and retries the claim

#### Scenario: Server crashes after claim
- **WHEN** the claim succeeds and the server crashes before durable run creation
- **THEN** the next healthy-database poll resolves the workflow from the label and safely ensures the missing deterministic run

#### Scenario: Repeated polling sees an existing run
- **WHEN** later polls see the same claimed parent
- **THEN** ensuring the deterministic run is idempotent and does not create a second run

#### Scenario: Cross-machine claims race
- **WHEN** two assignee-isolated deployments nevertheless add different workflow labels concurrently
- **THEN** a later poll detects multiple `wf:` labels, reports invalid ownership, and starts no further run from that ticket

#### Scenario: Canceled run history was retained out
- **WHEN** a labeled parent has no durable run but contains `<runID>:cancellation`
- **THEN** the Run Manager does not recreate the run

### Requirement: Workflow claims remain visible on mailboxes
The task adapter SHALL ensure `wf:<workflow>` is present on the parent and every mailbox subtask. Workflow claim labels SHALL remain after completion or cancellation as audit and recovery data.

#### Scenario: Mailboxes are created
- **WHEN** a run ensures its node mailboxes
- **THEN** each mailbox and its parent carry the same workflow label

#### Scenario: Run completes
- **WHEN** the workflow reaches `end`
- **THEN** the workflow labels remain on the parent and mailbox subtasks
