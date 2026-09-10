## Context

The current CLI is a thin standard-flag dispatcher over the Unix-socket API. `workflow list` prints only names, `workflow get` prints raw JSON, `run list` prints tab-separated rows, and `run get` prints raw JSON. The durable `run.Run` projection intentionally contains current run state, current node, retry metadata, and timestamps, but not an operator-oriented execution timeline. The existing workflow model contains the static graph and routes; it does not by itself identify which branch or loop a particular run executed.

The requested experience is the structure of `argo get` for a single workflow run: flat metadata, conditions, timestamps, duration/progress/resource summary, and a tree/table of executed steps. Workflow list/get and run list should become useful companion views without turning the CLI into a full-screen TUI. The server API remains the single local query boundary, and scripts must retain an explicit stable JSON path.

## Goals / Non-Goals

**Goals:**

- Make `run get --ticket` the primary operator detail view and match the Argo-shaped output order and tree structure.
- Render actual node visits, branches, loops, retries, and failure messages rather than guessing execution history from the workflow graph.
- Make workflow/run list and workflow get readable at a glance.
- Keep the output usable in a normal terminal, a pipe, and automation.
- Add scoped help and version information without requiring the server.
- Keep execution authority in the selected durable executor and keep the new timeline derived/read-only.
- Preserve the existing command names, filters, exit codes, Unix-socket API boundary, task/runner/harness behavior, and workflow YAML.

**Non-Goals:**

- Do not build a full-screen interactive terminal dashboard, curses application, or long-lived watch mode in this change.
- Do not change workflow execution, routes, retries, report delivery, cancellation, or recovery semantics.
- Do not let the CLI read SQLite files, go-workflows state, or Temporal history directly.
- Do not add a custom event bus, generic telemetry framework, or second execution state machine.
- Do not expose secrets, raw task credentials, or internal database details in human or JSON inspection output.
- Do not make the static workflow graph pretend to be the history of an individual run.

## Decisions

### 1. `run get` uses an Argo-shaped static layout

The output order is fixed:

```text
Name / Workflow / Repository / Ticket / Status
Conditions
Created / Started / Finished / Duration / Progress / ResourcesDuration
STEP / DURATION / MESSAGE / RUNTIME tree
```

The root tree row is the run. Child rows are actual executed node visits. The renderer uses Unicode tree connectors and status marks on capable terminals and an ASCII-safe equivalent when necessary. It does not add a dashboard sidebar, separate cards, or a second detail format for human output.

`RUNTIME` is the relay-flow equivalent of Argo's `PODNAME`: it identifies the relevant runner/harness runtime label when available. It is deliberately not called `PODNAME` because relay-flow supports Orca and Herdr terminals/workspaces rather than Kubernetes pods.

### 2. Add a derived step timeline, not an execution event bus

A run detail cannot accurately mark steps from `Run.CurrentNode` plus the static graph: branches and loops make earlier nodes ambiguous. Add a small relay-owned derived step read model, for example `relay_run_steps`, keyed by run and stable sequence/node-visit identity. It records the display fields required by the spec: node, visit, type, status, start/finish, duration, message, selected route, and runtime label.

Existing durable activities/interpreter transition points update this read model idempotently. The projection is display/cache data only. No workflow decision, report fence, retry, cancellation path, or recovery path reads it. This preserves the architecture's rule that the durable executor owns graph progression and avoids a new event bus.

Both executors must produce the same display DTO. Goworkflows records the steps at its existing projection/activity boundaries. Temporal records the same derived steps through its workflow/projection path and includes enough authoritative state during explicit projection rebuild to reconstruct the timeline where retained. If a historical optional field cannot be recovered, the renderer prints `-` or `unavailable`; it never fabricates a status or duration.

The public Unix-socket query returns base run information plus detail data in one response. Existing clients decoding the response into the base `run.Run` value can ignore additive detail fields. A new client/helper may expose the detail DTO to the CLI; it must remain a small consumer query boundary rather than leaking executor types.

### 3. Human and JSON output are separate deliberate modes

Interactive TTY output is human-first. `--json` is the explicit automation contract and returns stable structured data for list/get. Spinner frames and success/failure decorations go to stderr only. Non-TTY human output is plain, deterministic, and has no ANSI control sequences. `NO_COLOR` and `--no-color` disable ANSI colors but do not remove state text or status marks.

Changing `workflow get` and `run get` from raw JSON by default is an intentional CLI UX break. Scripts must use `--json`; the command docs and help make this visible. The server envelope is not printed by the human renderer.

### 4. Keep list views compact and detail views deep

`workflow list` shows one aligned row per definition with repository names, node count, active count, and latest-run summary. `run list` shows one aligned row per run with filters and aggregate counts. Neither list view renders a full graph or step tree. `workflow get` shows the static graph/routes and a small recent-run summary. Only `run get` renders the full Argo-shaped execution tree.

The workflow list latest-run summary is explicitly labeled as an execution summary; a status mark there never means that the static definition itself ran. The workflow graph uses `✓` only to mean the definition is present/validated.

### 5. Status marks are stable and accessible

The renderer uses:

```text
✓ completed or valid
○ pending, not reached, or no runs
⟳ running, waiting, or active
✗ failed or blocked
− canceled
```

The ASCII fallback is `[x]`, `[ ]`, `[>]`, `[!]`, and `[-]`. Color reinforces these meanings but is never the only signal. Loading uses a short spinner only for interactive server calls and is absent from JSON/piped output.

### 6. Help is parsed before command execution

The command dispatcher recognizes `--help`/`-h` at every group/leaf before creating a server request or parsing command-specific required arguments. Each group owns a concise usage block and examples. Root help is grouped; `workflow --help` does not print run/repo flags; leaf help does not contact the server. The implementation continues to use Go's standard `flag` package rather than adding a CLI framework.

### 7. Version metadata is build-injected and server-independent

Add release/version, commit, build timestamp, Go version, and platform variables in the CLI package with safe development defaults. `version` prints the long block; `--version`/`-v` prints one concise line. None of these commands loads machine state or creates a server client request. Existing release/build configuration should inject values where available without making local builds fail when metadata is absent.

## Risks / Trade-offs

- **[Risk]** A step projection can drift from durable execution if an activity retries between external effects and projection writes. **Mitigation:** writes are idempotent, keyed by run/visit/sequence, and never affect execution; detail output can show `unavailable` for missing optional fields.
- **[Risk]** Long-running workflows with many loops produce a large detail response. **Mitigation:** keep the first implementation bounded to retained run history and render the complete tree in a deterministic order; add truncation only as a later explicitly designed feature rather than hiding steps silently.
- **[Risk]** Human output changes can break scripts that currently parse raw JSON from `run get`. **Mitigation:** make `--json` stable, document it in scoped help, and test JSON output separately from TTY rendering.
- **[Risk]** Temporal projection recovery may not retain every old step field after history retention or a legacy run. **Mitigation:** preserve the tree/status shape and display `-`/`unavailable`; never infer a successful step from absence.
- **[Risk]** Terminal width can make aligned columns wrap. **Mitigation:** calculate widths from terminal size, cap long messages with an ellipsis in human list views, and keep full values in `run get --json`.
- **[Risk]** Workflow graph cycles are difficult to display as a static tree. **Mitigation:** workflow get renders route edges with cycle-safe visited labels; run get renders actual visit rows, so loops remain finite and unambiguous.

## Migration Plan

1. Add the read-only step-detail projection and query response additively; existing execution state and workflow YAML remain unchanged.
2. Update the two durable-executor projection paths to write detail rows idempotently and to rebuild what is available from authoritative state.
3. Add server/client detail queries and CLI renderers while preserving existing filters and exit codes.
4. Add TTY/non-TTY/JSON golden tests for the reference layouts in the capability spec.
5. Add scoped help and version handling and update README/CLI documentation.
6. Rollback is a code/version rollback only; the derived step table may remain unused by an older binary. No data migration or execution-history rewrite is required.

## Open Questions

None. The output structure, status marks, JSON escape hatch, query boundary, and derived-timeline responsibility are settled by this design.
