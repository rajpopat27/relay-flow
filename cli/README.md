# orca-jira-loop CLI

Daemon that keeps Jira tickets moving by dispatching opencode agents in Orca terminals.

## Install

```sh
cd cli
go install ./...   # installs to $(go env GOPATH)/bin/orca-jira-loop
```

## Config

Workflow config lives at `.workflow/<workflow-name>.yaml` in the working directory (the daemon reads it relative to cwd). Example (`kcc.yaml`):

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
        nudge_prompt: "Ticket {{ticket}} is back in '{{status}}'. Run `acli jira workitem view {{ticket}} --fields summary,description,comment --json` to read the latest feedback and continue. End with STATUS/SUMMARY as before."
      build:
        statuses:
          - name: done
            description: implementation is complete and ready for review
        jira_status_on:
          done: In Review
          blocked: Blocked

  Incident:
    statuses:
      "In Progress": plan
    close_on_statuses:
      - Done
    agents:
      plan:
        statuses:
          - name: done
            description: investigation complete, incident resolved
        jira_status_on:
          done: In Review
          blocked: Blocked
```

Each workflow maps a Jira status to an agent, and each agent maps a reported status to the next Jira status.

**Startup validation:** `run` verifies every status name referenced in the YAML (`statuses`, `jira_status_on` targets, `close_on_statuses`) against the Jira project — Jira's JQL parser rejects unknown statuses, so a typo like `"DO Done"` fails fast at startup instead of silently never matching.

## Usage

```sh
# Start the daemon for a workflow (run from the dir holding .workflow/).
# Self-daemonizes: detaches and logs to ~/.orca-jira-loop/<name>/daemon.log.
orca-jira-loop run kcc

# Foreground mode (logs to stderr AND the log file)
orca-jira-loop run --foreground kcc

# Stop it
orca-jira-loop stop kcc

# Manual agent report (normally invoked by the plugin, not by hand)
orca-jira-loop report --workflow kcc --ticket KCC-1234 --agent plan \
  --status done --summary "plan is complete"
```

## How it works

1. Poll the JQL every `poll_interval_seconds`; find tickets in a dispatchable status.
2. For each, start a new Orca terminal titled `<key>:<agent>` in the ticket's worktree (created on demand) with a prompt prefixed `title: <key>:<agent> |`.
3. If a terminal with that exact title already exists, **nudge it in place** instead of respawning: send the agent's `nudge_prompt` via `orca terminal send`. The session keeps its full context across review bounces — no token-burning context rebuild.
4. Each status+agent visit prompts **exactly once** (the initial `--prompt` on create counts). A status change re-arms the nudge. Nudges are skipped while the agent's TUI is busy, and retried next poll.
5. The opencode plugin (`report-status.ts`) parses the agent's STATUS/SUMMARY output and calls `report`.
6. On `report` success (comment + transition land), the terminal is deliberately **kept alive** so a later bounce reuses the session. Terminals are closed only when the ticket reaches a status listed in `close_on_statuses`.
7. Worktree creation checks local AND remote branches for the ticket key; a remote-only match (e.g. `origin/<user>/KCC-123`) is passed as `origin/...` base ref so Orca reuses the branch instead of auto-suffixing the worktree name.

## Layout

- `cmd/orca-jira-loop/main.go` — subcommands: `run` (self-daemonizing), `stop`, `report`
- `internal/config` — workflow YAML parsing + validation
- `internal/daemon` — poll loop, dispatch, nudge-once tracking, terminal lifecycle, prompt building, startup status validation
- `internal/opencode` — opencode CLI interaction (sessions, agents)
- `internal/orcacli` — Orca terminal/worktree API (`terminal create/list/send/close`, `worktree create/list`)
- `internal/acli` — Acquia CLI wrapper (Jira search/view/comments, transitions, status validation)

## Notes

- Pid/log files live under `~/.orca-jira-loop/<workflow>/` (`daemon.pid`, `daemon.log`).
- Git baseline: commands keep Git 2.25 compatibility and use capability caching per host.
