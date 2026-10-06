# Tasks — relay-flow-auz

Implement bounded machine prerequisites without restoring workflow-wide startup
validation. Parent ticket `relay-flow-auz` contains the approved design. The
existing runtime contracts and the two normative `docs/` files stay untouched.

## 1. Runner readiness (first bounded slice)

- [x] 1.1 Add a separate runner-owned `StartupProber` boundary and dispatch that fails explicitly for a selected runner without probe support. Limit each call to five seconds within the caller's deadline.
- [x] 1.2 Orca: one read-only repo-list request, accept an empty array, reject missing/null/malformed response envelopes. Select the explicit session launcher, dev launcher, or platform default once and reuse the resolved executable without fallback.
- [x] 1.3 Herdr: one snapshot, accept an empty runtime, reject missing/null/malformed snapshot arrays, preserve configured session/socket selectors, and perform no per-workspace discovery or writes.
- [x] 1.4 Test runner dispatch, single attempts, earlier deadlines, missing executable, launch/runtime failures, malformed responses, selector propagation, and cancellation including inherited output pipes. Update the strict Orca executable fake and document launcher selection.

## 2. Task and harness prerequisites (second bounded slice)

- [x] 2.1 Add minimal adapter-owned task startup dispatch; preserve local-only `NewLocal`. Check Jira credentials once without project/status/assignee queries, using a bounded single-attempt REST path while retaining normal runtime retries and redaction.
- [x] 2.2 Beads: check local `bd`; probe each distinct explicitly configured workspace once with the existing harmless limited CLI probe and its cwd/BEADS_DIR isolation. With no workspace, report connection verification deferred to registration. Never use an ambient workspace or auto-start/bootstrap services.
- [x] 2.3 Harness: resolve only the selected local Pi/OpenCode executable; do not enumerate agents, scan repo templates, or launch sessions. Add focused tests for task/harness paths.

## 3. Serve readiness and regression coverage

- [x] 3.1 After local plugin construction and before workers, pollers, recovery side effects or socket readiness, run selected dependency probes under a ten-second aggregate deadline and five-second per-external-probe ceiling. Include `runner.ProbeStartup`; require unsupported selected plugins to fail rather than silently skip. Do not multiply calls by workflows/nodes.
- [x] 3.2 Preserve zero-repo startup, stored workflow integrity isolation, full submission validation, and durable identity/recovery behavior.
- [x] 3.3 Propagate sanitized foreground and detached-child dependency/operation errors plus the log location. Preserve environment inheritance and existing-server reuse in guided onboarding. Keep `cmd/rf` symlinks intact.
- [x] 3.4 Cover both serve modes, selected-plugin-only calls, all dependency failure categories, no-repo/deferred Beads behavior, ordering, latency, invalid stored workflow isolation, and failed submission preserving accepted state. Update alternate plugin fakes for required probe support.
- [x] 3.5 Document the completed startup/submission split, time budgets, no-repo behavior and error categories in README and the affected OpenSpec specs.

## 4. Final verification

- [x] 4.1 Focused tests; full `GOTOOLCHAIN=auto go test ./...`; affected race tests; `GOTOOLCHAIN=auto go vet ./...`; `git diff --check`; GitNexus change detection. Record actual failures and limitations; do not fix unrelated execution behavior in this issue.

## First-slice verification

Passed: `GOTOOLCHAIN=auto go test ./internal/runner/... -count=1`,
`GOTOOLCHAIN=auto go test -race ./internal/runner/... -count=1`,
`GOTOOLCHAIN=auto go test ./...`, `GOTOOLCHAIN=auto go vet ./...`,
`git diff --check`, and
`openspec validate serve-startup-prerequisites --strict --no-interactive`.
GitNexus unstaged change detection reports MEDIUM risk in the runner CLI paths.
An earlier full-suite attempt with a three-minute package timeout was
inconclusive; the normal full suite subsequently passed, with the
`goworkflows` package taking about six minutes. No live dependency services
were launched or probed for verification. No commit was created.

The dependency probes are now called by `serve` before execution/recovery or
socket readiness. All implementation slices and final verification are complete;
see the final verification record below. Earlier slice records are historical.

## Second-slice boundaries

- Core calls `task.ProbeStartup(ctx, name, rootTaskConfig, machineRepos)` under
  its aggregate deadline. The selected factory must provide `ProbeStartup`;
  missing support is an error. Jira makes one credential request capped at five
  seconds without retries/redirects; normal runtime calls retain four retries.
- Beads canonicalizes/deduplicates explicit root and repo workspaces. It uses
  a registered code cwd when available, or the explicit root workspace itself
  when no repo supplies that connection. Root config never substitutes for a
  missing required repo `beadsDir`. No workspace means an informational deferred
  verification message after checking local `bd`, not an ambient probe.
- Core calls `harness.ProbeStartup(ctx, selectedHarness)`. Selected harnesses
  must provide the separate `StartupProber` boundary; Pi/OpenCode only resolve
  their executable and never validate/enumerate agents or launch sessions.
- Section 3.1 updates alternate named task factories and harness/runner fakes
  with the required probe boundaries. Existing production CLI lifecycle tests
  now use isolated Jira HTTP and Orca/OpenCode executable prerequisites rather
  than implicitly relying on absent or installed dependencies.

## Second-slice verification

Passed: `GOTOOLCHAIN=auto go test ./internal/task/... ./internal/harness/... -count=1`,
`GOTOOLCHAIN=auto go test -race ./internal/task/... ./internal/harness/... -count=1`,
`GOTOOLCHAIN=auto go test ./...`, `GOTOOLCHAIN=auto go vet ./...`,
`git diff --check`, and strict validation of this OpenSpec change.
The full suite's `goworkflows` package took about six minutes. Runtime Jira
rate-limit retry tests still pass alongside the new zero-retry startup tests.
GitNexus reports CRITICAL aggregate impact because task factories and the
shared Jira request helper reach auth, registration and execution consumers;
those packages are covered by the full suite. An incremental GitNexus FTS
failure was repaired by a successful index-only full rebuild, and subsequent
change detection completed. No application services were launched for
verification, and no commit was created. This verification predates the
section 3.1 serve gate; its current verification is recorded below.

## Third-slice scope

Implemented the ten-second aggregate gate in `serveRoot` after local adapter
construction, before workflow loading, engine opening/workers, pollers,
recovery rename/reset effects, or socket readiness. The existing readiness
request remains a relay-flow API liveness check, not a dependency re-probe.
Added regressions for selected-plugin-only calls, zero repos, unsupported probe
boundaries, pending probes, normal/recovery failure ordering, per-probe and
aggregate deadlines, accepted-workflow preflight avoidance and malformed
workflow isolation. Foreground/default detached dependency failures now have
real executable regressions proving nonzero exits and no success/readiness.

Pending at the end of the third slice: section 3.3 must enrich detached-child errors with the sanitized
selected dependency/operation from the current child, alongside the server-log
location; foreground diagnostics should also include that location. Review
onboarding propagation without changing existing-server reuse. Section 3.4
still needs the remaining full-category serve regressions and failed-submission
state preservation coverage. Section 3.5 needs final failure documentation and
the affected workflow-repo-management OpenSpec delta. No commit was created.

## Third-slice verification

Passed: targeted startup/readiness/CLI lifecycle tests,
`GOTOOLCHAIN=auto go test ./...`,
`GOTOOLCHAIN=auto go test -race ./cmd/relay-flow -count=1`,
`GOTOOLCHAIN=auto go vet ./...`, `git diff --check`, and strict validation of
this OpenSpec change. The CLI package took about three minutes in both the
full suite and its uncached race run. Existing internal package results were
cached from the previously verified slices. GitNexus aggregate change detection
reports CRITICAL risk across shared factories, Jira requests and CLI startup.
Its incremental FTS failure was repaired with a successful index-only full
rebuild before change detection. Normative docs and `cmd/rf` symlinks remain
unchanged. Tests used isolated HTTP/executable dependencies, not installed
Jira, Orca, Herdr, or Dolt services. Sections 3.3–3.5 were still pending at the
end of that slice; the final slice completes them.

## Final slice and verification

- Foreground and in-process startup paths write the same PID-tagged failure
  record. Detached startup reads at most 64 KiB of new log data from its own
  child, never old/unrelated records; diagnostics remove terminal controls and
  retain adapter-owned credential redaction. Both modes include the safe
  dependency/operation and server-log location. Environment inheritance and
  existing-server onboarding reuse are unchanged.
- Added real CLI regressions for launch, connection, authentication, malformed
  response and timeout failures in both modes, supplementing missing-executable
  regressions. Beads/Herdr/Pi tests prove zero-repo/deferred-connection readiness,
  selected session/socket propagation, one snapshot/read and configured
  connection rejection. Failed task/repo/agent submission validation preserves
  the prior exact YAML/hash, definition and routing bindings despite successful
  basic health checks.
- Added `cmd/rf/startup_errors.go` as a symlink to the shared implementation;
  no existing symlink was replaced and no implementation was duplicated.
  README and OpenSpec deltas document the complete boundary and failure rules.

Passed final checks:

- Focused startup, diagnostics, alternate-plugin and submission regressions.
- `GOTOOLCHAIN=auto go test ./... -count=1` (uncached full suite).
- `GOTOOLCHAIN=auto go test -race ./cmd/relay-flow ./internal/runner/... ./internal/task/... ./internal/harness/... -count=1`.
- `GOTOOLCHAIN=auto go vet ./...` and `git diff --check`.
- `openspec validate serve-startup-prerequisites --strict --no-interactive`.
- GitNexus unstaged change detection after index refresh.

The uncached full suite's CLI package took about 209 seconds and go-workflows
about 360 seconds; the affected CLI race suite took about 207 seconds. Early
focused compile failures (unused import and an incomplete test query fake)
were corrected before these green runs. No commit was created. Normative docs
and existing `cmd/rf` symlinks remain untouched. Tests use isolated dependencies;
no installed Jira/Orca/Herdr/Dolt service or affected Mac environment was
live-tested, and existing opt-in Temporal live tests remain gated. Review the
entire uncommitted tree, including new files, before merging.
