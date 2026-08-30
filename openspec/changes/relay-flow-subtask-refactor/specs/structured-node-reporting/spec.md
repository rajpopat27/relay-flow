## ADDED Requirements

### Requirement: Every node report follows the complete contract
Every agent and HITL completion report SHALL contain `STATUS`, `NEXT STEP`, all `SUMMARY` subsections including `COMMITS`, and all `FEEDBACK` subsections. `COMMITS` SHALL contain the relevant commit IDs or `None`. Empty content SHALL be represented by the literal `None`; required sections SHALL NOT be omitted.

```text
STATUS: success | failure
NEXT STEP: <one valid node name>

SUMMARY:
COMPLETED:
COMMITS:
NOT COMPLETED:
ISSUES DISCOVERED:
VERIFICATION:
NOTES:

FEEDBACK:
REASON FOR NEXT STEP:
REQUIRED ACTIONS:
RELEVANT CONTEXT:
EXPECTED RESULT:
```

#### Scenario: Complete report
- **WHEN** the assistant returns every field with a supported status and legal next step
- **THEN** the harness plugin parses it into the JSON report model

#### Scenario: Missing summary subsection
- **WHEN** the assistant omits `VERIFICATION`
- **THEN** the report is invalid and is not submitted as a completed node result

#### Scenario: Missing commit identity
- **WHEN** the assistant omits `COMMITS`
- **THEN** the report is invalid and is not submitted as a completed node result

#### Scenario: Empty subsection
- **WHEN** the node discovered no issues
- **THEN** it reports `ISSUES DISCOVERED: None` rather than omitting the subsection

### Requirement: Status and next step are graph-validated
`STATUS` SHALL be `success` or `failure`. `NEXT STEP` SHALL name exactly one target configured for that status on the current node. The mailbox description SHALL list legal next steps and their `when` explanations.

#### Scenario: Valid failure route
- **WHEN** coding reports failure and selects a configured coding retry target
- **THEN** report validation accepts the selection

#### Scenario: Target exists under wrong outcome
- **WHEN** the report status is failure but the selected target is legal only for success
- **THEN** validation rejects the report and does not persist a route

#### Scenario: Unknown target
- **WHEN** `NEXT STEP` names a node not configured for the current status
- **THEN** validation rejects the report and returns the legal choices

### Requirement: End reports have no feedback recipient
When `NEXT STEP` is reserved `end`, every feedback subsection SHALL equal `None`. The current-node summary SHALL remain required.

#### Scenario: Valid end report
- **WHEN** the final node selects `end`, includes its full summary, and sets all feedback fields to `None`
- **THEN** validation accepts the report

#### Scenario: End report attempts feedback
- **WHEN** the final node selects `end` with required actions for a next agent
- **THEN** validation rejects the report because `end` has no mailbox or agent

### Requirement: Report transport uses JSON
`relay-flow report` SHALL read one JSON object from standard input and SHALL send it to the server over the Unix socket. Exact top-level keys SHALL be lower-camel `runId`, `node`, `reportId`, and `report`; nested report keys SHALL be `status`, `nextStep`, `summary`, and `feedback`, with lower-camel subsection keys. Multiline summary and feedback SHALL NOT be passed as command-line flags.

#### Scenario: Multiline feedback
- **WHEN** feedback contains several Markdown lines and punctuation
- **THEN** JSON transport preserves the complete text without shell parsing

#### Scenario: Invalid JSON
- **WHEN** `relay-flow report` receives malformed JSON
- **THEN** it fails without sending a partial report

#### Scenario: Incorrect wire-key casing
- **WHEN** report JSON uses `runID` or `reportID`
- **THEN** strict request decoding rejects the payload

### Requirement: Acknowledgement follows durable signal persistence
The server SHALL acknowledge a current-visit report only after the embedded durable engine has persisted the report signal. A report not durably accepted SHALL receive no success acknowledgement.

#### Scenario: Server crashes before persistence
- **WHEN** the server dies before the report signal is stored
- **THEN** the plugin receives no acknowledgement and retries the same report after recovery

#### Scenario: Server crashes after persistence
- **WHEN** the signal is stored but the server dies before returning acknowledgement
- **THEN** the plugin retries and the duplicate handling prevents graph advancement twice

### Requirement: Duplicate reports are durably harmless
The plugin SHALL derive `reportId` from harness session/message identity (the OpenCode session and assistant-message IDs for the built-in harness). Before graph transition effects, the workflow SHALL durably remember every consumed ID; the SQLite receipt SHALL store only the ID and exact internal visit. If an ID is already processed, the server SHALL immediately return an accepted duplicate acknowledgement without validating, comparing, loading, or signaling its payload. A same-ID signal racing before the receipt update SHALL be ignored by replay-safe workflow state. The plugin SHALL NOT access SQLite.

#### Scenario: Duplicate arrives while visit is current
- **WHEN** the same valid report ID is delivered twice before the workflow advances
- **THEN** the workflow consumes one report and the duplicate causes no repeated comments, task changes, or runner work

#### Scenario: Duplicate arrives after advancement
- **WHEN** a plugin retries the same report ID after the run has moved to another visit
- **THEN** the server returns an accepted duplicate acknowledgement and does not signal new work

#### Scenario: Duplicate payload differs
- **WHEN** an existing report ID is resubmitted with different text
- **THEN** the server ignores the request body and returns an accepted duplicate acknowledgement

### Requirement: Unacknowledged reports retry quietly
The harness plugin SHALL maintain at most one unacknowledged delivery per `runId` and node. While one is pending, later report attempts for that run/node SHALL be ignored. It SHALL retry the exact parsed JSON with exponential backoff, 20-percent jitter, and a 5-minute cap until acknowledged. Delivery retries SHALL NOT create another LLM turn.

#### Scenario: Server is unavailable
- **WHEN** a valid report cannot reach the server
- **THEN** the plugin retries quietly using the shared backoff schedule without nudging the agent

#### Scenario: Plugin restarts
- **WHEN** the plugin restarts and can reread the valid assistant message for the same visit
- **THEN** it may submit the report again and duplicate handling keeps the run unchanged if already accepted

### Requirement: Agent nodes are nudged for invalid output
For a normal agent node, an idle completed assistant response that lacks a valid contract SHALL cause the runtime harness plugin to send a fixed correction containing every required report label to the same session. Workflow `nudgePrompt` text SHALL NOT define invalid-output behavior.

#### Scenario: Agent omits report contract
- **WHEN** an agent node becomes idle after returning ordinary prose without the contract
- **THEN** the plugin sends that session the complete required contract

#### Scenario: Agent response was aborted
- **WHEN** the last assistant response has no completed finish reason
- **THEN** the plugin does not submit or parse it as a report

### Requirement: HITL reports require explicit approval
For a HITL node, invalid or missing output without approval SHALL cause no automatic nudge and no report. A valid report without approval SHALL cause one correction directing the assistant to show it in OpenCode's Question tool with `Approve` and `Reject` options. Only an explicit `Approve` answer SHALL authorize delivery. If the completed output after approval is invalid, the plugin SHALL ask the assistant to regenerate the complete valid report. `Reject` or Question rejection SHALL clear authorization, submit nothing, and SHALL NOT become a failure outcome; any later report requires a new Question and approval. Aborted output SHALL remain silent.

#### Scenario: Human pauses collaboration
- **WHEN** a HITL session is idle while the human is away and no valid report exists
- **THEN** the plugin remains silent

#### Scenario: Human approves work
- **WHEN** the human explicitly selects `Approve` and the agent returns the complete valid contract
- **THEN** the plugin submits it and the durable run advances normally

#### Scenario: Human rejects proposed report
- **WHEN** the human selects `Reject` or rejects the Question
- **THEN** the plugin submits no report, clears authorization, and a later report must be presented through a new Question

#### Scenario: Valid report lacks approval
- **WHEN** a HITL assistant completes a valid report without a matching `Approve`
- **THEN** the plugin does not submit it and directs the assistant to present it through the Question tool

#### Scenario: Approved output is invalid
- **WHEN** the human approved and the subsequent completed assistant output does not contain the complete contract
- **THEN** the plugin does not submit it and directs the assistant to regenerate the valid report

### Requirement: Node visit identity remains internal
relay-flow SHALL generate `nodeVisitID` for durable workflow waits, activity fencing, and external-effect markers. It SHALL NOT inject it into the harness environment or require it in report JSON. The LLM SHALL NOT generate or infer it.

#### Scenario: Harness launches a visit
- **WHEN** relay-flow processes a node
- **THEN** the harness receives stable run and node metadata but no node visit ID

#### Scenario: Node is revisited
- **WHEN** the workflow returns to the same node
- **THEN** relay-flow creates a new internal visit while the terminal title and mailbox remain stable

### Requirement: Runtime plugin metadata is explicit
Each harness launch SHALL provide `RELAY_FLOW_RUN_ID`, `RELAY_FLOW_WORKFLOW`, `RELAY_FLOW_REPO`, `RELAY_FLOW_TICKET`, `RELAY_FLOW_NODE`, `RELAY_FLOW_NODE_TYPE`, `RELAY_FLOW_NUDGE_PROMPT`, and `RELAY_FLOW_NEXT_STEPS_JSON`. The next-steps JSON SHALL contain legal targets and their explanations.

The runtime plugin SHALL register an emitted harness session using exactly `{runId, node, sessionId}`. Relay-flow SHALL persist that session ID, and normal healthy-database execution SHALL use the persisted ID to resume the harness session. Runtime registration SHALL NOT contain `nodeVisitID`.

#### Scenario: Agent visit starts
- **WHEN** an agent node terminal starts
- **THEN** the runtime plugin can identify the run/node and parse the report without querying another system

#### Scenario: HITL visit starts
- **WHEN** a HITL node terminal starts
- **THEN** node type metadata tells the plugin to remain silent when output is absent or invalid

#### Scenario: Harness emits a session
- **WHEN** the runtime plugin receives a harness session event
- **THEN** it registers `{runId, node, sessionId}` and relay-flow persists the session ID for normal resume
