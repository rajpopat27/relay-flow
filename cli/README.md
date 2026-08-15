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
    jql: project = KCC AND issuetype = Task
    closeOn:
      - Done
    agents:
      plan:
        handles:
          - "Ready for dev"
        outcomes:
          done: "In Progress"
          blocked: Blocked
        nudgePrompt: "Ticket {{ticket}} is back in '{{status}}'. Run `acli jira workitem view {{ticket}} --fields summary,description,comment --json` to read the latest feedback and continue. End with STATUS/SUMMARY as before."
      build:
        handles:
          - "In Progress"
        outcomes:
          done: "In Review"
          blocked: Blocked

  incidentResponse:
    jql: project = KCC AND issuetype = Incident
    closeOn:
      - Done
    agents:
      investigate:
        handles:
          - "In Progress"
        outcomes:
          done: "In Review"
          blocked: Blocked
```

Each workflow owns its JQL and agents. Each agent owns the Jira statuses it `handles` plus `outcomes` that map its reported status to the next Jira status. `handles` and `closeOn` accept a scalar or list; lists are canonical.

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
orca-jira-loop report --config workflow --workflow taskDevelopment --ticket KCC-1234 --agent plan \
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
```

`report` is unchanged: the plugin still calls it as a one-shot command from
the ticket worktree, and it talks straight to Jira — never to the server.
If `.workflow/<name>.yaml` is absent in the worktree, `report` falls back
to the server's saved copy under `~/.orca-jira-loop/configs/`.

## How it works

1. Poll each workflow's JQL every `pollIntervalSeconds`; find tickets in a dispatchable status.
2. For each, start a new Orca terminal titled `<key>:<agent>` in the ticket's worktree (created on demand) with a prompt prefixed `title: <key>:<agent> |`.
3. If a terminal with that exact title already exists, **nudge it in place** instead of respawning: send the agent's `nudgePrompt` via `orca terminal send`. The session keeps its full context across review bounces — no token-burning context rebuild.
4. Each status+agent visit prompts **exactly once** (the initial `--prompt` on create counts). A status change re-arms the nudge. Nudges are skipped while the agent's TUI is busy, and retried next poll.
5. The opencode plugin (`report-status.ts`) parses the agent's STATUS/SUMMARY output and calls `report`.
6. On `report` success (comment + transition land), the terminal is deliberately **kept alive** so a later bounce reuses the session. Terminals are closed only when the ticket reaches a status listed in `closeOn`.
7. Worktree creation checks local AND remote branches for the ticket key; a remote-only match (e.g. `origin/<user>/KCC-123`) is passed as `origin/...` base ref so Orca reuses the branch instead of auto-suffixing the worktree name.

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
