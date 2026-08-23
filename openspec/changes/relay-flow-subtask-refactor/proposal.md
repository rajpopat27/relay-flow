## Why

The current implementation couples workflow polling, Jira ticket status, Orca execution, and OpenCode reporting around one parent ticket and in-memory daemons. It cannot provide isolated node context, durable multi-call transitions, repo-shared polling, or replaceable task-system, runner, and harness integrations without a substantial core rewrite.

## What Changes

- **BREAKING** Replace the current per-workflow daemon and parent-ticket-as-node model with a durable workflow run for each claimed parent ticket.
- Poll active parent tickets once per registered repo, route them to exactly one configured workflow in memory, and persist ownership with `wf:<workflow>` labels.
- Store submitted workflows under relay-flow's root directory, reload them on startup, and reject workflow replacement while runs are active.
- Create one reusable mailbox subtask for every agent or HITL node. Store the current node's summary in its mailbox and feedback in the selected next node's mailbox.
- Interpret every YAML graph through one durable workflow implementation backed initially by `go-workflows` and SQLite. Execute task-system, runner, and harness effects as ordered, retryable activities with roll-forward crash recovery.
- Require structured agent output containing status, one next step, a detailed summary, and feedback for the next node.
- Separate task-system, runner, harness, and durable-execution contracts so organizations can supply alternative integrations without changing workflow routing.
- Use one shared Repo Poller per repo, a Ticket Router for workflow selection, a Run Manager for claiming and run creation, and bounded Workflow/Activity Workers for execution.
- Support agent and HITL nodes with identical routing contracts; normal agent nodes may be nudged, while HITL nodes remain silent until valid output is produced.
- Add reliable JSON report delivery with `nodeVisitID`, persistence-before-acknowledgement, retry backoff, and harmless duplicate acknowledgement without a separate deduplication store.
- Add workflow, repo, and run management commands, cancellation, and explicit `serve --recover` disaster recovery for lost SQLite state.
- **BREAKING** Replace the current workflow and machine schemas with repo references, structured filters, plugin-owned task configuration, reserved `start`/`end` nodes, and root-level plugin and polling settings.

## Capabilities

### New Capabilities

- `repo-workflow-routing`: Register repos, poll each repo once, match active parent tickets to workflows, claim them, and recover claim-before-run failures.
- `durable-run-execution`: Execute every ticket through a durable serial graph with bounded workers, retries, cancellation, restart recovery, and explicit database-loss recovery.
- `node-mailboxes`: Create and reuse per-node subtasks, isolate node context, apply task-system-specific state changes, and transfer summaries and feedback safely.
- `structured-node-reporting`: Define, validate, deliver, acknowledge, retry, and deduplicate the structured agent/HITL output contract.
- `integration-contracts`: Define replaceable task-system, runner, harness, and durable-execution boundaries with Jira, Orca, OpenCode, and go-workflows as initial implementations.
- `workflow-repo-management`: Persist and manage machine configuration, registered repos, workflow definitions, active-run restrictions, and the Unix-socket CLI/API surface.
- `workflow-definition`: Define the workflow YAML graph, reserved lifecycle nodes, routes, node behavior, task configuration, nudge templates, defaults, and validation.

### Modified Capabilities

None. No existing OpenSpec capabilities are present; this change establishes the initial specifications.

## Impact

- Replaces most of `internal/daemon`, `internal/config`, `internal/server`, `internal/tasks`, and `internal/runner` orchestration rather than incrementally extending the current model.
- Introduces new workflow, repo, run, task, harness, execution, identity, paths, and retry boundaries while retaining useful Unix-socket, flock, ACLI, Orca CLI, and adapter-testing seams.
- Moves ACLI under the Jira adapter, Orca CLI under the Orca runner, and OpenCode-specific behavior under the OpenCode harness.
- Adds SQLite and a pinned `go-workflows` dependency, requiring a Go toolchain upgrade and a compatibility spike before implementation.
- Changes workflow YAML, root configuration, CLI commands, report payloads, terminal/session metadata, Jira polling, subtask usage, startup validation, and recovery behavior.
- Existing workflow files and current runtime behavior are not backward compatible and will require migration or replacement.
