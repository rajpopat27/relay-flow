# jira-workflow

Keeps Jira tickets moving by dispatching opencode agents: an opencode **plugin** that reports each agent's final STATUS/SUMMARY block, and an **`orca-jira-loop` CLI daemon** that posts comments and transitions tickets per a workflow config.

```
agent reply ──▶ opencode plugin (reports STATUS/SUMMARY) ──▶ orca-jira-loop CLI ──▶ Jira
```

## Components

| Component | Location | Role |
|---|---|---|
| opencode plugin | `plugin/report-status.ts` | Watches for a finished agent reply, parses STATUS/SUMMARY, calls the CLI |
| CLI daemon | `cli/` | Polls Jira, dispatches agents into Orca terminals, posts comments + transitions tickets |
| Workflow config | `.workflow/workflow.yaml` | JQL query, issue-type workflows, agent statuses, `close_on_statuses` |

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

### Install

Put `plugin/report-status.ts` in `.opencode/plugin/` at the repo root (opencode auto-loads every plugin in that directory — do **not** also list it in `opencode.json` or it registers twice and posts every comment twice).

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
orca-jira-loop report --workflow <name> --ticket <key> --agent <name> \
  --status done --summary "..."
```

3. The CLI posts a Jira comment with the summary and transitions the ticket per the workflow mapping in `.workflow/<name>.yaml`.
4. If the comment + transition land, the ticket moves to the next status. The agent's terminal is **kept alive** — if the ticket later bounces back to one of this agent's statuses, the daemon nudges the same session (configurable per agent via `nudge_prompt`) so it continues with full context. Terminals are closed by the daemon only when the ticket reaches a status in the issue type's `close_on_statuses`.

Statuses other than `done` (e.g. `blocked`) still post the comment and transition per `jira_status_on`.

## The CLI daemon (`orca-jira-loop`)

### Install

```sh
cd cli
go install ./...   # installs to $(go env GOPATH)/bin/orca-jira-loop
```

### Config

Workflow config lives at `.workflow/<workflow-name>.yaml` in the working directory (the daemon reads it relative to cwd). Example (`workflow.yaml`):

```yaml
jql: project = KCC
poll_interval_seconds: 15

workflows:
  Task:
    statuses:
      "Ready for dev": plan
      "In Progress": build
    # Terminals are closed only when the ticket reaches one of these
    # statuses. Unmapped statuses not listed here (e.g. "In Review")
    # leave terminals alive so a review bounce reuses the same session.
    close_on_statuses:
      - Done
    agents:
      plan:
        statuses:
          - name: done
            description: plan is complete and ready for implementation
        jira_status_on:
          done: In Progress
          blocked: Blocked
        # Optional. Sent into the agent's EXISTING terminal when the ticket
        # lands back on a status mapped to this agent. Placeholders:
        # {{ticket}}, {{status}}. A sensible default is used when omitted.
        nudge_prompt: "Ticket {{ticket}} is back in '{{status}}'. Run `acli jira workitem view {{ticket}} --fields summary,description,comment --json` to read the latest feedback and revise the plan. End with STATUS/SUMMARY as before."
```

## Tests

Tests live alongside the plugin in the consuming repo: `node --test .opencode/plugin/report-status.test.ts` (27 cases covering one-line, two-line, bold, fenced, lowercase, prose-before-status, 10-word boundary, multi-word status, trailing punctuation)

## Keep in sync

The plugin and the CLI are versioned together in this repo: the CLI flags (`--status`, `--summary`) and the plugin's output must stay in lockstep. If you change the parsing protocol, update the CLI flag contract in `cli/` and the tests.