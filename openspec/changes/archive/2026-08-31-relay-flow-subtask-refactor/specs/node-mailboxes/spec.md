## ADDED Requirements

### Requirement: Work nodes receive one mailbox each
The task system SHALL create or locate one child mailbox for every agent and HITL node before processing work. Reserved `start` and `end` nodes SHALL NOT receive mailboxes. A mailbox SHALL be the node subtask's description and comment section.

#### Scenario: New run creates mailboxes
- **WHEN** a workflow with exploration, coding, and review work nodes begins for a parent
- **THEN** the task system ensures exactly one mailbox for each of those nodes and none for `start` or `end`

#### Scenario: EnsureMailboxes is repeated
- **WHEN** `EnsureMailboxes` runs again for the same parent and workflow
- **THEN** it finds existing mailboxes, creates only missing ones, and returns the complete node-to-mailbox map

### Requirement: Mailbox identity is stable
Each mailbox SHALL have stable workflow/node identity under its parent, SHALL use title `<ticket>:<node>`, SHALL carry `wf:<workflow>`, and SHALL be reused for every revisit to that node. The task adapter SHALL detect existing mailboxes independently of in-memory state.

#### Scenario: Node is revisited
- **WHEN** review routes work back to coding
- **THEN** relay-flow reuses the original `<ticket>:coding` mailbox and preserves its existing comments

#### Scenario: Server restarts
- **WHEN** the server restarts and ensures mailboxes for a claimed parent
- **THEN** the task adapter rediscovers the existing mailbox children instead of creating duplicates

### Requirement: Mailbox description defines node work
The mailbox description SHALL contain the node name, node type, assigned agent, parent identity, node description, complete report contract and rules, legal success/failure next steps with their explanations, and HITL approval instructions when applicable. The compact agent launch prompt SHALL identify the parent Jira ticket and exact mailbox subtask, and SHALL direct the agent to read the parent for original context and only its mailbox description and comments for node instructions and feedback.

#### Scenario: Agent opens a coding mailbox
- **WHEN** the coding node is processed
- **THEN** its mailbox description tells the agent what coding work to perform, which next steps are legal, and how to produce the complete report

#### Scenario: Coding receives review feedback
- **WHEN** coding is revisited with mailbox subtask `PAY-234`
- **THEN** the follow-up says `New feedback was added to the comments section of your mailbox subtask PAY-234. Read it.` and does not direct the agent to a sibling mailbox

#### Scenario: Workflow is invalid
- **WHEN** a mailbox description cannot be rendered because a route target is invalid
- **THEN** workflow submission fails before any mailbox is created

### Requirement: Summary stays with the current node
After accepting a node's single report, relay-flow SHALL render that report's `summary` as `summaryReport` through the task system's summary-comment template and write it to the current node's mailbox before completing that node's task-system work. The comment SHALL be human-readable and SHALL include a stable marker derived from node visit and comment type.

#### Scenario: Coding completes successfully
- **WHEN** the coding report is accepted
- **THEN** coding's completed, not-completed, issues, verification, and notes sections are written to the coding mailbox

#### Scenario: Summary comment retry
- **WHEN** a retry occurs after the summary comment may already have been accepted
- **THEN** the task adapter checks the stable marker before posting and does not intentionally create another comment

### Requirement: Feedback is delivered only to the selected next mailbox
After accepting a report whose next step is a work node, relay-flow SHALL render that same report's `feedback` plus its summary commit IDs as `feedbackReport` through the task system's feedback-comment template and write it to only that selected node's mailbox. The comment body SHALL identify the source and target nodes and mailbox. Other node mailboxes SHALL NOT receive that feedback.

#### Scenario: Review sends work to coding
- **WHEN** review reports failure and selects coding
- **THEN** reason, required actions, relevant context, and expected result are written to coding's mailbox and not to unrelated mailboxes

#### Scenario: Work routes forward
- **WHEN** exploration selects planning
- **THEN** planning receives exploration's feedback before planning is processed

### Requirement: End has no mailbox
When a report selects reserved `end`, every feedback subsection SHALL be `None`, relay-flow SHALL write no feedback comment, and the current node summary SHALL remain recorded in its own mailbox.

#### Scenario: Final review selects end
- **WHEN** final review succeeds with `NEXT STEP: end`
- **THEN** review's summary is written, no end mailbox is created, and no feedback comment is attempted

#### Scenario: End report contains feedback
- **WHEN** a report selects `end` but one or more feedback fields are not `None`
- **THEN** report validation rejects it and the run does not advance

### Requirement: Task configuration controls mailbox processing
The task plugin SHALL apply merged root/repo/workflow/node task configuration to the parent and current mailbox through `ApplyTaskConfig`. Core workflow code SHALL NOT assume Jira status transitions. For Jira, the REST client SHALL skip a transition when its cached or refreshed current state already equals the target and SHALL otherwise use an available transition ID.

#### Scenario: Jira node enters review
- **WHEN** a Jira review node configures parent and task status `In Review`
- **THEN** the Jira adapter moves the relevant parent/mailbox statuses to those configured values idempotently

#### Scenario: Non-status task system processes a node
- **WHEN** another task plugin uses node config to assign a mailbox instead of changing status
- **THEN** core schedules the same `ApplyTaskConfig` primitive without requiring status semantics

### Requirement: Completed nodes are marked through the task plugin
After summary and applicable feedback comments are written, relay-flow SHALL call `CompleteMailbox` before applying next-node task configuration or starting the next terminal. `CompleteMailbox` SHALL only apply the task system's completed state to the current mailbox; it SHALL NOT write comments, select routes, process the next node, or perform runner work. For Jira, completing a mailbox SHALL move the subtask to `Done` idempotently.

#### Scenario: Current mailbox completes
- **WHEN** a valid node report has been persisted and its comments have been written
- **THEN** the task plugin marks the current mailbox complete before the next mailbox is processed

#### Scenario: Completion is retried
- **WHEN** the provider already completed the mailbox but activity acknowledgement was lost
- **THEN** the task plugin recognizes the completed state and returns success without applying a conflicting transition

### Requirement: Manual mailbox changes do not route the graph
Only a valid structured report SHALL select and process the next node. A human changing mailbox status without a report SHALL NOT advance the graph.

#### Scenario: Human marks mailbox done manually
- **WHEN** the current mailbox is manually moved to a completed status without a structured report
- **THEN** relay-flow does not infer success or select a next node

#### Scenario: Human conflicts with pending activity
- **WHEN** a manual mailbox change conflicts with an expected task operation
- **THEN** the run becomes blocked and retries reconciliation instead of overwriting blindly

### Requirement: HITL uses the same mailbox lifecycle
HITL nodes SHALL use one reusable mailbox, task configuration, legal route lists, summaries, and feedback just like agent nodes. Human pacing SHALL affect only automatic nudge behavior.

#### Scenario: Human approves review
- **WHEN** the human-guided agent returns a valid success report from the review mailbox
- **THEN** relay-flow records the review summary and processes the selected next node

#### Scenario: Human leaves review idle
- **WHEN** a HITL review session becomes idle without a valid report
- **THEN** the mailbox remains active and relay-flow sends no automatic report-format nudge

### Requirement: Recovery reuses mailboxes
During `serve --recover`, relay-flow SHALL call `EnsureMailboxes` to find existing children and create only missing ones, then SHALL reset mailbox task state using the task adapter while preserving descriptions, comments, labels, and code artifacts.

#### Scenario: All mailboxes already exist
- **WHEN** database recovery finds a parent with all workflow mailboxes
- **THEN** no new mailboxes are created and existing mailbox comments remain

#### Scenario: One mailbox is missing
- **WHEN** recovery finds a parent missing one node mailbox
- **THEN** `EnsureMailboxes` creates only that mailbox before reset and restart from `start`
