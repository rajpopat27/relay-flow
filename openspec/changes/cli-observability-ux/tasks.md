## 1. Contract and renderer fixtures

- [ ] 1.1 Add TTY, non-TTY, JSON, `NO_COLOR`, and terminal-width renderer fixtures covering the exact reference layouts in `specs/cli-observability/spec.md`.
- [ ] 1.2 Add status-mark tests for completed, pending, running/waiting, failed/blocked, canceled, and ASCII fallback output.
- [ ] 1.3 Add golden tests for the Argo-shaped `run get` metadata/conditions/timing/progress/step-tree order, including waiting, succeeded, failed, branch, loop, and retry examples.
- [ ] 1.4 Add golden tests for workflow list/get, run list, empty states, retry summaries, actionable next-command hints, and scoped help output.

## 2. Derived run-step read model

- [ ] 2.1 Define the relay-owned display step/detail values and the small read-only query boundary without exposing go-workflows, Temporal, SQLite, runner, or task-system implementation types.
- [ ] 2.2 Add an idempotent `relay_run_steps`-style projection table/schema keyed by run and node-visit/sequence identity, with status, node type, timestamps, duration, message, route, and runtime display fields.
- [ ] 2.3 Add projection insert/update/list methods and tests for retries, duplicate activity delivery, branches, loops, terminal steps, missing optional fields, and stable ordering.
- [ ] 2.4 Add derived run conditions, progress, and resource-duration aggregation without making the display projection an execution authority.
- [ ] 2.5 Add retention and projection-rebuild behavior for step rows consistent with existing run retention and both executor recovery semantics.

## 3. Durable-executor step recording

- [ ] 3.1 Record step lifecycle transitions from the go-workflows interpreter/projection path at existing durable boundaries; do not add an event bus or alter route/retry behavior.
- [ ] 3.2 Record the equivalent step timeline from the Temporal workflow/projection path and preserve authoritative state during explicit Temporal projection recovery.
- [ ] 3.3 Ensure step recording is idempotent and cannot change report acceptance, selected routes, retries, cancellation, mailbox effects, runner effects, or completion state.
- [ ] 3.4 Add cross-executor tests proving both engines return the same display detail shape for equivalent runs.

## 4. Read-only server API

- [ ] 4.1 Extend the run-detail query response with base metadata, conditions, progress, resource-duration summary, and ordered step entries while preserving additive compatibility for clients decoding the base run value.
- [ ] 4.2 Add workflow summary data needed by `workflow list` (repositories, node count, active count, latest-run summary) without N+1 CLI requests.
- [ ] 4.3 Add server/client tests for run detail, workflow summary, filters, missing runs/workflows, and standard JSON success/error envelopes.
- [ ] 4.4 Ensure CLI queries use only the Unix-socket API and never open SQLite or durable-executor state directly.

## 5. Argo-shaped run detail CLI

- [ ] 5.1 Implement the human `run get --ticket` renderer with the exact ordered metadata, conditions, timing, duration/progress/resource summary, and aligned `STEP/DURATION/MESSAGE/RUNTIME` tree structure.
- [ ] 5.2 Render actual node visits, branch routes, loops, retries, and failure messages without inferring completed nodes from the static graph.
- [ ] 5.3 Add waiting, succeeded, failed, canceled, and missing-optional-data rendering with `-`/`unavailable` placeholders instead of fabricated values.
- [ ] 5.4 Preserve `run get --json` as stable structured output and add JSON fixtures for all run states.

## 6. Workflow and run list/detail CLI

- [ ] 6.1 Replace bare workflow-name output with the aligned workflow summary table, status legend, aggregate counts, and actionable empty state.
- [ ] 6.2 Replace tab-separated run rows with the aligned run table, filters, aggregate state counts, retry/error summaries, and actionable empty state.
- [ ] 6.3 Implement `workflow get` metadata plus a cycle-safe static graph/route renderer and recent-run summary, clearly distinguishing definition validity from execution state.
- [ ] 6.4 Preserve existing workflow/run filters, exit codes, server-error handling, and stable JSON output for list/get commands.

## 7. Scoped help and version commands

- [ ] 7.1 Refactor command dispatch/help rendering so root, group, and leaf `--help`/`-h` output only the requested command's usage, flags, examples, and related commands without contacting the server.
- [ ] 7.2 Add grouped root help for setup, workflow, run, repository, and other commands.
- [ ] 7.3 Add build metadata variables and implement `relay-flow version`, `relay-flow --version`, and `relay-flow -v` without server/config requirements.
- [ ] 7.4 Add help/version tests for success exit codes, no server contact, short/long version output, unknown commands, and unknown flags.

## 8. Documentation and final verification

- [ ] 8.1 Update README CLI examples and automation guidance to use `--json` for scripts and document the Argo-shaped `run get` output.
- [ ] 8.2 Add the approved ASCII/Unicode reference diagrams to the capability spec and keep implementation golden fixtures aligned with them.
- [ ] 8.3 Run `gofmt`, targeted CLI/server/projection tests, `GOTOOLCHAIN=auto go test ./...`, `go test -race ./...`, and `go vet ./...`.
- [ ] 8.4 Run `git diff --check`, validate the OpenSpec change, and confirm no workflow YAML, report wire, task-system, runner, harness, or durable execution semantics changed.
