## 1. Contract and identity

- [x] 1.1 Record permanent cancellation and explicit restart semantics in the proposal, design, and capability specification.
- [x] 1.2 Add a numeric `AttemptID` and stable logical run ID alongside the durable execution `runID`.
- [x] 1.3 Preserve the original deterministic first-attempt ID while deriving fenced suffixed IDs for attempts `2`, `3`, and later.
- [x] 1.4 Add projection migration/backfill for logical run IDs and attempt numbers.

## 2. Run Manager and durable execution

- [x] 2.1 Add `RunManager.RestartByTicket` behind the engine-neutral `Executor`/`RunQueries` boundaries.
- [x] 2.2 Resolve the latest registered workflow and repo at restart time and snapshot the current workflow into the new durable run.
- [x] 2.3 Make normal polling reuse the newest active attempt and refuse to recreate canceled/completed attempts.
- [x] 2.4 Allocate the next numeric attempt under the shared lifecycle gate and make repeated restart requests idempotent while an attempt is active.
- [x] 2.5 Add durable restart preparation for mailbox reopening and stale-terminal closure while preserving the worktree.
- [x] 2.6 Add logical-ID stale-report acknowledgement so old attempt reports cannot affect a newer attempt.
- [x] 2.7 Add adapter-neutral actionable blocked-state messages and automatic retry behavior.

## 3. Task-system adapters

- [x] 3.1 Add the optional `task.RestartPreparer` boundary without adding provider status names to core.
- [x] 3.2 Implement Jira restart mailbox preparation and conflict propagation while leaving the parent status untouched.
- [x] 3.3 Implement Beads restart mailbox preparation for relay-owned states while preserving human-owned states.
- [x] 3.4 Add Jira tests for mailbox reopening and human-state conflicts.
- [x] 3.5 Add Beads tests for mailbox reopening and human-state conflicts.

## 4. API and CLI

- [x] 4.1 Add `POST /runs/by-ticket/{key}/restart` to the Unix-socket HTTP API.
- [x] 4.2 Add `Client.RestartRun` and composition-root wiring through `serveDeps`.
- [x] 4.3 Add `relay-flow run restart --ticket <key>` and expose restart identity in CLI output.
- [x] 4.4 Ensure `run get` exposes state, logical run ID, attempt ID, current node, last error, and retry details.
- [x] 4.5 Add API, CLI, conflict-mapping, and blocked-output tests.

## 5. Verification and E2E record

- [x] 5.1 Add identity, Run Manager, durable engine, projection, and stale-report tests.
- [x] 5.2 Add an end-to-end restart test proving cancel → workflow replacement → resubmit → explicit restart from `start`.
- [x] 5.3 Run the real Beads/Orca/OpenCode scenario in `/tmp/dummy-tui` and capture commands, IDs, statuses, logs, mailbox proof, and cleanup steps in `e2e-verification.md`.
- [x] 5.4 Run `gofmt`, `go test ./...`, `go test -race ./...`, `go vet ./...`, `cd plugin && bun test`, and `git diff --check`.
- [x] 5.5 Archive this completed OpenSpec change with the recorded E2E evidence.
