## ADDED Requirements

### Requirement: Every node report follows the complete contract
Every agent and HITL completion report SHALL contain `STATUS`, `NEXT STEP`, all `SUMMARY` subsections, and all `FEEDBACK` subsections. Empty content SHALL be represented by the literal `None`; required sections SHALL NOT be omitted.

```text
STATUS: success | failure
NEXT STEP: <one valid node name>

SUMMARY:
COMPLETED:
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

#### Scenario: Empty subsection
- **WHEN** the node discovered no issues
- **THEN** it reports `ISSUES DISCOVERED: None` rather than omitting the subsection

### Requirement: Status and next step are graph-validated
`STATUS` SHALL be `success` or `failure`. `NEXT STEP` SHALL name exactly one target configured for that status on the current node. The prompt SHALL list legal next steps and their `when` explanations.

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
`relay-flow report` SHALL read one JSON object from standard input and SHALL send it to the server over the Unix socket. Exact top-level keys SHALL be lower-camel `runId`, `nodeVisitId`, and `report`; nested report keys SHALL be `status`, `nextStep`, `summary`, and `feedback`, with lower-camel subsection keys. Multiline summary and feedback SHALL NOT be passed as command-line flags.

#### Scenario: Multiline feedback
- **WHEN** feedback contains several Markdown lines and punctuation
- **THEN** JSON transport preserves the complete text without shell parsing

#### Scenario: Invalid JSON
- **WHEN** `relay-flow report` receives malformed JSON
- **THEN** it fails without sending a partial report

#### Scenario: Incorrect wire-key casing
- **WHEN** report JSON uses `runID` or `nodeVisitID`
- **THEN** strict request decoding rejects the payload

### Requirement: Acknowledgement follows durable signal persistence
The server SHALL acknowledge a current-visit report only after the embedded durable engine has persisted the report signal. A report not durably accepted SHALL receive no success acknowledgement.

#### Scenario: Server crashes before persistence
- **WHEN** the server dies before the report signal is stored
- **THEN** the plugin receives no acknowledgement and retries the same report after recovery

#### Scenario: Server crashes after persistence
- **WHEN** the signal is stored but the server dies before returning acknowledgement
- **THEN** the plugin retries and the duplicate handling prevents graph advancement twice

### Requirement: Duplicate reports are harmless without a dedup store
The workflow SHALL consume only the first report for a current node visit. Any report whose `nodeVisitID` is not current SHALL be acknowledged as an old or stale delivery and ignored. The system SHALL NOT add a report-deduplication table, payload hash, JSONL outbox, or plugin SQLite access.

#### Scenario: Duplicate arrives while visit is current
- **WHEN** the same valid report signal is persisted twice before the workflow advances
- **THEN** the workflow consumes one report and the unused duplicate causes no repeated comments, task changes, or runner work

#### Scenario: Duplicate arrives after advancement
- **WHEN** a plugin retries a report after the run has moved to another visit
- **THEN** the server returns an accepted duplicate acknowledgement and does not signal new work

#### Scenario: Duplicate payload differs
- **WHEN** an old visit is resubmitted with different text
- **THEN** the visit is still treated as old and ignored because graph progression, not payload comparison, is authoritative

#### Scenario: Visit ID never belonged to run
- **WHEN** a report contains a node visit ID that is not current for the addressed run
- **THEN** the server acknowledges and ignores it without changing any workflow or external state

### Requirement: Unacknowledged reports retry quietly
The harness plugin SHALL maintain at most one delivery retry loop per `nodeVisitID`. It SHALL retry the exact parsed JSON with exponential backoff, 20-percent jitter, and a 5-minute cap until acknowledged or recognized as an old visit. Delivery retries SHALL NOT create another LLM turn.

#### Scenario: Server is unavailable
- **WHEN** a valid report cannot reach the server
- **THEN** the plugin retries quietly using the shared backoff schedule without nudging the agent

#### Scenario: Plugin restarts
- **WHEN** the plugin restarts and can reread the valid assistant message for the same visit
- **THEN** it may submit the report again and duplicate handling keeps the run unchanged if already accepted

### Requirement: Agent nodes are nudged for invalid output
For a normal agent node, an idle completed assistant response that lacks a valid contract SHALL cause the runtime harness plugin to send the configured node nudge or default nudge to the same session.

#### Scenario: Agent omits report contract
- **WHEN** an agent node becomes idle after returning ordinary prose without the contract
- **THEN** the plugin nudges that session with the required contract and legal next steps

#### Scenario: Agent response was aborted
- **WHEN** the last assistant response has no completed finish reason
- **THEN** the plugin does not submit or parse it as a report

### Requirement: HITL nodes remain silent without valid output
For a HITL node, an idle response without a valid contract SHALL cause no automatic nudge and no report. A valid human-guided report SHALL use the normal delivery path.

#### Scenario: Human pauses collaboration
- **WHEN** a HITL session is idle while the human is away and no valid report exists
- **THEN** the plugin remains silent

#### Scenario: Human approves work
- **WHEN** the human-guided agent returns the complete valid contract
- **THEN** the plugin submits it and the durable run advances normally

### Requirement: Node visit identity is relay-flow metadata
relay-flow SHALL generate `nodeVisitID`, inject it into the harness launch environment, and require it in report JSON. The LLM SHALL NOT generate or infer it, and harness-specific message IDs SHALL NOT become core report identity.

#### Scenario: Harness launches a visit
- **WHEN** relay-flow processes a node
- **THEN** the harness receives the run and node visit IDs as environment/report metadata

#### Scenario: Node is revisited
- **WHEN** the workflow returns to the same node
- **THEN** the harness launch receives a new visit ID even though the terminal title and mailbox remain stable

### Requirement: Runtime plugin metadata is explicit
Each harness launch SHALL provide `RELAY_FLOW_RUN_ID`, `RELAY_FLOW_NODE_VISIT_ID`, `RELAY_FLOW_WORKFLOW`, `RELAY_FLOW_REPO`, `RELAY_FLOW_TICKET`, `RELAY_FLOW_NODE`, `RELAY_FLOW_NODE_TYPE`, `RELAY_FLOW_NUDGE_PROMPT`, and `RELAY_FLOW_NEXT_STEPS_JSON`. The next-steps JSON SHALL contain legal targets and their explanations.

#### Scenario: Agent visit starts
- **WHEN** an agent node terminal starts
- **THEN** the runtime plugin can identify the run/visit, parse legal next steps, and use the rendered nudge without querying another system

#### Scenario: HITL visit starts
- **WHEN** a HITL node terminal starts
- **THEN** node type metadata tells the plugin to remain silent when output is absent or invalid
