# relay-flow section-9 e2e pipeline

Step-by-step recorded verification of the full flow on a dummy repo, all under `/tmp/relayflow-e2e`.
Each step is a script in this directory; run via `run-step.sh <NN-name>` which records
`asciinema` (asciicast-v2) and renders a GIF via `agg` into `/tmp/relayflow-e2e/gifs/`.
Run steps strictly in order; approve each GIF before the next.

## Prereqs

- Installed binary: `go install ./cmd/relay-flow`, with `relay-flow` available on `PATH`
- `asciinema`, `agg`, `jq`, `acli` (authed to wkengineering), `orca-ide`

## Steps

| # | script | proves |
|---|--------|--------|
| 01 | repo    | git repo + plugin in `.opencode/plugins/` |
| 02 | jira    | GHCOS ticket created, To Do, assigned; component set by the Atlassian MCP tool |
| 02b | jira-verify | component, assignee, status, labels, and subtasks verified with explicit fields |
| 03 | orca    | Orca project added (DisplayName = repo name) |
| 04 | init    | config.yaml + state.db, 0700 |
| 05 | config  | root taskConfig (assignee) + pollIntervalSeconds |
| 06 | serve   | socket + lock up, API answers |
| 07 | register| repo registered with project/component keys |
| 08 | workflow| workflow.yaml committed + submitted |
| 09 | claim   | ticket labeled wf:helloFlow, run exists |
| 10 | mailboxes| implement/verify/pr-review subtasks created |
| 11 | implement| terminal `<T>:implement` exists, agent active |
| 12 | verify  | implement mailbox Done, verify terminal up |
| 13 | hitl-wait| `<T>:pr-review` waiting, no nudge |
| 14 | hitl-input| human rejects -> routes to implement (loop) |
| 15 | loop    | implement reopened, feedback comment present |
| 16 | approve | second pass, human approves -> end |
| 17 | done    | parent Cancelled, mailboxes Done, run completed |
| 99 | teardown| stop serve, wipe run state (repo/gifs kept) |

## Jira component assignment

After step 02 creates the ticket, the E2E operator sets component `raj-test-repo` using the Atlassian MCP tool (`WAtlassian.editjiraissue`). Do not ask the user to assign it manually.

## Known gaps (flagged, not built)

- acli cannot set Jira components; the E2E operator sets the component through the Atlassian MCP tool after step 02.

## Section 14 execution checklist

1. Confirm Sections 10-13 and both full suites are green.
2. Harden every E2E script to assert its exact promised outcome and fail on timeout or mismatch.
3. Stop serve safely with `RELAY_FLOW_HOME=/tmp/relayflow-e2e/home relay-flow stop`; never use `pkill`.
4. Ask the user to remove the Orca project, then delete `/tmp/relayflow-e2e`.
5. Run recorded steps strictly in order: `00-setup`, `01-repo`, `02-jira`.
6. Set Jira component `raj-test-repo` through the Atlassian MCP tool, then run `02b-jira-verify` with explicit fields.
7. Continue `03-orca` through `09-claim`, checking every cast, GIF, and serve log.
8. Run `10-mailboxes`; require exactly the implement, verify, and pr-review mailboxes.
9. Run `11-implement`; require the exact stable tab title from `visualLayouts`, an active OpenCode process, successful session registration, and no returned shell.
10. Run `12-verify`; verify the implement report/effects and transition to verify.
11. Run `13-hitl-wait`; verify HITL waits silently without a nudge.
12. Run `14-hitl-input`; send human rejection through the Orca terminal.
13. Run `15-loop`; verify mailbox/session reuse, a fresh visit ID, and correct feedback.
14. Run `16-approve`, then `17-done`; verify parent is Cancelled, mailboxes are Done, and the run completed.
15. Capture and sanitize real Jira, Orca, and OpenCode contracts for strict fixtures.
16. Run `go test ./...`, `cd plugin && bun test`, and all strict contract tests with no failures or skipped assertions.
17. Run `99-teardown` to stop serve safely and clean temporary E2E run state.
18. Mark Section 14 complete only after the complete live pipeline and final suites pass.

On any mismatch: stop serve safely, add a numbered `14.x` task to `tasks.md`, commit only `tasks.md`, notify the foreman, and pause E2E.
