# Temporal Executor MVP Scope Audit

This note records the Section 0 scope guardrails and local reference checks. It is
an implementation checklist, not runtime state or a migration plan.

## Explicit prohibitions

The MVP does not:

- import or build the Temporal Server module;
- access Temporal Server's underlying database directly;
- support Temporal Cloud, TLS, API keys, or remote deployment configuration;
- switch, migrate, combine, or silently fall back between executors;
- use Temporal as an arbitrary database or persist unsupported application state;
- remove the `relay_runs`, `relay_node_runtime`, `relay_processed_reports`, or
  `relay_node_sessions` tables;
- reset tickets, mailboxes, or task-system state during Temporal projection
  recovery;
- create one worker, queue, namespace, or long-lived process per ticket;
- add a custom state machine, event bus, dependency-injection framework, or
  compatibility/migration layer;
- change the report wire contract or expose `nodeVisitID` to the plugin;
- let the plugin access relay-flow SQLite or write a JSONL report outbox; or
- implement rollback or compensation.

The selected executor remains immutable per initialized home. `goworkflows` is
the default, Temporal history is authoritative in Temporal mode, and explicit
Temporal recovery rebuilds only the local relay projection.

## Local reference and runtime checks

The required local reference paths were checked without changing either
external checkout:

- `/home/raj/raj/sdk-go` — present (checkout `57cc5a7d`; its local `go.mod`
  declares Go `1.26.0`);
- `/home/raj/raj/temporal` — present (checkout `44131b26`);
- `localhost:7233` — TCP endpoint reachable from this workspace.

The endpoint check used a TCP probe rather than an HTTP request because Temporal
serves gRPC on this port. The committed application must use the pinned public
SDK and must not use either checkout as a module replacement.

## Source alignment

Section 0 was checked against the Temporal MVP proposal, design, task list, the
Temporal and modified capability specs, the archived go-workflows rewrite
references, `docs/structs-methods-interfaces.md`, and `docs/feature-tracker.md`.
