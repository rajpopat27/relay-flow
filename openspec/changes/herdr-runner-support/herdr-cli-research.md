# Herdr CLI contract and preflight plan

This file records the Herdr CLI contract read from the supplied terminal-lifecycle summary and the Herdr source/docs at `/home/raj/raj/herdr`. A live smoke check was also run with the installed Herdr CLI in a disposable named session during this change: the installed binary is `herdr 0.8.2` and reports protocol `20`. Section 1 of `tasks.md` still requires the implementer to capture sanitized command/output fixtures before adapter code is written; the live smoke check is evidence for the command plan, not a substitute for committed contract fixtures.

## Runtime identity

Herdr owns terminal processes and PTYs in its headless server. A normal pane has one terminal runtime.

- `workspace_id` identifies the repository-level Herdr workspace.
- `pane_id` is the public pane handle and the durable automation identity.
- `terminal_id` identifies the current server runtime and is regenerated after Herdr restart.
- `pane.process-info` is the public liveness/process inspection operation.

Relay-flow must persist a Herdr `pane_id` in `runner.Terminal.ID`; it must never persist `PaneInfo.terminal_id`.

## Commands verified against the installed CLI

The installed `herdr 0.8.2` accepted the following command families during the disposable smoke check. Workspace creation is an operator setup command; the production runner wrapper uses the remaining commands:

```text
# Operator setup (not a relay-flow adapter operation)
herdr workspace create --cwd PATH --label LABEL --no-focus

# Production adapter operations
herdr api snapshot
herdr tab create --workspace WORKSPACE_ID --cwd PATH --label LABEL --no-focus [--env KEY=VALUE ...]
herdr tab list --workspace WORKSPACE_ID
herdr pane list --workspace WORKSPACE_ID
herdr pane get PANE_ID
herdr pane process-info --pane PANE_ID
herdr pane rename PANE_ID LABEL
herdr pane run PANE_ID COMMAND
herdr pane close PANE_ID
```

`herdr api snapshot`, workspace creation, tab creation, pane list/get, and process-info return JSON responses. The mutating pane commands return a successful exit status without requiring relay-flow to parse a success payload. Errors are JSON error responses and nonzero exit statuses. Global session selection is accepted before the subcommand, for example `herdr --session <name> api snapshot`; the equivalent `HERDR_SESSION=<name>` environment variable is also supported.

## Response locations

The wrapper should parse only the fields it needs and ignore unrelated fields:

```text
api snapshot:
  result.snapshot.workspaces[]
  result.snapshot.panes[]

workspace create:
  result.workspace.workspace_id
  result.workspace.label
  result.root_pane.pane_id
  result.root_pane.terminal_id
  result.root_pane.cwd

tab create:
  result.tab.tab_id
  result.root_pane.pane_id
  result.root_pane.terminal_id
  result.root_pane.cwd

tab list:
  result.tabs[]

pane list:
  result.panes[]

pane get:
  result.pane

pane process-info:
  result.process_info
```

`PaneInfo` includes `pane_id`, `terminal_id`, `workspace_id`, `tab_id`, `cwd`, optional `foreground_cwd`, and optional `label`. `PaneProcessInfo` includes optional `shell_pid`, optional `foreground_process_group_id`, and foreground process records.

## Workspace discovery

Herdr `WorkspaceInfo` does not expose cwd directly. `DiscoverRepos` must inspect snapshot panes, group them by `workspace_id`, and use a pane's saved `cwd` to produce `runner.RepoCandidate{Name: workspace.label, Path: cwd}`. The installed CLI returned pane `cwd` values in `result.snapshot.panes[]` and `result.panes[]`. Matching an existing workspace should prefer a normalized repository-path match and use the workspace label as a tie-breaker.

The settled provisioning policy is: registration and normal startup validation require an existing matching workspace; neither registration nor the adapter creates one. If a workspace is deleted, the operator recreates it with `herdr workspace create --cwd ... --label ... --no-focus` before registering the repo again or restarting relay-flow. The adapter must not silently attach to an ambiguous workspace.

## Pane creation and command execution

Herdr has no public `terminal create` command. The adapter creates a tab/root pane with the repository workspace, applies the stable `<ticket>:<node>` label, and runs the structured harness command in that pane.

The runner command's environment must be passed as repeated `--env KEY=VALUE` options to `tab create`. The command executable and arguments are rendered as one shell command for `pane run`; quoting must match the shell used by the existing relay-flow runtime for multiline prompts and other values. The wrapper must preserve the complete command as one process argument so the Herdr CLI can forward it to the pane shell. The adapter remains CLI-only and does not add a separate platform transport.

The preflight must capture command execution with arguments containing spaces, quotes, newlines, and the required `RELAY_FLOW_*` values. No command or prompt payload may be logged at info level.

## Liveness and restart behavior

A pane is not considered a usable agent terminal merely because its record exists. The adapter must use `pane get` and `pane process-info` to distinguish a live foreground agent process from a restored shell. After a Herdr restart, a saved pane can have a fresh shell and fresh `terminal_id`; relay-flow must relaunch the harness in a replacement pane or in a deliberately reused shell without treating the old `terminal_id` as durable.

When a pane is missing, the adapter creates a new pane. When a create acknowledgement is lost, the adapter scans the workspace snapshot for the exact stable pane label before creating another pane. If a pane remains but is unusable, the adapter closes it before creating its replacement.

## Live preflight result and repeatable procedure

A live smoke check in this change used the installed CLI and a disposable named session. It confirmed:

- `herdr --version` is `0.8.2`;
- `herdr api schema --json` reports protocol `20` (the installed binary is authoritative; do not copy a different protocol number from unreleased source docs);
- a named server can be started with `herdr --session <name> server`;
- readiness is reliably established by a successful `herdr --session <name> api snapshot`, not merely by the exit status of `herdr --session <name> status server --json`;
- `workspace create --cwd ... --label ... --no-focus` returns `result.workspace` and `result.root_pane`;
- `tab create --workspace ... --cwd ... --label ... --no-focus --env KEY=VALUE` returns `result.tab` and `result.root_pane`;
- `tab list --workspace ...` returns `result.tabs[]` and exposes the tab label;
- the created root pane initially has no pane label, so `pane rename <pane_id> <label>` is required for stable cleanup discovery; the tab label is the recovery marker during the gap before pane rename;
- `pane run <pane_id> <command>` submits command text and Enter, and repeated `--env` values reach the launched command;
- `pane process-info --pane ...` shows a foreground command while it runs;
- after stopping and restarting the named Herdr server, workspace and public pane IDs remained the same, the terminal ID changed, and the pane was restored as a shell.

The implementation must encode these observations in sanitized fixtures and strict tests. The strict fake executable is test-only; the production factory must always invoke the installed Herdr binary. Use a disposable session and temporary directory for any live smoke check, never the default session:

```sh
SESSION="relay-flow-herdr-spec-<unique>"
TMP="$(mktemp -d)"
herdr --session "$SESSION" server >"$TMP/server.log" 2>&1 &
SERVER_PID=$!
until herdr --session "$SESSION" api snapshot >"$TMP/ready.json" 2>"$TMP/ready.err"; do
  kill -0 "$SERVER_PID" 2>/dev/null || { cat "$TMP/server.log"; exit 1; }
  sleep 0.1
done

# Exercise workspace/tab/pane commands, then stop and restart:
herdr --session "$SESSION" server stop
herdr --session "$SESSION" server >"$TMP/server.log" 2>&1 &
SERVER_PID=$!
until herdr --session "$SESSION" api snapshot >"$TMP/restarted.json" 2>"$TMP/restarted.err"; do
  kill -0 "$SERVER_PID" 2>/dev/null || { cat "$TMP/server.log"; exit 1; }
  sleep 0.1
done

herdr --session "$SESSION" server stop || true
for _ in $(seq 1 100); do
  ! herdr --session "$SESSION" api snapshot >/dev/null 2>&1 && break
  sleep 0.1
done
wait "$SERVER_PID" 2>/dev/null || true
herdr session delete "$SESSION" --json || true
rm -rf "$TMP"
```

The preflight must not inspect Herdr internals or persisted session files as an implementation shortcut. It must leave the user's existing default session/workspace untouched and remove only its disposable session/workspace after verification.
