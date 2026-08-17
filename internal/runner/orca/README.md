# runner/orca — Orca adapter

Implements `runner.Runner` over the Orca CLI: each ticket gets an Orca
worktree; each node visit gets a fresh terminal titled
`<key>:<agent>:<node>` running opencode.

## Config (runner.config)

Empty — the adapter takes no YAML fields. Repo binding arrives at runtime:
the server resolves the submitter's repo and calls `WithRepo(repoID,
displayName, dryRun)` after construction.

## Behavior

- **Spawn** — verifies the opencode agent exists (`opencode agent list`),
  ensures the ticket's worktree (creates off the repo's main worktree branch;
  reuses an existing ticket branch if one exists; verifies the exact name
  landed because Orca silently auto-suffixes on collisions), then creates the
  terminal running `opencode --agent <agent> --prompt <p>` with `RELAY_FLOW_*` env
  markers (`RELAY_FLOW_WORKFLOW/TICKET/NODE/AGENT`) so the plugin can report back.
- **Find** — exact-title match on the ticket's terminal list. A missing
  worktree (`selector_not_found`) means "no session", not an error — that's
  what lets bounce respawn after a crash.
- **Nudge** — waits for `tui-idle` (typed text mid-turn corrupts the input
  box), then sends the prompt flattened to one line (keystroke simulation
  submits on newline).
- **Close** — closes every terminal titled `<key>:*`; scaffolding tabs
  ("Terminal 1", "Setup") survive.

## Writing a new runner (tmux, ...)

1. Create `internal/runner/<name>/` with:
   ```go
   func init() {
       runner.Register("<name>", runner.Factory{
           UnmarshalConfig: unmarshalConfig, // strict-decode runner.config (empty is fine)
           New: func(cfg any) (runner.Runner, error) { ... },
       })
   }
   ```
2. Implement the interface:
   ```go
   type Runner interface {
       Spawn(t tasks.Ticket, node, agent, prompt string, env map[string]string) error
       Find(t tasks.Ticket, node string) (runner.Session, bool, error)
       Nudge(s runner.Session, prompt string) error
       Close(t tasks.Ticket) error
   }
   ```
   Contract highlights:
   - **Titles are identity**: sessions must be findable later by
     `<key>:<agent>:<node>` — bounce depends on it.
   - **env must reach the agent process** — the report plugin keys off
     `RELAY_FLOW_WORKFLOW/TICKET/NODE/AGENT`.
   - **Find must distinguish "gone" from "error"** — a missing session is a
     normal bounce case (respawn), not a failure.
   - The runner must still launch **opencode** inside whatever session it
     creates — the report plugin is opencode-specific. (tmux example:
     `tmux new-session -s key-agent-node` + `send-keys "RELAY_FLOW_*=... opencode --agent ..." Enter`.)
3. If your runner needs the repo binding, implement
   `WithRepo(repoID, repoName string, dryRun bool)` — the server calls it via
   interface assertion when present.
4. Import for side effects in `internal/server/server.go`.
5. Tests with a fake CLI seam (see `orca_test.go` / the `orcaCLI` interface).
