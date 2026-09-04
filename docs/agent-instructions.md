# Relay-flow Runtime Agent Instructions

These instructions are for agents executing workflow nodes through relay-flow.
They are separate from the repository-maintainer and orchestration instructions
in the root `AGENTS.md`.

## Before doing work

1. Read the parent ticket for the original requirement and acceptance criteria.
2. Read the current node mailbox description and its comments. The mailbox is
   the source of node-specific instructions and feedback from prior nodes.
3. Work only in the ticket-scoped worktree supplied by relay-flow. All nodes in
   one run share that worktree.
4. Inspect existing changes before editing. Preserve correct work from earlier
   nodes and address the selected feedback.
5. Run relevant tests, linters, builds, or other verification before reporting.
6. Do not manually change workflow labels, parent status, mailbox status, or
   graph routing. Relay-flow and the task system own those transitions.

## Task-system access

Use the configured task-system tools to read the parent ticket and current
mailbox. For Beads, use `bd` with the configured Beads workspace. For Jira,
use the configured Jira integration. Do not access relay-flow's SQLite state,
write JSONL report files, or invent task-system identifiers.

Only read the current mailbox for node feedback. Do not use sibling mailboxes
as a source of instructions unless the current node explicitly directs you to
do so.

## Work and review behavior

- Make the smallest change that satisfies the parent ticket and current node.
- Keep unrelated files and behavior unchanged.
- Preserve existing worktree changes made by earlier nodes.
- Do not claim a commit that does not exist.
- On a review node, inspect the implementation, tests, and verification results.
- If changes are required, put the requested changes in `FEEDBACK` and select
  the implementation route configured for the review node.
- A human review node is a decision point. Do not treat a human rejection as a
  task-system status change; select the configured implementation route.

## Report rules

The runtime plugin parses one plain-text report from the assistant response.
Return the complete contract below with the labels spelled exactly as shown.
Do not wrap it in a Markdown code fence and do not return JSON.

`STATUS` describes the result of work at the current workflow node. It is not
Jira or Beads status. `NEXT STEP` must exactly match one configured route for
the reported status. Route `when` text is explanatory; it is not evaluated as
a condition by relay-flow.

`SUMMARY` describes the current node's work. `FEEDBACK` is for the selected
next work node only. Use the literal `None` for an intentionally empty field.
If `NEXT STEP` is `end`, every feedback field must be `None`.

```text
STATUS: success | failure
NEXT STEP: <one configured route>

SUMMARY:
COMPLETED: <what was completed>
COMMITS: <commit IDs or None>
NOT COMPLETED: <remaining work or None>
ISSUES DISCOVERED: <issues or None>
VERIFICATION: <commands and results>
NOTES: <notes or None>

FEEDBACK:
REASON FOR NEXT STEP: <reason or None>
REQUIRED ACTIONS: <actions or None>
RELEVANT CONTEXT: <context or None>
EXPECTED RESULT: <expected result or None>
```

Report only after the current node's work is complete or intentionally blocked.
If output is invalid, the runtime plugin will request the report contract
again for an agent node. HITL nodes remain silent until valid output is
approved by the human through the harness UI.
