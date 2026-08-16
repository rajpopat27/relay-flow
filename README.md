# relay

Graph-based agent workflow engine. Tickets are tokens moving across **nodes**; each node has an **agent** (an OpenCode agent); the node's edges (`onSuccess`/`onFailure`) decide where the token goes next. The tracker (Jira built-in) is just the scoreboard; the runner (Orca built-in) is just the playing field. Both are pluggable.

```
agent reply ──▶ opencode plugin (parses STATUS/SUMMARY) ──▶ relay report ──▶ relay serve ──▶ tracker
```

## Components

| Component | Location | Role |
|---|---|---|
| opencode plugin | `plugin/report-status.ts` | Parses STATUS/SUMMARY deterministically, calls `relay report` |
| relay CLI | `cli/` | `serve` (poll loops), `submit`, `report` (thin socket client), `init` |
| Jira adapter | `cli/internal/tasks/jira/` | Tasks implementation over `acli` — [readme](cli/internal/tasks/jira/README.md) |
| Orca adapter | `cli/internal/runner/orca/` | Runner implementation over the Orca CLI — [readme](cli/internal/runner/orca/README.md) |

## Workflow YAML (one workflow per file)

```yaml
name: xyzTaskFlow               # camelCase identity: registry key + claim label wf:<name>
pollIntervalSeconds: 15         # optional, default 15

tasks:                          # ticket-system adapter
  type: jira
  config:                       # opaque to core; validated strictly by the adapter
    query: project = ABCD       # JQL fragment (no issuetype/assignee/ORDER BY)
    issueTypes: [Task]
    assigneeIsAgent: true       # or false → assignee from `relay init` machine config

runner:                         # execution backend
  type: orca

closeOn: [done]                 # terminal nodes whose tickets close their terminals

nodes:
  coding:
    agent: build                # OpenCode agent for this node
    when: "In Progress"         # tracker state routing tickets here (unique per file)
    onSuccess: reviewing        # outcome edges — required for agent nodes
    onFailure: coding           # self-loop allowed → comment only, no transition
    nudgePrompt: "..."          # optional; {{ticket}} {{node}} templates; sane default
  reviewing:
    agent: build                # same agent may serve many nodes
    when: "In Review"
    onSuccess: done
    onFailure: coding
  done:
    when: "Done"                # no agent → terminal/human-gate node
```

Rules: one node = one tracker state (`when` unique); agent nodes need both edges; agentless nodes get no automation (claimed so other workflows skip them, but never spawned/nudged); `closeOn` alone controls terminal teardown.

Machine identity never lives in YAML (the file is committed, team-shared). Assignee lives in `~/.relay/config.yaml` via `relay init --assignee "<display name|accountId>"` (probe-validated, 0600).

## Usage

```sh
relay init --assignee "Jane Doe"     # once per machine
relay serve                          # central process (~/.relay/)
relay submit -f .workflow/workflow.yaml
relay stop serve
```

Server artifacts: `~/.relay/` (`server.sock`, `server.lock`, `server.log`, `plugin.log`). Single instance via flock. Stateless: restart = resubmit; the tracker is the source of truth, claim labels survive restarts and drive bounce recovery (poll re-finds the ticket's terminal by title `<key>:<agent>:<node>` and nudges it; dead sessions respawn).

## Development

```sh
cd cli
go test ./... -race
go install ./...
```
