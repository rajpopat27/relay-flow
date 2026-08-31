# Tasks

## Completed preflight and live demo (reference only)

The mandatory live Beads preflight and dependency demonstration have already been completed. Read `bd-cli-research.md` and `beads-feature.md` before starting Section 3; do not repeat the setup unless `/tmp/beads-demo` has been recreated.

The disposable live environment is running here:

```text
Beads workspace: /tmp/beads-demo/.beads
Dolt data:      /tmp/beads-demo/dolt-data
Dolt server:    127.0.0.1:13307
Server PID:     /tmp/beads-demo/dolt-server.pid
Server log:     /tmp/beads-demo/dolt-server.log
Metadata:       /tmp/beads-demo/.beads/metadata.json
Port record:    /tmp/beads-demo/.beads/dolt-server.port
```

The configured `dolt_server_port` is `13307` in `metadata.json`, and `dolt-server.port` also contains `13307`. The server was started with an explicit fixed port, so a restart must use `--port 13307`; it must not choose a new port. Because the workspace was initialized with `--external`, `bd` does not supervise or automatically restart the Dolt process. If PID `6386` dies, rerun the documented server command on port `13307` and verify it with the readiness probe before continuing.

The current live dependency demo is:

```text
Parent epic:        demo-9u5     Relay-flow Beads adapter demo
Ready child:        demo-9u5.1   Demo: exercise bd CLI client
Blocked child:      demo-9u5.2   Demo: implement Beads adapter
Blocked child:      demo-9u5.3   Demo: verify relay-flow integration
```

```text
demo-9u5.2 blocks on demo-9u5.1
demo-9u5.3 blocks on demo-9u5.2
```

The dependency graph was walked with the real `bd` CLI: closing `.1` made `.2` ready while `.3` remained blocked, and reopening `.1` restored the initial graph. Use returned Beads issue IDs, not titles. The implementation agent must understand this observed hierarchy/dependency/ready behavior before writing mocks.

## Guidelines

- **Source of truth:** this change's `proposal.md`, `design.md`, `beads-feature.md`, `bd-cli-research.md`, and `specs/beads-task-system/spec.md`, together with the existing relay-flow contracts in `docs/structs-methods-interfaces.md` and `docs/feature-tracker.md`.
- **KISS/YAGNI:** use the existing task-system, Repo Poller, runner, harness, and durable-execution seams. Do not add a Beads SDK, direct storage access, `bd serve` lifecycle, compatibility fallbacks, migration tooling, or a second poller.
- **Ordering:** write behavior tests before production adapter code; implement in dependency order; verify last.
- **Status policy:** Beads reads current state with `bd show`, blocks/retries an observed incompatible state, no-ops an already-target state, and otherwise uses an unconditional `bd update`. The small read/write race is accepted for Beads. Do not add `--if-status`.
- **Do not edit:** `docs/structs-methods-interfaces.md` or `docs/feature-tracker.md`.

## 3. Behavior tests first

- [x] 3.1 Add strict fake-`bd` command-shape tests for working directory, `BEADS_DIR`, environment isolation, argv, stdin, JSON arrays/objects, empty results, malformed output, stderr, and ordinary non-zero exits.
- [x] 3.2 Add `bdcli` tests for ready/claimed/child/show/comment operations, child creation, label updates, status updates, close/reopen, and recovery reset. Assert that status operations use `bd show` plus unconditional `bd update` and never `--if-status`.
- [x] 3.3 Add Beads adapter tests for strict config, required `beadsDir`, canonical scope, independent scopes, duplicate scopes, no-op auth, issue normalization, workflow labels, and ready/claimed deduplication.
- [x] 3.4 Add parent/child tests proving every normalized issue with a non-empty `parent` is excluded from polling even if the CLI result contains it. Test stable mailbox titles, missing-child creation, existing-child reuse, description/label reconciliation, duplicate mailbox rejection, and revisits.
- [x] 3.5 Add status tests for expected source, incompatible observed state, already-target state, unconditional update, conflict/retry conversion, recovery reset, and the accepted read/write race. Keep the target-state no-op for durable retry idempotency.
- [x] 3.6 Add comment/template tests for marker detection, duplicate prevention, stdin/multiline bodies, summary destination, selected-next feedback destination, end-with-no-feedback, and strict template validation.
- [x] 3.7 Add repository/composition tests with multiple registered repos proving different `beadsDir` values create independent task systems/pollers and a shared `beadsDir` is rejected. Verify the real repo path reaches `cmd.Dir` and the Beads workspace reaches `BEADS_DIR`.

## 4. Beads implementation

- [x] 4.1 Implement `internal/task/beads/bdcli` and make all command-shape tests pass without importing Beads or reading `.beads/issues.jsonl`/Dolt tables.
- [x] 4.2 Implement `internal/task/beads/beads.go`, factory registration as `beads`, strict config/defaults, required repo keys, canonical `TaskScopeKey`, `New`, and no-op authentication.
- [x] 4.3 Implement `Poll`, issue normalization, claimed-label extraction, and in-memory `CompileFilter`. Read ready and claimed parents separately, merge by ID, and defensively filter children.
- [x] 4.4 Implement `Claim` using `wf:<workflow>` label addition. Do not use `bd ready --claim`, change assignees, or add stale-label cleanup.
- [x] 4.5 Implement `EnsureMailboxes` and `CompleteMailbox` using reusable child issues with stable `<parent-id>:<node>` titles and separate mailbox completion semantics.
- [x] 4.6 Implement Beads status reconciliation: `bd show`, target no-op, incompatible conflict/retry, expected-state unconditional `bd update`; accept the documented race and do not implement `--if-status` or a fallback.
- [x] 4.7 Implement Beads-owned text rendering, `HasComment`, `Comment`, lifecycle defaults, `ApplyTaskConfig`, and `ResetForRecovery`. Preserve comments, labels, descriptions, history, worktrees, and code during recovery.

## 5. Wiring and documentation

- [ ] 5.1 Add static blank imports for `internal/task/beads` to `cmd/relay-flow/main.go` and `cmd/relay-flow/serve.go`. Confirm the existing machine plugin selection, repo registration, startup validation, and one-poller-per-repo wiring use the Beads factory.
- [ ] 5.2 Add a real composition test using the temporary Beads workspace, strict fake `bd`, real repo service, and existing runner/harness seams. Verify poll → route → claim → run creation ordering and mailbox/comment calls.
- [ ] 5.3 Document local and Dolt-server-backed Beads setup, required `beadsDir`, `repo register --set beadsDir=...`, optional repo-specific prefixes, `bd` prerequisites, one poller per registered repo, and the fact that prefixes are not components or workspace selectors.
- [ ] 5.4 Do not add `bd serve` startup, Beads workspace initialization, migration tooling, direct storage access, or runner/harness changes.

## 6. Verification

- [ ] 6.1 Run `gofmt` on changed Go files, `go test ./...`, `go test -race ./...`, `go vet ./...`, and `cd plugin && bun test`.
- [ ] 6.2 Re-run the disposable real-`bd` smoke test and verify parent/child creation, labels, comments, listings, status transitions, close/reopen, and recovery reset against `/tmp/beads-demo`.
- [ ] 6.3 Run `git diff --check` and review that no Beads Go dependency, direct Dolt/JSONL access, `os.Chdir`, `bd serve` lifecycle, or unsupported compatibility fallback was introduced.
- [ ] 6.4 Stop only the recorded disposable Dolt PID and remove `/tmp/beads-demo` after verification. Leave all production repositories and workspaces untouched.
