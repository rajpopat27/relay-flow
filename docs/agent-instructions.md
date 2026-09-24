# Relay-flow Runtime Agent Instructions

These instructions are for agents executing workflow nodes through relay-flow.
They are separate from the repository-maintainer and orchestration instructions
in the root `AGENTS.md`.

## Before doing work

1. For Jira, read only the assigned mailbox description on the first visit
   (`acli jira workitem view "<mailbox>" --fields "summary,description" --json`).
   Read the parent Jira ticket only when this node needs its details.
2. On Jira continuation, read only the newest mailbox comment
   (`acli jira workitem comment list --key "<mailbox>" --limit 1 --order "-created" --json`).
   For Beads, read the parent ticket and current mailbox description/comments
   with the configured Beads tools.
3. Work only in the ticket-scoped worktree supplied by relay-flow. All nodes in
   one run share that worktree.
4. Inspect existing changes before editing. Preserve correct work from earlier
   nodes and address the selected feedback.
5. Run relevant tests, linters, builds, or other verification before reporting.
6. Do not manually change workflow labels, parent status, mailbox status, or
   graph routing. Relay-flow and the task system own those transitions.

## Task-system access

Use the configured task-system tools for the reads required by this node.
For Beads, use `bd` with the configured Beads workspace to read the parent and
current mailbox. For Jira, use the compact `acli` mailbox reads above, and
read the parent only when the node description asks for it. Do not access relay-flow's SQLite state,
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
Return the four-field contract below with the labels spelled exactly as shown.
Do not wrap it in a Markdown code fence and do not return JSON.

`STATUS` describes the result of work at the current workflow node. It is not
Jira or Beads status. `NEXT STEP` must exactly match one configured route for
the reported status. Route `when` text is explanatory; it is not evaluated as
a condition by relay-flow.

`SUMMARY` briefly describes the current node's work. `FEEDBACK` goes only
to the selected next work node; include actionable details when needed. If
`NEXT STEP` is `end`, `FEEDBACK` must be exactly `None`. The plugin maps the
concise fields into the existing JSON report shape using `None` for unused
subsections.

```text
STATUS: success | failure
NEXT STEP: <one valid route>
SUMMARY: <concise result>
FEEDBACK: <concise handoff, or None when NEXT STEP is end>
```

Report only after the current node's work is complete or intentionally blocked.
If output is invalid, the runtime plugin will request the report contract
again. On a HITL node that request is sent only when the output is non-empty
but invalid; missing output stays silent, and no report is delivered until the
human approves a valid report through the harness UI.
