# relay-flow-ne7 Herdr + Beads E2E verification

Date: 2026-09-09
PR: #25
Change under test: interactive runner repository registration and Herdr source-workspace reconciliation

This record summarizes a real-process E2E run. The disposable raw evidence was kept under `/tmp/relay-flow-ne7-e2e`; no E2E state was placed in the operator's normal relay-flow, Beads, OpenCode, or Herdr directories.

## Components and isolation

- relay-flow binary: `/tmp/relay-flow-ne7-e2e/bin/relay-flow`
- Git repository: `/tmp/relay-flow-ne7-e2e/repo`
- local Beads workspace: `/tmp/relay-flow-ne7-e2e/repo/.beads`
- relay-flow home: `/tmp/relay-flow-ne7-e2e/relay-home`
- Herdr worktree directory: `/tmp/relay-flow-ne7-e2e/herdr-worktrees`
- Herdr session: `relay-ne7-e2e`
- Herdr version: `0.8.2`
- Beads version: `1.2.2`

The disposable Herdr config set its `[worktrees].directory` to the E2E worktree directory. The binary was built with `-buildvcs=false` and invoked by absolute path.

## Interactive registration

A real PTY drove `relay-flow repo register` with the Herdr runner:

1. Herdr initially reported zero workspaces.
2. The Huh multi-select displayed `[+] Add repository`.
3. The test selected Add and entered:
   - path: `/tmp/relay-flow-ne7-e2e/repo`
   - registered name: `herdr-ne7`
4. The list was refreshed and the new candidate was automatically selected.
5. The Beads workspace `/tmp/relay-flow-ne7-e2e/repo/.beads` was entered and persisted.

The resulting repository registration was:

```json
{
  "name": "herdr-ne7",
  "path": "/tmp/relay-flow-ne7-e2e/repo",
  "taskConfig": {
    "beadsDir": "/tmp/relay-flow-ne7-e2e/repo/.beads"
  }
}
```

Herdr then reported one source workspace (`w1`) at the repository path. Calling `/repos/ensure` twice more returned success without creating another workspace; the workspace count remained one. This verifies the plain-source-workspace and repeated-ensure fixes.

## Real Beads workflow run

A real Beads parent was created through `bd`:

```text
ID:     e2e-a96
Labels: relay-ready, repo:herdr-ne7, wf:herdrBeadsRegistrationE2E
```

The current relay-flow binary claimed the issue, created the `implement` and `verify` mailboxes, and started the durable run:

```text
Run:     herdr-ne7/herdrBeadsRegistrationE2E/e2e-a96
Visit 1: implement
Visit 2: verify
```

Herdr created the ticket worktree and labelled panes entirely under `/tmp`:

```text
worktree: /tmp/relay-flow-ne7-e2e/herdr-worktrees/repo/e2e-a96
pane:     e2e-a96:implement
pane:     e2e-a96:verify
```

`pane process-info` showed real `opencode` processes in both panes, with the ticket worktree as their CWD.

For deterministic graph progression, reports were submitted through the real `relay-flow report` command, matching the manual-report pattern used by the prior Beads E2E. The Herdr panes and OpenCode processes were still real.

The E2E created and committed the requested marker in the ticket checkout:

```text
commit: c5327c1b62e666570afeec89917bde14eb17028c
file:   e2e-marker.txt
value:  herdr-beads-e2e
```

The source checkout did not contain the marker. After the workflow reached `end`, the parent and both mailboxes were closed.

## Cleanup assertions

With `cleanupRunnerOnEnd: true`:

- The ticket workspace and labelled ticket panes were closed.
- The source workspace remained open.
- The ticket worktree and branch remained on disk.
- `e2e-marker.txt` and commit `c5327c1` remained in the ticket checkout.
- The Herdr and relay-flow servers were stopped.
- The disposable Herdr session was deleted.
- No disposable E2E processes remained.
- Server and Herdr logs contained zero `ERROR`/`WARN` entries.

The raw local evidence includes JSON responses, run snapshots, Beads comments, process-info output, worktree listings, and final logs under `/tmp/relay-flow-ne7-e2e/evidence`.

## Related automated verification

- `GOTOOLCHAIN=auto go test ./cmd/relay-flow ./internal/repo ./internal/runner/... ./internal/server`
- `GOTOOLCHAIN=auto go test -race ./cmd/relay-flow ./internal/repo ./internal/runner/... ./internal/server`
- `GOTOOLCHAIN=auto go vet ./...`
- `cd plugin && bun test` — 70 passed
- `RELAY_FLOW_HERDR_LIVE=1 GOTOOLCHAIN=auto go test -count=1 ./internal/runner/herdr/herdrcli/ -run Live -v`

This E2E used Herdr to keep all runtime resources in `/tmp`; no persistent Orca registry state was modified. Orca behavior remains covered by the strict adapter and CLI contract tests in this change.
