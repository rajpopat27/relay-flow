# Relay-flow Rewrite — Agent Context

This repo is mid-rewrite: the current Go code is the OLD per-workflow, in-memory daemon. The target is the durable architecture in the OpenSpec change. Do not assume the target is implemented.

## Source of truth (read these before acting; never invent behavior)

- `openspec/changes/relay-flow-subtask-refactor/tasks.md` — numbered implementation tasks in 6 sections: 1 remove legacy code, 2 foundations/toolchain, 3 behavior tests, 4 implementation, 5 startup wiring, 6 verification
- `openspec/changes/relay-flow-subtask-refactor/proposal.md` and `design.md` — why, scope, and every settled decision with rejected alternatives
- `openspec/changes/relay-flow-subtask-refactor/specs/*/spec.md` — normative requirements and scenarios
- `docs/structs-methods-interfaces.md` — normative structs/methods/interfaces; match signatures exactly; never edit
- `docs/feature-tracker.md` — behavior descriptions; never edit

## Non-negotiable rules

- KISS/YAGNI: no speculative infrastructure, no compatibility layers, no fallbacks, no migration tooling, no event bus/DI/frameworks. Beta breaking rewrite — deleting old code is correct.
- Interfaces only at replaceable boundaries (task system, runner, harness, durable executor, small consumer query needs); concrete structs elsewhere.
- Unstable state is EXPECTED: after section 1 the build fails; after section 3 tests are red. Never short-circuit task order or "fix" unrelated red. Do only what the current task says.
- If something is genuinely ambiguous or contradictory between artifacts, STOP and ask the user. Do not guess.
- Settled decisions, never relitigate: one Repo Poller per registered repo (not per workflow); stale-label cleanup intentionally absent; no rollback/compensation ever; recovery rolls forward; `CompleteMailbox` separate from `ApplyTaskConfig`; mailbox subtasks and session continuity are intentional; extra LLM calls and rare duplicate comments after recovery are accepted; no manual pause/resume.
- Naming: lifecycle nodes are `start`/`end`; `terminal` means runner terminal only; cleanup field is `cleanupRunnerOnEnd`; terminal titles are stable `<ticket>:<node>`; JSON wire keys are `runId`/`nodeVisitId`; visit/run IDs are relay-flow-generated, never LLM-generated; routes cannot target `start`; feedback to `end` is all `None` with no comment written; ack only after durable signal persistence.

## Core architecture (roles)

- Task system: owns parent tickets, mailbox subtasks, task state, labels, comments, adapter config. The parent ticket is the unit of work.
- Durable workflow engine (`go-workflows` + SQLite): owns graph progression, waits, reports, retries, recovery. No custom Saga/state machine.
- Mailbox subtask: one agent/HITL node's scratch space; description defines node work, comments hold that node's summary + selected incoming feedback.
- Harness: owns agent launch, session/report behavior, parsing, nudging, resume semantics.
- Runner: owns ticket worktrees/environments, terminals, liveness, execution of harness commands.
- Compensation/rollback of Jira, runner, mailbox, or agent work NEVER exists; recovery always rolls forward through idempotent activities.

## More settled decisions (never relitigate)

- Assignee isolation prevents competing machines from processing the same tickets.
- Parent stays the workflow unit; mailboxes never become independent workflow runs.
- New `nodeVisitID` does NOT imply discarding a live session: reuse a live usable ticket/node session; relaunch only when absent/unusable.
- Normal transition ordering: persist report+selected route → write current summary → write feedback only to selected next mailbox → `CompleteMailbox` current → process next node.
- `ApplyTaskConfig` starts/processes a node with opaque adapter-owned config; `CompleteMailbox` (e.g. Jira subtask → Done) is separate and narrow.

## Database-loss recovery (`serve --recover` only)

- Treat ALL SQLite execution state as gone/untrusted. Evidence = current task-system state + surviving runner sessions. Config is loaded only to know which repos/workflows to rebuild.
- Never resume old routes, current nodes, visit IDs, selected targets, activity checkpoints, or report history.
- Discover+close surviving run-owned terminals; reset Jira parent/mailbox state (original starting state unknown); preserve comments, labels, mailbox history, worktrees, branches, code; rediscover mailboxes, create only missing; fresh durable runs starting at `start`; fresh visit/run identity.
- Database loss is NEVER inferred from a missing run. In a healthy database, a missing run for a labeled ticket is only claim-before-run recovery.

## Reports

- Complete structured contract every visit. `STATUS` = success|failure; `NEXT STEP` = exactly one valid route for that status; summary = current node; feedback = selected next node only; next step `end` → all feedback `None`, no comment written.
- JSON transport only (no shell args, JSONL, or plugin SQLite writes). Plugin retries the exact parsed report until ack. Invalid agent output is nudged; invalid/missing HITL output stays silent. Duplicate/stale visit reports are acked safely with no repeated graph effects.

## Review guidance (applies to any review)

- First check whether an apparent gap is already answered by another artifact (proposal=why/scope, design=architecture/tradeoffs, specs=normative behavior, tasks=steps). Don't criticize one artifact for another's content.
- Flag only real contradictions, missing behavior required by the agreed design, or ambiguity that could cause different implementations. Findings brief, severity-ordered, citing artifact+section.
- Reviews never edit code unless explicitly asked.

## Orchestrated workers (foreman/implementer/verifier)

If your prompt names a section and a peer terminal handle, you are an orchestrated worker:

1. Load the Orca orchestration skill FIRST so you use the correct, version-matched commands: run `orca-ide skills get orchestration`.
2. Use `orca-ide` (never bare `orca`) for all orchestration commands.
3. Follow the messaging protocol in your agent profile (`.opencode/agents/implementer.md` or `verifier.md`): `send --to <peer handle>` for handoffs, `check --wait` + `--ack` for receiving, `ask` for blocking questions. Resend after FAIL fixes; re-resolve stale handles with `terminal list`.

# GitNexus — Code Intelligence

Indexed as **jira-workflow**. Stale? `node .gitnexus/run.cjs analyze`.

- MUST `impact({target, direction:"upstream"})` before editing any symbol; warn the user on HIGH/CRITICAL risk before proceeding.
- MUST `detect_changes()` before committing.
- Prefer `query({search_query})` over grep for exploration; `context({name})` for callers/callees/flows; `explain({target})` for taint findings.
- NEVER find-and-replace renames — use the `rename` tool (call-graph aware).
- Guides: `.claude/skills/gitnexus/gitnexus-{exploring,impact-analysis,debugging,refactoring,guide,cli}/SKILL.md`.


<!-- gitnexus:end -->

<!-- gitnexus:start -->
# GitNexus — Code Intelligence

This project is indexed by GitNexus as **relay-flow** (2830 symbols, 9263 relationships, 189 execution flows). Use the GitNexus MCP tools to understand code, assess impact, and navigate safely.

> Index stale? Run `node .gitnexus/run.cjs analyze` from the project root — it auto-selects an available runner. No `.gitnexus/run.cjs` yet? `npx gitnexus analyze` (npm 11 crash → `npm i -g gitnexus`; #1939).

## Always Do

- **MUST run impact analysis before editing any symbol.** Before modifying a function, class, or method, run `impact({target: "symbolName", direction: "upstream"})` and report the blast radius (direct callers, affected processes, risk level) to the user.
- **MUST run `detect_changes()` before committing** to verify your changes only affect expected symbols and execution flows. For regression review, compare against the default branch: `detect_changes({scope: "compare", base_ref: "main"})`.
- **MUST warn the user** if impact analysis returns HIGH or CRITICAL risk before proceeding with edits.
- When exploring unfamiliar code, use `query({search_query: "concept"})` to find execution flows instead of grepping. It returns process-grouped results ranked by relevance.
- When you need full context on a specific symbol — callers, callees, which execution flows it participates in — use `context({name: "symbolName"})`.
- For security review, `explain({target: "fileOrSymbol"})` lists taint findings (source→sink flows; needs `analyze --pdg`).

## Never Do

- NEVER edit a function, class, or method without first running `impact` on it.
- NEVER ignore HIGH or CRITICAL risk warnings from impact analysis.
- NEVER rename symbols with find-and-replace — use `rename` which understands the call graph.
- NEVER commit changes without running `detect_changes()` to check affected scope.

## Resources

| Resource | Use for |
|----------|---------|
| `gitnexus://repo/relay-flow/context` | Codebase overview, check index freshness |
| `gitnexus://repo/relay-flow/clusters` | All functional areas |
| `gitnexus://repo/relay-flow/processes` | All execution flows |
| `gitnexus://repo/relay-flow/process/{name}` | Step-by-step execution trace |

## CLI

| Task | Read this skill file |
|------|---------------------|
| Understand architecture / "How does X work?" | `.claude/skills/gitnexus/gitnexus-exploring/SKILL.md` |
| Blast radius / "What breaks if I change X?" | `.claude/skills/gitnexus/gitnexus-impact-analysis/SKILL.md` |
| Trace bugs / "Why is X failing?" | `.claude/skills/gitnexus/gitnexus-debugging/SKILL.md` |
| Rename / extract / split / refactor | `.claude/skills/gitnexus/gitnexus-refactoring/SKILL.md` |
| Tools, resources, schema reference | `.claude/skills/gitnexus/gitnexus-guide/SKILL.md` |
| Index, status, clean, wiki CLI commands | `.claude/skills/gitnexus/gitnexus-cli/SKILL.md` |

<!-- gitnexus:end -->
