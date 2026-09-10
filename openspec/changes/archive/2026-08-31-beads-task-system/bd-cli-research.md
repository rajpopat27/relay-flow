# Beads CLI research and live preflight record

This file records the Beads investigation performed before implementation. Read it before starting Section 3 of `tasks.md`. It is the observed CLI contract for the disposable environment, not permission to read Beads storage directly.

## Live disposable environment

```text
Workspace: /tmp/beads-demo/.beads
Dolt data: /tmp/beads-demo/dolt-data
Server:    127.0.0.1:13307
PID file:  /tmp/beads-demo/dolt-server.pid
Log file:  /tmp/beads-demo/dolt-server.log
Metadata:  /tmp/beads-demo/.beads/metadata.json
Port file: /tmp/beads-demo/.beads/dolt-server.port
```

The workspace metadata records `dolt_server_port: 13307`, and the Beads port record also contains `13307`. The server is intentionally bound to this fixed port. Since the workspace uses `bd init --server --external`, Beads does not supervise or automatically restart the Dolt process; if the process dies, rerun the same explicit `--port 13307` command and wait for the readiness probe. It must not be restarted on an automatically selected port.

The server is started with:

```sh
nohup dolt sql-server \
  --data-dir /tmp/beads-demo/dolt-data \
  --host 127.0.0.1 \
  --port 13307 \
  > /tmp/beads-demo/dolt-server.log 2>&1 &
echo $! > /tmp/beads-demo/dolt-server.pid
```

The workspace was initialized as an externally managed, server-backed workspace:

```sh
cd /tmp/beads-demo
BEADS_DOLT_SERVER_TLS=0 \
BEADS_DOLT_PASSWORD= \
bd init \
  --server \
  --external \
  --server-host 127.0.0.1 \
  --server-port 13307 \
  --server-user root \
  --prefix demo \
  --non-interactive \
  --skip-hooks \
  --skip-agents
```

The selected tools in this environment are:

```text
bd:   1.2.2 (Homebrew)
dolt: 2.3.1
```

The `bd` process uses the external workspace from any working directory with:

```sh
export BEADS_DIR=/tmp/beads-demo/.beads
export BEADS_DOLT_SERVER_TLS=0
export BEADS_DOLT_PASSWORD=
```

The relay-flow process must use the registered code repository as `exec.Cmd.Dir` and set `BEADS_DIR` on every child command. It must never call `os.Chdir`.

## Commands verified against the real workspace

The following operations were executed successfully through the real `bd` binary:

- `bd where --json`
- `bd info --json`
- `bd create ... --json`
- `bd create ... --parent ... --no-inherit-labels --labels ... --stdin --json`
- `bd update <id> --add-label wf:<workflow> --json`
- `bd update <id> --status <status> --json`
- `bd update <id> --description=- --json` (multiline description on stdin)
- `bd update <id> --assignee <assignee> --json`
- `bd update <id> --defer "" --json` (clears a defer date and returns the issue to `open`)
- `bd comment <id> --stdin --json`
- `bd comments <id> --json`
- `bd show <id> --json`
- `bd list --ready --no-parent --limit 0 --json`
- `bd list --no-parent --status open,in_progress,blocked,deferred --label-pattern 'wf:*' --limit 0 --json`
- `bd list --parent <id> --all --limit 0 --json`
- `bd close <id> --reason ... --json`
- `bd reopen <id> --reason ... --json`
- `bd dep add <dependent> <blocker> --type blocks --json`
- `bd dep tree <id>`
- `bd ready --explain --json`

Use issue IDs returned by JSON output. Do not derive an issue ID from its title. For example, the child title `demo-9u5:implement` has the returned Beads issue ID `demo-9u5.1`.

The canonical claimed-parent status set for relay-flow is `open`,
`in_progress`, `blocked`, and `deferred`. The live preflight verified the
`deferred` query shown above, so the adapter does not substitute `hooked` for
`deferred` in its claimed-parent poll.

A later verification pass against `bd 1.2.2` confirmed the remaining commands
the adapter emits, which the first preflight had not recorded:

- `--description=-` round-trips multiline text exactly;
- `--defer ""` clears a defer date and returns a `deferred` issue to `open`;
- `--status closed` followed by `--status in_progress` reopens a mailbox, which
  is the workflow-revisit path;
- `--status` and `--assignee` may be combined in one `bd update`;
- `assignee` is a real JSON field on both `bd show` and `bd list`, and is
  distinct from the `owner` field that records the git identity;
- `bd list --ready --no-parent` still returns child issues carrying a
  non-empty `parent`, confirming that the adapter's defensive parent filter is
  required rather than redundant;
- `bd show <missing-id>` exits non-zero, and empty list/comment results are
  `[]`.

## Observed JSON shapes

The adapter should parse only the fields it needs and tolerate unrelated fields.

- `bd create --json` returns one issue object.
- `bd show --json` returns an array containing the issue object.
- `bd list ... --json` returns an array of issue objects.
- `bd comments --json` returns an array with fields such as `id`, `issue_id`, `author`, `text`, and `created_at`.
- `bd comment --stdin --json` returns one created-comment object.
- Issue fields observed include `id`, `title`, `description`, `status`, `priority`, `issue_type`, `owner`, `created_at`, `created_by`, `updated_at`, `labels`, `parent`, dependency fields, and comment counts.
- Child issues include a non-empty `parent` field in list output.
- `bd ready --explain --json` contains separate `ready` and `blocked` arrays. Blocked entries include `blocked_by`; ready entries include a reason and may include `parent`.

The production adapter must defensively filter any normalized issue with a non-empty `parent`, even when `--no-parent` is passed. The observed CLI output showed that `--no-parent` should not be the sole correctness boundary across all ready/list paths.

## Status behavior decision

The installed `bd 1.2.2` does not expose `--if-status`. Beads therefore uses the deliberately chosen read-before-write policy:

```text
bd show <id>

current == target
  -> idempotent success; do not write

current != expected and current != target
  -> return a task conflict; durable execution blocks/retries

current == expected
  -> bd update <id> --status <target> --json
```

The read/write race is accepted for Beads as last-writer-wins. Do not add `--if-status`, do not retry it and fall back, and do not classify an arbitrary command failure as permission to overwrite.

`bd close` and `bd reopen` use `--reason`; parent closure may require `--force` when open children or blockers are present. That close-policy behavior must be tested against the actual selected CLI rather than inferred from an issue title.

## Live dependency demo

The live dependency graph is stored in `/tmp/beads-demo` and its IDs are recorded in `/tmp/beads-demo/demo-ids.env`:

```text
DEMO_PARENT=demo-9u5
DEMO_BDCLI=demo-9u5.1
DEMO_ADAPTER=demo-9u5.2
DEMO_INTEGRATION=demo-9u5.3
```

Current graph:

```text
demo-9u5       Relay-flow Beads adapter demo (epic)
├── demo-9u5.1  Demo: exercise bd CLI client (ready)
├── demo-9u5.2  Demo: implement Beads adapter (blocked by demo-9u5.1)
└── demo-9u5.3  Demo: verify relay-flow integration (blocked by demo-9u5.2)
```

Inspect it with:

```sh
export BEADS_DIR=/tmp/beads-demo/.beads
export BEADS_DOLT_SERVER_TLS=0
export BEADS_DOLT_PASSWORD=

bd dep tree demo-9u5.2
bd dep tree demo-9u5.3
bd ready --explain --json
bd list --parent demo-9u5 --all --limit 0 --json
```

The dependency walk was verified by closing `demo-9u5.1`, observing `demo-9u5.2` become ready while `.3` remained blocked, and reopening `.1` to restore the initial graph.

The earlier CLI-only fixture (`demo-47i`) was closed so the dependency graph above is the primary live example.

## Implementation consequences

Implement only the following production surfaces:

```text
internal/task/beads/bdcli/bdcli.go
internal/task/beads/beads.go
internal/task/beads/*_test.go
internal/task/beads/bdcli/*_test.go
cmd/relay-flow/main.go       (blank import only)
cmd/relay-flow/serve.go      (blank import only)
```

Use standard-library subprocess/JSON/environment handling. Keep Beads-specific issue/comment values inside the adapter packages. Core continues to own the existing `task.System`, Repo Poller, router, run manager, runner, harness, durable execution, and SQLite contracts.

Do not read:

```text
/tmp/beads-demo/.beads/issues.jsonl
any Beads Dolt table
Beads internal Go packages
```

Direct SQL is permitted only for the disposable server readiness probe:

```sh
dolt --no-tls --host 127.0.0.1 --port 13307 sql -q 'SELECT 1'
```

If `/tmp/beads-demo` is recreated, repeat the setup and update the live IDs in any local notes before continuing. The IDs are disposable and are not part of the relay-flow configuration.
