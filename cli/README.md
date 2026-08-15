# orca-jira-loop CLI

Daemon that keeps Jira tickets moving by dispatching opencode agents in Orca terminals.

## Install

```sh
cd cli
go install ./...   # installs to $(go env GOPATH)/bin/orca-jira-loop
```

## Config

Workflow config lives at `.workflow/<config-name>.yaml` in the working directory (the daemon reads it relative to cwd). Example (`workflow.yaml`):

```yaml
pollIntervalSeconds: 15

workflows:
  taskDevelopment:
    jql: project = xyz
    issueTypes: [Task]
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
        nudgePrompt: "Ticket {{ticket}} is back in '{{status}}'. Run `acli jira workitem view {{ticket}} --fields summary,description,comment --json` to read the latest feedback and continue. End with STATUS/SUMMARY as before."
      build:
        handles:
          - status: "In Progress"
            outcomes:
              done: "In Review"
              blocked: "In Progress"

  incidentResponse:
    jql: project = xyz
    issueTypes: [Incident]
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

Each workflow owns its JQL and agents. `issueTypes` (required, scalar or list) is appended to the JQL as `AND issuetype IN (...)` — workflows map to issue types, so JQL must not contain an issuetype clause. `handles` is a list of `{status, outcomes}` entries — one per Jira status the agent serves, each with its own outcome map, so one agent can report `done` with different targets depending on the ticket's current status. An outcome target equal to the current status is a self-loop: the report comment posts but no Jira transition is attempted. `closeOn` accepts a scalar or list; lists are canonical.

**Startup validation:** `run` verifies every status name referenced in the YAML (`handles`, `outcomes` targets, `closeOn`) against each workflow's Jira project — Jira's JQL parser rejects unknown statuses, so a typo like `"DO Done"` fails fast at startup instead of silently never matching.

## Usage

```sh
# Start the daemon for a config (run from the dir holding .workflow/).
# Self-daemonizes: detaches and logs to ~/.orca-jira-loop/<name>/daemon.log.
orca-jira-loop run workflow

# Foreground mode (logs to stderr AND the log file)
orca-jira-loop run --foreground workflow

# Stop it
orca-jira-loop stop workflow

# Manual agent report (normally invoked by the plugin, not by hand)
orca-jira-loop report --config workflow --workflow taskDevelopment --ticket ABCD-1234 --agent plan \
  --status done --summary "plan is complete"
```

## Server mode

One central process hosting many configs, instead of a separate `run` per
config:

```sh
# Start the central server (self-daemonizes; logs to ~/.orca-jira-loop/server.log)
orca-jira-loop serve

# Submit a config from inside the repo it governs: validates YAML + Jira
# statuses, saves a copy to ~/.orca-jira-loop/configs/<name>.yaml, starts
# a poll-loop goroutine. Re-submitting the same name restarts it.
orca-jira-loop submit workflow            # reads .workflow/workflow.yaml
orca-jira-loop submit hotfix -f other.yaml

# Inspect / stop
orca-jira-loop list
orca-jira-loop remove workflow            # stops daemon, deletes saved YAML
orca-jira-loop stop serve                 # stop the central server itself
```

`report` is unchanged: the plugin still calls it as a one-shot command from
the ticket worktree, and it talks straight to Jira — never to the server.
If `.workflow/<name>.yaml` is absent in the worktree, `report` falls back
to the server's saved copy under `~/.orca-jira-loop/configs/`.

## How it works

1. Poll each workflow's JQL every `pollIntervalSeconds`; find tickets in a dispatchable status.
2. For each, start a new Orca terminal titled `<key>:<agent>:<status>` in the ticket's worktree (created on demand). The status is part of the title because one agent can handle multiple statuses with different outcomes — each status visit gets a fresh session, so context from a prior status never leaks into the new task.
3. If a terminal with that exact title already exists, **nudge it in place** instead of respawning: send the agent's `nudgePrompt` via `orca terminal send`. A ticket bouncing back to a previously-visited status reuses that status's session, keeping full context — no token-burning context rebuild. Terminals from other statuses stay open.
4. Each status+agent visit prompts **exactly once** (the initial `--prompt` on create counts). A status change re-arms the nudge. Nudges are skipped while the agent's TUI is busy, and retried next poll.
5. The opencode plugin (`report-status.ts`) parses the agent's STATUS/SUMMARY output and calls `report`.
6. On `report` success (comment + transition land), the terminal is deliberately **kept alive** so a later bounce reuses the session. Terminals are closed only when the ticket reaches a status listed in `closeOn`.
7. Worktree creation checks local AND remote branches for the ticket key; a remote-only match (e.g. `origin/<user>/ABCD-123`) is passed as `origin/...` base ref so Orca reuses the branch instead of auto-suffixing the worktree name.

## Layout

- `cmd/orca-jira-loop/main.go` — subcommands: `run` (self-daemonizing), `stop`, `serve`, `submit`, `remove`, `list`, `report`
- `internal/config` — workflow YAML parsing + validation, saved-config paths
- `internal/daemon` — poll loop, dispatch, nudge-once tracking, terminal lifecycle, prompt building, status validation
- `internal/server` — central `serve` process (unix socket, config registry, submit/remove/list) + client
- `internal/opencode` — opencode CLI interaction (sessions, agents)
- `internal/orcacli` — Orca terminal/worktree API (`terminal create/list/send/close`, `worktree create/list`)
- `internal/acli` — Acquia CLI wrapper (Jira search/view/comments, transitions, status validation)

## Notes

- Pid/log files live under `~/.orca-jira-loop/<config>/` (`daemon.pid`, `daemon.log`).
- Git baseline: commands keep Git 2.25 compatibility and use capability caching per host.
