# relay-flow-ne7 Orca + Beads E2E verification

Date: 2026-09-09
PR: #25
Change under test: interactive runner repository registration and Orca runner provisioning

This record summarizes a second real-process E2E run using an isolated headless Orca runtime. The disposable raw evidence was kept under `/tmp/relay-flow-ne7-e2e-orca`; no repo, Beads workspace, relay-flow state, Orca runtime state, worktree, or binary was created outside `/tmp`.

## Components and isolation

- relay-flow binary: `/tmp/relay-flow-ne7-e2e-orca/bin/relay-flow`
- Git repository: `/tmp/relay-flow-ne7-e2e-orca/repo2`
- local Beads workspace: `/tmp/relay-flow-ne7-e2e-orca/repo2/.beads`
- relay-flow home: `/tmp/relay-flow-ne7-e2e-orca/relay-home3`
- isolated Orca home: `/tmp/relay-flow-ne7-e2e-orca/orca-home3`
- isolated Orca project root: `/tmp/relay-flow-ne7-e2e-orca/repo2`
- Orca worktree directory: `/tmp/relay-flow-ne7-e2e-orca/orca-home3/orca/workspaces`
- Orca version: `1.4.197`
- Beads version: `1.2.2`

Orca was started with `orca-ide serve --project-root ... --json`, and all CLI calls used the disposable pairing URL from that server. This avoided the persistent desktop Orca runtime and kept all runtime resources in `/tmp`.

## Interactive registration

A real PTY drove `relay-flow repo register` with the Orca runner:

1. Orca initially reported zero repositories in the isolated runtime.
2. The Huh multi-select displayed `[+] Add repository`.
3. The test selected Add and entered:
   - path: `/tmp/relay-flow-ne7-e2e-orca/repo2`
   - registered name: `orca-ne7-2`
4. The candidate list refreshed and the new candidate was selected.
5. The Beads workspace `/tmp/relay-flow-ne7-e2e-orca/repo2/.beads` was entered and persisted.

The resulting relay-flow registration was:

```json
{
  "name": "orca-ne7-2",
  "path": "/tmp/relay-flow-ne7-e2e-orca/repo2",
  "taskConfig": {
    "beadsDir": "/tmp/relay-flow-ne7-e2e-orca/repo2/.beads"
  }
}
```

The isolated Orca runtime reported the repository at the requested `/tmp` path.

## Real Beads workflow run

A real Beads parent was created through `bd`:

```text
ID:     orcae2e2-6bl
Labels: relay-ready, repo:orca-ne7-2, wf:orcaBeadsRegistrationE2E
```

The current relay-flow binary claimed the issue, created the `implement` and `verify` mailboxes, and started the durable run:

```text
Run:     orca-ne7-2/orcaBeadsRegistrationE2E/orcae2e2-6bl
Visit 1: implement
Visit 2: verify
```

Orca created the ticket worktree and labelled terminals under `/tmp`:

```text
worktree: /tmp/relay-flow-ne7-e2e-orca/orca-home3/orca/workspaces/repo2/orcae2e2-6bl
terminal: orcae2e2-6bl:implement
terminal: orcae2e2-6bl:verify
```

The isolated Orca terminal list showed both labelled terminals connected and the relay-flow command running in the ticket worktree. The server log recorded `ensure-environment result=created`, subsequent `result=exists`, terminal creation, stable terminal discovery, and both node transitions.

For deterministic graph progression, reports were submitted through the real `relay-flow report` command, matching the manual-report pattern used by the prior Beads E2E. The Orca runtime, terminals, and OpenCode processes were real.

The E2E created and committed the requested marker in the ticket checkout:

```text
commit: c25b654
file:   e2e-marker.txt
value:  orca-beads-e2e
```

The source checkout did not contain the marker. The parent and both Beads mailboxes closed after the workflow reached `end`.

## Cleanup assertions

With `cleanupRunnerOnEnd: true`:

- The labelled Orca terminals were closed.
- The child Orca worktree was removed by the Orca runner cleanup path.
- The source repository worktree remained as the only Orca worktree.
- The source checkout did not contain `e2e-marker.txt`.
- The relay-flow server and isolated Orca server were stopped.
- No disposable E2E processes remained.
- The relay-flow server log contained zero `ERROR`/`WARN` entries.

The headless Orca stderr contains expected environment warnings about DBus/keyring and a stale Codex hook compatibility check; these occurred during isolated Orca server startup and did not affect the run. The Orca runtime operations and relay-flow server completed successfully.

## Related automated verification

- `GOTOOLCHAIN=auto go test ./cmd/relay-flow ./internal/repo ./internal/runner/... ./internal/server`
- `GOTOOLCHAIN=auto go test -race ./cmd/relay-flow ./internal/repo ./internal/runner/... ./internal/server`
- `GOTOOLCHAIN=auto go vet ./...`
- `cd plugin && bun test` — 70 passed
- Strict Orca adapter and CLI contract tests passed.

Raw Orca E2E JSON responses, Beads state, run snapshots, terminal lists, reports, and logs remain under `/tmp/relay-flow-ne7-e2e-orca/evidence`.
