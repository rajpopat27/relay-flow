# jira-workflow

Keeps Jira tickets moving by dispatching opencode agents: an opencode **plugin** that reports each agent's final STATUS/SUMMARY block, and an **`orca-jira-loop` CLI daemon** that posts comments and transitions tickets per workflow config.

```
agent reply ──▶ opencode plugin (reports STATUS/SUMMARY) ──▶ orca-jira-loop CLI ──▶ Jira
```

## Components

| Component | Location | Role |
|---|---|---|
| opencode plugin | `plugin/report-status.ts` | Watches for a finished agent reply, parses STATUS/SUMMARY, calls the CLI |
| CLI daemon | `cli/` | Polls each workflow JQL, dispatches agents into Orca terminals, posts comments + transitions tickets |
| Workflow config | `.workflow/workflow.yaml` | Poll interval plus one or more JQL-selected workflows, each with agents, handles, outcomes, and closeOn |

## Install from npm

Both components are published as npm packages (no manual copy needed).

### 1. Plugin — `jira-workflow`

Add to `opencode.json`:

```json
{
  "plugin": ["jira-workflow"]
}
```

Restart opencode — it auto-installs the package and loads the plugin on startup.

### 2. CLI — `jira-workflow-cli`

```sh
npx jira-workflow-cli --help   # first run builds the Go binary, then runs it
```

Or install it permanently:

```sh
npm install -g jira-workflow-cli   # provides `orca-jira-loop` on your PATH
```

## The opencode plugin (`report-status.ts`)

An opencode plugin that watches for a finished agent reply, parses its STATUS/SUMMARY block, and reports the result back to Jira via the `orca-jira-loop` CLI.

### Protocol

The agent must end its reply with one of:

```
STATUS: done
SUMMARY: <at least 10 words describing what was done>
```

or a single-line variant:

```
STATUS: done SUMMARY: everything is implemented and ready for review now
```

Both bolded labels (`**STATUS:**`) and code fences are accepted. Parsing is done by the exported `parseStatusBlock()`, which normalizes lowercase statuses, trailing punctuation, bullets/tabs, and multi-word statuses.

### What the plugin does

On `session.idle`:

1. Extracts the STATUS/SUMMARY block from the agent's last reply.
2. Normalizes status + summary and calls:

```sh
orca-jira-loop report --config workflow --workflow taskDevelopment --ticket <key> --agent <name> \
  --status done --summary "..."
```

3. The CLI posts a Jira comment with the summary and transitions the ticket per the workflow's `outcomes` mapping.
4. If the comment + transition land, the ticket moves to the next status. The agent's terminal is **kept alive** — if the ticket later bounces back to one of this agent's `handles` statuses, the daemon nudges the same session (configurable per agent via `nudgePrompt`) so it continues with full context. Terminals are closed by the daemon only when the ticket reaches a status in the workflow's `closeOn`.

Statuses other than `done` (e.g. `blocked`) still post the comment and transition per `outcomes`.

## The CLI daemon (`orca-jira-loop`)

### Install

```sh
cd cli
go install ./...   # installs to $(go env GOPATH)/bin/orca-jira-loop
```

### Server mode

Instead of one `run` process per config, a central server hosts many:

```sh
orca-jira-loop serve            # start central process (daemonizes)
orca-jira-loop submit workflow  # from inside the repo; validates + starts
orca-jira-loop list
orca-jira-loop remove workflow
```

`report` (plugin's Jira gateway) is unchanged and never talks to the
server. See `cli/README.md` for details.

### Config

Workflow config lives at `.workflow/<config-name>.yaml` in the working directory (the daemon reads it relative to cwd). Example (`workflow.yaml`):

```yaml
pollIntervalSeconds: 15

workflows:
  taskDevelopment:
    jql: project = KCC AND issuetype = Task
    closeOn:
      - Done
    agents:
      plan:
        handles:
          - status: "Ready for dev"
            outcomes:
              done: "In Progress"
              blocked: "Ready for dev"
          - status: "In Review"
            outcomes:
              done: Done
              blocked: "Ready for dev"
        nudgePrompt: "Ticket {{ticket}} is back in '{{status}}'. Run `acli jira workitem view {{ticket}} --fields summary,description,comment --json` to read the latest feedback and revise the plan. End with STATUS/SUMMARY as before."
      build:
        handles:
          - status: "In Progress"
            outcomes:
              done: "In Review"
              blocked: "Ready for dev"

  incidentResponse:
    jql: project = KCC AND issuetype = Incident
    closeOn:
      - Done
    agents:
      investigate:
        handles:
          - status: "In Progress"
            outcomes:
              done: "In Review"
              blocked: "In Progress"
```

`handles` is a list of `{status, outcomes}` entries — one per Jira status the agent serves, each with its own outcome map. This lets one agent handle multiple statuses with different targets (plan's `done` → In Progress from "Ready for dev", but → Done from "In Review"). An outcome target equal to the current status is a self-loop: the report comment is posted but no Jira transition is attempted (Jira has no self-transitions). `closeOn` accepts either a scalar (`closeOn: Done`) or a list; lists are canonical.

## Tests

Tests live alongside the plugin in the consuming repo: `node --test .opencode/plugin/report-status.test.ts` (27 cases covering one-line, two-line, bold, fenced, lowercase, prose-before-status, 10-word boundary, multi-word status, trailing punctuation)

## Keep in sync

The plugin and the CLI are versioned together in this repo: the CLI flags (`--config`, `--workflow`, `--status`, `--summary`) and the plugin's output must stay in lockstep. If you change the parsing protocol, update the CLI flag contract in `cli/` and the tests.
