# Implementation instructions

The Herdr CLI contract below has already been verified with the installed Herdr CLI in a disposable named session. These are instructions and observed facts, not implementation tasks. Do not repeat the live preflight against the user's default Herdr session.

## Production/test boundary

Mocks and strict fake executables are test-only. The production `herdr` factory SHALL always construct the real `herdrclicli.CLI`, which invokes the installed `herdr` binary with `exec.CommandContext`; there SHALL be no config flag, environment switch, build tag, or fallback that selects a fake. A typed `herdrclicli.Client` fake is allowed only inside `_test.go` adapter tests for deterministic decision testing. The strict fake executable tests the real CLI wrapper, and the live smoke task tests the installed Herdr CLI. No production code may import, instantiate, or branch on a fake.

## Verified Herdr CLI contract

- Installed baseline: `herdr 0.8.2`; `herdr api schema --json` reports protocol `20`.
- Global session selection is placed before the subcommand: `herdr --session <name> <command>`. `HERDR_SESSION=<name>` is the equivalent environment selector. `HERDR_SOCKET_PATH` remains supported for an explicit socket.
- Readiness is established by a successful `herdr --session <name> api snapshot`. `herdr status server --json` is diagnostic output and is not the adapter readiness probe.
- Use only these public command forms:

  ```text
  # Operator setup (not a relay-flow adapter operation)
  herdr workspace create --cwd PATH --label LABEL --no-focus

  # Production adapter operations
  herdr api snapshot
  herdr tab create --workspace WORKSPACE_ID --cwd PATH --label LABEL --no-focus --env KEY=VALUE ...
  herdr tab list --workspace WORKSPACE_ID
  herdr pane rename PANE_ID LABEL
  herdr pane run PANE_ID COMMAND...
  herdr pane list --workspace WORKSPACE_ID
  herdr pane get PANE_ID
  herdr pane process-info --pane PANE_ID
  herdr pane close PANE_ID
  ```

- `workspace create` returns `result.workspace` and `result.root_pane`; `tab create` returns `result.tab` and `result.root_pane`; `tab list` returns `result.tabs`; `pane list` returns `result.panes`; `pane get` returns `result.pane`; `pane process-info` returns `result.process_info`; `api snapshot` returns `result.snapshot`.
- Workspace and tab creation accept `--no-focus`. A newly created tab's root pane does not necessarily have a pane label, so call `pane rename` with the exact `<ticket>:<node>` label before using the pane as a recovery handle.
- Repeated `--env KEY=VALUE` options on workspace/tab creation reach the launched pane process. `pane run` submits command text followed by Enter; the wrapper must preserve the complete command as one logical command value while the Herdr CLI accepts its documented `COMMAND...` arguments.
- `pane process-info` reports a foreground process while a command is running. After a Herdr server restart, the workspace ID and public pane ID remain usable, the pane receives a newly generated `terminal_id`, and the pane is restored as a shell. A shell-only restored pane must therefore be relaunched or replaced by relay-flow.
- Herdr has no public `terminal create`, terminal list, terminal health, or general terminal recreate command. Do not invent any of those commands or flags.
- The runner maps one relay-flow repo to one Herdr workspace. Registration and normal startup use an existing unambiguous workspace identified by pane cwd/path; the adapter does not provision or recreate a missing workspace. The operator must use the documented `herdr workspace create` command before registration or restart. Cleanup closes ticket-labelled panes and never deletes the shared workspace. `SetEnvironmentStatus` is a successful no-op.
- Herdr public pane IDs are the only durable runner terminal handles. Never persist or compare `terminal_id` values across restart.

## 1. Freeze the internal wrapper contract

- [x] 1.1 Before writing tests or implementation, use the exact `herdrclicli.Client`, `Options`, `Snapshot`, `Workspace`, `Tab`, `Pane`, `ProcessInfo`, and `ForegroundProcess` names and signatures defined in `design.md` section 1a. Do not invent a different method solely to make a test convenient.
- [x] 1.2 Confirm the production factory will receive the concrete `*herdrclicli.CLI`; the `Client` interface is only the documented adapter test boundary, with no fake-selection configuration or production fake path.
- [x] 1.3 Define the Herdr adapter construction contract in `design.md` before writing adapter behavior tests: the adapter-owned `Config` type and fields, strict raw-config decoding, the production constructor/factory signature, the test construction signature, and the exact package/test boundary.
- [x] 1.4 Choose and document whether the test construction path is an unexported `newAdapter(cli herdrclicli.Client, cfg Config)` helper or another explicitly justified shape. The chosen contract SHALL be intentional and SHALL NOT be inferred by adapter tests from the Orca implementation.
- [x] 1.5 Confirm the production factory always constructs the real `herdrclicli.CLI`; no fake-selection configuration, environment switch, build tag, compatibility fallback, or test-only production constructor is permitted. Keep Tasks 2.4–2.8 blocked until Tasks 1.3–1.5 are complete.

## 2. Behavior tests first

- [x] 2.1 Add a strict fake executable named `herdr` under the runner tests. It SHALL reject every unsupported command, flag, positional argument, and production argv shape; it SHALL validate absolute `--cwd` values and configured Herdr environment selectors. It checks the wrapper's fixed argument order, not an artificial restriction on Herdr's parser.
- [x] 2.2 Add strict CLI-wrapper tests for every production command shape and exact flag: `api snapshot`, `tab create`, `tab list`, `pane list`, `pane get`, `pane process-info`, `pane rename`, `pane run`, and `pane close`. `workspace create` is an operator setup command and is not a production adapter operation.
- [x] 2.3 Add captured-fixture parsing tests for `result.snapshot`, `result.tab`, `result.tabs`, `result.root_pane`, `result.panes`, `result.pane`, and `result.process_info`, including real-shaped error envelopes, empty results, malformed JSON, stderr, and nonzero exits.
- [x] 2.4 After Tasks 1.3–1.5 define the adapter construction contract, add adapter tests for workspace discovery, normalized path matching, label tie-breaking, ambiguous workspace rejection, workspace reuse, missing-workspace errors, and concurrent lookup idempotence.
- [x] 2.5 Using the construction contract from Tasks 1.3–1.5, add adapter tests proving `runner.Terminal.ID` is the public `pane_id`, `terminal_id` changes are ignored, exact `<ticket>:<node>` labels are used, and node/workflow/agent/nodeVisitID metadata never enters the label.
- [x] 2.6 Add adapter tests for live-pane reuse, restored-shell detection, missing-pane replacement, lost-create-ack recovery through the tab label before pane rename and the pane label afterward, multiline `SendTerminal`, and opaque harness command/environment forwarding.
- [x] 2.7 Add cleanup and recovery tests proving only `<ticket>:` panes are closed, unrelated ticket panes remain, shared repository workspaces remain open, missing panes are idempotent, and `SetEnvironmentStatus` is a successful no-op.
- [x] 2.8 Keep all adapter test fakes and strict CLI fixtures under `internal/runner`; do not add production test-only constructors, fake-selection configuration, or permissive mocks that bypass the verified CLI contract. The production factory must always use the real CLI client.

## 3. Implement the Herdr CLI wrapper

- [x] 3.1 Create `internal/runner/herdr/herdrclicli` with the exact client contract and adapter-owned Herdr response values defined in `design.md` section 1a.
- [ ] 3.2 Implement subprocess execution with explicit Herdr session/socket environment selection and absolute path arguments; never call `os.Chdir`, read Herdr storage, import Herdr internals, or use a raw socket client.
- [ ] 3.3 Implement JSON parsing and command/API error propagation while keeping stdout and stderr separate and excluding command/prompt payloads from info-level error logging.
- [ ] 3.4 Implement the exact public production command methods and flag ordering listed in the verified contract, including `tab list` for tab-label recovery and repeated `--env KEY=VALUE` options and pane command/input behavior; exclude the operator-only workspace creation command from the production wrapper.
- [ ] 3.5 Make all strict CLI-wrapper tests pass before implementing the higher-level runner adapter.

## 4. Implement the Herdr runner adapter

- [ ] 4.1 Create `internal/runner/herdr/herdr.go` with adapter-owned strict configuration, the `herdr` factory registration, and construction of the real CLI client.
- [ ] 4.2 Implement `DiscoverRepos` from Herdr snapshot workspaces and pane cwd values, returning deterministic `runner.RepoCandidate` values without exposing Herdr-specific types to core.
- [ ] 4.3 Implement `ValidateRepo` and `EnsureEnvironment` for one pre-existing repository workspace, including normalized path matching, label tie-breaking, ambiguity errors, and deterministic missing-workspace errors without provisioning or recreation.
- [ ] 4.4 Implement node terminal creation as a Herdr tab/root pane with exact ticket/node labels, repeated environment flags, pane-label reconciliation, and command submission through `pane run`.
- [ ] 4.5 Implement pane-handle liveness using `pane get` and `pane process-info`; persist only public pane IDs, detect restored shell-only panes, and replace unusable panes without creating duplicates.
- [ ] 4.6 Implement `EnsureTerminal`, `FindTerminal`, `SendTerminal`, and `CloseTerminal` with find-before-create, tab-label/pane-label recovery, multiline input, and idempotent missing-pane handling.
- [ ] 4.7 Implement ticket-scoped `CloseTerminals` and `CleanupRun` while preserving the shared repository workspace; implement `SetEnvironmentStatus` as a documented successful no-op.
- [ ] 4.8 Ensure adapter logs follow existing runner logging conventions and never include harness command, prompt, environment payload, or Herdr selector secrets at info level.
- [ ] 4.9 Run the adapter tests against both the strict fake CLI and the typed behavior fake; no production path may be covered only by the permissive fake.

## 5. Minimal production wiring

- [ ] 5.1 Add only static blank imports for `internal/runner/herdr` to `cmd/relay-flow/main.go` and `cmd/relay-flow/serve.go` so `herdr` is available to init selection and serve factory construction.
- [ ] 5.2 Confirm no changes are required in the runner interface, durable executor, task system, harness, workflow parser, report transport, or SQLite schema.
- [ ] 5.3 Confirm the existing generic `repo register` path accepts the Herdr runner's `RepoCandidate` values, and that startup preflight, cancellation, normal reconciliation, and `serve --recover` select the Herdr adapter through the existing runner interface without Herdr-specific logic outside `internal/runner`.

## 6. Verification

- [ ] 6.1 Run `gofmt` on changed Go files, `go test ./...`, `go test -race ./...`, and `go vet ./...`.
- [ ] 6.2 Run `cd plugin && bun test` to verify the unchanged harness/report plugin remains green.
- [ ] 6.3 Run a live smoke check after implementation with the installed Herdr CLI and a real configured harness command in a new disposable named Herdr session, using the exact command/readiness/cleanup procedure in `herdr-cli-research.md`; do not substitute the strict fake binary for this check, never use the default session, and never treat a status diagnostic alone as readiness.
- [ ] 6.4 Run `git diff --check` and GitNexus change detection; verify the diff contains no Herdr SDK, raw socket client, direct Herdr storage access, unsupported CLI flags, compatibility fallback, fake-selection path, or changes outside the adapter plus the two required blank imports.
- [ ] 6.5 Leave all user-owned Herdr sessions/workspaces untouched and remove only disposable resources created for verification.
