# report-status opencode plugin

Watches for a finished agent reply, parses its STATUS/SUMMARY block
deterministically, and reports the outcome to the running relayflow server via
`relayflow report` (a thin socket client). Never closes its own terminal — the
daemon tears terminals down at `closeOn` nodes.

## Install

Put `report-status.ts` in `.opencode/plugin/` at the repo root (opencode
auto-loads every plugin in that directory — do **not** also list it in
`opencode.json` or it registers twice and reports twice). Commit it so every
ticket worktree inherits it.

## Protocol

The agent must end its reply with one of:

```
STATUS: success
SUMMARY: <at least 10 words describing what was done>
```

or the single-line variant:

```
STATUS: failure SUMMARY: tests still failing on the parser edge case
```

Statuses are `success` or `failure` (lowercase accepted; bolded labels and
code fences tolerated). `parseStatusBlock()` is the source of truth.

## What the plugin does

On `session.idle`:

1. Skips unless `RELAYFLOW_WORKFLOW/TICKET/NODE/AGENT` env vars are set (the
   runner injects them; a developer's own opencode session never reports).
2. Pins the session title to `<ticket>:<agent>:<node>` (opencode's naming
   agent would otherwise overwrite the title bounce-matching relies on).
3. Extracts and validates the STATUS/SUMMARY block from the last assistant
   message (aborted turns are skipped).
4. Calls:

```sh
relayflow report --workflow <name> --ticket <key> --node <node> \
  --outcome <success|failure> --summary "..."
```

5. On `{action: transitioned|commented}` — done. On `{action: error}` —
   retries 3×, then nudges the session with the error detail. A missing
   STATUS/SUMMARY block nudges the session asking for one.

## Files

- `report-status.ts` — the plugin (parsing protocol + reporting)

## Keep in sync

Plugin and CLI are versioned together in this repo: the `relayflow report` flag
contract (`--workflow --ticket --node --outcome --summary`), the `RELAYFLOW_*`
env names injected by the runner, and the success/failure vocabulary must
stay in lockstep across `plugin/`, `cli/cmd/relayflow/`, and
`cli/internal/runner/`.
