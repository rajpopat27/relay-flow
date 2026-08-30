## ADDED Requirements

### Requirement: Plugin types are selected at machine scope
Machine config SHALL select one task plugin, one runner plugin, and one harness plugin. Workflows SHALL NOT override plugin types. Plugin configuration SHALL be strictly validated by the owning plugin.

#### Scenario: Server starts with supported plugins
- **WHEN** all three configured plugin names are registered and their root configuration is valid
- **THEN** startup constructs the configured task systems, runner, and harness

#### Scenario: Workflow attempts plugin override
- **WHEN** workflow YAML contains a task, runner, or harness plugin selector
- **THEN** strict workflow validation rejects the unsupported field

#### Scenario: Plugin name is unknown
- **WHEN** machine config selects an unregistered plugin name
- **THEN** startup fails and lists available registered names

### Requirement: Task system supplies parent work
The task-system contract SHALL poll active parent tasks, compile workflow filters, claim a parent, validate task configuration, ensure mailbox children, apply task configuration to a parent/mailbox target, complete a mailbox, write and find marked comments, and reset mailbox state for explicit recovery. One task-system instance SHALL be bound to each registered repo and SHALL be safe for concurrent use.

#### Scenario: Repo Poller invokes task system
- **WHEN** a Repo Poller executes
- **THEN** it calls the repo-bound task system and receives normalized parent tickets

#### Scenario: Jira implements task configuration
- **WHEN** Jira receives node task configuration with parent/task statuses
- **THEN** the Jira adapter applies those transitions without core understanding Jira status semantics

#### Scenario: Another task system uses assignment
- **WHEN** another adapter interprets node task config as an assignee change
- **THEN** core schedules the same task operation without requiring a Jira-style transition

#### Scenario: Jira completes a mailbox
- **WHEN** core requests mailbox completion
- **THEN** the Jira adapter moves the subtask to `Done` idempotently

#### Scenario: Recovery checks cancellation
- **WHEN** database recovery evaluates a labeled parent
- **THEN** the task adapter can determine whether the stable cancellation marker exists before a fresh run is created

### Requirement: Jira integration uses REST API v3 efficiently
The Jira adapter SHALL use a small handwritten REST API v3 client rather than ACLI. It SHALL reuse HTTP connections, paginate enhanced JQL search, request blockers with candidate fields, combine assignment with transition when the transition screen permits it, create missing mailboxes in batches of at most 50 with parent/description/label set at creation, and update an existing mailbox's description and label in one issue edit. It SHALL preserve separate durable summary, feedback, mailbox-completion, and next-node operations. Jira descriptions and comments SHALL use Atlassian Document Format. Existing stable comment-marker checks SHALL remain the comment idempotency mechanism. The client SHALL honor `429 Retry-After` and retry safe requests with bounded exponential backoff without logging credentials.

#### Scenario: Poll page includes dependency state
- **WHEN** Jira enhanced search returns candidate fields and inward issue links
- **THEN** the adapter determines blocker eligibility locally with one Jira call per search page

#### Scenario: Missing mailboxes are created together
- **WHEN** a parent needs several missing mailbox subtasks
- **THEN** the adapter creates up to 50 mailboxes in one bulk-create request with ADF descriptions and workflow labels

#### Scenario: Transition accepts assignment
- **WHEN** a mailbox transition exposes assignee on its transition screen
- **THEN** the adapter sends the transition and assignee field in one request

#### Scenario: Comment is retried
- **WHEN** a durable comment activity is retried
- **THEN** the adapter reads existing comments for the stable marker and creates an ADF comment only when the marker is absent

### Requirement: Task configuration uses one adapter-owned schema
The task plugin SHALL own one typed config that can represent supported root, repo, workflow, and node values. Core SHALL retain raw serializable values, merge root-to-node precedence, validate effective values during startup/submission, and pass effective raw values to adapter operations. Core SHALL NOT import the concrete adapter config type.

#### Scenario: Jira config spans scopes
- **WHEN** assignee is configured at root, project/component at repo, filters at workflow, and transitions at node
- **THEN** the Jira adapter validates all values through one Jira config schema

#### Scenario: Unknown node config field
- **WHEN** a Jira node contains an unknown task config key
- **THEN** strict Jira validation rejects the workflow before execution

### Requirement: Task plugins declare required repo keys explicitly
Every task factory SHALL return the YAML keys required for repo registration and SHALL derive an opaque canonical task-scope key from root and repo task config. `repo register` SHALL collect or deterministically derive required values, SHALL reject a task-scope key already used by another repo, and SHALL NOT depend on reflection or separate prompt metadata. The Jira component is derived from the Orca repo name rather than prompted.

#### Scenario: Jira repo registration
- **WHEN** the Jira factory declares `project` and `component`
- **THEN** repo registration asks for project once and derives each selected repo's component from its Orca repo name before saving

#### Scenario: Required value is absent
- **WHEN** the user omits a required repo key
- **THEN** registration fails before machine config changes

#### Scenario: Task scope is already registered
- **WHEN** a second repo resolves to the same Jira site/project/component scope as an existing repo
- **THEN** registration fails before creating a duplicate poller

### Requirement: Runner executes harnesses in ticket-scoped environments
The runner SHALL discover and validate repos, ensure exactly one ticket-scoped execution environment per run, find live terminals, close selected/run terminals while preserving the environment, start a terminal with a structured command, and clean the complete run environment when requested. All node agents in a run SHALL share that environment. The runner SHALL NOT parse workflow routes, reports, or harness-specific command syntax.

#### Scenario: Orca starts OpenCode
- **WHEN** the harness returns an OpenCode command for a node
- **THEN** the Orca runner creates or reuses the ticket worktree and starts that command in the node terminal

#### Scenario: Successive nodes share files
- **WHEN** a run moves from exploration to coding
- **THEN** both node terminals execute in the same ticket worktree while retaining separate harness sessions

#### Scenario: Runner recovers without SQLite
- **WHEN** explicit database recovery calls `CloseTerminals` using repo/workflow/ticket values
- **THEN** the runner locates and closes stale run terminals without requiring persisted runner IDs and preserves the workspace/code

#### Scenario: End cleanup is requested
- **WHEN** a workflow reaches end with runner cleanup enabled
- **THEN** the runner closes terminals and removes other run-environment resources it owns

### Requirement: Runner terminal titles are stable and minimal
Runner terminal titles SHALL contain only `<ticket>:<node>`. They SHALL NOT contain `nodeVisitID`, workflow name, agent name, or other changing metadata. Terminal identity SHALL be ticket scoped, and terminal lookup SHALL return only a live usable terminal.

#### Scenario: Node is first processed
- **WHEN** ticket `PAY-101` enters coding
- **THEN** the runner uses terminal title `PAY-101:coding`

#### Scenario: Coding is revisited
- **WHEN** the same ticket returns to coding with a new node visit ID
- **THEN** the previous coding terminal is closed, a terminal with title `PAY-101:coding` is started with stable run/node metadata, and any resumable harness session may continue

#### Scenario: Same visit is reconciled
- **WHEN** reconciliation checks the current visit and its live terminal exists
- **THEN** the runner reuses that terminal without closing or renaming it

#### Scenario: Found terminal is dead
- **WHEN** lookup finds a stale/non-usable terminal record
- **THEN** the runner treats it as absent and starts a usable terminal

### Requirement: Harness owns agent launch semantics
The harness SHALL validate agents, find prior harness sessions by stable title where supported, construct a structured executable/args/environment command with stable run/node metadata, and encode resume syntax. The runner SHALL execute the command but SHALL NOT construct it.

#### Scenario: OpenCode agent is invalid
- **WHEN** startup or workflow validation references an unavailable OpenCode agent
- **THEN** harness validation fails before the run processes that node

#### Scenario: Prior session can resume
- **WHEN** a compatible prior OpenCode session exists for the stable ticket/node title
- **THEN** the harness command may resume it with stable run/node metadata

#### Scenario: Prior session cannot resume
- **WHEN** no compatible session exists
- **THEN** the harness builds a fresh launch command and the mailbox supplies correctness context

#### Scenario: New visit resumes conversation
- **WHEN** a prior harness session can resume for a new visit
- **THEN** the harness resumes the prior conversation while internal visit identity changes independently

#### Scenario: New visit reuses a live terminal
- **WHEN** the workflow revisits a node whose OpenCode terminal is still live
- **THEN** the runner identifies the exact mailbox subtask whose comments contain the new feedback, followed by that node's rendered `nudgePrompt` when configured

#### Scenario: Same visit keeps a live terminal
- **WHEN** runtime setup repeats for the same visit and its OpenCode terminal is live
- **THEN** the runner sends no prompt

### Requirement: Runtime harness plugin owns message behavior
The runtime harness plugin SHALL read completed assistant messages, parse structured output, implement agent/HITL nudge policy, and retry report delivery. It SHALL NOT call the task system directly, write SQLite, or manage runner environments.

#### Scenario: Valid OpenCode report
- **WHEN** the OpenCode plugin observes a completed valid assistant report
- **THEN** it invokes relay-flow report delivery without calling Jira itself

#### Scenario: HITL idle
- **WHEN** a HITL assistant session is idle without a valid report
- **THEN** the plugin performs no nudge and no task-system mutation

### Requirement: Durable execution is replaceable
Core run orchestration SHALL depend on an engine-neutral `Executor` command interface and a separate `RunQueries` interface. `go-workflows` contexts, instances, backends, queues, and errors SHALL remain inside its adapter package.

#### Scenario: Run Manager starts work
- **WHEN** the Run Manager ensures a run
- **THEN** it calls the engine-neutral executor without importing go-workflows types

#### Scenario: API lists runs
- **WHEN** the server handles run list/get
- **THEN** it uses `RunQueries` without requiring command callers to implement query behavior

### Requirement: go-workflows compatibility is proven before implementation
Before production implementation, the project SHALL pin a stable `go-workflows` release and verify SQLite restart, explicit instance IDs, duplicate start behavior, separate workflow/activity workers, signals, durable timers, cancellation cleanup, history inspection, and projection access. The Go toolchain SHALL be upgraded to the selected release's minimum requirement.

#### Scenario: Compatibility spike fails a required behavior
- **WHEN** the candidate engine release cannot satisfy one required behavior
- **THEN** implementation SHALL NOT depend on an unverified assumption and the release or adapter design must be revised first

#### Scenario: Release is selected
- **WHEN** the spike passes all required behaviors
- **THEN** the exact engine version and Go toolchain are pinned in module/build configuration

### Requirement: Adapter internals remain with adapters
The Jira REST v3 client SHALL live under the Jira task adapter, the Orca CLI wrapper SHALL live under the Orca runner, and OpenCode-specific validation/command behavior SHALL live under the OpenCode harness.

#### Scenario: Alternative runner is added
- **WHEN** a new runner does not use Orca
- **THEN** it does not import the Orca CLI wrapper or OpenCode harness behavior

#### Scenario: Alternative task system is added
- **WHEN** a new task plugin does not use Jira
- **THEN** it does not import the Jira REST client or Jira config types
