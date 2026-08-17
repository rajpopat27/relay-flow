# tasks/jira — Jira adapter

Implements `tasks.Tasks` over the [acli](https://github.com/acli) CLI. Jira
statuses are node states (`when`); claim labels `wf:<workflow>` are Jira
labels.

## Config (tasks.config)

| Field | Required | Meaning |
|---|---|---|
| `query` | yes | JQL fragment, e.g. `project = ABCD`. Must not contain `issuetype`, `assignee`, or `ORDER BY` — the adapter appends those. |
| `issueTypes` | yes | Scalar or list, e.g. `[Task, Story]`. Rendered as `AND issuetype IN (...)`. |
| `assigneeIsAgent` | no | Centralized mode: no assignee clause (org server owns the queue). Default false → `AND assignee = "<machine config>"` is appended. |

Built JQL: `(query) AND issuetype IN (...) AND component = "<repo displayName>" [AND assignee = "..."] ORDER BY updated`.
The repo displayName (resolved by the server from the submitter's cwd) scopes
the poll to one repo's tickets.

## Behavior

- **List** — one search per poll; maps each ticket's status → node via `when`
  (unmapped → `Node: ""`, daemon skips); first `wf:*` label → `ClaimedBy`.
- **Claim** — adds `wf:<name>` label (labels are never removed; they're the
  cross-restart mutex).
- **Report** — transitions to the target node's `when` status + posts the
  summary comment (`[wf] KEY (agent: x, node: y) reported outcome → target`).
  Self-loop (target status == current) → comment only; acli FAILURE envelopes
  (exit-0 failures) are detected and returned as errors.

## Submit-time validation

`ProjectKeyFromQuery` + `ValidateStates` probe every `when` status against the
project — Jira's JQL parser rejects unknown statuses, so typos fail at submit
instead of silently matching nothing.

## Writing a new tasks adapter (beads, Linear, GitHub, ...)

1. Create `internal/tasks/<name>/` with:
   ```go
   func init() {
       tasks.Register("<name>", tasks.Factory{
           UnmarshalConfig: unmarshalConfig, // strict-decode tasks.config into your struct
           New: func(cfg any, wfName string, nodes map[string]config.Node, assignee, repoName string) (tasks.Tasks, error) {
               // build your adapter; use nodes[..].When as the tracker-state map,
               // wfName for claim labels, assignee/repoName if your tracker scopes by them
           },
       })
   }
   ```
2. Implement the interface:
   ```go
   type Tasks interface {
       List() ([]tasks.Ticket, error)                       // Node + ClaimedBy filled
       Claim(t tasks.Ticket) error                          // attach wf:<name> marker
       Report(t tasks.Ticket, outcome, targetNode, summary string) error
   }
   ```
   `Ticket{Key, Summary, Node, ClaimedBy}` — you fill `Node` by reverse-mapping
   the ticket's tracker state through `nodes[*].When`, and `ClaimedBy` from
   your tracker's equivalent of claim markers (labels, assignments, ...).
3. Strictly validate your `tasks.config` in `UnmarshalConfig` (unknown fields
   must error — core can't see inside).
4. Import your package for side effects (`_ "relay-flow/internal/tasks/<name>"`)
   in `internal/server/server.go`, next to the jira import.
5. Table-driven tests with a fake for your tracker's client seam (see
   `jira_test.go` and the `aclier` interface for the pattern).

External adapters (out of tree): fork the repo, add your package + import —
the registry is deliberately in-process (no .so loading, YAGNI).
