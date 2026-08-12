# report-status opencode plugin

An opencode plugin that watches for a finished agent reply, parses its STATUS/SUMMARY block, and reports the result back to Jira via the `orca-jira-loop` CLI.

## Install

Put `report-status.ts` in `.opencode/plugin/` at the repo root (opencode auto-loads every plugin in that directory — do **not** also list it in `opencode.json` or it registers twice and posts every comment twice).

## Protocol

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

## What the plugin does

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

## Files

- `report-status.ts` — the plugin (source of truth for the parsing protocol)
- Tests live alongside in the consuming repo: `node --test .opencode/plugin/report-status.test.ts` (27 cases covering one-line, two-line, bold, fenced, lowercase, prose-before-status, 10-word boundary, multi-word status, trailing punctuation)

## Keep in sync

The plugin and the CLI are versioned together in this repo: the CLI flags (`--status`, `--summary`) and the plugin's output must stay in lockstep. If you change the parsing protocol, update the CLI flag contract in `cli/` and the tests.
