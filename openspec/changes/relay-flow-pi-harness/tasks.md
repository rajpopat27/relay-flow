# Tasks

## Completed investigation and Pi compatibility review (reference only)

The Pi source investigation is complete and recorded in `pi-cli-research.md`. Do not repeat it before implementation unless the targeted Pi version or its extension/CLI contracts change. The implementation agent should read `proposal.md`, `design.md`, and `pi-cli-research.md`, complete the required real-contract capture below, then begin with the behavior tests.

- [x] 0.1 Reviewed the current relay-flow harness, runner, durable execution, runtime registration, report transport, and init wiring. The existing interfaces are sufficient; no core interface change is required.
- [x] 0.2 Reviewed `/home/raj/raj/pi` source and documentation for interactive-mode TTY detection, `--session-id`, `--name`, extension loading, `agent_settled`, session entries, and direct extension UI.
- [x] 0.3 Verified the installed Pi runtime is `0.84.1`; Pi has one built-in coding agent and no named-agent listing command. `pi --list-models` lists models, not agents.
- [x] 0.4 Settled the first implementation scope: use `agent: default`, keep the user's Pi model/settings, keep plugin installation manual, use direct HITL `ctx.ui.select`, and add no auto-install/profile/SDK infrastructure.
- [x] 0.5 Recorded the initial installed Pi 0.84.1/source API contract: no named-agent listing command, no `--agent` or `--interactive` flags, interactive mode is TTY-gated, sessions use `--session-id`, and HITL approval uses `ctx.ui.select` rather than a built-in Question tool. Full live command/lifecycle capture remains a required pre-implementation task below.

## Required installed-runtime contract doubles (reference before implementation)

The detailed installed-runtime findings are recorded in `pi-cli-research.md`. This section is the implementation-facing mock inventory; it must remain consistent with that research record.

The Pi checkout and installed runtime used for source verification are:

```text
source checkout:
  path:    /home/raj/raj/pi
  origin:  https://github.com/earendil-works/pi.git
  branch:  main
  commit:  853a80d26c90a14c1886f0ebb8ffaae133ca2185
  source:  package version 0.84.4
installed runtime:
  command: /home/linuxbrew/.linuxbrew/Cellar/pi-coding-agent/0.84.1/bin/pi
  launcher: /home/linuxbrew/.linuxbrew/Cellar/pi-coding-agent/0.84.1/libexec/bin/pi
  package: /home/linuxbrew/.linuxbrew/Cellar/pi-coding-agent/0.84.1/libexec/lib/node_modules/@earendil-works/pi-coding-agent
  docs:    /home/linuxbrew/.linuxbrew/Cellar/pi-coding-agent/0.84.1/libexec/lib/node_modules/@earendil-works/pi-coding-agent/docs
  version: 0.84.1
```

If a Pi behavior is unclear during implementation, inspect the installed 0.84.1 package, dist declarations/source maps, and docs first, then use the source checkout for implementation context, before changing a mock. Do not use a newer source-checkout behavior unless it is also present in the installed runtime contract. The relevant files are:

```text
/home/linuxbrew/.linuxbrew/Cellar/pi-coding-agent/0.84.1/libexec/lib/node_modules/@earendil-works/pi-coding-agent/dist/index.d.ts
/home/linuxbrew/.linuxbrew/Cellar/pi-coding-agent/0.84.1/libexec/lib/node_modules/@earendil-works/pi-coding-agent/docs/extensions.md
/home/linuxbrew/.linuxbrew/Cellar/pi-coding-agent/0.84.1/libexec/lib/node_modules/@earendil-works/pi-coding-agent/docs/rpc.md
/home/linuxbrew/.linuxbrew/Cellar/pi-coding-agent/0.84.1/libexec/lib/node_modules/@earendil-works/pi-coding-agent/docs/packages.md
/home/raj/raj/pi/packages/coding-agent/src/main.ts
/home/raj/raj/pi/packages/coding-agent/src/cli/args.ts
/home/raj/raj/pi/packages/coding-agent/src/core/extensions/types.ts
/home/raj/raj/pi/packages/coding-agent/src/core/extensions/runner.ts
/home/raj/raj/pi/packages/coding-agent/src/core/agent-session.ts
/home/raj/raj/pi/packages/coding-agent/src/core/session-manager.ts
/home/raj/raj/pi/packages/coding-agent/src/modes/interactive/interactive-mode.ts
/home/raj/raj/pi/packages/coding-agent/examples/extensions/question.ts
```

The implementation must use these strict doubles. They are modeled from the installed Pi `0.84.1` contract; they are not invented substitute APIs:

1. **Pi CLI fake:** a temporary executable on `PATH` only for availability tests. It models only the real `pi` executable lookup/version behavior used by the adapter, validates the real cwd/environment when executed, and rejects invented subcommands or flags.
2. **Command-capture fake:** a fake runner captures the returned `runner.Command` and asserts the real launch shape: `pi`, optional `--name <title>`, optional `--session-id <id>`, `--`, and the prompt. It rejects `--agent`, `--interactive`, `--report`, and unsupported mode/install flags.
3. **Pi event registrar:** a fake `ExtensionAPI.on` stores handlers for actual `session_start` and `agent_settled` events. It does not add OpenCode `session.*` events or nonexistent Pi `question.*` events.
4. **Pi session context:** a fake `ExtensionContext.sessionManager` implements the actual methods used by the plugin, especially `getSessionId()` and `getBranch()`. Branch entries use stable `SessionEntry.id` values and real assistant message fields (`role`, `content`, `stopReason`) from the installed Pi contract.
5. **Pi UI fake:** a fake `ctx.ui.select(title, options, opts?)` returns `string | undefined` and records the exact `Approve`/`Reject` options. No fake Question tool, Question REST API, or invented UI method is allowed.
6. **Pi action fake:** a fake `pi` records `setSessionName()` and `sendUserMessage()`. It does not expose named-agent, model-selection, or profile methods absent from the v0.84.1 integration.
7. **Report transport fake:** the existing relay-flow process fake captures only the real `runtime-register` and `report` commands and their stdin JSON. It validates stdin framing, argv, cwd, and environment, and must not invent Pi-specific transport commands.
8. **PTY boundary:** no CI fake claims to prove TTY behavior. The real PTY requirement is checked only by the manual/local Orca smoke test; automated tests assert that the command does not request print/JSON/RPC mode.

## Required live Pi contract capture (before the main implementation tasks)

The static source/API investigation is complete and recorded in `pi-cli-research.md`, but the following real-runtime contract capture must be completed before production implementation is declared ready. Use the installed Pi `0.84.1` command at `/home/linuxbrew/.linuxbrew/Cellar/pi-coding-agent/0.84.1/bin/pi`; do not substitute a permissive mock. Store sanitized observations/fixtures at `internal/harness/pi/testdata/pi-0.84.1/` or `plugin/testdata/pi-0.84.1/`, according to ownership. This is a required local preflight, not a CI dependency.

- [ ] 0.6 Run the real `/home/linuxbrew/.linuxbrew/Cellar/pi-coding-agent/0.84.1/bin/pi --version` and `--help`; record the version, accepted options, option ordering, positional-message behavior, and confirmed absence of `pi agent list`, `--agent`, and `--interactive`.
- [ ] 0.7 Run the real fresh and resumed command in a local PTY using `--name`, optional `--session-id`, `--`, and a multiline prompt. Record argv, stdin/stdout TTY state, child cwd, inherited relay-flow environment, process lifetime after the first response, and exit/error behavior.
- [ ] 0.8 Run the real manually installed Pi extension in `0.84.1`. Record extension loading, `session_start`, `agent_settled`, session-entry shape, `ctx.sessionManager.getSessionId()`, `getBranch()`, `pi.setSessionName()`, `pi.sendUserMessage()`, and `ctx.ui.select()` behavior with `Approve`/`Reject`.
- [ ] 0.9 Restart the real session with the captured session ID and verify session JSONL persistence, stable session ID/name, resume behavior, and lifecycle events. Record invalid-session and unavailable-server errors without logging credentials.
- [ ] 0.10 Convert the captured behavior into strict CI fakes located only in `*_test.go`, `*.test.ts`, or test fixtures. Update `pi-cli-research.md` with the captured contract before changing the fakes. The fakes must reject incorrect argv order, unsupported flags, incorrect cwd/environment, incorrect stdin framing, invalid event/context shapes, and invented Question APIs. No production behavior may be implemented only against a permissive fake.

## Guidelines

- Read this change's `proposal.md`, `design.md`, `pi-cli-research.md`, and `specs/pi-harness/spec.md` before implementation.
- Follow the existing relay-flow contracts in `docs/structs-methods-interfaces.md`, `docs/feature-tracker.md`, and the completed rewrite change. Do not edit those two normative docs.
- Do not modify the Pi source checkout. The integration belongs in relay-flow's Go adapter and runtime plugin.
- Keep Pi's model/settings user-owned. Pi has one built-in coding agent represented as `agent: default`; use that value in Pi workflow tests and do not add named-agent infrastructure.
- Use the existing task, runner, harness, executor, and server seams. Do not add a new report endpoint, approval field, SQLite table, event bus, or compatibility fallback.
- Pi node commands must run in a real interactive PTY. Never use `-p`, `--mode json`, or `--mode rpc` for the runner-launched path.
- Plugin installation is manual in this change. The user installs the pinned `relay-flow-plugin` package through Pi before running relay-flow; do not add auto-install logic.
- Write behavior tests before production implementation. Do not add real provider/API-key dependencies to tests.
- CI has no Pi or Orca installation. All automated tests must use strict mocks/fakes that mirror the installed Pi 0.84.1 CLI and extension API captured by the live preflight; a temporary mock `pi` executable is allowed only for process-lookup tests, and fake Pi extension contexts are used for runtime behavior. Real binaries are allowed only in explicitly manual local smoke checks.
- Never add a mock-only flag, command, event, response field, session API, or Question API. If a test double needs behavior, first verify that behavior exists in the installed Pi 0.84.1 CLI/API or the recorded live contract. Production code must always use the real Pi CLI/API; no fake-selection configuration, build tags, test fallback, compatibility fallback, or `if test` production path is allowed.

## 1. Behavior tests first

- [ ] 1.1 Add `internal/harness/pi` command tests using a strict live-contract CLI fake. Assert executable `pi`, exact argument order, `--name <ticket>:<node>`, optional `--session-id`, prompt after `--`, exact cwd/environment handoff, no package-install/print/JSON/RPC flags, and the complete `RELAY_FLOW_*` environment contract without `nodeVisitID`. The fake must reject invented flags, including `--agent` and `--interactive`.
- [ ] 1.2 Add Pi harness prompt tests for initial/feedback template substitution, node nudge timing inputs, the `default` agent label, unsupported-agent rejection, and absence of OpenCode Question-tool instructions.
- [ ] 1.3 Add Pi harness validation tests using a temporary mock `pi` executable and controlled `PATH` values rather than a new production registry seam. The mock must implement only the real executable lookup/version behavior used by the adapter and must reject unsupported invocations. Cover unavailable Pi, the available `default` agent, and rejection of unsupported labels without a named-agent registry. The tests must pass when Pi is not installed on the host.
- [ ] 1.4 Add Pi plugin tests with a fake Pi `ExtensionAPI`/context matching the real 0.84.1 types for `session_start` registration, stable session naming, retry after registration failure, `agent_settled` message selection, stable session-entry-based report IDs, and aborted/error output handling. Do not start a Pi process.
- [ ] 1.5 Add Pi plugin HITL tests with a fake `ctx.ui.select` matching Pi's real extension UI method for missing/invalid silence, options exactly `Approve`/`Reject`, approval delivery, rejection/cancel behavior, duplicate settled events, and no-UI behavior. Do not require Pi's TUI or an installed Question tool; do not invent `question.*` events.
- [ ] 1.6 Add Pi plugin agent-node tests for one fixed correction on invalid output, valid report delivery, and no duplicate correction for the same assistant entry.
- [ ] 1.7 Add transport/retry tests using the existing real-command-shape transport fake. Prove report JSON is written unchanged to stdin, retries use identical bytes, duplicate acknowledgements stop delivery, incorrect cwd/environment/argv are rejected, and failed delivery does not create another Pi turn.
- [ ] 1.8 Add package smoke tests proving `relay-flow-plugin` contains both `relay-flow.ts` and `pi.ts`, has a Pi manifest entry, and still loads the OpenCode entry point. Package tests must inspect/load the extension shape without requiring the Pi executable.
- [ ] 1.9 Add init/wiring tests proving Pi appears through the existing dynamic harness registry, interactive selection titles remain unchanged, and `--harness-plugin pi` writes the selected harness without a Pi-specific command branch.

## 2. Pi harness implementation

- [ ] 2.1 Add `internal/harness/pi/pi.go` with package-local factory registration under the name `pi`, adapter-owned defaults, strict config decoding, and the existing five harness methods.
- [ ] 2.2 Implement simple agent validation: accept only `default`, verify the real Pi executable is available with executable lookup, and do not implement named Pi agent discovery or model-ID interpretation.
- [ ] 2.3 Implement the strict Pi `harnessConfig` with only `initial` and `feedback` templates, using the documented defaults, and render through the existing `PromptKind`, `PromptData`, and `nudgeTemplate` contract. Pi HITL approval must not be encoded as an instruction requiring an LLM Question-tool call.
- [ ] 2.4 Implement `BuildCommand` with `pi --name <title> [--session-id <ResumeID>] -- <prompt>` and the required relay-flow environment values. Assume the user has manually installed the runtime plugin.
- [ ] 2.5 Keep `SetupRepo` side-effect free and `FindSession` discovery-free. Normal resume uses only the persisted session ID supplied in `LaunchSpec.ResumeID`.

## 3. Pi runtime plugin and packaging

- [x] 3.1 Add `plugin/pi.ts` as a standard Pi extension factory. Make it a no-op when relay-flow launch metadata is absent.
- [x] 3.2 Register the Pi session on `session_start` with `{runId, node, sessionId}` and set the stable Pi session name when needed.
- [x] 3.3 Process completed assistant output on `agent_settled` by reading the active branch, selecting the latest completed assistant message, extracting text parts, and calling the shared report parser.
- [x] 3.4 Reuse the fixed agent correction and exact report delivery helpers from `plugin/index.ts`. Move the existing Node `spawn` transport into the shared `plugin/transport.ts` module and use it for both OpenCode and Pi wrappers; never pipe JSON through a shell or use `pi.exec` for stdin transport.
- [x] 3.5 Implement direct HITL approval with Pi `ctx.ui.select` using the fixed title `Approve relay-flow report for <ticket>:<node>` and exactly `Approve`/`Reject`. Keep invalid/missing HITL output silent and leave rejection outside workflow routing.
- [x] 3.6 Guard assistant-entry handling and report delivery so duplicate `agent_settled` events cannot open duplicate approval dialogs or retry loops.
- [ ] 3.7 Extend `plugin/package.json` with `pi.extensions: ["./pi.ts"]` and include all required runtime files while retaining the OpenCode `main` entry. Document manual installation; do not add auto-install behavior.
- [ ] 3.8 Update plugin README/package tests for both OpenCode and Pi loading, interactive-only launch behavior, direct HITL approval, shared transport, and the single manual global package-loading strategy.

## 4. Command wiring and documentation

- [ ] 4.1 Add static blank imports for `internal/harness/pi` to `cmd/relay-flow/main.go` and `cmd/relay-flow/serve.go`.
- [ ] 4.2 Confirm existing `harness.Names`, init flags, default loading, serve factory selection, and composition-root wiring require no Pi-specific branching.
- [ ] 4.3 Update the root README and OpenSpec documentation to list Pi as a harness, document `agent: default`, show the interactive Pi command behavior and manual plugin installation, and explain direct HITL UI approval.
- [ ] 4.4 Record Pi `0.84.1` as the version used by the smoke test and acceptance test, and keep the sanitized result in `pi-cli-research.md` plus the relevant test fixtures.

## 5. Interactive and durable verification

- [ ] 5.1 **Manual/local only, required before closing this change, not a CI gate:** run the real contract capture and Orca terminal smoke test with installed Pi `0.84.1`. Verify version/help, accepted flags, argv, stdin, cwd, environment, both TTY streams, process lifetime after the initial response, extension lifecycle, session JSONL persistence, and real restart/resume behavior.
- [ ] 5.2 In automated tests, use strict mock/fake Pi surfaces that follow the recorded 0.84.1 behavior to verify runtime registration and `--session-id` command construction. **Manual/local only:** verify a replacement launch resumes the real Pi session ID and that the captured lifecycle/response behavior matches the implementation.
- [ ] 5.3 Verify with strict extension-context fakes an agent invalid-output correction, a valid agent report, a direct HITL approval, a HITL rejection, and a later approved report; repeat the end-to-end equivalents in the required local smoke test where Pi's real UI is involved.
- [ ] 5.4 Verify node revisit/reconcile behavior preserves the stable `<ticket>:<node>` terminal title while internal `nodeVisitID` changes according to the existing durable engine rules.
- [ ] 5.5 Verify server-unavailable report retries send the exact same JSON and stop on a duplicate/stale acknowledgement using the existing transport fake; do not require a running Pi or relay-flow server process.

## 6. Final verification

- [ ] 6.1 Run `gofmt` on changed Go files and `go test ./...`.
- [ ] 6.2 Run `go test -race ./...` and `go vet ./...`.
- [ ] 6.3 Run `cd plugin && bun test`.
- [ ] 6.4 Run `git diff --check` and inspect that no runner, durable-engine, report-wire, task-system, or Pi-source changes were introduced beyond this change's scope.
- [ ] 6.5 Confirm `pi-cli-research.md` records the real installed contract, every automated Pi double matches the captured real Pi 0.84.1 flag/API/event shape, rejects incorrect argv/cwd/environment/stdin, no mock-only behavior was added, the Pi plugin remains manually installed, no auto-install path was added, CI uses only mocks/fakes, and no print/JSON/RPC launch path is used for interactive nodes.
