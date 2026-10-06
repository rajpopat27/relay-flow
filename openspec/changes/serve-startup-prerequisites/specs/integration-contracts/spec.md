## ADDED Requirements

### Requirement: Runners expose a separate machine-level startup probe

Selected runners SHALL expose an adapter-owned read-only startup probe separate
from repository validation and discovery. Dispatch SHALL fail when a selected
runner does not implement the probe; it SHALL NOT silently skip that dependency.
Each probe SHALL execute once, without retries or writes, under a context capped
at five seconds and any earlier caller deadline. Orca SHALL use one repository
list request; Herdr SHALL use one snapshot without per-workspace Git or worktree
requests. Empty repository lists and empty snapshots SHALL succeed. Missing,
null, or malformed required response envelopes/arrays SHALL fail rather than
being interpreted as an empty healthy runtime. Cancellation SHALL remain
bounded even if a launcher descendant retains its output pipes.

#### Scenario: Selected runner lacks probe support
- **WHEN** startup dispatch receives a runner without the startup probe boundary
- **THEN** dispatch returns an explicit error and does not assume readiness

#### Scenario: Orca has no repositories
- **WHEN** Orca returns a valid response with an empty `result.repos` array
- **THEN** its startup probe succeeds without creating or validating repositories

#### Scenario: Herdr has an empty session
- **WHEN** Herdr returns a valid snapshot with empty workspace, tab and pane arrays
- **THEN** its startup probe succeeds after exactly one snapshot request

#### Scenario: Herdr has existing source workspaces
- **WHEN** the startup snapshot contains source workspaces requiring further Git identity lookup for registration
- **THEN** startup performs no such lookup and creates no resource

#### Scenario: Aggregate deadline expires first
- **WHEN** the caller's prerequisite deadline is earlier than five seconds
- **THEN** the probe retains that earlier deadline and exposes cancellation/timeout

### Requirement: Orca launcher selection is explicit and stable

The Orca adapter SHALL select `ORCA_CLI_COMMAND` when nonempty; otherwise it
SHALL select `orca-dev` when `ORCA_DEV_REPO_ROOT` is nonempty, `orca-ide` on
Linux, or `orca` on macOS. Selection and local executable resolution SHALL
occur once in the server's environment. The same resolved executable SHALL be
used for probing and later operations. A selection or launch failure SHALL NOT
fall through to another binary, shell alias, or legacy shim. Diagnostics SHALL
identify the selected executable and safe operation, not full command arguments
containing prompts or environments.

#### Scenario: Linux has only the unrelated bare orca executable
- **WHEN** no explicit session/development selector is set and only `orca` exists
- **THEN** the adapter reports missing `orca-ide` without invoking bare `orca`

#### Scenario: Explicit session selector is unusable
- **WHEN** `ORCA_CLI_COMMAND` selects a missing executable
- **THEN** the adapter reports that selected executable and tries no alternative

#### Scenario: Client environment changes after construction
- **WHEN** PATH or a launcher selector changes after the server's CLI is constructed
- **THEN** later adapter operations still use the originally resolved executable

### Requirement: Task plugins expose bounded machine prerequisite checks

The selected task factory SHALL provide a separate startup probe, independent
of local-only repo construction and full registration/submission validation.
Dispatch SHALL fail if the selected factory lacks that probe, even with zero
registered repositories. The adapter SHALL own credentials, connection
selection and deduplication. Each external probe SHALL have a five-second
ceiling within the caller's earlier aggregate deadline. Startup probes SHALL
make no writes, ticket/history scans, or project-specific validation requests.

Jira SHALL validate system-wide credentials once using `GET /rest/api/3/myself`,
without retries, Retry-After delays or redirects. Normal Jira requests SHALL
retain their runtime retry behavior. Credential errors SHALL remain redacted,
responses SHALL remain bounded, and authentication, connection, malformed
response and timeout failures SHALL be distinguishable.

Beads SHALL check local `bd` availability and use its existing harmless
`list --ready --limit 1 --no-parent --json` probe once per distinct canonical
explicitly configured workspace. Registered code cwd and BEADS_DIR isolation
SHALL be preserved. An explicitly configured root connection without a matching
registered repo SHALL use that workspace itself as cwd. Root config SHALL NOT
substitute for a missing required repo workspace key. When no workspace is
configured, the adapter SHALL report connection verification deferred to
registration and SHALL NOT probe an ambient workspace or start/bootstrap Beads
or Dolt.

#### Scenario: Jira startup has no repos
- **WHEN** the selected task plugin is Jira and no repo is registered
- **THEN** startup probing still makes one authenticated credential request without dummy repo or project queries

#### Scenario: Jira startup is rate limited
- **WHEN** the credential request returns HTTP 429 with Retry-After
- **THEN** startup fails after that single response without waiting or retrying

#### Scenario: Beads repositories share a workspace
- **WHEN** several repo registrations reference the same workspace through equivalent paths or symlinks
- **THEN** startup probing makes one limited read using one registered code cwd and the canonical configured BEADS_DIR

#### Scenario: Beads has no configured workspace
- **WHEN** Beads is selected without any explicit root or repo workspace
- **THEN** startup probing checks only local bd availability and reports connection verification deferred to registration

#### Scenario: Beads has a configured root connection but no repos
- **WHEN** a root beadsDir explicitly selects a workspace and no repo is registered
- **THEN** startup probing verifies that connection rather than silently deferring it

### Requirement: Harness startup checks only the selected executable

The selected harness SHALL expose a separate startup probe. Dispatch SHALL fail
if it is absent. Pi and OpenCode SHALL perform only local executable resolution
for their selected runtime, honoring cancellation. They SHALL NOT enumerate
agents, validate node agent names, inspect repo templates, configure repositories,
or launch sessions at startup. Full agent validation SHALL remain separate.

#### Scenario: Selected harness executable exists
- **WHEN** the selected Pi or OpenCode executable resolves locally
- **THEN** its startup probe succeeds without executing any command

#### Scenario: Another harness executable exists
- **WHEN** the selected harness executable is absent but the other harness executable is installed
- **THEN** its startup probe fails without falling back to the other harness
