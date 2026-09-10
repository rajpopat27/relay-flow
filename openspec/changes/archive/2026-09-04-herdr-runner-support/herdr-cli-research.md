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
# Production adapter operations
herdr api snapshot
herdr worktree list --cwd PATH
herdr worktree create --cwd PATH --branch NAME --base REF --label TEXT --no-focus
herdr worktree open --cwd PATH --branch NAME --label TEXT --no-focus
herdr tab create --workspace WORKSPACE_ID --cwd PATH --label LABEL --no-focus
herdr tab list --workspace WORKSPACE_ID
herdr pane list --workspace WORKSPACE_ID
herdr pane get PANE_ID
herdr pane process-info --pane PANE_ID
herdr pane rename PANE_ID LABEL
herdr pane run PANE_ID COMMAND
herdr pane close PANE_ID
herdr workspace close WORKSPACE_ID
```

Successful commands return `{"id":...,"result":{...}}` on stdout with exit 0; `pane run` prints nothing. Failures return `{"id":...,"error":{"code","message"}}` on **stderr** with exit 1 and empty stdout. There is no `ok` field. Observed error codes: `pane_not_found`, `workspace_not_found`, `worktree_not_found`, `not_git_worktree`, `worktree_create_failed`. Global session selection is accepted before the subcommand, for example `herdr --session <name> api snapshot`; the equivalent `HERDR_SESSION=<name>` environment variable is also supported.

## Response locations

The wrapper should parse only the fields it needs and ignore unrelated fields:

```text
api snapshot:
  result.snapshot.workspaces[]
  result.snapshot.panes[]

worktree list:
  result.source.repo_root
  result.worktrees[].{path,branch,is_linked_worktree,open_workspace_id}

worktree create / worktree open:
  result.workspace.workspace_id
  result.workspace.label
  result.workspace.worktree.{checkout_path,repo_root,repo_name,is_linked_worktree}
  result.worktree.{path,branch,open_workspace_id}
  result.root_pane.pane_id

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

## Repository discovery and ticket environments

Every workspace reports its Git identity directly:

```text
workspace.worktree = { checkout_path, repo_key, repo_name, repo_root, is_linked_worktree }
```

`DiscoverRepos` therefore reads `api snapshot` and deduplicates workspaces by `repo_root`; a ticket worktree workspace reports the same `repo_root` as its source checkout. Pane `cwd` values are not used for discovery.

`worktree list --cwd PATH` returns `result.source{repo_key, repo_name, repo_root, source_checkout_path}` and `result.worktrees[]{path, branch, is_linked_worktree, open_workspace_id}`, where `open_workspace_id` is absent when the checkout exists but is closed. It works with **no** workspace open, and returns `not_git_worktree` for a non-repository path or a path that does not exist; a subdirectory resolves to the repository root. `ValidateRepo` uses this and requires the registered path to equal `source.repo_root`.

Ticket environments are Herdr-managed Git worktrees, one per ticket:

- `worktree open --cwd REPO --branch TICKET --label TICKET --no-focus` reuses the checkout, reporting `already_open` when its workspace is open, and returns `worktree_not_found` when no checkout exists for the branch.
- `worktree create --cwd REPO --branch TICKET --base ORIGIN_REF --label TICKET --no-focus` creates the checkout and opens it as its own workspace. Verified: an existing branch is checked out as-is with its commits intact, so `--base` applies only to a new branch; creating over an existing checkout fails with `worktree_create_failed`.
- Reopening a closed checkout yields a **new** workspace ID (`w2 → w3`), so workspace IDs are current handles only. Identity is the branch and checkout path.
- `workspace close WORKSPACE_ID` closes the workspace and leaves the checkout, branch, and files on disk. `worktree remove` is never used by the adapter.
- Registration needs no operator setup: `worktree list` and `worktree create` both work against a Herdr session with nothing open, and `worktree create` also opens the repository's source workspace as a side effect.

## Pane creation and command execution

Herdr has no public `terminal create` command. The adapter creates a tab/root pane with the repository workspace, applies the stable `<ticket>:<node>` label, and runs the structured harness command in that pane.

The runner command's environment is rendered into the `pane run` command line together with the quoted executable and arguments, exactly as the Orca adapter does. `tab create --env` is intentionally unused: environment bound at tab creation persists on the pane, so a pane adopted from an earlier run would carry a stale `RELAY_FLOW_RUN_ID`. The wrapper must preserve the complete command as one process argument so the Herdr CLI can forward it to the pane shell. The adapter remains CLI-only and does not add a separate platform transport.

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
- `worktree create --cwd ... --branch ... --base ... --label ... --no-focus` creates a linked checkout and opens it as its own workspace, returning `result.workspace`, `result.tab`, `result.root_pane`, and `result.worktree`;
- `worktree open --cwd ... --branch ... --label ... --no-focus` reopens an existing checkout and reports `already_open`;
- `tab create --workspace ... --cwd ... --label ... --no-focus` returns `result.tab` and `result.root_pane`;
- `tab list --workspace ...` returns `result.tabs[]` and exposes the tab label;
- the created root pane initially has no pane label, so `pane rename <pane_id> <label>` is required for stable cleanup discovery; the tab label is the recovery marker during the gap before pane rename;
- `pane run <pane_id> <command>` submits command text and Enter, including environment assignments rendered into the command line;
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
