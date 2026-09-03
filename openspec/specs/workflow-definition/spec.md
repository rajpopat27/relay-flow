# workflow-definition Specification

## Purpose
TBD - created by archiving change relay-flow-subtask-refactor. Update Purpose after archive.
## Requirements
### Requirement: Workflow root schema is strict
A workflow SHALL contain `name`, one or more `repos`, optional `cleanupRunnerOnEnd`, optional adapter-owned `taskConfig`, and `nodes`. Unknown root fields, including plugin selectors, arbitrary query fields, polling intervals, and legacy `tasks`, `runner`, or `closeOn`, SHALL be rejected.

#### Scenario: Minimal valid workflow
- **WHEN** YAML defines a valid name, registered repo, start/work/end nodes, and legal routes
- **THEN** strict parsing succeeds

#### Scenario: Legacy field is present
- **WHEN** YAML contains legacy `closeOn` or node-level tracker `when`
- **THEN** strict parsing rejects the workflow

#### Scenario: Plugin override is present
- **WHEN** YAML contains `runnerPlugin` or `harnessPlugin`
- **THEN** parsing rejects it because plugin selection is machine scoped

### Requirement: Workflow identity is stable
Workflow `name` SHALL be non-empty, unique among stored workflows, and SHALL use lower-camel alphanumeric form beginning with a lowercase letter. It SHALL determine the permanent claim label `wf:<name>`.

#### Scenario: Valid workflow name
- **WHEN** the name is `basicFlow`
- **THEN** validation accepts it and the claim label is `wf:basicFlow`

#### Scenario: Invalid workflow name
- **WHEN** the name contains spaces, punctuation, or begins with an uppercase letter
- **THEN** validation rejects it

### Requirement: Repos are required and unique
Every workflow SHALL list at least one registered repo. Repo names within a workflow SHALL be unique.

#### Scenario: No repos
- **WHEN** a workflow contains an empty repo list
- **THEN** validation fails

#### Scenario: Duplicate repo
- **WHEN** the same repo appears twice
- **THEN** validation fails

#### Scenario: Unknown repo
- **WHEN** a workflow references an unregistered repo
- **THEN** submission fails before storage

### Requirement: Start is the reserved entry node
Every workflow SHALL contain exactly one node named `start`. `start` SHALL have no type, agent, description, nudge prompt, or failure routes. It SHALL have exactly one success target and MAY contain task configuration.

#### Scenario: Valid start node
- **WHEN** start contains task configuration and one success target
- **THEN** validation accepts it as the graph entry

#### Scenario: Start has several targets
- **WHEN** start declares more than one success route
- **THEN** validation fails because entry must be deterministic

#### Scenario: Start declares agent
- **WHEN** start contains an agent or type
- **THEN** validation rejects it

### Requirement: End is the reserved completion node
Every workflow SHALL contain exactly one node named `end`. `end` SHALL have no type, agent, description, nudge prompt, success routes, or failure routes. It MAY contain task configuration. The word `terminal` SHALL refer only to runner terminals and SHALL NOT be a reserved lifecycle node.

#### Scenario: Valid end node
- **WHEN** end contains only task configuration
- **THEN** validation accepts it as workflow completion

#### Scenario: End has route
- **WHEN** end declares an outgoing route
- **THEN** validation fails

#### Scenario: Workflow declares terminal lifecycle node instead
- **WHEN** the graph lacks `end` and declares `terminal` as its lifecycle exit
- **THEN** validation fails because the reserved exit name is `end`

### Requirement: Work nodes define executable work
Every non-reserved node SHALL have type `agent` or `hitl`, a non-empty agent name, a non-empty description, optional nudge prompt, optional task configuration, and non-empty success and failure route lists.

#### Scenario: Valid agent node
- **WHEN** an agent node defines agent, description, success routes, and failure routes
- **THEN** validation accepts it

#### Scenario: Valid HITL node
- **WHEN** a HITL node defines the same work and route fields
- **THEN** validation accepts it and marks only its nudge behavior as HITL

#### Scenario: Work node lacks failure route
- **WHEN** a work node has no failure route
- **THEN** validation rejects it because a failure report would have no legal next step

### Requirement: Routes are explicit and single-target
Each route SHALL contain one existing target node other than reserved `start` and MAY contain a human-readable `when` explanation. A node MAY declare several routes for an outcome, but each report SHALL select exactly one target. Routes SHALL NOT be evaluated automatically from `when` text.

#### Scenario: Several failure choices
- **WHEN** coding declares failure routes to coding and planning with explanations
- **THEN** both are rendered as legal choices and the report must select one

#### Scenario: Dangling route
- **WHEN** a route targets an unknown node
- **THEN** validation fails

#### Scenario: Route targets start
- **WHEN** a success or failure route targets reserved `start`
- **THEN** validation fails because `start` is entry-only

#### Scenario: Explanation is present
- **WHEN** a route has a `when` value
- **THEN** it is included in prompts but is not executed as a condition by core

### Requirement: The graph is fully valid
All work nodes SHALL be reachable from `start`, `end` SHALL be reachable, and every route target SHALL exist. The graph MAY contain backward edges and self-loops but SHALL execute only one node at a time.

#### Scenario: Unreachable node
- **WHEN** a node is not reachable from start
- **THEN** validation rejects the workflow

#### Scenario: End cannot be reached
- **WHEN** no route path reaches end
- **THEN** validation rejects the workflow

#### Scenario: Self-loop exists
- **WHEN** a failure route targets its own node
- **THEN** validation accepts the loop and each revisit receives a new node visit ID

### Requirement: Task configuration is adapter owned
Workflow and node `taskConfig` SHALL be opaque to core, merged with root and repo task configuration, and strictly validated by the selected task plugin for every referenced repo. Core SHALL NOT assume status fields.

#### Scenario: Jira configuration is valid
- **WHEN** Jira workflow filters and node transitions use valid project statuses and fields
- **THEN** workflow validation succeeds for every referenced repo

#### Scenario: Jira status is invalid
- **WHEN** a node config references a Jira status unavailable to one referenced repo
- **THEN** submission fails and identifies the repo/node/value

#### Scenario: Linear-style configuration is used
- **WHEN** a different task plugin defines assignment fields rather than transitions
- **THEN** core accepts the opaque shape after that plugin validates it

### Requirement: Task config merge behavior is deterministic
Task config layers SHALL be applied in root, repo, workflow, then node order. Maps SHALL merge recursively. A later scalar or list SHALL replace the earlier value. Omitted keys SHALL inherit. Explicit YAML `null` SHALL be rejected. The implementation SHALL use a small tested local merge function rather than an additional merge dependency.

#### Scenario: Nested maps merge
- **WHEN** root and node config provide different keys under the same nested map
- **THEN** the effective config contains both keys with node values taking precedence on collisions

#### Scenario: List override
- **WHEN** workflow config provides a list already present at root
- **THEN** the workflow list replaces the root list rather than appending

#### Scenario: Scalar override
- **WHEN** node config provides a scalar already present at workflow scope
- **THEN** the node scalar replaces the workflow scalar

#### Scenario: Key is omitted
- **WHEN** a lower scope omits a configured key
- **THEN** the nearest higher-scope value is inherited

#### Scenario: Explicit null is used
- **WHEN** any task config layer supplies YAML `null`
- **THEN** validation rejects the workflow or machine config

### Requirement: Jira transition defaults are deterministic
For the initial Jira task adapter, omitted transition values SHALL default to parent status `In Progress` for `start`, mailbox task status `In Progress` for agent/HITL work nodes, and parent status `Done` for `end`. An omitted work-node parent status SHALL leave the parent unchanged.

#### Scenario: Start transition is omitted
- **WHEN** a Jira workflow omits the start parent transition
- **THEN** Jira processing uses parent status `In Progress`

#### Scenario: Work-node task transition is omitted
- **WHEN** a Jira work node omits mailbox task status
- **THEN** Jira processing uses mailbox status `In Progress` and does not change the parent unless a parent status is configured

#### Scenario: End transition is omitted
- **WHEN** a Jira workflow omits the end parent transition
- **THEN** Jira processing uses parent status `Done`

### Requirement: Cleanup behavior is explicit
`cleanupRunnerOnEnd` SHALL be a workflow boolean controlling runner resource cleanup after end task configuration. When omitted, it SHALL default to `false`.

#### Scenario: Cleanup omitted
- **WHEN** the field is absent
- **THEN** reaching end does not automatically close runner resources

#### Scenario: Cleanup enabled
- **WHEN** the field is true
- **THEN** reaching end closes run-owned runner resources

### Requirement: Nudge templates are validated custom instructions
Agent and HITL nodes MAY define `nudgePrompt` as custom instructions for that node. Supported template variables SHALL be `{{ticket}}`, `{{workflow}}`, `{{repo}}`, `{{node}}`, and `{{nextSteps}}`. Unknown variables SHALL fail submission. The rendered instructions SHALL be appended to every new node-visit prompt, including a new visit reusing a live terminal, but SHALL NOT be sent during same-visit retry or restart.

#### Scenario: Custom instructions render
- **WHEN** a work node uses supported variables
- **THEN** relay-flow renders current ticket, workflow, repo, node, and legal next-step text into its new-visit prompt

#### Scenario: Unknown nudge variable
- **WHEN** a nudge uses `{{assignee}}`
- **THEN** workflow validation fails

#### Scenario: Same visit retries
- **WHEN** runtime setup retries or restarts for the same node visit
- **THEN** relay-flow sends no prompt, including no repeated custom instructions

### Requirement: Workflow updates require no active runs
The workflow definition SHALL NOT be replaced or removed while any run using it is starting, running, waiting, blocked, or canceling. New runs SHALL use the current in-memory definition, while active durable runs SHALL replay their immutable accepted snapshot.

#### Scenario: Update while HITL waits
- **WHEN** a HITL run is waiting and the workflow is resubmitted
- **THEN** replacement is rejected

#### Scenario: Update after runs finish
- **WHEN** no active run uses the workflow
- **THEN** a validated replacement becomes the definition for future runs
