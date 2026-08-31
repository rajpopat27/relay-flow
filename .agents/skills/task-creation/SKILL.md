---
name: task-creation
description: Creates implementation-ready, dependency-ordered tasks for features and integrations. Use when planning OpenSpec changes or task lists, especially when external CLIs/APIs, behavior tests, mocks, CI limitations, adapters, or replaceable boundaries are involved.
---

# Task Creation

Create tasks that another implementer can execute without guessing. This skill is generic: replace `<external-tool>`, `<adapter>`, `<resource>`, and `<core-boundary>` with the feature's actual names.

## Non-negotiable rule

Never create a behavior-test task that references an undefined production API, constructor, type, error, or test seam.

If a test would need to invent something like:

```go
adapter.New(fakeClient, config)
```

stop and create a concrete contract task first. Do not infer the API from a similar adapter, and do not ask the implementer to decide it while writing tests.

A concrete prerequisite contract task is correct. A vague meta-task such as “audit the rest of the tasks” is not. The task author must perform the audit and add the actual missing prerequisite.

## Phase 1: Read the source of truth

Before writing tasks:

1. Read `AGENTS.md` and all applicable project instructions.
2. Read the relevant OpenSpec proposal, design, specs, and existing tasks.
3. Read normative interfaces, structs, methods, and feature documents completely.
4. Inspect the current implementation and tests.
5. Identify which behavior is already implemented, which is intentionally absent, and which artifacts are stale or contradictory.

Do not treat an existing implementation as the source of truth for a new contract unless the design explicitly adopts it.

## Phase 2: Inspect the real external system

If the feature integrates with a CLI, API, daemon, SDK, or desktop application, inspect the real system before defining implementation tasks.

For a CLI, use the installed executable where available:

```bash
<external-tool> --version
<external-tool> --help
<external-tool> <command> --help
```

Verify the actual:

- command names and subcommands;
- flags and positional arguments;
- argument ordering chosen by the wrapper;
- stdin and multiline-input behavior;
- environment variables and precedence;
- working-directory and path behavior;
- output envelopes and field locations;
- error envelopes, exit codes, and not-found behavior;
- resource creation, lookup, liveness, cleanup, and restart behavior;
- stable versus ephemeral identifiers;
- concurrency and idempotence characteristics.

Use a disposable external environment for mutating or lifecycle checks. Never modify a user's default workspace, session, database, or credentials.

Record verified facts as implementation instructions in the design/tasks. Do not leave live discovery as an ambiguous future task when it has already been performed.

For an API or SDK, use the real documented wire contract and capture representative successful and failed responses. Do not replace an external contract with a hand-written fake model that was never observed.

## Phase 3: Define the production/test boundary

Separate three layers explicitly:

```text
core/adapter behavior
    ↓ narrow documented port
real transport wrapper
    ↓ actual command/API
external system
```

Production MUST use the real transport implementation. Tests may use:

1. A strict fake executable/server/transport to test the real wrapper's command or wire contract.
2. A typed fake behind an already-documented narrow interface to test adapter policy deterministically.
3. A separate live smoke test using the real installed external system.

Do not add:

- fake-selection configuration;
- test-only production flags or environment switches;
- build-tagged production fallbacks;
- `if test` branches;
- global mutable command hooks;
- a fake interface invented solely because a test needs an API that production does not yet have;
- permissive mocks that accept commands the real external tool does not accept.

If a lower-level test seam is needed, define its exact types, methods, constructor, visibility, and production wiring in `design.md` before any test task uses it. Prefer an unexported adapter construction helper when no external caller needs dependency injection. Keep fakes in `_test.go` or test fixtures.

## Phase 4: Resolve design decisions before task ordering

Resolve or explicitly record:

- ownership and scope of external resources;
- application resource versus provider resource naming;
- stable logical identity versus provider runtime handles;
- provisioning policy;
- cleanup ownership and what must be preserved;
- liveness predicate;
- restart and recovery behavior;
- not-found versus retryable error behavior;
- command/response serialization;
- configuration fields and precedence;
- concurrency/idempotence rules;
- CI availability of external tools;
- supported platform/runtime constraints.

Do not leave a task saying “handle errors” or “support recovery” without specifying the observable behavior. Keep the design small: use a sentinel/helper or simple predicate when that is enough; do not invent a framework or taxonomy without a requirement.

## Phase 5: Freeze every API needed by tests

Before behavior tests, document the exact production-facing contracts they will call:

- adapter-owned config type and strict decoding;
- production constructor/factory;
- test construction path, if one is needed;
- narrow transport interface and its method signatures;
- transport value types and observed response fields;
- error/sentinel classification needed by the adapter;
- command serialization rules;
- liveness and cleanup semantics.

Example:

```go
type Client interface {
    Snapshot(context.Context) (Snapshot, error)
    GetResource(context.Context, string) (Resource, error)
    CreateResource(context.Context, ResourceSpec) (Resource, error)
}

type Config struct {
    Endpoint string `yaml:"endpoint,omitempty"`
}

func New(raw config.RawValues) (runner.Runner, error)       // production
func newAdapter(Client, Config) (*adapter, error)           // internal tests
```

The exact shape must come from the feature design. The example is not a license to invent one.

## Phase 6: Build dependency-ordered tasks

Use this default order for an external integration:

```text
1. Contract definition
2. Strict transport/wire behavior tests
3. Transport wrapper implementation
4. Adapter behavior tests
5. Adapter implementation
6. Minimal production wiring
7. Live smoke verification
```

A design-only contract task is not implementation. It exists so tests can be written against an agreed API.

Every task must state its prerequisite when it depends on a contract:

```text
Adapter behavior tests depend on the adapter Config and construction
contract. They MUST NOT invent a constructor or fake seam.
```

Do not mark a task complete merely because a similar task exists elsewhere. Check that the referenced symbol or artifact is actually present and accepted.

### Task quality

Each task must:

- use `- [ ] X.Y` checkbox syntax;
- describe one verifiable outcome;
- name the files/package or contract it affects when useful;
- be small enough for one focused implementation session;
- avoid mixing design, tests, implementation, and verification unless they are one inseparable vertical slice;
- state what must not be changed when scope is intentionally narrow.

Avoid tasks that say only “implement support” or “add tests.” Name the behavior and observable assertion.

## CLI-backed test strategy

For a CLI-backed integration, require both:

### Strict wrapper contract tests

The test installs a fake executable with the exact real tool name earlier on `PATH`. It must:

- reject unsupported commands and flags;
- reject malformed positional arguments;
- verify the wrapper's chosen argv shape and option order;
- verify absolute paths and environment selectors;
- return captured-like successful JSON responses;
- return captured-like stderr/error responses and nonzero exits;
- exercise stdin and multiline values where applicable.

These tests invoke the real Go wrapper, not a fake wrapper object.

### Adapter behavior tests

After the adapter construction contract exists, use a narrow typed fake to test decisions such as:

- resource lookup and scope mapping;
- ambiguity handling;
- stable/ephemeral ID rules;
- liveness and restart decisions;
- find-before-create;
- cleanup ownership;
- idempotence and retry behavior.

The fake must model the same fields and errors exercised by the strict wrapper fixtures. It must not become a second invented external implementation.

### Live smoke test

Keep real external-tool verification separate from default unit/CI tests when CI does not install the tool. The live test uses:

- the installed real executable/API;
- a disposable named session/workspace/resource;
- a real configured harness or command;
- explicit readiness checks based on a successful API operation, not a weak process/status signal;
- deterministic cleanup that leaves user-owned resources untouched.

The live smoke test is manual or a dedicated environment job unless the repository explicitly provisions the external dependency in CI.

## CI rule

Default CI must run without the external tool unless the project explicitly installs and provisions it.

Use the strict fake executable for wrapper tests. The production binary still resolves the real tool name; the fake is visible only because the test changes `PATH` or uses an equivalent test-local transport setup.

Never skip required default behavior tests merely because the external tool is absent. Put real-tool checks in a separate explicit job or smoke command.

## KISS rule

Add the smallest contract and task set that closes real ambiguity. Prefer:

- one narrow interface;
- one concrete production client;
- one simple sentinel for not-found when needed;
- one direct liveness predicate;
- one strict executable fixture;
- one live smoke path;
- existing core seams and registries.

Do not add a generic repository layer, event bus, DI framework, compatibility fallback, migration path, provider SDK, or extra abstraction unless the source of truth requires it.

## Final task-list review

Before declaring the task list ready, personally verify:

- every test task can name the exact API it will call;
- every adapter constructor/config/error seam is defined before adapter tests;
- every external command/API shape is based on real observation;
- production wiring uses the real integration;
- CI does not require unavailable tools;
- live verification is separate and safe;
- resource identity, cleanup, restart, and liveness are unambiguous;
- no task asks the implementer to guess or perform a meta-audit;
- no unrelated package, capability, or core interface is added.

## Required planning report

When handing the tasks to an implementer, report:

1. What was inspected and verified.
2. What is already implemented.
3. Which contracts were frozen.
4. Which tasks are complete, blocked, or ready.
5. Why each test seam is test-only.
6. Which CI commands work without the external tool.
7. Which live commands require the real external system.
8. Any remaining question that genuinely cannot be resolved from the source of truth.
