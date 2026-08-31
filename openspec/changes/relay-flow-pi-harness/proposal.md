# Add Pi as a harness

## Why

Relay-flow currently has OpenCode as its only built-in harness. Pi can provide the same launch, session, prompt, and structured-report behavior through its CLI and TypeScript extension API, while also allowing the runtime extension to ask HITL approval directly through Pi's host UI.

The integration should reuse the existing harness, runner, durable execution, and report contracts. It must launch a persistent interactive Pi session in the Orca terminal, not a one-shot print-mode process.

## What Changes

- Add a statically registered `pi` implementation of the existing `harness.Harness` interface.
- Launch `pi` in its default interactive TUI mode through the existing opaque `runner.Command` boundary.
- Resume persisted Pi sessions with Pi's exact `--session-id` option and name sessions with the stable `<ticket>:<node>` identity.
- Add a Pi runtime extension to the existing `relay-flow-plugin` package.
- Reuse the existing report parser, JSON transport, retry policy, and durable report endpoint.
- Use Pi's direct `ctx.ui.select()` API for HITL `Approve`/`Reject` decisions instead of asking the LLM to call a Question tool.
- Make Pi use its single built-in coding agent with the user's normal Pi model/settings. Relay-flow represents that agent as `agent: default`; it is not an OpenCode-style named-agent selector.
- Require the user to install the runtime package manually in Pi's global package settings before running the harness; relay-flow init does not install it.
- Make Pi appear automatically in the existing `relay-flow init` harness selection and support `--harness-plugin pi` without a new command surface.

## Goals

- Run relay-flow nodes in persistent, interactive Pi sessions.
- Preserve terminal/session identity across revisits and normal restart.
- Keep `nodeVisitID` internal and use a stable Pi session/message-entry identity for `reportId`.
- Let Pi extensions nudge invalid agent output and directly gate HITL reports on human approval.
- Keep the existing task system, runner, durable engine, server, report wire format, and workflow graph unchanged.
- Keep tests independent of real model providers by using the `default` agent label and fake external commands where appropriate.
- Follow KISS/YAGNI: reuse the existing harness, runner, durable-execution, and plugin seams without adding profiles, SDK hosting, auto-installation, or compatibility infrastructure.
- Make CI doubles strict Pi 0.84.1 contract fakes derived from the inspected CLI and extension API; do not invent flags, commands, events, or Question APIs that Pi does not provide.

## Non-Goals

- Adding named Pi agent profiles or changing the meaning of the workflow `agent` field into a model ID. Pi has one logical relay-flow agent, `default`, in this change.
- Selecting a model, provider, tools, or system prompt from relay-flow in the first version; Pi uses the user's normal Pi configuration.
- Modifying the Pi source repository.
- Adding a new durable approval API, report field, database table, or server endpoint.
- Using Pi print mode, JSON event mode, or RPC mode for runner-launched nodes.
- Making the Pi runtime extension call the task system, write SQLite, or manage runner environments.
- Automatic plugin installation or repository configuration; plugin installation remains a manual global Pi prerequisite.
- Permissive test doubles or production-only behavior that is not verified against the installed Pi CLI/API.

## Impact

The change adds one Go harness adapter and one Pi runtime-extension entry point. The existing `relay-flow-plugin` package gains a Pi manifest entry while retaining its OpenCode entry point. `cmd/relay-flow/main.go` and `cmd/relay-flow/serve.go` add only the static Pi adapter import needed by the existing registry.

The existing `harness.Harness` interface is sufficient. No changes are expected in `internal/execution/goworkflows`, `internal/runner`, `internal/server`, `internal/run`, or the report protocol.

Pi's runtime package is installed manually by the user in Pi's global package settings, for example `pi install npm:relay-flow-plugin@<version>`; relay-flow does not install or configure it. A real Orca/PTY smoke test is required before this change is complete because Pi chooses print mode whenever either standard stream is not a TTY, but it is a local/manual acceptance check rather than a CI gate. CI SHALL use strict installed-runtime Pi 0.84.1 contract fakes, a mock `pi` executable where process lookup is tested, and fake runner/plugin boundaries; CI must not require Pi, Orca, or model-provider credentials to be installed. Local interactive verification targets the installed Pi `0.84.1` binary and records the real CLI/API behavior used by the implementation.

## User-facing Pi documentation contract

The machine harness selection remains generic: choose `harnessPlugin: pi` (or
`relay-flow init --harness-plugin pi` with the existing task and runner flags),
and use `agent: default` in Pi workflow nodes. The label identifies Pi's one
built-in coding agent; model, provider, tools, extensions, and other runtime
settings remain user-owned.

Before launching Pi nodes, install the published runtime package manually in
Pi's global package settings:

```text
pi install npm:relay-flow-plugin@<version>
```

Pi 0.84.1 nodes use the interactive command shape
`pi --name <ticket>:<node> [--session-id <session-id>] <prompt>`. The prompt is
positional and there is no bare `--` terminator: Pi 0.84.1 rejects that option.
The runner supplies the required PTY, while relay-flow keeps report and
registration JSON on their separate stdin transport.

For valid HITL reports, the Pi extension asks through the host UI with the
fixed title `Approve relay-flow report for <ticket>:<node>` and exactly the
`Approve` and `Reject` choices. Approve submits the report; Reject or Escape
leaves the durable run waiting. This is direct `ctx.ui.select()` approval, not
an LLM Question-tool call.
