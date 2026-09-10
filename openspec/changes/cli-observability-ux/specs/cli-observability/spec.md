# CLI observability and operator UX

## Purpose

Define a readable, operator-first inspection surface for workflow definitions and durable runs while preserving an explicit machine-readable CLI contract.

## ADDED Requirements

### Requirement: Human inspection output is terminal-aware

When `workflow list`, `workflow get`, `run list`, or `run get` is invoked from an interactive terminal without `--json`, the CLI SHALL render the human-readable view defined by this capability. It SHALL use color only when the output is a color-capable terminal and `NO_COLOR`/`--no-color` has not disabled it. It SHALL render plain text without ANSI control sequences when output is not a terminal or color is disabled. `--json` SHALL select stable machine-readable output regardless of terminal state.

Loading indicators SHALL be written to stderr, SHALL be used only for interactive human output, and SHALL finish with a success or failure mark before the data view is written. Loading indicators SHALL never be emitted in JSON mode or into stdout data.

#### Scenario: Interactive run detail

- **WHEN** `relay-flow run get --ticket relay-flow-4bg` is run in a terminal without `--json`
- **THEN** the CLI shows a terminal-aware Argo-shaped human view with status marks, aligned columns, and no raw JSON envelope

#### Scenario: Piped inspection output

- **WHEN** `relay-flow run list` is piped to another process
- **THEN** the CLI emits plain output without ANSI escape sequences or spinner frames written to stdout

#### Scenario: JSON inspection output

- **WHEN** `relay-flow run get --ticket relay-flow-4bg --json` is invoked
- **THEN** the CLI emits only the stable JSON detail response on stdout and emits no spinner or human decoration

### Requirement: Run detail follows the Argo-shaped single-run structure

`relay-flow run get --ticket <key>` SHALL present one run using this ordered structure:

1. flat metadata fields;
2. a `Conditions` block;
3. created/started/finished timestamps;
4. duration, progress, and resource-duration summary;
5. an aligned step tree with `STEP`, `DURATION`, `MESSAGE`, and `RUNTIME` columns.

The human view SHALL use the run's status values `Succeeded`, `Running`, `Waiting`, `Failed`, and `Canceled` (or the equivalent current lifecycle state) and SHALL expose the current error/retry message in the metadata or step message column. The step tree root SHALL represent the run and its children SHALL represent the actual executed node visits in order. Branches, loops, revisits, and retries SHALL be represented by nested tree rows; the CLI SHALL NOT infer completed steps merely from the static workflow graph.

The normative layout is:

```text
Name:             relay-flow-4bg
Workflow:         relayFlowCoderReviewFlow
Repository:       relay-flow
Ticket:           relay-flow-4bg
Status:           Waiting

Conditions:
  NodeRunning:      False
  Waiting:          True
  RetryScheduled:   True
  Completed:        False

Created:          2026-09-09 09:42:10 UTC
Started:          2026-09-09 09:42:12 UTC
Finished:         -
Duration:         25m 01s
Progress:         4/5
ResourcesDuration: runner 12s, task-system 8s, harness 4m 31s

STEP                                      DURATION   MESSAGE                  RUNTIME
⟳ relay-flow-4bg                         25m 01s    waiting                  -
├── ✓ start                               1s         completed                -
├── ✓ coder                               8m 41s     report accepted          coder
├── ✓ reviewer                            5m 12s     report accepted          reviewer
├── ⟳ humanReview                        10m 57s     waiting for approval     reviewer
└── ○ end                                 -          pending                  -
```

The implementation MAY use ASCII-safe equivalents `[x]`, `[ ]`, `[>]`, `[!]`, and `[-]` when Unicode symbols are unavailable. The output SHALL preserve the same tree structure and column order.

#### Scenario: Waiting run

- **WHEN** a run is waiting for a report or human decision
- **THEN** the detail view shows `Status: Waiting`, a true waiting condition, the current node in the step tree, and pending downstream steps with the empty-circle mark

#### Scenario: Completed run

- **WHEN** a run is completed
- **THEN** the detail view shows `Status: Succeeded`, `Completed: True`, a finished timestamp, a duration, full progress, and a completed root/step tree using check marks

#### Scenario: Failed run

- **WHEN** a run fails or is blocked by a terminal error
- **THEN** the detail view shows `Status: Failed`, a failure condition, the failing step with a cross mark, the failure message, and downstream steps as not reached rather than completed

#### Scenario: Branched or revisited run

- **WHEN** a workflow takes a branch or revisits a node
- **THEN** the step tree contains one row per actual node visit in execution order, with retry/revisit rows nested below the relevant node and no fabricated completion marks

### Requirement: Run detail exposes a derived execution step timeline

The read-only run detail query SHALL expose enough derived data to render the Argo-shaped tree for both durable executors. Each step entry SHALL identify its run sequence, node, node visit when available, node type, status, start time, finish time when terminal, duration, message/error, selected route when known, and runner runtime label when known. The detail query SHALL also expose the run's conditions, progress counts, and resource-duration summary.

The step timeline SHALL be a relay-owned read model/cache and SHALL never be consulted by workflow progression, route selection, report acceptance, retry scheduling, cancellation, or recovery decisions. Projection writes SHALL be idempotent and safe to repeat after activity retry. The implementation SHALL use the existing durable-executor/projection boundaries; it SHALL NOT add an event bus or make the CLI read SQLite or durable-engine internals directly.

Projection recovery SHALL rebuild step detail from the authoritative durable executor when the executor supports it. If a historical step field is unavailable, the CLI SHALL display `-` or `unavailable` rather than inventing a value; it SHALL preserve the tree and status structure.

#### Scenario: Step data is queried

- **WHEN** a client requests run detail for an active or terminal run
- **THEN** the server returns base run metadata and an ordered derived step timeline suitable for rendering without another request per step

#### Scenario: Projection write retries

- **WHEN** a step projection activity is retried after partially writing its data
- **THEN** the final detail query contains one stable entry for each node visit and no duplicated tree row

#### Scenario: Projection data is missing

- **WHEN** a historical step duration or runtime label cannot be recovered
- **THEN** the detail output retains the step status/tree and prints `-` or `unavailable` for only the missing field

### Requirement: Workflow list is a compact operator table

`relay-flow workflow list` SHALL display one aligned row per configured workflow. Human output SHALL include the workflow name, registered repositories, node count, active-run count, and latest-run state/current node when a run exists. The list SHALL include a summary count and an actionable empty state. It SHALL use the same status marks as run detail for latest-run state, but a mark in this view SHALL describe the latest execution summary, not pretend that the static definition itself executed.

The normative shape is:

```text
⠋ Loading workflows...
✓ Loaded 2 workflows

WORKFLOWS
================================================================================
STATE  NAME                         REPOS       NODES  ACTIVE  LAST RUN
--------------------------------------------------------------------------------
⟳      relayFlowCoderReviewFlow     relay-flow  5      1       waiting / humanReview
✓      releaseFlow                  payments    1      0       completed
================================================================================

2 workflows | 1 active run

Legend: ✓ healthy/complete  ⟳ active  ✗ failed  ○ no runs
```

#### Scenario: Workflows exist

- **WHEN** configured workflows are listed
- **THEN** the CLI prints the aligned workflow table and aggregate counts without requiring one human command per workflow

#### Scenario: No workflows exist

- **WHEN** the workflow registry is empty
- **THEN** the CLI prints an empty-circle message and an example `workflow submit` command instead of a blank screen

### Requirement: Workflow detail shows the validated static graph

`relay-flow workflow get --name <name>` SHALL show flat workflow metadata, validation status, repository names, node count, cleanup policy, run summary, and a readable static node/route graph. Marks in this view SHALL mean the definition is present and valid; they SHALL NOT be presented as execution history. The graph SHALL show route targets and route conditions and SHALL handle branches/cycles without claiming a single execution path.

The normative shape is:

```text
WORKFLOW  relayFlowCoderReviewFlow
================================================================================
STATUS       ✓ VALID
REPOSITORIES relay-flow
NODES        5
CLEANUP      enabled
ACTIVE RUNS  1
================================================================================

GRAPH
  ✓ start
    |
    +--> ✓ coder
           |-- success -> coder       More coding remains
           |-- success -> reviewer    Ready for review
           `-- failure -> coder       Implementation needs another pass
                  |
                  `-- success -> humanReview
                         |-- success -> end
                         `-- failure -> coder

RECENT RUNS
  ⟳ relay-flow-4bg   waiting    humanReview
```

#### Scenario: Valid workflow detail

- **WHEN** a stored workflow is requested
- **THEN** the CLI displays its validated metadata and static graph/routes, including cycles without collapsing them into a false linear execution path

#### Scenario: Workflow has no runs

- **WHEN** a valid workflow has no run history
- **THEN** the detail view shows an empty recent-runs state and still displays the complete static graph

### Requirement: Run list is a compact execution table

`relay-flow run list` SHALL display aligned rows containing status, ticket, repository, workflow, current node, update time, and retry/error summary when present. It SHALL support the existing repository/workflow/ticket filters and an active-only filter. The human view SHALL show aggregate counts and actionable detail hints; `--json` SHALL return structured rows without formatting.

The normative shape is:

```text
⠋ Loading runs...
✓ Loaded 4 runs

RUNS
================================================================================
STATE  TICKET          REPOSITORY  WORKFLOW                   NODE         AGE
--------------------------------------------------------------------------------
⟳      relay-flow-4bg  relay-flow  relayFlowCoderReviewFlow   humanReview  2m
✗      relay-flow-76s  relay-flow  relayFlowCoderReviewFlow   cleanup      8m
✓      relay-flow-c5l  relay-flow  relayFlowCoderReviewFlow   end          14m
−      relay-flow-ne7  relay-flow  relayFlowCoderReviewFlow   end          1h
================================================================================

4 runs | 1 active | 1 failed | 1 completed | 1 canceled
```

#### Scenario: Runs match filters

- **WHEN** runs are listed with repository, workflow, ticket, or active-only filters
- **THEN** only matching rows are shown and the summary describes the filtered set

#### Scenario: No runs match

- **WHEN** no runs match the supplied filters
- **THEN** the CLI displays an empty-circle message and the effective filters rather than returning an unexplained blank table

### Requirement: Help is scoped to the requested command

The CLI SHALL support `--help` and `-h` at the root, command-group, and leaf-command levels. Help output SHALL describe only the selected command's usage, options, examples, and directly related commands. Help SHALL not contact the relay-flow server, start a spinner, or require initialized durable state. The root help SHALL provide grouped top-level commands and a pointer to nested help rather than printing every nested flag.

#### Scenario: Workflow help

- **WHEN** `relay-flow workflow --help` is invoked
- **THEN** only workflow commands and workflow-level guidance are printed

#### Scenario: Leaf help

- **WHEN** `relay-flow run get --help` is invoked
- **THEN** only run-detail usage and its flags are printed, and the command exits successfully without contacting the server

#### Scenario: Root help

- **WHEN** `relay-flow --help` is invoked
- **THEN** the output groups setup, workflow, run, repository, and other commands and points users to scoped help

### Requirement: Version information is available without the server

The CLI SHALL support `relay-flow version`, `relay-flow --version`, and `relay-flow -v`. The long version command SHALL display the release version and SHALL include commit, build timestamp, Go version, and platform when build metadata is available. The short flags SHALL print a concise version line. Version commands SHALL not contact the server or require initialized state and SHALL exit zero.

#### Scenario: Long version

- **WHEN** `relay-flow version` is invoked
- **THEN** the CLI prints the relay-flow version and available build metadata in a readable block

#### Scenario: Short version

- **WHEN** `relay-flow --version` or `relay-flow -v` is invoked
- **THEN** the CLI prints one concise version line and exits zero

### Requirement: Human status marks have stable meanings

The human CLI SHALL use these meanings consistently in workflow and run views:

```text
✓ / [x]  completed or valid
○ / [ ]  pending, not reached, or no runs
⟳ / [>]  running, waiting, or active
✗ / [!]  failed or blocked
− / [-]  canceled
```

The Unicode form SHALL be preferred on capable terminals; the ASCII-safe form SHALL be used when Unicode output is unavailable. Color SHALL reinforce, never replace, the mark's meaning.

#### Scenario: Failed step is rendered

- **WHEN** a step fails
- **THEN** it uses the failure mark and a non-empty message, with color optional

#### Scenario: Non-color terminal

- **WHEN** color is disabled
- **THEN** the same state remains unambiguous from the mark and text alone
