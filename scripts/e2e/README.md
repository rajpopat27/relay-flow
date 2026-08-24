# relay-flow section-9 e2e pipeline

Step-by-step recorded verification of the full flow on a dummy repo, all under `/tmp/relayflow-e2e`.
Each step is a script in this directory; run via `run-step.sh <NN-name>` which records
`asciinema` (asciicast-v2) and renders a GIF via `agg` into `/tmp/relayflow-e2e/gifs/`.
Run steps strictly in order; approve each GIF before the next.

## Prereqs

- Built binary: `go build -buildvcs=false -o /tmp/relayflow-e2e/relay-flow ./cmd/relay-flow`
- `asciinema`, `agg`, `jq`, `acli` (authed to wkengineering), `orca-ide`
- `export JIRA_API_TOKEN=...` (raw API token for the component-set REST call in step 02)

## Steps

| # | script | proves |
|---|--------|--------|
| 01 | repo    | git repo + plugin in `.opencode/plugins/` |
| 02 | jira    | GHCOS ticket created, component set, To Do, assigned |
| 03 | orca    | Orca project added (DisplayName = repo name) |
| 04 | init    | config.yaml + state.db, 0700 |
| 05 | config  | root taskConfig (assignee) + pollIntervalSeconds |
| 06 | serve   | socket + lock up, API answers |
| 07 | register| repo registered with project/component keys |
| 08 | workflow| workflow.yaml committed + submitted |
| 09 | claim   | ticket labeled wf:hello-flow, run exists |
| 10 | mailboxes| implement/verify/pr-review subtasks created |
| 11 | implement| terminal `<T>:implement` exists, agent active |
| 12 | verify  | implement mailbox Done, verify terminal up |
| 13 | hitl-wait| `<T>:pr-review` waiting, no nudge |
| 14 | hitl-input| human rejects -> routes to implement (loop) |
| 15 | loop    | implement reopened, feedback comment present |
| 16 | approve | second pass, human approves -> end |
| 17 | done    | parent Done, mailboxes Done, run completed |
| 99 | teardown| stop serve, wipe run state (repo/gifs kept) |

## Known gaps (flagged, not built)

- No `--debug`/verbose logging in serve; observation is via acli state + `orca terminal show/read` + `run list/get`.
- acli cannot set Jira components; step 02 uses REST with `JIRA_API_TOKEN`.
