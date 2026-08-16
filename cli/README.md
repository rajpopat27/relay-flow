# relay CLI

Central workflow engine: polls the tracker, dispatches agents into runner sessions, routes outcome reports back to the tracker.

## Install

```sh
cd cli
go install ./...   # installs to $(go env GOPATH)/bin/relay
```

## Commands

```sh
relay init --assignee "Jane Doe"    # machine identity → ~/.relay/config.yaml (probe-validated)
relay serve [--dry-run] [--foreground]
relay submit [-f .workflow/workflow.yaml]
relay report --workflow <name> --ticket <key> --node <node> --outcome <success|failure> --summary <text>
relay stop serve
```

## Config

One workflow per YAML file — see the repo root README for the full schema (`nodes`, `when`, `onSuccess`/`onFailure`, `closeOn`, `tasks`, `runner`). Strict parsing: unknown fields, duplicate `when` values, dangling edges all fail at submit.

## Architecture

- `internal/config` — workflow YAML schema + validation
- `internal/tasks` — `Tasks` interface + registry; `tasks/jira` adapter (acli)
- `internal/runner` — `Runner` interface + registry; `runner/orca` adapter (worktrees + terminals)
- `internal/daemon` — poll loop, 3-way claim switch, dispatch/bounce
- `internal/server` — unix-socket server (`/submit`, `/report`, `/shutdown`) + client
- `internal/discovery` — repo resolution, `~/.relay/` paths, flock
- `internal/acli`, `internal/orcacli`, `internal/opencode` — CLI wrappers

Tests: `go test ./... -race`
