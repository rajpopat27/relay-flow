# Implementation instructions

The Herdr CLI contract below was verified with the installed Herdr CLI in disposable named sessions, and every JSON fixture in `internal/runner/herdr/herdrcli/testdata` is captured output with only paths and host identifiers sanitized. These are instructions and observed facts, not implementation tasks. Do not repeat the live preflight against the user's default Herdr session.

## Production/test boundary

Mocks and strict fake executables are test-only. The production `herdr` factory SHALL always construct the real `herdrcli.CLI`, which invokes the installed `herdr` binary with `exec.CommandContext`; there SHALL be no config flag, environment switch, build tag, or fallback that selects a fake. A typed `herdrcli.Client` fake is allowed only inside `_test.go` adapter tests. The strict fake executable tests the real CLI wrapper, and the live test (`RELAY_FLOW_HERDR_LIVE=1`) drives that same wrapper against the installed binary.

## Verified Herdr CLI contract

- Installed baseline: `herdr 0.8.2`; `herdr api schema --json` reports protocol `20`.
- Global session selection is placed before the subcommand: `herdr --session <name> <command>`. `HERDR_SESSION=<name>` is the equivalent environment selector. `HERDR_SOCKET_PATH` remains supported for an explicit socket.
- Transport contract: success returns `{"id":...,"result":{...}}` on **stdout** with exit 0; failure returns `{"id":...,"error":{"code","message"}}` on **stderr** with exit 1 and empty stdout. There is no `ok` field. `pane run` prints nothing on success.
- Observed error codes: `pane_not_found`, `workspace_not_found`, `worktree_not_found`, `not_git_worktree`, `worktree_create_failed`.
- Use only these public command forms:

  ```text
  herdr api snapshot
  herdr worktree list --cwd PATH
  herdr worktree create --cwd PATH --branch NAME --base REF --label TEXT --no-focus
  herdr worktree open --cwd PATH --branch NAME --label TEXT --no-focus
  herdr tab create --workspace WORKSPACE_ID --cwd PATH --label LABEL --no-focus
  herdr tab list --workspace WORKSPACE_ID
  herdr pane rename PANE_ID LABEL
  herdr pane run PANE_ID COMMAND
  herdr pane list --workspace WORKSPACE_ID
  herdr pane get PANE_ID
  herdr pane process-info --pane PANE_ID
  herdr pane close PANE_ID
  herdr workspace close WORKSPACE_ID
  ```

- Response locations: `result.snapshot`, `result.source`/`result.worktrees`, `result.workspace`/`result.worktree`, `result.tab`/`result.root_pane`, `result.tabs`, `result.panes`, `result.pane`, `result.process_info`.
- Every workspace reports `worktree{repo_root, repo_name, checkout_path, is_linked_worktree}`, which is the repository identity used for discovery and validation.
- `worktree list` and `worktree create` work with no workspace open, so registration requires no operator setup. `worktree create` reuses an existing branch, so `--base` matters only for a new branch and prior agent commits are never discarded. `worktree open` returns `worktree_not_found` when the branch has no checkout.
- Reopening a closed checkout produces a **new** workspace ID: workspace IDs are current handles, never durable identity. The durable identity is the ticket branch and its checkout path.
- `workspace close` keeps the checkout on disk; the adapter never runs `worktree remove`.
- A newly created tab's root pane has no pane label, so `pane rename` applies the exact `<ticket>:<node>` label; the tab label is the recovery marker until then.
- Command environment is rendered into the `pane run` command line; `--env` is deliberately not used so a reused pane cannot inherit a previous run's values.
- After a Herdr restart, the pane is restored as a shell with a new `terminal_id`; `pane process-info` reports the shell itself in the foreground, which is how the adapter detects an unusable pane.
- Herdr has no public `terminal create`, terminal list, terminal health, or terminal recreate command. Do not invent any of those.

## 1. Freeze the internal wrapper contract

- [x] 1.1 Use the exact `herdrcli.Client`, `Options`, `Snapshot`, `Workspace`, `WorkspaceWorktree`, `Worktree`, `WorktreeListing`, `WorktreeSource`, `Tab`, `Pane`, `ProcessInfo`, and `ForegroundProcess` names and signatures defined in `design.md` section 1a, plus the sentinel errors `ErrPaneNotFound`, `ErrWorkspaceNotFound`, `ErrWorktreeNotFound`, and `ErrNotGitWorktree`. Do not invent a different method solely to make a test convenient.
- [x] 1.2 Confirm the production factory will receive the concrete `*herdrcli.CLI`; the `Client` interface is only the documented adapter test boundary, with no fake-selection configuration or production fake path.
- [x] 1.3 Define the Herdr adapter construction contract in `design.md` before writing adapter behavior tests: the adapter-owned `Config` type and fields, strict raw-config decoding, the production constructor/factory signature, the test construction signature, and the exact package/test boundary.
- [x] 1.4 Choose and document whether the test construction path is an unexported `newAdapter(cli herdrcli.Client)` helper or another explicitly justified shape. The chosen contract SHALL be intentional and SHALL NOT be inferred by adapter tests from the Orca implementation.
- [x] 1.5 Confirm the production factory always constructs the real `herdrcli.CLI`; no fake-selection configuration, environment switch, build tag, compatibility fallback, or test-only production constructor is permitted. Keep Tasks 2.4–2.8 blocked until Tasks 1.3–1.5 are complete.

## 2. Behavior tests first

- [x] 2.1 Add a strict fake executable named `herdr` under the runner tests. It SHALL reject every unsupported command, flag, positional argument, and production argv shape; it SHALL validate absolute `--cwd` values and configured Herdr environment selectors. It checks the wrapper's fixed argument order, not an artificial restriction on Herdr's parser.
- [x] 2.2 Add strict CLI-wrapper tests for every production command shape and exact flag: `api snapshot`, `worktree list`, `worktree create`, `worktree open`, `tab create`, `tab list`, `pane list`, `pane get`, `pane process-info`, `pane rename`, `pane run`, `pane close`, and `workspace close`. `workspace create` and `worktree remove` are not production adapter operations.
- [x] 2.3 Add parsing tests against captured fixtures for `result.snapshot`, `result.source`/`result.worktrees`, `result.workspace`/`result.worktree`, `result.tab`, `result.tabs`, `result.root_pane`, `result.panes`, `result.pane`, and `result.process_info`, including captured error envelopes on stderr with nonzero exits, empty results, malformed JSON, and stderr warnings. Fixtures SHALL be captured from the installed CLI, never hand-written.
- [x] 2.4 After Tasks 1.3–1.5 define the adapter construction contract, add adapter tests for repository discovery deduplicated by repository root, repository-root validation, ticket-worktree reuse, ticket-worktree creation from the resolved origin base, the full base-ref ladder, and propagation of transport failures.
- [x] 2.5 Using the construction contract from Tasks 1.3–1.5, add adapter tests proving `runner.Terminal.ID` is the public `pane_id`, `terminal_id` changes are ignored, exact `<ticket>:<node>` labels are used, and node/workflow/agent/nodeVisitID metadata never enters the label.
- [x] 2.6 Add adapter tests for live-pane reuse, restored-shell detection, missing-pane replacement, lost-create-ack recovery through the tab label before pane rename and the pane label afterward, multiline `SendTerminal`, and opaque harness command/environment forwarding.
- [x] 2.7 Add cleanup and recovery tests proving only `<ticket>:` panes are closed, unrelated panes remain, `CleanupRun` closes the ticket workspace without removing the worktree, an absent repository/checkout/workspace rolls forward as success, transport failures still propagate, and `SetEnvironmentStatus` is a successful no-op.
- [x] 2.8 Keep all adapter test fakes and strict CLI fixtures under `internal/runner`; do not add production test-only constructors, fake-selection configuration, or permissive mocks that bypass the verified CLI contract. The production factory must always use the real CLI client.

## 3. Implement the Herdr CLI wrapper

- [x] 3.1 Create `internal/runner/herdr/herdrcli` with the exact client contract, adapter-owned Herdr response values, and error sentinels defined in `design.md` section 1a.
- [x] 3.2 Implement subprocess execution with explicit Herdr session/socket environment selection and absolute path arguments; never call `os.Chdir`, read Herdr storage, import Herdr internals, or use a raw socket client.
- [x] 3.3 Implement the observed transport contract: parse `result` from stdout, parse the error envelope from stderr, map documented codes to sentinels, return other failures unchanged, and exclude command/prompt payloads from error logging. There is no `ok` field.
- [x] 3.4 Implement the exact public production command methods and flag ordering listed in the verified contract, including `worktree list/create/open` for environments, `tab list` for label recovery, and `workspace close` for cleanup; exclude `workspace create` and `worktree remove` from the production wrapper.
- [x] 3.5 Make all strict CLI-wrapper tests pass before implementing the higher-level runner adapter.

## 4. Implement the Herdr runner adapter

- [x] 4.1 Create `internal/runner/herdr/herdr.go` with adapter-owned strict configuration, the `herdr` factory registration, and construction of the real CLI client.
- [x] 4.2 Implement `DiscoverRepos` from the `worktree` identity each snapshot workspace reports, deduplicated by repository root, returning deterministic `runner.RepoCandidate` values without exposing Herdr-specific types to core.
- [x] 4.3 Implement `ValidateRepo` through `worktree list` against the repository root, and `EnsureEnvironment` as open-then-create of the ticket-branch worktree with the origin base-ref ladder; return the open workspace ID and checkout path, and never treat the workspace ID as durable identity.
- [x] 4.4 Implement node terminal creation as a Herdr tab/root pane in the ticket worktree workspace with exact ticket/node labels, pane-label reconciliation, and command submission through `pane run` with the environment rendered into the command line.
- [x] 4.5 Implement pane-handle liveness using `pane get` and `pane process-info`; persist only public pane IDs, detect restored shell-only panes, and replace unusable panes without creating duplicates.
- [x] 4.6 Implement `EnsureTerminal`, `FindTerminal`, `SendTerminal`, and `CloseTerminal` with find-before-create, tab-label/pane-label recovery, multiline input, and idempotent missing-pane handling.
- [x] 4.7 Implement ticket-scoped `CloseTerminals`, and `CleanupRun` that also closes the ticket workspace while preserving the worktree, branch, and files; roll forward when the environment is absent; implement `SetEnvironmentStatus` as a documented successful no-op.
- [x] 4.8 Ensure adapter logs follow existing runner logging conventions and never include harness command, prompt, environment payload, or Herdr selector secrets at info level.
- [x] 4.9 Run the adapter tests against both the strict fake CLI and the typed behavior fake; no production path may be covered only by the permissive fake.

## 5. Minimal production wiring

- [x] 5.1 Add only static blank imports for `internal/runner/herdr` to `cmd/relay-flow/main.go` and `cmd/relay-flow/serve.go` so `herdr` is available to init selection and serve factory construction.
- [x] 5.2 Confirm no changes are required in the runner interface, durable executor, task system, harness, workflow parser, report transport, or SQLite schema.
- [x] 5.3 Confirm the existing generic `repo register` path accepts the Herdr runner's `RepoCandidate` values, and that startup preflight, cancellation, normal reconciliation, and `serve --recover` select the Herdr adapter through the existing runner interface without Herdr-specific logic outside `internal/runner`.

## 6. Verification

- [x] 6.1 Run `gofmt` on changed Go files, `go test ./...`, `go test -race ./...`, and `go vet ./...`.
- [x] 6.2 Run `cd plugin && bun test` to verify the unchanged harness/report plugin remains green.
- [x] 6.3 Run the live wrapper test (`RELAY_FLOW_HERDR_LIVE=1 go test ./internal/runner/herdr/herdrcli/ -run Live`) against the installed Herdr binary in a disposable named session. It SHALL drive the production Go wrapper, not hand-run CLI commands; readiness is a successful wrapper `Snapshot`; never use the default session.
- [x] 6.6 Run the full manual end-to-end procedure in `e2e-test.md` against a real task system, a real repository, and a real agent, and confirm every documented assertion: registration provisions nothing, one worktree workspace per ticket, one labelled pane per node, handoff feedback on the next mailbox only, cleanup closing panes and the workspace while preserving the worktree, and zero errors or warnings in the server log.
- [x] 6.4 Run `git diff --check` and GitNexus change detection; verify the diff contains no Herdr SDK, raw socket client, direct Herdr storage access, unsupported CLI flags, compatibility fallback, fake-selection path, or changes outside the adapter plus the two required blank imports.
- [x] 6.5 Leave all user-owned Herdr sessions/workspaces untouched and remove only disposable resources created for verification.
