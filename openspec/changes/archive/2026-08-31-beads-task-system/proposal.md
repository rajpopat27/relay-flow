# Add Beads as a task-system plugin

## Why

Relay-flow needs Beads as a selectable task-system integration for repositories that use the local `bd` CLI and Beads-managed Dolt storage instead of Jira. Beads should fit the existing repo-bound task-system seam: one task-system instance and one Repo Poller per registered repository, while runner, harness, workflow routing, and durable execution remain unchanged.

The integration must work with both embedded Beads workspaces and server-backed Dolt workspaces. A repository's code path and its Beads workspace path are separate concerns, so the task configuration needs an explicit `beadsDir` value for scope and workspace selection.

## What Changes

- Add a statically registered `beads` task plugin.
- Execute the local `bd` CLI with JSON output rather than importing Beads internals or reading Beads storage directly.
- Bind each Beads task system to its registered repository path for command execution.
- Select the Beads workspace through repo-scoped `taskConfig.beadsDir` and use that workspace as the task scope key.
- Poll ready top-level parent issues and claimed active parent issues once per registered repository.
- Use `wf:<workflow>` labels for relay-flow ownership.
- Represent node mailboxes as reusable Beads child issues.
- Implement Beads-specific status reconciliation using a read-before-write check followed by an unconditional update for the expected state.
- Preserve the existing comment markers, durable ordering, recovery, and task-system contracts.
- Add mandatory live CLI verification against a disposable `/tmp/beads-demo` workspace and Dolt server before adapter implementation.
- Reuse the shared task configuration vocabulary: `filters`, `templates`, optional top-level `assignee`, and `transitionTo.parentStatus`/`transitionTo.taskStatus`.

## Goals

- Let users select Beads without changing runner or harness selection.
- Support one independent Beads workspace per registered repository.
- Support an external/server-backed Beads workspace without requiring `.beads` in the code repository.
- Keep all Beads-specific configuration and command semantics inside the Beads adapter.
- Keep tests deterministic with a strict fake `bd` executable plus a real disposable Beads preflight.
- Preserve parent-ticket ownership, mailbox history, comments, labels, and roll-forward recovery.

## Non-Goals

- Importing the Beads Go module.
- Reading `.beads/issues.jsonl`, Dolt tables, or Beads internal packages.
- Starting or managing `bd serve` from relay-flow.
- Mapping Beads prefixes to Jira-like components or tenants.
- Sharing one Beads workspace between multiple registered repositories.
- Adding a Beads-specific poller or retry system.
- Changing runner, harness, workflow, report, SQLite, or Unix-socket APIs.
- Automatic Beads workspace initialization or migration.
- Adding a compatibility fallback around status updates.

## Impact

The change adds `internal/task/beads` and `internal/task/beads/bdcli`, plus static blank-import wiring and tests. The selected task plugin remains machine-scoped, while `beadsDir` makes the physical Beads workspace explicit per repository. The existing `task.System` interface is reused without adding Beads types to core.

The Beads adapter intentionally accepts the small race between its status read and unconditional status write. If an incompatible state is observed before the write, the adapter returns a conflict and the durable run retries; if the state changes after the read, Beads' last-writer-wins behavior is accepted for this integration.

## Configuration compatibility

Beads reuses the existing task configuration field names wherever they have a
meaning at the Beads boundary. The supported shared fields are `filters`,
`templates`, optional top-level `assignee`, and `transitionTo` with its
`parentStatus` and `taskStatus` members. An explicit `filters.assignees` value
takes precedence; otherwise the top-level `assignee` supplies the same default
assignee-filter behavior as Jira. These fields retain their existing scope and
merge behavior.

`beadsDir` is the only Beads-specific required field. It is required in each
registered repository's task configuration because it identifies the physical
Beads workspace and task scope; it is never inferred from a code repository,
Jira project, component, or issue prefix. Jira-only `project` and `component`
fields remain rejected rather than accepted and ignored. The former
Beads-only `status.parent` and `status.mailbox` vocabulary is not part of the
public configuration shape; lifecycle transitions use `transitionTo` instead.

Status values remain provider-specific. Beads configurations use native Beads
status names such as `open`, `in_progress`, and `closed`, while Jira
configurations use names such as `In Progress` and `Done`. Relay-flow does not
silently translate arbitrary Jira status values into Beads values.
