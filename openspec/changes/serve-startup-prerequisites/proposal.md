## Why

`serve` can publish readiness before the selected task connection, runner runtime,
or local harness executable is usable. Full workflow validation belongs at
submission; it is not a suitable machine-level startup health check.

## What Changes

- Introduce minimal plugin-owned prerequisite probes for selected dependencies.
- Gate work and successful foreground/detached readiness on probes with a
  five-second per-probe ceiling and ten-second aggregate deadline, without
  retries, workflow duplication, writes, agent launches, or ticket/history scans.
- Keep zero registered repositories valid. Check configured Beads workspaces;
  defer Beads connection verification to registration only when none is configured.
- Use one resolved Orca launcher for startup and subsequent operations; probe
  Herdr through one snapshot rather than per-workspace repository discovery.
- Distinguish missing executable, launch, authentication, connection, malformed
  response and timeout failures; propagate safe dependency/operation diagnostics
  and the detached server log location.
- Preserve full submission validation, invalid stored-workflow isolation, normal
  runtime retries, and existing durable identity/recovery rules.

## Capabilities

### Modified Capabilities

- `integration-contracts`: explicit adapter-owned startup boundaries and runner probes.
- `herdr-runner`: a single read-only machine snapshot with selector preservation.
- `workflow-repo-management`: prerequisite-gated serve readiness, independent of repo count.

## Impact

Changes stay in plugin boundaries, CLI startup wiring, focused tests, README and
OpenSpec. No new configuration knobs, health endpoint/framework, services,
compatibility fallback, migration, or edits to the two normative `docs/` files.
Implementation is split into the bounded slices recorded in `tasks.md`.
