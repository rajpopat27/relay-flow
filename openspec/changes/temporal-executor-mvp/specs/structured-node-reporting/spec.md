## MODIFIED Requirements

### Requirement: Acknowledgement follows durable signal persistence
The server SHALL acknowledge a current-visit report only after the configured durable executor has durably persisted the report signal. In `goworkflows` mode this is the embedded SQLite engine; in `temporal` mode this is the Temporal Server signal/history write. A report not durably accepted SHALL receive no success acknowledgement.

#### Scenario: Embedded engine persists report
- **WHEN** the `goworkflows` executor accepts a report
- **THEN** the server acknowledges only after the embedded durable engine has persisted its signal

#### Scenario: Temporal persists report
- **WHEN** the `temporal` executor accepts a report
- **THEN** the server acknowledges only after Temporal Server accepts and durably appends the signal

#### Scenario: Server crashes before persistence
- **WHEN** the server dies before the selected durable executor persists the report signal
- **THEN** the plugin receives no acknowledgement and retries the same report after recovery

#### Scenario: Server crashes after persistence
- **WHEN** the selected durable executor persists the signal but the server dies before returning acknowledgement
- **THEN** the plugin retries and duplicate handling prevents graph advancement twice

### Requirement: Duplicate reports are durably harmless
The plugin SHALL derive `reportId` from harness session/message identity (the OpenCode session and assistant-message IDs for the built-in harness). Before graph transition effects, the selected durable workflow SHALL durably remember every consumed ID; the SQLite receipt SHALL store only the ID and exact internal visit when the local projection is available. If an ID is already processed, the server SHALL immediately return an accepted duplicate acknowledgement without validating, comparing, loading, or signaling its payload. A same-ID signal racing before the receipt update SHALL be ignored by replay-safe workflow state. In Temporal mode, Temporal workflow state/history is the final duplicate authority even when the SQLite receipt is absent. The plugin SHALL NOT access SQLite.

#### Scenario: Duplicate arrives while visit is current
- **WHEN** the same valid report ID is delivered twice before the workflow advances
- **THEN** the selected durable workflow consumes one report and the duplicate causes no repeated comments, task changes, or runner work

#### Scenario: Duplicate arrives after advancement
- **WHEN** a plugin retries the same report ID after the run has moved to another visit
- **THEN** the server returns an accepted duplicate acknowledgement and does not signal new work

#### Scenario: Duplicate payload differs
- **WHEN** an existing report ID is resubmitted with different text
- **THEN** the server ignores the request body and returns an accepted duplicate acknowledgement

#### Scenario: Temporal projection receipt is missing
- **WHEN** a Temporal report was consumed but its local SQLite receipt is absent after projection loss
- **THEN** Temporal workflow state/history still prevents duplicate graph effects
