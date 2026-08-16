# relay

Graph-based agent workflow engine. Tickets are tokens moving across **nodes**; each node has an **agent** (an OpenCode agent); edges (`onSuccess`/`onFailure`) decide where the token goes next. The tracker (Jira built-in; beads/Linear/GitHub pluggable) is just the scoreboard.

```
agent reply ──▶ opencode plugin (parses STATUS/SUMMARY) ──▶ relay report ──▶ relay serve ──▶ tracker
```

Full design: [docs/relay-v4-plan.md](docs/relay-v4-plan.md). Visual tour: `docs/v4-architecture.md`, `docs/v4-animation.html`.

## Components

| Component | Location | Role |
|---|---|---|
| opencode plugin | `plugin/report-status.ts` | Parses STATUS/SUMMARY deterministically, calls `relay report` |
| relay CLI | `cli/` | `serve` (poll loops), `submit`, `report` (thin socket client), `init` |
| Workflow config | `.workflow/workflow.yaml` | One workflow per file: nodes/when/edges/closeOn + adapter specs |

## Workflow YAML

```yaml
name: xyzTaskFlow
pollIntervalSeconds: 15

tasks:
  type: jira
  config:
    query: project = ABCD
    issueTypes: [Task]
    assigneeIsAgent: true        # or false → assignee from `relay init`

runner:
  type: orca

closeOn: [done]

nodes:
  coding:
    agent: build
    when: "In Progress"          # tracker state routing to this node
    onSuccess: reviewing
    onFailure: coding            # self-loop: comment only
  reviewing:
    agent: build                 # same agent may serve many nodes
    when: "In Review"
    onSuccess: done
    onFailure: coding
  done:
    when: "Done"                 # no agent → terminal (must be in closeOn)
```

Rules: one node = one tracker state (`when` unique); agent nodes need both edges; terminal nodes have no agent and must be in `closeOn`. Machine identity never lives in YAML — `relay init --assignee "<display name>"` writes `~/.relay/config.yaml` (0600).

## Usage

```sh
relay init --assignee "Jane Doe"     # once per machine
relay serve                          # central process (~/.relay/)
relay submit -f .workflow/workflow.yaml
relay stop serve
```

Server artifacts: `~/.relay/` (`server.sock`, `server.lock`, `server.log`, `plugin.log`). Single instance via flock; restart = resubmit (stateless — tracker is the source of truth; claim labels `wf:<name>` survive restarts and drive bounce recovery).

## Extending

New tracker (beads, Linear, GitHub): implement `tasks.Tasks` (`List`/`Claim`/`Report`) + `tasks.Register("name", factory)` in your fork — see `cli/internal/tasks/jira/`. New execution backend (tmux, ...): implement `runner.Runner` (`Spawn`/`Find`/`Nudge`/`Close`) — see `cli/internal/runner/orca/`.
