# Beads end-to-end verification

A reproducible, real-process end-to-end run of the Beads task system: real
`bd` CLI and workspace, real Orca worktrees and terminals, real OpenCode agent
sessions, the real `relay-flow-plugin@0.2.2-alpha` report path, and the real
durable engine.

This is not a unit or composition test. Nothing is faked. It exists to prove
the section 8 lifecycle changes behave correctly against live tooling, and to
give the next run a baseline to compare against.

## Scope

Verified in one pass:

| Behavior | Where it is asserted below |
|---|---|
| Poll finds a ready top-level parent, routes it, claims it with `wf:<workflow>` | Step 7 |
| A second poll reuses the claim and does not create a duplicate run | Step 7 |
| `start` moves the parent `open → in_progress` (task 8.1) | Step 8 |
| Mailboxes are created as children with stable `<parent-id>:<node>` titles | Step 8 |
| Runner creates the ticket worktree, environment, and `<ticket>:<node>` terminal | Step 9 |
| Harness launches the agent, which reads the parent and its mailbox | Step 9 |
| Plugin registers the session and posts structured reports | Step 10 |
| Summary is written to the current mailbox only | Step 10 |
| Feedback is written to the selected next mailbox only | Step 11 |
| `CompleteMailbox` closes the current mailbox | Step 11 |
| Node revisit reuses the mailbox and reopens it `closed → in_progress` | Step 12 |
| A stale report for an already-decided visit is acked with no graph effect | Step 11 |
| `end` closes the parent, writes no feedback, and creates no `end` mailbox | Step 13 |
| A closed parent leaves both poll queries, so no re-run loop occurs | Step 13 |
| A human-owned parent status blocks the close and retries (task 8.4) | Step 14 |
| Recovery rolls forward once the human state clears, with no operator action | Step 15 |
| Worktrees, branches, comments, and labels are never compensated | Step 13, 15 |

## Prerequisites

Versions used for the recorded output:

```text
bd        1.2.2 (Homebrew)
opencode  1.18.25
orca      CLI + app running (orca status -> runtimeState: ready)
go        1.24+
```

The Orca app must be running and reachable, and OpenCode must have a working
model, otherwise the agent session in step 9 starts but never reports.

## Layout

Everything lives under one disposable directory. Use a distinctive name so no
other process reuses it:

```text
/tmp/relay-e2e-beads/
  bin/relay-flow          purpose-built binary; never installed, never on PATH
  home/                   isolated HOME, so state lands in home/.relay-flow
  payments/               sample git repo + .beads workspace + opencode.json
  payments-workflow.yaml  the workflow under test
```

Machine state is isolated by exporting `HOME`. `relay-flow` resolves its root
through `os.UserHomeDir()`, so this keeps the run completely away from a real
`~/.relay-flow`.

## Step 1 — build the binary into the disposable tree

Do not use `go install`, and do not put the binary on `PATH`. Always call it by
absolute path.

```sh
E2E=/tmp/relay-e2e-beads
rm -rf $E2E && mkdir -p $E2E/home $E2E/bin
cd <relay-flow checkout>
go build -buildvcs=false -o $E2E/bin/relay-flow ./cmd/relay-flow
```

`-buildvcs=false` is required when the checkout is a git worktree; without it
the Go toolchain fails with `error obtaining VCS status: exit status 128`.

## Step 2 — create the sample repository

```sh
mkdir -p $E2E/payments/src && cd $E2E/payments
git init -q .
git config user.email "e2e@example.com" && git config user.name "E2E Runner"
cat > opencode.json <<'JSON'
{
  "$schema": "https://opencode.ai/config.json",
  "plugin": ["relay-flow-plugin@0.2.2-alpha"]
}
JSON
printf 'def add(a, b):\n    return a + b\n' > src/calc.py
printf '.beads/embeddeddolt/\n' > .gitignore
git add -A && git commit -q -m "initial commit"
```

`repo register` later rewrites `opencode.json` idempotently. Pre-creating it
with the alpha plugin also verifies that existing content is preserved.

## Step 3 — initialize the Beads workspace

relay-flow never creates a Beads workspace, runs migrations, or starts
`bd serve`. Create it first:

```sh
cd $E2E/payments
bd init --prefix pay --non-interactive --skip-hooks --skip-agents
BEADS_DIR=$E2E/payments/.beads bd list --ready --no-parent --limit 0 --json
```

```text
[]
```

## Step 4 — add the repo to the runner

The Orca runner resolves a repo by path **and** display name, so the name used
in step 6 must equal the Orca display name. Orca derives it from the directory
name, which is why the directory is called `payments`:

```sh
orca repo add --path /tmp/relay-e2e-beads/payments
```

```text
id: 30263fd8-a3f6-4073-b08d-efaa1c2d3023
path: /tmp/relay-e2e-beads/payments
displayName: payments
```

Registering relay-flow with a name that does not match fails with
`orca: repo "<name>" at "<path>" not registered`.

## Step 5 — init and start the server

Every command from here on uses the isolated `HOME` and the absolute binary
path:

```sh
export HOME=/tmp/relay-e2e-beads/home
RF=/tmp/relay-e2e-beads/bin/relay-flow

$RF init --task-plugin beads --runner-plugin orca --harness-plugin opencode
$RF task auth            # Beads: no-op, writes no credentials
$RF serve --background --debug
```

```text
Task system: beads
Runner: orca
Harness: opencode
Relay-flow initialized
Relay-flow server started
```

`task auth` must exit 0 and must not create `home/.relay-flow/credentials.yaml`;
Beads authentication belongs to the `bd` workspace.

`repo register` and `workflow submit` are server-backed, so `serve` must come
first.

## Step 6 — register the repo

```sh
$RF repo register --name payments --path /tmp/relay-e2e-beads/payments \
  --set beadsDir=/tmp/relay-e2e-beads/payments/.beads
```

```text
payments
```

`home/.relay-flow/config.yaml` gains the repo-scoped workspace:

```yaml
repos:
    payments:
        path: /tmp/relay-e2e-beads/payments
        taskConfig:
            beadsDir: /tmp/relay-e2e-beads/payments/.beads
```

`opencode.json` keeps the plugin exactly once:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "plugin": ["relay-flow-plugin@0.2.2-alpha"]
}
```

## Step 7 — submit the workflow and create a ticket

`payments-workflow.yaml` is `start → implement → review → end`, with
`review` failing back to `implement` so a revisit is exercised. `transitionTo`
is configured on nodes, never at workflow scope.

```yaml
name: paymentsBeadsFlow
repos:
  - payments
cleanupRunnerOnEnd: false

taskConfig:
  filters:
    parentStatuses: [open]
    issueTypes: [task]
    labels: [relay-ready]

nodes:
  start:
    taskConfig:
      transitionTo:
        parentStatus: in_progress
    onSuccess:
      - target: implement
        when: The parent is ready for implementation

  implement:
    type: agent
    agent: build
    description: Implement the change described in the parent ticket and verify it.
    taskConfig:
      transitionTo:
        taskStatus: in_progress
    onSuccess:
      - target: review
        when: Implementation and verification are complete
    onFailure:
      - target: implement
        when: Implementation needs another pass

  review:
    type: agent
    agent: plan
    description: Review the implementation and approve or request changes.
    taskConfig:
      transitionTo:
        taskStatus: in_progress
    onSuccess:
      - target: end
        when: The review is approved
    onFailure:
      - target: implement
        when: Changes are requested

  end:
    taskConfig:
      transitionTo:
        parentStatus: closed
```

```sh
$RF workflow submit --file $E2E/payments-workflow.yaml     # -> paymentsBeadsFlow

export BEADS_DIR=/tmp/relay-e2e-beads/payments/.beads
cd $E2E/payments
bd create "Add multiply() to calc" --type task --labels relay-ready --json
```

Within one poll interval (15s) the server log shows the claim and the run,
then shows that the next poll does **not** create a second run:

```text
msg="poll cycle" repo=payments batch=1
msg="poll ticket" repo=payments ticket=pay-9yr id=pay-9yr title="Add multiply() to calc" claims=""
msg="route outcome" repo=payments ticket=pay-9yr workflow=paymentsBeadsFlow outcome=claimed
msg="ensure-run outcome" ticket=pay-9yr ... runID=payments/paymentsBeadsFlow/pay-9yr outcome=created
msg="poll ticket" repo=payments ticket=pay-9yr ... claims=wf:paymentsBeadsFlow
msg="route outcome" repo=payments ticket=pay-9yr workflow=paymentsBeadsFlow outcome=already-claimed
msg="ensure-run outcome" ticket=pay-9yr ... outcome=exists
```

## Step 8 — parent and mailbox state after `start`

```sh
bd show pay-9yr --json
bd list --parent pay-9yr --all --limit 0 --json
```

```text
parent:  pay-9yr | in_progress | ['relay-ready', 'wf:paymentsBeadsFlow']
         pay-9yr.2 | pay-9yr:review    | open        | ['wf:paymentsBeadsFlow']
         pay-9yr.1 | pay-9yr:implement | in_progress | ['wf:paymentsBeadsFlow']
```

This is the task 8.1 change. Before it, the parent stayed `open` for the whole
run. `in_progress` is inside the claimed-parent poll set
(`open,in_progress,blocked,deferred`), so the parent stays visible.

The parent no longer matches `filters.parentStatuses: [open]`, which is
correct: a claimed ticket is routed by its `wf:` label and filters are not
re-evaluated.

## Step 9 — runner and harness handoff

```text
msg="orca outcome" op=ensure-environment ticket=pay-9yr ... result=created
msg="orca outcome" op=set-environment-status envID=...::/home/raj/orca/workspaces/payments/pay-9yr status=in-progress result=ok
msg="orca outcome" op=find-terminal title=pay-9yr:implement result=absent
msg="harness call"   op=launch agent=build session="" mode=fresh ... node=implement nodeVisitID=4d7c20db...
msg="harness outcome" op=launch ... result=ok
msg="orca outcome" op=ensure-terminal title=pay-9yr:implement result=created
msg="orca outcome" op=find-terminal title=pay-9yr:implement result=found
```

```sh
orca terminal list | grep pay-9yr
```

```text
term_8f1d0856-...  OC | Beads Ticket pay-9yr.1 Instructions  connected  /home/raj/orca/workspaces/payments/pay-9yr
  tab 84cfa60c-...  pay-9yr:implement
```

Reading the pane shows the agent using the `bd` CLI to read the parent ticket
and its mailbox before working:

```text
I'll query the local Beads CLI for the parent and mailbox tickets, then read the mailbox comments.
$ bd --help
▣  Build · GPT-5.6 Luna
```

The terminal tab title is the stable `<ticket>:<node>`. The OpenCode session
title is chosen by the agent and is not a relay-flow contract.

## Step 10 — plugin reporting

`home/.relay-flow/plugin.log`:

```text
msg="runtime registration succeeded" runId="payments/paymentsBeadsFlow/pay-9yr" node="implement" sessionId="ses_fa67f894..."
```

Server log for an agent-produced report:

```text
msg="report received"  ... node=implement reportID=ses_fa67f894...:msg_05980dac... status=failure nextStep=implement
msg="report persisted" ... nodeVisitID=4d7c20db...
msg="report ack sent"  ... nodeVisitID=4d7c20db...
```

The summary lands on the current mailbox with a visit-scoped marker:

```text
Summary for implement

COMPLETED:
Read parent ticket pay-9yr and mailbox pay-9yr.1; no comments or feedback found.
...
<!-- 4d7c20db9f9f5419da9b7785628f3e2c:summary -->
```

## Step 11 — advancing a node

Reports can be driven deterministically through the same endpoint the plugin
uses, which is what makes this run repeatable:

```sh
$RF report <<'JSON'
{"runId":"payments/paymentsBeadsFlow/pay-9yr","node":"implement","reportId":"e2e-implement-1",
 "report":{"status":"success","nextStep":"review",
  "summary":{"completed":"Added multiply() to src/calc.py.","commits":"None","notCompleted":"None",
             "issuesDiscovered":"None","verification":"import passes.","notes":"None"},
  "feedback":{"reasonForNextStep":"Implementation is complete and ready for review.",
              "requiredActions":"Review multiply() for correctness and style.",
              "relevantContext":"multiply() was added next to add().",
              "expectedResult":"Review approves or requests changes."}}}
JSON
```

```text
msg="report received"  ... node=implement reportID=e2e-implement-1 status=success nextStep=review
msg="mailbox completed" ... node=implement mailbox=pay-9yr.1
```

```text
parent            in_progress
pay-9yr:implement -> closed
pay-9yr:review    -> in_progress
```

Feedback went only to the selected next mailbox:

```text
review mailbox comments: 1
Feedback from implement to review mailbox pay-9yr.2
REASON FOR NEXT STEP:
Implementation is complete and ready for review.
```

A live agent racing the same visit is handled correctly. When the agent's
report arrived after the visit had already been decided:

```text
msg="report received"     ... node=review reportID=ses_fa67d5ea...:msg_059832a9... status=failure nextStep=implement
msg="report duplicate ack" ... node=review reportID=ses_fa67d5ea...:msg_059832a9... state=waiting
```

It is acked, and produces no second transition and no duplicate comment.

## Step 12 — node revisit

Reporting `failure`/`implement` from `review` returns to a mailbox that is
already `closed`:

```text
parent            in_progress
pay-9yr:review    -> closed
pay-9yr:implement -> in_progress
child count       2
```

The mailbox is reused, not recreated, and `closed → in_progress` is an
accepted relay-flow-owned transition rather than a conflict.

## Step 13 — end

```text
run state: completed

parent  pay-9yr | closed | ['relay-ready', 'wf:paymentsBeadsFlow']
        pay-9yr:review    | closed
        pay-9yr:implement | closed

pay-9yr.1: total=19 summary=10 feedback=9
pay-9yr.2: total=4  summary=2  feedback=2
```

`end` wrote a summary but no feedback, and created no `end` mailbox. The
mailbox with 10 summaries is the effect of a real agent repeatedly reporting
`failure`/`implement`; a self-looping node is by design and is not bounded.

The closed parent then leaves both poll queries, so it is never re-run:

```sh
bd list --ready --no-parent --limit 0 --json                                    # []
bd list --no-parent --status open,in_progress,blocked,deferred \
        --label-pattern 'wf:*' --limit 0 --json                                 # []
```

```text
msg="poll cycle" repo=payments batch=0
```

This is exactly what the pre-8.5 examples broke: a `transitionTo` at workflow
scope overrode the `end` default, the parent never closed, and it stayed in
every poll batch until retention removed the completed run and it re-ran.

Nothing was compensated:

```sh
git -C /home/raj/orca/workspaces/payments/pay-9yr log --oneline   # worktree intact
git -C $E2E/payments branch -a                                    # master, pay-9yr
```

The whole run produced no `ERROR` and no `WARN` lines.

## Step 14 — a human-owned parent status blocks the close

This is the task 8.4 change. Run a second ticket to `review`, then park the
parent the way a human would:

```sh
bd create "Add divide() to calc" --type task --labels relay-ready --json   # -> pay-3ei
# ... advance to review ...
bd update pay-3ei --status blocked --json
# ... then report review -> end ...
```

```text
run state:  blocked
currentNode: review
parent:      blocked          <- not overwritten

msg="retry scheduled" ticket=pay-3ei ... kind=conflict delayMs=1712 \
  error="issue \"pay-3ei\" has status \"blocked\"; expected one of [open in_progress] before changing to \"closed\"" node=end
msg="retry scheduled" ... delayMs=4343 ...
msg="retry scheduled" ... delayMs=7475 ...
```

Before task 8.4 the adapter accepted `blocked`, `deferred`, and `hooked` as
close sources, so this human signal was silently overwritten. The mailbox rule
already conflicted here; now the parent behaves the same way.

## Step 15 — roll forward

Clear the human state and take no other action:

```sh
bd update pay-3ei --status in_progress --json
```

```text
[1] run=blocked
[2] run=blocked
[3] run=completed

parent            closed
pay-3ei:implement closed
pay-3ei:review    closed
```

The durable retry rolls forward on its own. There is no rollback and no
operator step.

## Teardown

```sh
HOME=/tmp/relay-e2e-beads/home /tmp/relay-e2e-beads/bin/relay-flow stop
orca terminal stop --worktree "<repoId>::/home/raj/orca/workspaces/payments/<ticket>"
rm -rf /tmp/relay-e2e-beads
```

Orca keeps the repo registration and the ticket worktrees; remove them from the
Orca UI if the host should be left completely clean.

## Notes for the next run

- Ticket IDs are workspace-generated (`pay-9yr`, `pay-3ei`) and will differ.
  Compare shapes and transitions, not identifiers.
- A live agent competes with any manually submitted report. To drive the graph
  deterministically, stop the ticket's terminals first
  (`orca terminal stop --worktree ...`), then report; otherwise expect
  `report duplicate ack` lines, which are correct behavior.
- The default harness `initial` prompt says "Use the `<taskSystem>` tools".
  There is no Beads MCP server, so the agent spends its first turn discovering
  the `bd` CLI. It recovers on its own; a Beads-specific prompt would remove
  that turn.
- Comment counts grow with agent retries. Assert on summary/feedback ratios and
  destinations rather than absolute totals.
