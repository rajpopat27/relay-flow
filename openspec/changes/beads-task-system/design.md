# Design: Beads task-system plugin

## Context

Relay-flow already has a repo-bound `task.System` seam and creates one Repo Poller per registered repository. A Beads integration should use that seam directly. The registered repository path is available in `task.RepoSpec.Path` when a task system is constructed, while the current `TaskScopeKey` callback receives only root and repo task-config maps. Beads therefore stores its workspace identity in repo-scoped task config.

The detailed command, package, and test plan is in `beads-feature.md` in this change directory.

## Decisions

### 1. Use the `bd` CLI

The adapter invokes `bd` as a subprocess and requests JSON output. It does not import Beads Go packages, issue internal SQL queries, read JSONL exports, or depend on Beads' storage interfaces.

The selected `bd` binary must be exercised in a disposable live preflight before adapter implementation starts.

### 2. Separate code-repository path from Beads workspace

A registered repo has:

```yaml
repos:
  payments:
    path: /work/payments
    taskConfig:
      beadsDir: /var/lib/beads/payments/.beads
```

`path` is the code/runner repository. `beadsDir` is the Beads workspace/configuration context. For a local workspace, `beadsDir` may be `/work/payments/.beads`; for server-backed Beads it may be external to the code repository.

The Beads factory requires `beadsDir` as a repo key. This makes the physical task scope visible to `TaskScopeKey` without changing the existing callback signature.

Beads uses the shared task configuration vocabulary where it has an adapter
meaning: `filters`, `templates`, optional top-level `assignee`, and
`transitionTo.parentStatus`/`transitionTo.taskStatus`. The workspace remains
the sole Beads-specific required field and must be supplied at repository
scope; it is not inferred from another field.

### 3. Use one task system and poller per registered repo

The existing `RepoPoller` owns polling. The Beads system is constructed once for each registered repo, stores `task.RepoSpec.Path`, and runs `bd` commands with:

```text
cmd.Dir = spec.Path
BEADS_DIR = configured beadsDir
```

The adapter never calls `os.Chdir`. There is no Beads-specific polling loop.

Two registered repos with different `beadsDir` values are allowed. Two repos with the same canonical `beadsDir` are rejected as duplicate task scopes.

### 4. Use the workspace as the task scope

`TaskScopeKey` returns a canonical `beadsDir`. The Beads issue prefix is not a scope selector and is not treated as a Jira component. A repo-specific prefix may be used when initializing Beads to make issue IDs recognizable, but relay-flow does not use it for routing.

### 5. Use CLI polling plus defensive normalization

Each poll reads:

```sh
bd list --ready --no-parent --limit 0 --json
bd list --no-parent --status open,in_progress,blocked,deferred --label-pattern 'wf:*' --limit 0 --json
```

The canonical claimed-parent status set is `open`, `in_progress`, `blocked`,
and `deferred`. The adapter intentionally polls `deferred` rather than
`hooked`, matching the selected `bd` CLI contract and live preflight; `hooked`
is not part of the claimed-parent poll query.

The adapter merges the two result sets by issue ID and defensively removes every issue whose normalized `parent` field is non-empty, regardless of the CLI filter result. Workflow filters are compiled once and applied in memory by the existing router.

### 6. Claim with workflow labels

Relay-flow does not use `bd ready --claim`. After workflow resolution, it claims with:

```sh
bd update <id> --add-label wf:<workflow> --json
```

The parent remains the workflow unit. Mailbox children are never independently routed.

### 7. Use child issues as mailboxes

The adapter finds children with `bd list --parent <id> --all --limit 0 --json`, matches stable titles `<parent-id>:<node>`, updates existing descriptions/labels, and creates only missing children. Every mailbox receives `wf:<workflow>` and the node work description. Revisited nodes reuse the same child issue.

`CompleteMailbox` only completes the current mailbox. It does not write comments, select routes, modify the parent, or perform runner work.

### 8. Beads status reconciliation is read-before-write

Beads does not require `bd update --if-status`. For a transition with expected source status `S` and target status `T`:

```text
bd show

current == T
  -> success/no-op

current != S and current != T
  -> retry.ConflictError

current == S
  -> unconditional bd update --status T
```

The read protects ordinary manual-state handling and makes retries idempotent. A change between `bd show` and `bd update` is an explicitly accepted Beads-specific last-writer-wins race. This is different from conflict-aware adapters, but it does not change graph routing or add a core status policy.

### 9. Use stable comment markers

`HasComment` reads `bd comments <id> --json`, and `Comment` checks the marker before writing through `bd comment <id> --stdin --json`. Summary and feedback remain separate comments on their existing destinations. No feedback comment is written when the selected next node is `end`.

### 10. Support Dolt server mode through workspace metadata

The adapter uses the same CLI for embedded and server-backed Beads. For server mode, the configured `beadsDir` contains the Beads metadata that selects the Dolt SQL server/database. Relay-flow does not start `dolt sql-server` or `bd serve`; the live preflight starts only a disposable server for verification.

### 11. Recovery rolls forward

Recovery resets Beads parent/mailbox state through the adapter, preserves comments, labels, descriptions, history, worktrees, and code, and never deletes or compensates external work. It follows the existing explicit `serve --recover` contract.

### 12. Keep the transport seam narrow

`internal/task/beads/bdcli` owns subprocess execution, environment, stdin/stdout/stderr, JSON parsing, and command errors. `internal/task/beads` owns task normalization, filters, claims, mailboxes, statuses, comments, text, and recovery. Core does not learn Beads command syntax.

### 13. Reuse the shared configuration vocabulary

The Beads adapter accepts the existing `filters`, `templates`, optional
top-level `assignee`, and `transitionTo` fields. `transitionTo` uses the
existing `parentStatus` and `taskStatus` members: parent transitions apply to
the parent issue and task transitions apply to a mailbox. An explicit
`filters.assignees` value wins; when it is absent, top-level `assignee` is the
default assignee filter, matching Jira's behavior.

The old Beads-only `status.parent` and `status.mailbox` fields are not
supported. Jira-only `project` and `component` fields remain rejected, and
`beadsDir` remains required at repository scope as the physical task scope.
Status values are never translated between providers: Beads uses native values
such as `in_progress` and `closed`, whereas Jira uses values such as `In
Progress` and `Done`. Arbitrary Jira values must not be silently accepted as
Beads values.

## Alternatives Rejected

| Alternative | Why rejected |
|---|---|
| Import Beads' Go module | Toolchain mismatch and unnecessary coupling to a large storage API. |
| Read `.beads/issues.jsonl` or Dolt tables | Bypasses Beads' supported CLI contract and storage ownership. |
| Use `bd serve` as the initial transport | Adds endpoint configuration and server lifecycle management without a current requirement. |
| Use the Beads prefix as task scope | Prefixes identify issue IDs; they do not multiplex or isolate databases in one workspace. |
| Use labels as a fake component/tenant | Requires a relay-flow convention on every issue and duplicates workspace isolation. |
| Change `TaskScopeKey` to receive repo path | Explicit `beadsDir` keeps the existing core factory contract unchanged. |
| Use `--if-status` | The selected CLI may not expose it; Beads uses the agreed read-before-write policy. |
| Retry `--if-status` and fall back to an unconditional update | Creates an implicit compatibility mode and obscures command/status failures. |
