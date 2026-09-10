# Section-3 Test Audit (task 6.1)

Audit of every section-3 behavior test against the section-4/5 implementation.
Every test is listed below with a verdict (kept / rewritten / removed) and a
one-line reason. Code-side fixes made under 6.1 follow the per-test tables.

## Summary

- Test files changed: **5** (`internal/server/fixture_test.go`,
  `internal/server/api_test.go`, `internal/server/shutdown_test.go`,
  `cmd/relay-flow/commands_test.go`,
  `internal/execution/goworkflows/recovery_test.go` — see 6.3 dedupe pass).
- Test files unchanged: all other `internal/**/*_test.go` and
  `plugin/*.test.ts`.
- Production code changed: `internal/execution/goworkflows/engine.go`
  (added `InitDatabase`), `internal/task/factory.go`,
  `internal/runner/factory.go`, `internal/harness/factory.go` (added
  `ValidateName`), `cmd/relay-flow/main.go` (implemented `cmdInit`),
  `cmd/relay-flow/serve.go` (now calls `recover.FromTaskSystem`),
  new package `internal/recover` (the 5.6 composition, previously
  unexported inside `cmd/relay-flow`).
- No test was removed. No spec-backed assertion was weakened.

## Per-test inventory

### `cmd/relay-flow/commands_test.go` — file verdict: rewritten

| Test | Verdict | Reason |
|---|---|---|
| `TestCommandSurfaceExists` | kept (compile fix) | Aliased `internal/run` import to `runsvc` so the package-local `run(args, stdin) int` seam (allowed seam e) compiles. No assertion changed. |
| `TestUnknownFlagExits2` | kept (compile fix) | Same `runsvc` alias. |
| `TestRequiredFlagMissingExits2` | kept (compile fix) | Same `runsvc` alias. |
| `TestServerLockIsOwnerOnly` | rewritten | Now seeds the home via real `init` first (normal serve refuses to start without durable state per 5.5). Asserted mode `0600` on `server.lock` unchanged. |
| `TestServerSocketIsOwnerOnly` | rewritten | Same init-seeding change. Asserted mode `0600` on `server.sock` unchanged. |
| `TestReportReadsOneJSONObjectFromStdin` | kept (compile fix) | Same `runsvc` alias. |
| `TestReportAckMatrix` | kept (compile fix + stub surface) | `ackServer` now satisfies the full `server.Deps` (unreachable panic stubs outside `SubmitReport`) so `server.New(deps)` compiles. Ack matrix assertions unchanged. |
| `TestReportUnreachableServerExits1` | kept (compile fix) | Same `runsvc` alias. |
| `TestInitRefusesToOverwrite` | kept | Ran green against the newly implemented `cmdInit` (see code-side fix #3). |

### `internal/config/machine_test.go` — file verdict: kept

| Test | Verdict | Reason |
|---|---|---|
| `TestLoadMachineDefaults` | kept | Green as written. |
| `TestLoadMachineRejectsNonPositiveGlobals` | kept | Green. |
| `TestLoadMachineRejectsUnknownFields` | kept | Green. |
| `TestSaveAndLoadRoundTrip` | kept | Green. |
| `TestRepoEntryHoldsOnlyPathAndTaskConfig` | kept | Green. |
| `TestFixedFilesystemLayoutAndPermissions` | kept | Green. |

### `internal/config/merge_test.go` — file verdict: kept

| Test | Verdict | Reason |
|---|---|---|
| `TestMergePrecedenceRootToNode` | kept | Green. |
| `TestMergeMapsRecursively` | kept | Green. |
| `TestMergeListReplaces` | kept | Green. |
| `TestMergeScalarReplaces` | kept | Green. |
| `TestMergeOmittedKeyInherits` | kept | Green. |
| `TestMergeRejectsExplicitNull` | kept | Green. |
| `TestMergeNullLayerRejectedAtValidation` | kept | Green. |
| `TestMergeDoesNotMutateInputs` | kept | Green. |

### `internal/config/writeatomic_test.go` — file verdict: kept

| Test | Verdict | Reason |
|---|---|---|
| `TestWriteAtomicCreatesAndReplaces` | kept | Green. |
| `TestWriteAtomicSetsModeOnReplace` | kept | Green. |
| `TestWriteAtomicLeavesNoTempOnSuccess` | kept | Green. |
| `TestWriteAtomicFailureLeavesPriorFileUsable` | kept | Green. |

### `internal/execution/goworkflows/engine_test.go` — file verdict: kept

| Test | Verdict | Reason |
|---|---|---|
| `TestRunBeginsAtStartAndFollowsEntryEdge` | kept | Green. |
| `TestSerialGraphOneNodeAtATime` | kept | Green. |
| `TestRevisitCreatesNewVisit` | kept | Green. |
| `TestEndAppliesConfigAndCompletes` | kept | Green. |
| `TestTransitionOrdering` | kept | Green. |
| `TestEndSkipsFeedbackComment` | kept | Green. |
| `TestReportAckOnlyAfterDurablePersistence` | kept | Green. |
| `TestNonCurrentVisitAckedAsOldDuplicate` | kept | Green. |
| `TestFirstReportOnlyConsumed` | kept | Green. |
| `TestEnsureRunIdempotentAndVisitStableAcrossReplay` | kept | Green. |

### `internal/execution/goworkflows/mailbox_test.go` — file verdict: kept

| Test | Verdict | Reason |
|---|---|---|
| `TestMailboxesEnsuredForWorkNodesOnly` | kept | Green. |
| `TestMailboxCarriesWorkflowLabel` | kept | Green. |
| `TestRevisitReusesSameMailbox` | kept | Green. |
| `TestSummaryMarkerAndContentOnCurrentMailbox` | kept | Green. |
| `TestSummaryCurrentFeedbackSelectedNextOnly` | kept | Green. |
| `TestManualMailboxStatusDoesNotRouteGraph` | kept | Green. |
| `TestHITLUsesSameMailboxLifecycle` | kept | Green. |
| `TestRecoveryReusesMailboxesCreatesOnlyMissing` | kept | Green. |

### `internal/execution/goworkflows/recovery_test.go` — file verdict: kept

| Test | Verdict | Reason |
|---|---|---|
| `TestFreshRunGeneratesFreshVisitIDs` | kept | Green. |
| `TestVisitIDStableAcrossNormalRestart` | kept | Green. |
| `TestCancelRun` | kept | Green. |
| `TestCancelDuringRunningActivity` | kept | Green. |
| `TestCrashImmediatelyAfterReportPersistence` | kept | Green. |
| `TestCrashAfterSummaryFeedbackBeforeCompletion` | kept | Green. |
| `TestConflictMarksBlockedThenRecovers` | kept | Green. |
| `TestTerminalReconcile` | kept | Green. |
| `TestHITLReconcileRestoresMissingSessionNeverNudgesIdle` | kept | Green. |
| `TestAckImpliesDurablePersistenceAcrossRestart` | kept | Green. |
| `TestHealthyDatabaseMissingRunIsClaimBeforeRun` | kept | Green. |
| `TestNormalServeRequiresExistingDatabase` | kept | Green. |
| `TestDatabaseFileIsOwnerOnly` | kept | Green. |
| `TestServeRecoverRebuildsFreshRuns` | rewritten (6.3) | Was driving a test-local `recoverTickets` copy; now drives the real `recover.FromTaskSystem` via the settled seams. Same assertions. See 6.3 dedupe pass below. |
| `TestRetentionRemovesOldTerminalRunsKeepsOthers` | kept | Green. |
| `TestRunProjectionQueries` | kept | Green. |

### `internal/harness/contract_test.go` — file verdict: kept

| Test | Verdict | Reason |
|---|---|---|
| `TestValidateAgent` | kept | Green. |
| `TestFindSessionByStableTitle` | kept | Green. |
| `TestBuildCommandEnvContract` | kept | Green. |
| `TestHarnessDoesNotManipulateRunnerState` | kept | Green. |

### `internal/harness/plugin_selection_test.go` — file verdict: kept

| Test | Verdict | Reason |
|---|---|---|
| `TestHarnessRegistryUnknownNameListsRegistered` | kept | Green. |
| `TestRunnerRegistryUnknownNameListsRegistered` | kept | Green. |
| `TestTaskRegistryUnknownNameListsRegistered` | kept | Green. |
| `TestDuplicateRegistrationPanics` | kept | Green. |
| `TestNamesListsRegistered` | kept | Green. |

### `internal/repo/poller_test.go` — file verdict: kept

| Test | Verdict | Reason |
|---|---|---|
| `TestPollerGroupRunsOnePollerPerRepo` | kept | Green. |
| `TestPollerGroupCapsConcurrentPolls` | kept | Green. |
| `TestRepoPollerUsesConfiguredInterval` | kept | Green. |
| `TestPollerGroupPollsEveryRepoOnSharedInterval` | kept | Green. |
| `TestPollIntervalDefaultsTo15Seconds` | kept | Green. |
| `TestRepoPollerOnlyFetchesAndHandles` | kept | Green. |

### `internal/repo/service_test.go` — file verdict: kept

| Test | Verdict | Reason |
|---|---|---|
| `TestDiscoverDelegatesToRunner` | kept | Green. |
| `TestRequiredRepoKeysDelegated` | kept | Green. |
| `TestRegisterMissingRequiredKeyFails` | kept | Green. |
| `TestRegisterRejectsDuplicateName` | kept | Green. |
| `TestRegisterRejectsDuplicateCanonicalPath` | kept | Green. |
| `TestRegisterRejectsDuplicateTaskScope` | kept | Green. |
| `TestRegisterValidatesRunnerRepo` | kept | Green. |
| `TestRegisterValidatesTaskConnectivity` | kept | Green. |
| `TestRegisterAtomicallyPersists` | kept | Green. |
| `TestRemoveStopsPollerAndRemoves` | kept | Green. |
| `TestRemoveRejectedWhileWorkflowReferences` | kept | Green. |
| `TestRemoveRejectedWhileRunActive` | kept | Green. |

### `internal/router/router_test.go` — file verdict: kept

| Test | Verdict | Reason |
|---|---|---|
| `TestMultipleClaimsInvalid` | kept | Green. |
| `TestSingleClaimResolvesDirectly` | kept | Green. |
| `TestUnknownClaimInvalid` | kept | Green. |
| `TestClaimForWorkflowNotBoundToRepo` | kept | Green. |
| `TestZeroMatchesIgnored` | kept | Green. |
| `TestOneMatchSelected` | kept | Green. |
| `TestMultipleMatchesAmbiguous` | kept | Green. |

### `internal/runner/contract_test.go` — file verdict: kept

| Test | Verdict | Reason |
|---|---|---|
| `TestTerminalTitleIsTicketColonNode` | kept | Green. |
| `TestFindTerminalReturnsOnlyLiveUsable` | kept | Green. |
| `TestCloseTerminalsPreservesEnvironment` | kept | Green. |
| `TestCleanupRunRemovesAllRunResources` | kept | Green. |
| `TestEnsureIdempotent` | kept | Green. |

### `internal/run/run_identity_test.go` — file verdict: kept

| Test | Verdict | Reason |
|---|---|---|
| `TestRunIDDeterministic` | kept | Green. |
| `TestRunIDDelimiterSafe` | kept | Green. |
| `TestNodeVisitIDsUniquePerEntry` | kept | Green. |

### `internal/run/run_manager_test.go` — file verdict: kept

| Test | Verdict | Reason |
|---|---|---|
| `TestUnclaimedTicketClaimedBeforeEnsureRun` | kept | Green. |
| `TestClaimedTicketSkipsClaiming` | kept | Green. |
| `TestClaimFailurePreventsRunCreation` | kept | Green. |
| `TestCancellationMarkerSkipsRunCreation` | kept | Green. |
| `TestDeterministicRunID` | kept | Green. |
| `TestCancelByTicketResolvesActiveRun` | kept | Green. |

### `internal/server/api_test.go` — file verdict: rewritten

| Test | Verdict | Reason |
|---|---|---|
| `TestSuccessEnvelope` | kept | Green. |
| `TestErrorEnvelopeAndStatusMapping` | rewritten | Malformed-JSON case moved from `POST /workflows` (raw YAML body by design) to `POST /repos` (JSON body) so the 400 envelope is exercised on an endpoint that actually parses JSON. 404/405 sub-assertions unchanged. |
| `TestWorkflowConflictMapsTo409` | kept (via fixture fix) | No edits in this test; the fixture's `errActiveRun` now wraps `server.ErrConflict` so the 409 mapping runs through the documented typed-error channel. |
| `TestUnexpectedFailureMapsTo500` | kept | Green. |
| `TestReportEndpointAcceptsJSON` | kept | Green. |
| `TestReportRejectsWrongWireKeyCasing` | kept | Green. |
| `TestReportMultilineFieldsPreserved` | kept | Green. |
| `TestRepoAndRunEndpoints` | kept (via fixture fix) | No edits; the fixture's `errNotFound` now wraps `server.ErrNotFound` so 404 mapping is typed. |
| `TestRepoOperations` | rewritten | `/repos/discover` is `GET`, not `POST` (docs routes). Behavior asserted unchanged. |
| `TestWorkflowGetRemoveAndRunCancel` | rewritten | (a) Submit body is raw YAML, not `{"yaml":...}` wrapper (docs routes + `Client` sends raw). (b) Cancel route is `/runs/by-ticket/{key}/cancel`, not `/runs/{key}/cancel`. Assertions unchanged. |

### `internal/server/fixture_test.go` — file verdict: rewritten

Not a test itself; holds `fakeServices`. Rewritten to satisfy the final
`server.Deps` (RepoCandidate/Info/RegisterInput types, added `TaskFields` and
`Shutdown`), and to wrap `errActiveRun`/`errNotFound` in
`server.ErrConflict`/`server.ErrNotFound` so error mapping uses the
documented typed-error channel.

### `internal/server/shutdown_test.go` — file verdict: rewritten

| Test | Verdict | Reason |
|---|---|---|
| `TestShutdownStopsAcceptingWithinBound` | kept | Green. |
| `TestShutdownWaitsForRunningCall` | kept | Green. |
| `TestRestartOnSameStateResumes` | rewritten | Submit body changed to raw YAML (same reason as api_test). Assertions (state resumes across restart) unchanged. |

### `internal/task/contract_test.go` — file verdict: kept

| Test | Verdict | Reason |
|---|---|---|
| `TestPollReturnsActiveParentsOnly` | kept | Green. |
| `TestEnsureMailboxesFindsExistingCreatesOnlyMissing` | kept | Green. |
| `TestCompleteMailboxIsNarrow` | kept | Green. |
| `TestSeparatePrimitives` | kept | Green. |

### `internal/task/jira/filters_test.go` — file verdict: kept

| Test | Verdict | Reason |
|---|---|---|
| `TestCompileFilterMatchesNormalizedFields` | kept | Green. |
| `TestCompileFilterRejectsNonMatching` | kept | Green. |
| `TestCompileFilterRejectsUnknownField` | kept | Green. |
| `TestMatchingIsInMemoryNoRequery` | kept | Green. |
| `TestJiraJSONNormalization` | kept | Green. |
| `TestCompileFilterAssigneeMatchAndMismatch` | kept | Green. |

### `internal/task/jira/transition_defaults_test.go` — file verdict: kept

| Test | Verdict | Reason |
|---|---|---|
| `TestStartDefaultParentInProgress` | kept | Green. |
| `TestWorkNodeDefaultMailboxInProgressParentUnchanged` | kept | Green. |
| `TestEndDefaultParentDone` | kept | Green. |
| `TestExplicitTransitionsWin` | kept | Green. |

### `internal/workflow/report_test.go` — file verdict: kept

| Test | Verdict | Reason |
|---|---|---|
| `TestValidateReportAcceptsCompleteSuccess` | kept | Green. |
| `TestValidateReportRequiresEverySection` | kept | Green. |
| `TestValidateReportStatusValues` | kept | Green. |
| `TestValidateReportNextStepMustMatchStatusRoute` | kept | Green. |
| `TestValidateReportEndRequiresNoneFeedback` | kept | Green. |
| `TestValidateReportCalledForBothNodeTypes` | kept | Green. |
| `TestValidateReportNoneMeansIntentionallyEmpty` | kept | Green. |

### `internal/workflow/store_test.go` — file verdict: kept

| Test | Verdict | Reason |
|---|---|---|
| `TestStorePutGetLoadAll` | kept | Green. |
| `TestServiceSubmitValidatesBeforeStoring` | kept | Green. |
| `TestServiceSubmitRejectsUnknownRepo` | kept | Green. |
| `TestServiceReplaceRejectedWhileActive` | kept | Green. |
| `TestServiceFailedWritePreservesExisting` | kept | Green. |
| `TestServiceRemoveProtectsActiveRuns` | kept | Green. |
| `TestRegistryReferencesRepoAndReplace` | kept | Green. |

### `internal/workflow/workflow_test.go` — file verdict: kept

| Test | Verdict | Reason |
|---|---|---|
| `TestParseMinimalValid` | kept | Green. |
| `TestParseRejectsUnknownRootFields` | kept | Green. |
| `TestValidateWorkflowName` | kept | Green. |
| `TestValidateReposRequiredAndUnique` | kept | Green. |
| `TestValidateStartNode` | kept | Green. |
| `TestValidateEndNode` | kept | Green. |
| `TestValidateWorkNodes` | kept | Green. |
| `TestValidateRoutes` | kept | Green. |
| `TestValidateGraphReachability` | kept | Green. |
| `TestValidateNudgeTemplate` | kept | Green. |
| `TestStartTarget` | kept | Green. |
| `TestCleanupRunnerOnEndDefaultsFalse` | kept | Green. |
| `TestRenderNudge` | kept | Green. |

### `plugin/parse.test.ts` — file verdict: kept

| Test | Verdict | Reason |
|---|---|---|
| `parses a complete report` | kept | Green (`bun test`). |
| `missing STATUS is invalid` | kept | Green. |
| `missing NEXT STEP is invalid` | kept | Green. |
| `missing a SUMMARY subsection is invalid` | kept | Green. |
| `missing a FEEDBACK subsection is invalid` | kept | Green. |
| `unsupported STATUS value is invalid` | kept | Green. |
| `literal None is accepted as intentionally empty` | kept | Green. |
| `an aborted/ordinary-prose message is not a report` | kept | Green. |
| `multiline fields are preserved` | kept | Green. |

### `plugin/nudge.test.ts` — file verdict: kept

| Test | Verdict | Reason |
|---|---|---|
| `invalid output sends the nudge through the session API` | kept | Green. |
| `missing output sends the nudge` | kept | Green. |
| `valid output sends no nudge` | kept | Green. |
| `aborted response (no completed finish reason) is not nudged` | kept | Green. |
| `invalid output sends no nudge and no report` (HITL) | kept | Green. |
| `missing output stays silent` (HITL) | kept | Green. |
| `valid HITL output reports normally` | kept | Green. |

### `plugin/report_retry.test.ts` — file verdict: kept

| Test | Verdict | Reason |
|---|---|---|
| `matches internal/retry DefaultBackoffPolicy` | kept | Green. |
| `sends one JSON object on stdin and stops on ack` | kept | Green. |
| `retries the exact unchanged report until acknowledged` | kept | Green. |
| `duplicate/stale ack is success and stops the loop` | kept | Green. |
| `a rejected report is not retried as delivery` | kept | Green. |
| `at most one retry loop runs per node visit` | kept | Green. |

## Code-side fixes made under 6.1

These are implementation gaps the audit exposed, fixed to satisfy
specification-backed test assertions. None weaken a spec-backed assertion.

1. **`internal/execution/goworkflows/engine.go` — added `InitDatabase(path)`.**
   `relay-flow init` must initialize a real SQLite database (docs
   lines 950/1028; design decision 22). The full `Engine.New` requires
   Repos/Runner/Harness dependencies that init does not have. Added a narrow
   `InitDatabase` that opens the SQLite file, applies the `relay_runs`
   projection schema, sets mode `0600`, and closes — the same open/migrate
   sequence `Engine.New` uses.

2. **`internal/task/factory.go`, `internal/runner/factory.go`,
   `internal/harness/factory.go` — added `ValidateName(name) error`.** Init
   must validate the three plugin selections against the registered
   factories and reject unknown names with an error listing the registered
   set (plugin-selection behavior in 3.9). Each package already had the
   lookup; this exposes it without constructing a System/Runner/Harness.

3. **`cmd/relay-flow/main.go` — implemented `cmdInit`.** 4.15 delegated init
   to the section-5 composition root; the delegation target was missing.
   Implemented per docs: normally refuse overwrite if config or database exists,
   create root `0700`, read three plugin selections from stdin (drives the
   documented `run(args, stdin) int` seam), `ValidateName` each, then
   `config.SaveMachine` + `goworkflows.InitDatabase`. **Updated in 8.1/8.2:**
   on a TTY, plugin selection is now an interactive `huh` form (one
   searchable select per plugin); non-interactive flags
   `--task-plugin/--runner-plugin/--harness-plugin` (all-or-none) skip both
   the form and stdin. The stdin three-line path remains as the documented
   seam and script path. See the 8.5 sweep record at the end of this file.

4. **`cmd/relay-flow/commands_test.go` — init-driven seeding.**
   `TestServerLockIsOwnerOnly` and `TestServerSocketIsOwnerOnly` now call
   the real `init` first so `serve` has a valid machine config and database
   (normal-serve refusal is a 5.5 requirement; the blind tests were written
   before that rule was wired).

## Deferred / out of scope

- ~~**`repo register` interactive `huh` selection**~~ — **resolved in
   8.3/8.4.** The stub was replaced by the real interactive flow: the CLI
  discovers candidates via `Client.DiscoverRepos`, runs a searchable `huh`
  multi-select, prompts once for Jira project, and derives each component
  from the selected Orca repo name. 8.4 added the fully-flagged
  non-interactive path (`--name/--path/--set project=value`) that never
  prompts and derives component from `--name`. Verified live against `serve` with the built
  binary: an interactive run registered `GHO-AZ-AGENT-CHART` and a flagged
  run registered `GHO-Cobra`, each confirmed via `repo get`. See the 8.5
  sweep record at the end of this file.

## 6.3 dedupe pass

No blind section-3 test and section-5 wiring test cover the same behavior:
section 5 introduced no separate wiring test set — the section-3 tests were
written against the five settled seams and continue to exercise the real
wiring. 6.3 is otherwise a no-op for de-duplication.

One implementation-alignment fix fell out of the pass:

- **`internal/execution/goworkflows/recovery_test.go` —
  `TestServeRecoverRebuildsFreshRuns` rewritten.** Previously the test used
  a test-local `recoverTickets` helper that re-implemented the recover flow
  (an invented seam). It now drives the real composition:
  `recover.FromTaskSystem` (new package `internal/recover`) is the actual
  5.6 wiring, called by `cmd/relay-flow/serve.go` and by the test. The
  helper builds the repo registry, binds the workflow, constructs the
  `run.RunManager`, and calls `FromTaskSystem` with the engine's
  `MailboxSpecs` — closing the 5.6 coverage gap without touching a single
  assertion.

## 6.2 seam-leak pass

Audited all production code for test-only seams beyond the five authorized
ones. Findings: **no leaks**.

- No engine-internal clock hooks (`clock`, `NowFunc`, `TimerFunc`,
  `SleepFunc` all absent outside tests).
- No failure-injection hooks in production code.
- No test-only exported constructors: every exported constructor is either
  used by the composition root (`server.New`, `repo.NewServiceWithRegistry`,
  `workflow.NewService`, `goworkflows.New`, `run.NewManager` via struct
  literal, `recover.FromTaskSystem`) or is a documented plugin/registry API
  (`task.New/Register/ValidateName/Names`, same for runner/harness).
- `cmd/relay-flow` honors `RELAY_FLOW_HOME` as the temp-root seam (c);
  that is the documented override, not an invented seam.
- Plugin TypeScript `handleIdle`/`deliverReport` take `send`/`sleep`/`rand`
  as caller-supplied dependencies — these are interface-fake injections at
  the documented boundary (seam a), not test-only seams in production code.

No code changes required for 6.2.

## Suite result

- `go test ./...` — green (including the crash-boundary, duplicate-report,
  ambiguous-comment, blocked-state, cancellation, restart,
  terminal-reconcile, and database-loss recovery tests under
  `internal/execution/goworkflows`).
- `cd plugin && bun test` — 22 pass, 0 fail.

## 8.5 deferred-work sweep record

**Exact commands run (reproducible):**

```sh
grep -rniE "TODO|FIXME|not wired|\bstub\b|not implemented|unimplemented|placeholder" \
  cmd/ internal/ plugin/
grep -rniE "TODO|FIXME|\blater\b|not wired|refinement|temporary|for now|\bhack\b|\bstub\b" \
  cmd/ internal/ plugin/ | grep -vE "defer |defer\("
```

`.git`, `node_modules`, and third-party `.opencode`/`.github` skill
assets are outside these paths.

**First command output: zero lines (exit 1).** No production Go/TS code
or string contains a deferral marker.

**Second command output (the softer prose words): exactly three lines:**

- `internal/config/writeatomic.go:12` — "creates a temporary sibling":
  describes renameio's temp file, not deferred work.
- `internal/config/config.go:17` and `merge_test.go:14` — "later scalar
  or list replaces": describes merge-order semantics, not deferred work.

Within this change's own artifacts (`openspec/.../tasks.md`,
`test-audit.md`), the marker words appear only in normative task text
(tasks.md 1.11 and 8.5 themselves), in the historical audit rows quoted
below, and in this record quoting the search. Those are documentation
self-references, not production markers.

**Pattern hunt results (evidence cited):**

- (a) interactive/TTY paths replaced by the stdin seam: none found.
  `cmd/relay-flow/main.go` `pickPluginsInteractive` (init, 8.1) and
  `cmdRepoRegister` (8.3) build `huh` forms gated on
  `isatty.IsTerminal`. Evidence: 8.1 PTY run rendered the form and wrote
  `config.yaml` + `state.db`; 8.3 PTY run against live `serve`
  registered `GHO-AZ-AGENT-CHART` (arrow-down + enter on the searchable
  select, defaults from the selected candidate, typed project/component)
  confirmed via `repo get`.
- (b) placeholder exits: none found (first grep above). The two commands
  previously stubbed in section 4/6 — `init` and `repo register` — are
  covered by `TestInitRefusesToOverwrite`,
  `TestInitFlagsNonInteractive`, and
  `TestRepoRegisterFlagsNonInteractive` in
  `cmd/relay-flow/commands_test.go`, plus the live PTY verifications
  above.
- (c) documented routes: `internal/server/server.go` lines 79-89
  register every route in docs/structs-methods-interfaces.md lines
  1058-1076; the `/runs/by-ticket/{key}/cancel` sub-route is dispatched
  at server.go line 428. Covered by `internal/server/api_test.go`.
- (d) typed error mappings: `mapErr` (server.go lines 115-125) maps
  `ErrNotFound`→404 `notFound`, `ErrConflict`→409 `conflict`,
  `ErrInvalid`→400 `invalid`, default 500. Covered by
  `internal/server/api_test.go` (400 malformed JSON, 404 unknown
  repo/workflow).
- (e) test-local re-implementations: **one was found and corrected in
  6.3** — `TestServeRecoverRebuildsFreshRuns` previously drove a
  test-local `recoverTickets` copy (recorded above in this file's 6.3
  entry); it now drives the real `recover.FromTaskSystem`, the same
  function production serve calls at `cmd/relay-flow/serve.go` line 272.
  The 8.5 re-grep found no new instances.

**Five authorized seams re-audited:**

- (seam a) interface fakes — exist only in tests behind the documented
  `task.System`/`runner.Runner`/`harness.Harness`/`run.Executor`
  interfaces (6.2 seam-leak pass confirmed no production hooks; the
  `ackServer`/`registerServer` "panic stubs" in
  `cmd/relay-flow/commands_test.go` are such test doubles, not
  production code).
- (seam b) temp SQLite engine — tests run the real `goworkflows.New`
  engine in a `t.TempDir()`; production serve uses the same constructor.
- (seam c) temp root — `RELAY_FLOW_HOME` in `cmd/relay-flow/main.go`
  `home()` is the documented override; `paths.ForUserHome` remains the
  default.
- (seam d) `server.New(deps) http.Handler` — the production handler;
  `cmd/relay-flow/serve.go` serves it on the socket; tests serve the
  same handler in-process.
- (seam e) `run(args, stdin) int` — the documented `main` entry shape;
  `main()` calls `os.Exit(run(...))`; tests call the same function.

No seam masks unimplemented production behavior.

**Rerun after this record:** `go build ./...`, `go vet ./...`,
`go test ./...` green; `cd plugin && bun test` 22 pass, 0 fail. The
sweep required no new numbered fix tasks; the only 8.5 edits were to
this audit file.
