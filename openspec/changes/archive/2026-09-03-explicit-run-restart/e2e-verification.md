# Explicit restart E2E verification

Status: passed.

This is the exact real-process scenario used to verify the explicit restart
implementation. It uses the disposable repository that was created for the
native TUI research:

```text
Code repository:   /tmp/dummy-tui
Task system:       Beads 1.2.2 (embedded Dolt)
Beads workspace:   /tmp/dummy-tui/.beads
Runner:            Orca (repo already registered as dummy-tui)
Harness:           OpenCode 1.18.27
Relay-flow binary: purpose-built binary built from this checkout
Parent ticket:     demo-jmm
Workflow:          restartDemo
```

The scenario proves:

```text
cancel run
  -> update workflow
  -> resubmit workflow
  -> verify canceled run does not restart automatically
  -> explicitly restart run
  -> verify numeric attempt 2 starts at start/implement
  -> verify updated workflow and preserved worktree/mailbox
```

The E2E does not complete the agent report. It deliberately stops after the
new attempt reaches the `implement` wait so the restart result can be inspected
without adding another model decision. A delayed report from the canceled
attempt did arrive during the run and was acknowledged as a duplicate.

## Prerequisites

Install and verify:

```text
bd        1.2.2 or a compatible Beads CLI
opencode  1.18.27 or the installed OpenCode version
orca      Orca app/CLI running and ready
Go        the version required by the checkout
```

The Orca app must already contain `/tmp/dummy-tui` as a repository with display
name `dummy-tui`. Verify it before running:

```sh
orca-ide repo list --json
```

The response used in this run contained:

```json
{
  "id": "d6a831af-0109-448e-8ad2-fdefa3df79de",
  "path": "/tmp/dummy-tui",
  "displayName": "dummy-tui"
}
```

OpenCode must be able to start the configured `build` agent. The restart
scenario only waits for the new terminal; it does not require a report to
complete the run.

## Step 1 — prepare the disposable Beads workspace

The dummy repository initially contains only the TUI demo. Initialize Beads
once in that repository. The `--stealth` option keeps Beads files out of the
repository's tracked changes.

```sh
cd /tmp/dummy-tui
rm -rf .beads
bd init \
  --prefix demo \
  --non-interactive \
  --skip-agents \
  --skip-hooks \
  --stealth
bd list --ready --limit 1 --no-parent --json
```

Expected initial list:

```json
[]
```

relay-flow does not initialize Beads or manage Dolt. This step is performed by
the operator before repository registration.

## Step 2 — build the checkout's relay-flow binary

Do not use the globally installed binary. Build an isolated binary into the
E2E state directory:

```sh
cd /home/raj/orca/workspaces/relay-flow/question-fix
rm -rf /tmp/dummy-tui-e2e-state
mkdir -p /tmp/dummy-tui-e2e-state

go build -buildvcs=false \
  -o /tmp/dummy-tui-e2e-state/relay-flow \
  ./cmd/relay-flow
chmod 755 /tmp/dummy-tui-e2e-state/relay-flow
```

`-buildvcs=false` avoids the Go VCS-stamping failure observed in this checkout:

```text
error obtaining VCS status: exit status 128
```

Set the isolated relay-flow home for every subsequent command:

```sh
export RELAY_FLOW_HOME=/tmp/dummy-tui-e2e-state/home
RF=/tmp/dummy-tui-e2e-state/relay-flow
```

## Step 3 — initialize and start relay-flow

```sh
rm -rf "$RELAY_FLOW_HOME"
"$RF" init \
  --task-plugin beads \
  --runner-plugin orca \
  --harness-plugin opencode
"$RF" task auth
"$RF" serve --background --debug
```

Observed output:

```text
Task system: beads
Runner: orca
Harness: opencode
Relay-flow initialized
Relay-flow server started
```

For Beads, `task auth` is a no-op. It does not create relay-flow
credentials. The server must be running before `repo register` and
`workflow submit`.

## Step 4 — register `/tmp/dummy-tui`

```sh
"$RF" repo register \
  --name dummy-tui \
  --path /tmp/dummy-tui \
  --set beadsDir=/tmp/dummy-tui/.beads
```

Observed output:

```text
dummy-tui
```

Registration validates the existing Orca repository, probes Beads, and configures
OpenCode. The current harness setup writes/updates:

```text
/tmp/dummy-tui/opencode.json       server plugin entry
/tmp/dummy-tui/.opencode/tui.json  native TUI plugin entry
```

The original demo TUI config was restored after this verification. On a fresh
replication, it is safe for `repo register` to add the relay-flow entries.

## Step 5 — submit the initial workflow and create the parent

Create `/tmp/dummy-tui-e2e-state/workflow.yaml`:

```yaml
name: restartDemo
repos: [dummy-tui]
taskConfig:
  filters:
    parentStatuses: [open]
    issueTypes: [epic]
    labels: [restart-e2e]
nodes:
  start:
    onSuccess:
      - target: implement
  implement:
    type: agent
    agent: build
    description: |
      This is the relay-flow restart E2E node. Do not make code changes.
      Return the complete report contract when asked.
    onSuccess:
      - target: end
    onFailure:
      - target: implement
  end: {}
```

Submit it and create a Beads parent:

```sh
"$RF" workflow submit --file /tmp/dummy-tui-e2e-state/workflow.yaml

cd /tmp/dummy-tui
PARENT=$(bd create \
  'Relay-flow restart E2E parent' \
  --type epic \
  --description 'Disposable parent for explicit restart E2E.' \
  --labels restart-e2e \
  --silent)
printf 'PARENT=%s\n' "$PARENT" > /tmp/dummy-tui-e2e-state/ids
bd show "$PARENT" --json
```

In the recorded run:

```text
PARENT=demo-jmm
```

Wait until the first run is created and reaches the work node:

```sh
for i in $(seq 1 40); do
  out=$("$RF" run get --ticket "$PARENT" 2>/dev/null || true)
  printf '%s\n' "$out"
  if printf '%s' "$out" | grep -q '"currentNode": "implement"'; then
    break
  fi
  sleep 2
done
```

Observed first attempt:

```json
{
  "id": "dummy-tui/restartDemo/demo-jmm",
  "logicalRunId": "dummy-tui/restartDemo/demo-jmm",
  "attemptId": 1,
  "state": "waiting",
  "currentNode": "implement",
  "currentNodeVisitId": "421ee1bc8891651bd827a13957970ede"
}
```

The Beads parent was observed as:

```json
{
  "id": "demo-jmm",
  "status": "in_progress",
  "labels": ["restart-e2e", "wf:restartDemo"]
}
```

The first attempt also created the stable mailbox:

```text
demo-jmm:implement
```

and the Orca terminal with the same stable title.

## Step 6 — cancel the first attempt

```sh
"$RF" run cancel \
  --ticket "$PARENT" \
  --reason 'E2E explicit restart test'
```

Poll until the state is `canceled`:

```sh
for i in $(seq 1 40); do
  out=$("$RF" run get --ticket "$PARENT" 2>/dev/null || true)
  state=$(printf '%s' "$out" | grep -o '"state": "[^"]*"' | head -1 | cut -d'"' -f4 || true)
  printf 'state=%s\n' "$state"
  [ "$state" = canceled ] && break
  sleep 2
done
"$RF" run get --ticket "$PARENT"
```

Observed:

```json
{
  "id": "dummy-tui/restartDemo/demo-jmm",
  "logicalRunId": "dummy-tui/restartDemo/demo-jmm",
  "attemptId": 1,
  "state": "canceled",
  "currentNode": "implement",
  "currentNodeVisitId": "421ee1bc8891651bd827a13957970ede"
}
```

Verify that cancellation did not remove human/task-system history:

```sh
cd /tmp/dummy-tui
bd show "$PARENT" --json
```

Observed parent properties:

```text
status:       in_progress
labels:       restart-e2e, wf:restartDemo
comment_count: 1
```

The parent remained `in_progress`; relay-flow did not roll it back or change it
to a completed state. The cancellation comment carried the stable logical
marker:

```text
dummy-tui/restartDemo/demo-jmm:cancellation
```

During the recorded run, the old OpenCode session later delivered a report for
the canceled attempt. The server log proved it was harmless:

```text
msg="report duplicate ack"
runID=dummy-tui/restartDemo/demo-jmm
state=canceled
```

No graph transition or mailbox effect occurred.

## Step 7 — update and resubmit the workflow

Replace the `implement.description` in
`/tmp/dummy-tui-e2e-state/workflow.yaml` with:

```yaml
    description: |
      UPDATED workflow definition for the explicit restart E2E.
      Do not make code changes. Return the complete report contract when asked.
```

Submit the changed workflow:

```sh
"$RF" workflow submit --file /tmp/dummy-tui-e2e-state/workflow.yaml
"$RF" run get --ticket "$PARENT"
"$RF" workflow get --name restartDemo
```

Expected behavior:

- workflow submission succeeds because the old run is terminal/canceled;
- `run get` still reports the old attempt as `canceled`;
- no new run is created by workflow replacement;
- polling does not automatically restart the canceled ticket.

The stored workflow output contained:

```text
UPDATED workflow definition for the explicit restart E2E.
```

## Step 8 — explicitly restart the ticket

```sh
"$RF" run restart --ticket "$PARENT" \
  | tee /tmp/dummy-tui-e2e-state/restart-response.json
```

Observed response:

```json
{
  "id": "dummy-tui/restartDemo/demo-jmm~attempt~2",
  "logicalRunId": "dummy-tui/restartDemo/demo-jmm",
  "attemptId": 2,
  "state": "starting"
}
```

The new ID is numeric-attempt based. No UUID is present in the attempt
identity.

Immediately querying the ticket returned the same new attempt, proving the
restart command is idempotent and `run get` selects the newest attempt:

```sh
"$RF" run get --ticket "$PARENT"
```

## Step 9 — verify the new attempt from `start`

Wait for the new attempt to reach `implement`:

```sh
for i in $(seq 1 50); do
  out=$("$RF" run get --ticket "$PARENT" 2>/dev/null || true)
  state=$(printf '%s' "$out" | grep -o '"state": "[^"]*"' | head -1 | cut -d'"' -f4 || true)
  node=$(printf '%s' "$out" | grep -o '"currentNode": "[^"]*"' | head -1 | cut -d'"' -f4 || true)
  attempt=$(printf '%s' "$out" | grep -o '"attemptId": [0-9]*' | head -1 | cut -d: -f2 | tr -d ' ' || true)
  printf 'state=%s node=%s attempt=%s\n' "$state" "$node" "$attempt"
  if [ "$attempt" = 2 ] && [ "$node" = implement ] &&
     { [ "$state" = waiting ] || [ "$state" = running ]; }; then
    printf '%s\n' "$out" > /tmp/dummy-tui-e2e-state/restarted-run.json
    break
  fi
  sleep 3
done
cat /tmp/dummy-tui-e2e-state/restarted-run.json
```

Observed new attempt:

```json
{
  "id": "dummy-tui/restartDemo/demo-jmm~attempt~2",
  "logicalRunId": "dummy-tui/restartDemo/demo-jmm",
  "attemptId": 2,
  "state": "waiting",
  "currentNode": "implement",
  "currentNodeVisitId": "21ab3dbefb25a06d23b01184276828d1"
}
```

The node visit ID changed from the first attempt. The node did not resume the
old visit or old OpenCode session.

Verify the reused Beads mailbox:

```sh
cd /tmp/dummy-tui
bd list --parent "$PARENT" --all --no-pager --json
```

Observed mailbox proof:

```text
one child:       demo-jmm.1
stable title:    demo-jmm:implement
status:          in_progress
workflow label:  wf:restartDemo
comment history: preserved
```

The mailbox description contained the updated text:

```text
UPDATED workflow definition for the explicit restart E2E.
```

This proves the new attempt used the latest workflow snapshot while reusing the
existing mailbox rather than creating a duplicate.

## Step 10 — verify runner cleanup and fresh harness launch

Search the isolated server log:

```sh
rg -n \
  'attempt~2|close-terminals|ensure-environment|node entered|harness call|ensure-terminal' \
  "$RELAY_FLOW_HOME/server.log"
```

The recorded log showed:

```text
run created ... runID=dummy-tui/restartDemo/demo-jmm~attempt~2
close-terminals ... ticket=demo-jmm ...
close-terminal title=demo-jmm:implement ... result=ok
close-terminals ticket=demo-jmm result=ok closed=1
ensure-environment ticket=demo-jmm ... result=exists
node entered ... runID=...~attempt~2 node=implement nodeVisitID=21ab...
harness call ... session="" mode=fresh ... runID=...~attempt~2
ensure-terminal title=demo-jmm:implement ... result=created
```

This confirms:

- the prior terminal was closed before the new node terminal started;
- the ticket-scoped Orca environment/worktree was reused;
- the fresh attempt had no persisted session ID;
- OpenCode was launched with a fresh session;
- the terminal title remained the stable `demo-jmm:implement`.

## Step 11 — cleanup after verification

The new attempt was canceled so the E2E did not leave an active model session:

```sh
"$RF" run cancel \
  --ticket "$PARENT" \
  --reason 'E2E cleanup after restart verification'
"$RF" stop
```

The second cancellation reused the existing logical cancellation marker rather
than adding a duplicate marker comment. The relay-flow socket was removed and
the server process exited. Verify no live run-owned terminal remains:

```sh
orca-ide terminal list \
  --worktree path:/tmp/dummy-tui \
  --include-visual-layouts \
  --json
```

The recorded response contained no `demo-jmm` terminal.

The following evidence was retained for inspection:

```text
/tmp/dummy-tui-e2e-state/config.yaml
/tmp/dummy-tui-e2e-state/ids
/tmp/dummy-tui-e2e-state/plugin.log
/tmp/dummy-tui-e2e-state/restart-response.json
/tmp/dummy-tui-e2e-state/restarted-run.json
/tmp/dummy-tui-e2e-state/server.log
/tmp/dummy-tui-e2e-state/state.db
/tmp/dummy-tui-e2e-state/workflow.yaml
```

The original `/tmp/dummy-tui/.opencode/tui.json` demo configuration was restored
and the generated root `opencode.json` was removed after the run. The Beads
workspace remains at `/tmp/dummy-tui/.beads` if the parent and mailbox history
need to be inspected.

## Result

The E2E passed all restart assertions:

- canceled run stayed canceled after workflow replacement;
- workflow replacement succeeded without creating a run;
- explicit restart created numeric attempt `2`;
- `run get` returned the new attempt;
- new attempt used the updated workflow;
- new attempt started from `start` and reached `implement`;
- node visit ID was fresh;
- old report was acknowledged as a duplicate;
- mailbox and worktree were preserved;
- old terminal was closed;
- new OpenCode session was fresh; and
- no duplicate active attempt was created.

This E2E used Beads because `/tmp/dummy-tui` has no Jira project. Jira restart
mailbox preparation and human-status conflict behavior are covered by the
Jira adapter tests; the core restart path is task-system independent.
