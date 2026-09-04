# Herdr runner end-to-end test

Manual, reproducible end-to-end verification of the `herdr` runner plugin against
**real** components: the installed `herdr` binary, a real Jira project, a real
OpenCode agent, and a real Git repository. Nothing in this procedure uses a fake
CLI, a stub task system, or a test-only constructor.

Automated tests (`go test ./...`) cover the wrapper and adapter in isolation, and
`RELAY_FLOW_HERDR_LIVE=1 go test ./internal/runner/herdr/herdrcli/ -run Live`
covers the wrapper against the installed binary. This document covers the one
thing neither can: a complete ticket → worktree → pane → agent → report → handoff
→ cleanup cycle.

## What this proves

| Claim | Verified by |
| --- | --- |
| Repo registration provisions nothing in Herdr | Step 6 snapshot shows `workspaces: 0` |
| Each ticket gets its own Git worktree workspace | Step 9 snapshot shows a second `is_linked_worktree: true` workspace |
| Base-ref ladder falls back to local `main` with no `origin` | Step 3 creates a remote-less repo; `ensure-environment result=created` |
| One labelled pane per node, title `<ticket>:<node>` | Step 9/10 pane labels |
| Pane liveness uses the foreground process, not the shell | Step 9 `process-info` shows `opencode` ≠ `shell_pid` |
| Env reaches the agent through the `pane run` command line | The agent registers its session and delivers reports |
| Handoff writes feedback only to the next mailbox | Step 11 mailbox comments |
| Cleanup closes panes and the workspace | Step 12 `workspaces` / `panes` |
| **Cleanup never removes the worktree, branch, or commits** | Step 12 `git log` in the preserved checkout |
| Cleanup is idempotent | `close-terminals … closed=0 result=ok` after `cleanup-run` |

## Safety rules

- **Never use the default Herdr session.** Create a disposable named session and
  delete it during teardown.
- **Never use the operator's `~/.relay-flow`.** Point `RELAY_FLOW_HOME` at a
  temporary directory.
- **Do not `go install`.** Build the binary into the temporary tree and always
  invoke it by absolute path. Do not add it to `PATH`.
- **Use a dedicated Jira component**, not an existing one, so the poller can only
  ever see tickets this test created.
- **Confirm no other relay-flow server is running** against the same Jira
  project/component, or two servers will compete for the same tickets.
- **Use a unique root directory** (`mktemp -d`) so parallel agents or sessions on
  the same machine cannot collide on a shared path.
- Teardown deletes the Jira issues and component this test created. Do not point
  it at anything you did not create.

## Prerequisites

```bash
herdr --version          # 0.8.2 or newer
opencode --version       # 1.18.25 or newer, already authenticated
go version               # toolchain from go.mod
```

Jira credentials in `~/.relay-flow/credentials.yaml` (`site`, `email`, `token`)
with permission to create a component and a Story in the target project.

---

## Step 1 — Set the environment

```bash
export E2E_ROOT=$(mktemp -d /tmp/relay-e2e-XXXXXX)
export RELAY_FLOW_HOME=$E2E_ROOT/home
export RF=$E2E_ROOT/bin/relay-flow
export E2E_SESSION=relay-e2e-$(basename $E2E_ROOT)
export E2E_REPO=$E2E_ROOT/repos/herdr-e2e
export JIRA_PROJECT=GHCOS        # a project you own
export JIRA_COMPONENT=herdr-e2e  # must NOT already exist

SITE=$(grep '^site:'  ~/.relay-flow/credentials.yaml | awk '{print $2}' | tr -d '"')
EMAIL=$(grep '^email:' ~/.relay-flow/credentials.yaml | awk '{print $2}' | tr -d '"')
TOKEN=$(grep '^token:' ~/.relay-flow/credentials.yaml | awk '{print $2}' | tr -d '"')
```

Confirm nothing else will compete for the tickets:

```bash
pgrep -af 'relay-flow serve' || echo 'no relay-flow server running'
```

```text
no relay-flow server running
```

## Step 2 — Build the binary into the temporary tree

`-buildvcs=false` is required because the build runs from an Orca worktree.

```bash
mkdir -p $E2E_ROOT/bin $RELAY_FLOW_HOME
go build -buildvcs=false -o $RF ./cmd/relay-flow
$RF 2>&1 | head -2
```

```text
relay-flow — durable ticket runner

```

## Step 3 — Create the test repository

Deliberately **no `origin` remote**: this forces the base-ref ladder to its last
step (local `main`), which is the case the ladder exists for.

```bash
mkdir -p $E2E_REPO && cd $E2E_REPO
git init -q -b main
git config user.email "e2e@relay-flow.local"
git config user.name  "Relay Flow E2E"

cat > opencode.json <<'JSON'
{
  "$schema": "https://opencode.ai/config.json",
  "plugin": ["relay-flow-plugin@0.2.1-alpha"]
}
JSON

cat > README.md <<'MD'
# herdr-e2e

Scratch repository for the relay-flow + Herdr end-to-end smoke test.
MD

cat > AGENTS.md <<'MD'
# herdr-e2e

Tiny scratch repo. Keep changes minimal and commit them.
MD

git add -A && git commit -qm "init e2e repo"
git log --oneline
```

```text
3fbe765 init e2e repo
```

The plugin pin must match `relayFlowPlugin` in
`internal/harness/opencode/repo_setup.go`; if it does, the harness leaves
`opencode.json` untouched instead of rewriting it.

## Step 4 — Initialize relay-flow with the Herdr runner

```bash
$RF init --task-plugin jira --runner-plugin herdr --harness-plugin opencode
cp ~/.relay-flow/credentials.yaml $RELAY_FLOW_HOME/credentials.yaml
chmod 600 $RELAY_FLOW_HOME/credentials.yaml
```

```text
Task system: jira
Runner: herdr
Harness: opencode
Relay-flow initialized
```

Point the runner at the disposable session by adding `runnerConfig` to
`$RELAY_FLOW_HOME/config.yaml`:

```bash
python3 - <<PY
import os
p = os.environ["RELAY_FLOW_HOME"] + "/config.yaml"
s = open(p).read().replace(
    "runnerPlugin: herdr\n",
    "runnerPlugin: herdr\nrunnerConfig:\n    session: %s\n" % os.environ["E2E_SESSION"])
open(p, "w").write(s)
PY
grep -A2 '^runnerPlugin' $RELAY_FLOW_HOME/config.yaml
```

```text
runnerPlugin: herdr
runnerConfig:
    session: relay-e2e-XXXXXX
```

## Step 5 — Start the disposable Herdr session

```bash
nohup herdr --session $E2E_SESSION server > $E2E_ROOT/herdr-server.log 2>&1 &
sleep 3
herdr --session $E2E_SESSION status server
```

```text
status: running
version: 0.8.2
protocol: 20
```

The session must start empty:

```bash
herdr --session $E2E_SESSION api snapshot
```

```json
{"id":"cli:api:snapshot","result":{"snapshot":{"agents":[],"layouts":[],"panes":[],"protocol":20,"tabs":[],"version":"0.8.2","workspaces":[]},"type":"session_snapshot"}}
```

## Step 6 — Start the server and register the repository

```bash
$RF serve --background --debug
$RF repo register --name herdr-e2e --path $E2E_REPO --set project=$JIRA_PROJECT
$RF repo get --name herdr-e2e
```

```text
Relay-flow server started
herdr-e2e
{
  "name": "herdr-e2e",
  "path": "/tmp/relay-e2e-XXXXXX/repos/herdr-e2e",
  "taskConfig": {
    "component": "herdr-e2e",
    "project": "GHCOS"
  }
}
```

**Key assertion — registration creates nothing in Herdr:**

```bash
herdr --session $E2E_SESSION api snapshot | python3 -c \
 'import json,sys; d=json.load(sys.stdin)["result"]["snapshot"]; print("workspaces:",len(d["workspaces"]),"panes:",len(d["panes"]))'
```

```text
workspaces: 0 panes: 0
```

`ValidateRepo` used `worktree list --cwd`, which works with nothing open. If this
prints anything other than `0 0`, the adapter is provisioning during
registration, which is a defect.

## Step 7 — Create the Jira component

```bash
curl -s -u "$EMAIL:$TOKEN" -X POST -H 'Content-Type: application/json' \
  "$SITE/rest/api/3/component" \
  -d "{\"name\":\"$JIRA_COMPONENT\",\"description\":\"Temporary component for relay-flow Herdr runner e2e\",\"project\":\"$JIRA_PROJECT\",\"assigneeType\":\"PROJECT_DEFAULT\"}"
```

```json
{"self":"…/rest/api/3/component/10648","id":"10648","name":"herdr-e2e", … }
```

Record the component id for teardown:

```bash
export E2E_COMPONENT_ID=10648
```

## Step 8 — Submit the workflow

Two agent nodes, so the run must perform a real handoff. Adjust status names to
your project's Story workflow.

```bash
cat > $E2E_ROOT/herdrE2eFlow.yaml <<'YAML'
name: herdrE2eFlow
repos:
  - herdr-e2e
cleanupRunnerOnEnd: true

taskConfig:
  filters:
    parentStatuses:
      - To Do
    issueTypes:
      - Story
    assignees:
      - you@example.com

nodes:
  start:
    taskConfig:
      transitionTo:
        parentStatus: In Progress
    onSuccess:
      - target: implement
        when: The parent story is ready for implementation

  implement:
    type: agent
    agent: build
    description: |
      Do exactly what the parent ticket asks and nothing more.
      Commit the change with a short message. Do not create a branch, do not push.
    taskConfig:
      transitionTo:
        taskStatus: In Progress
    onSuccess:
      - target: verify
        when: The change is made and committed
    onFailure:
      - target: implement
        when: The change could not be completed and needs another pass

  verify:
    type: agent
    agent: plan
    description: |
      Verify the previous node's work: confirm the requested file exists with the
      requested content and that it is committed. Report what you verified.
      Do not modify files.
    taskConfig:
      transitionTo:
        taskStatus: In Review
    onSuccess:
      - target: end
        when: The change is present, correct, and committed
    onFailure:
      - target: implement
        when: The change is missing or incorrect

  end:
    taskConfig:
      transitionTo:
        parentStatus: Done
YAML

$RF workflow submit --file $E2E_ROOT/herdrE2eFlow.yaml
```

```text
herdrE2eFlow
```

`filters.assignees` must be your own Jira account so the poller cannot claim a
ticket somebody else files against the component.

## Step 9 — Create the driving ticket and watch the run start

```bash
ACC=$(curl -s -u "$EMAIL:$TOKEN" "$SITE/rest/api/3/myself" | python3 -c 'import json,sys;print(json.load(sys.stdin)["accountId"])')

cat > $E2E_ROOT/issue.json <<JSON
{"fields":{
 "project":{"key":"$JIRA_PROJECT"},
 "issuetype":{"name":"Story"},
 "components":[{"name":"$JIRA_COMPONENT"}],
 "assignee":{"id":"$ACC"},
 "summary":"[e2e] Herdr runner smoke: add hello.txt",
 "description":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"Automated end-to-end smoke test for the relay-flow Herdr runner. Task: create a file named hello.txt at the repository root containing exactly the single line 'hello from herdr' and commit it. Do not change anything else. Do not create a branch and do not push."}]}]}
}}
JSON

curl -s -u "$EMAIL:$TOKEN" -X POST -H 'Content-Type: application/json' \
  "$SITE/rest/api/3/issue" -d @$E2E_ROOT/issue.json
```

```json
{"id":"160248","key":"GHCOS-41415","self":"…/rest/api/3/issue/160248"}
```

```bash
export TICKET=GHCOS-41415
```

Poll until the run appears (default poll interval is 15s; expect ~20s):

```bash
$RF run list
```

```text
herdr-e2e/herdrE2eFlow/GHCOS-41415	GHCOS-41415	herdrE2eFlow	starting
```

Within ~15s more, the ticket worktree and first pane exist:

```bash
$RF run get --ticket $TICKET
```

```json
{
  "id": "herdr-e2e/herdrE2eFlow/GHCOS-41415",
  "repo": "herdr-e2e",
  "workflow": "herdrE2eFlow",
  "ticket": { "id": "160248", "key": "GHCOS-41415", "title": "" },
  "state": "waiting",
  "currentNode": "implement",
  "currentNodeVisitId": "1792ce6859b7fa273811d5c45712b0d8",
  "startedAt": "...",
  "updatedAt": "..."
}
```

```bash
herdr --session $E2E_SESSION api snapshot | python3 -c '
import json,sys
d=json.load(sys.stdin)["result"]["snapshot"]
for w in d["workspaces"]:
    wt=w.get("worktree") or {}
    print("ws",w["workspace_id"],w.get("label"),"|",wt.get("checkout_path"),"| linked:",wt.get("is_linked_worktree"))
for t in d["tabs"]:  print("tab",t["tab_id"],repr(t.get("label")),t.get("workspace_id"))
for p in d["panes"]: print("pane",p["pane_id"],repr(p.get("label")),p.get("cwd"))'
```

```text
ws w1 herdr-e2e | /tmp/relay-e2e-XXXXXX/repos/herdr-e2e | linked: False
ws w2 GHCOS-41415 | /home/raj/.herdr/worktrees/herdr-e2e/ghcos-41415 | linked: True
tab w1:t1 '1' w1
tab w2:t1 '1' w2
tab w2:t2 'GHCOS-41415:implement' w2
pane w1:p1 None /tmp/relay-e2e-XXXXXX/repos/herdr-e2e
pane w2:p1 None /home/raj/.herdr/worktrees/herdr-e2e/ghcos-41415
pane w2:p2 'GHCOS-41415:implement' /home/raj/.herdr/worktrees/herdr-e2e/ghcos-41415
```

`w1` is the repository's own source workspace, which Herdr opens as a side effect
of `worktree create`. `w2` is the ticket workspace. Only `w2:p2` carries a
relay-flow label; `w2:p1` is the workspace's untouched root pane.

```bash
herdr --session $E2E_SESSION worktree list --cwd $E2E_REPO | python3 -c '
import json,sys
for w in json.load(sys.stdin)["result"]["worktrees"]:
    print(w["branch"], w["path"], "open_ws:", w.get("open_workspace_id"))'
```

```text
main /tmp/relay-e2e-XXXXXX/repos/herdr-e2e open_ws: w1
GHCOS-41415 /home/raj/.herdr/worktrees/herdr-e2e/ghcos-41415 open_ws: w2
```

The branch is the ticket key and the checkout is a linked worktree — Orca parity.

Confirm the pane is running the agent, not a shell:

```bash
herdr --session $E2E_SESSION pane process-info --pane w2:p2
```

```json
{"result":{"process_info":{
  "foreground_process_group_id": 491288,
  "foreground_processes": [{
    "argv": ["opencode","--agent","build","--prompt","Task system: jira\nUse the jira tools to read the parent ticket GHCOS-41415.\n\nYour mailbox is GHCOS-41416. …"],
    "cwd": "/home/raj/.herdr/worktrees/herdr-e2e/ghcos-41415",
    "name": "opencode", "pid": 491288 }],
  "pane_id": "w2:p2", "shell_pid": 491086 }}}
```

`pid` ≠ `shell_pid` is what `FindTerminal` uses to call the pane usable. If the
foreground process were the shell, the adapter would treat the pane as absent.

Mailboxes exist for both agent nodes:

```bash
curl -s -u "$EMAIL:$TOKEN" "$SITE/rest/api/3/issue/$TICKET?fields=status,subtasks,labels" | python3 -c '
import json,sys
d=json.load(sys.stdin)["fields"]
print("parent:",d["status"]["name"],"labels:",d["labels"])
for s in d["subtasks"]: print(" ",s["key"],s["fields"]["summary"],"->",s["fields"]["status"]["name"])'
```

```text
parent: In Progress labels: ['wf:herdrE2eFlow']
  GHCOS-41416 GHCOS-41415:implement -> In Progress
  GHCOS-41417 GHCOS-41415:verify -> To Do
```

## Step 10 — Monitor the handoff to completion

```bash
for i in $(seq 1 20); do
  sleep 20
  N=$($RF run get --ticket $TICKET | python3 -c 'import json,sys;d=json.load(sys.stdin);print(d["state"],d.get("currentNode"))')
  P=$(herdr --session $E2E_SESSION api snapshot | python3 -c \
      'import json,sys;print(",".join(str(p.get("label")) for p in json.load(sys.stdin)["result"]["snapshot"]["panes"] if p.get("label")))')
  echo "t+$((i*20))s run=[$N] panes=[$P]"
  case "$N" in *completed*|*failed*|*cancel*) echo TERMINAL; break;; esac
done
```

```text
t+20s  run=[waiting implement]  panes=[GHCOS-41415:implement]
t+40s  run=[running implement]  panes=[GHCOS-41415:implement]
t+60s  run=[waiting implement]  panes=[GHCOS-41415:implement]
…
t+140s run=[waiting verify]     panes=[GHCOS-41415:implement,GHCOS-41415:verify]
…
t+220s run=[completed verify]   panes=[]
TERMINAL
```

The three transitions that matter:

1. A **second labelled pane appears** (`…:verify`) while the first is still open —
   one pane per node, both in the same ticket workspace.
2. `panes=[]` at the end — `cleanupRunnerOnEnd: true` closed every ticket pane.
3. `completed` — the graph reached `end`.

Typical wall time is 3–5 minutes and depends entirely on agent latency. If a node
loops back to `implement` the agent judged its own work incomplete; that is valid
workflow behavior, not a runner failure.

## Step 11 — Verify the task-system result

```bash
curl -s -u "$EMAIL:$TOKEN" "$SITE/rest/api/3/issue/$TICKET?fields=status,subtasks,labels" | python3 -c '
import json,sys
d=json.load(sys.stdin)["fields"]
print("parent:",d["status"]["name"],"labels:",d["labels"])
for s in d["subtasks"]: print(" ",s["key"],s["fields"]["summary"],"->",s["fields"]["status"]["name"])'
```

```text
parent: Done labels: ['wf:herdrE2eFlow']
  GHCOS-41416 GHCOS-41415:implement -> Done
  GHCOS-41417 GHCOS-41415:verify -> Done
```

The `verify` mailbox must hold exactly two comments: inbound feedback from
`implement`, then its own summary.

```bash
curl -s -u "$EMAIL:$TOKEN" "$SITE/rest/api/3/issue/GHCOS-41417/comment" | python3 -c '
import json,sys
def flat(n):
    if isinstance(n,dict):
        if n.get("type")=="text": return n.get("text","")
        return "".join(flat(c) for c in n.get("content",[]))
    return ""
for c in json.load(sys.stdin)["comments"]:
    print("---"); print(flat(c["body"])[:600])'
```

```text
---
Feedback from implement to verify mailbox GHCOS-41417
COMMITS: d22db8a
REASON FOR NEXT STEP: Change is implemented and committed.
REQUIRED ACTIONS: Verify the committed file.
RELEVANT CONTEXT: Commit d22db8a.
EXPECTED RESULT: Verification confirms the requested change.
<!-- f75f9e13c824c209b189e818fc207131:feedback -->
---
Summary for verify
COMPLETED: Verified root hello.txt contains exactly one line: hello from herdr
COMMITS: d22db8a
NOT COMPLETED: None
ISSUES DISCOVERED: None
VERIFICATION: Commit d22db8a adds only hello.txt with the requested content; worktree is clean
NOTES: None
<!-- 7cfd94b1ffc19364d3090324d74bda21:summary -->
```

Feedback appears **only** on the selected next mailbox. If feedback shows up on a
node that was not selected, routing is broken.

## Step 12 — Verify cleanup preserved the worktree

This is the intentional divergence from the Orca runner and the most important
assertion in this document.

```bash
herdr --session $E2E_SESSION api snapshot | python3 -c '
import json,sys
d=json.load(sys.stdin)["result"]["snapshot"]
print("workspaces:",[(w["workspace_id"],w.get("label")) for w in d["workspaces"]])
print("panes:",[(p["pane_id"],p.get("label")) for p in d["panes"]])'
```

```text
workspaces: [('w1', 'herdr-e2e')]
panes: [('w1:p1', None)]
```

The ticket workspace `w2` is gone; the source workspace is untouched.

```bash
herdr --session $E2E_SESSION worktree list --cwd $E2E_REPO | python3 -c '
import json,sys
for w in json.load(sys.stdin)["result"]["worktrees"]:
    print(w["branch"], w["path"], "open_ws:", w.get("open_workspace_id"))'
```

```text
main /tmp/relay-e2e-XXXXXX/repos/herdr-e2e open_ws: w1
GHCOS-41415 /home/raj/.herdr/worktrees/herdr-e2e/ghcos-41415 open_ws: None
```

The checkout still exists with `open_workspace_id: None` — closed, not removed.

```bash
cd ~/.herdr/worktrees/herdr-e2e/ghcos-41415
git log --oneline -3
git status --short
cat hello.txt
```

```text
d22db8a Add hello file
3fbe765 init e2e repo
hello from herdr
```

The source checkout never saw the change:

```bash
cd $E2E_REPO && git log --oneline -3 && ls
```

```text
3fbe765 init e2e repo
AGENTS.md  opencode.json  README.md
```

## Step 13 — Verify the adapter log

```bash
grep -E 'op=(validate-repo|ensure-environment|find-terminal|create-terminal|ensure-terminal|send-terminal|close-terminal|close-terminals|cleanup-run|set-environment-status)' \
  $RELAY_FLOW_HOME/server.log | grep outcome | sed 's/time=[^ ]* //'
```

```text
level=INFO msg="herdr outcome" op=validate-repo repo=herdr-e2e result=ok
level=INFO msg="herdr outcome" op=ensure-environment ticket=GHCOS-41415 … result=created
level=INFO msg="herdr outcome" op=ensure-environment ticket=GHCOS-41415 … result=exists
level=INFO msg="herdr outcome" op=find-terminal title=GHCOS-41415:implement handle="" result=absent
level=INFO msg="herdr outcome" op=create-terminal envID=w2 title=GHCOS-41415:implement result=ok
level=INFO msg="herdr outcome" op=ensure-terminal envID=w2 title=GHCOS-41415:implement result=created
level=INFO msg="herdr outcome" op=find-terminal title=GHCOS-41415:implement handle=w2:p2 result=found
level=INFO msg="herdr outcome" op=send-terminal title=GHCOS-41415:implement handle=w2:p2 result=ok
level=INFO msg="herdr outcome" op=set-environment-status result=ok
level=INFO msg="herdr outcome" op=close-terminal title=GHCOS-41415:implement handle=w2:p2 result=ok
level=INFO msg="herdr outcome" op=close-terminal title=GHCOS-41415:verify handle=w2:p3 result=ok
level=INFO msg="herdr outcome" op=cleanup-run ticket=GHCOS-41415 … result=ok
level=INFO msg="herdr outcome" op=close-terminals ticket=GHCOS-41415 … closed=0 result=ok
```

What to check:

- **Exactly one `result=created`** for `ensure-environment`; every later call is
  `result=exists`. More than one `created` means the adapter is re-creating a
  worktree it should have reused.
- The first `find-terminal` is `result=absent` with `handle=""`; later ones are
  `result=found` with a stable `w2:pN`. A pane ID that changes mid-node means the
  adapter is churning panes.
- `close-terminals … closed=0 result=ok` **after** `cleanup-run` proves cleanup is
  idempotent rather than erroring on already-closed panes.
- `set-environment-status result=ok` confirms the documented no-op.

There must be no errors or warnings:

```bash
grep -icE 'level=(ERROR|WARN)' $RELAY_FLOW_HOME/server.log
```

```text
0
```

## Step 14 — Teardown

Run every step. Nothing here should be skipped, and nothing here touches the
default Herdr session or `~/.relay-flow`.

```bash
# 1. Jira: delete the issue tree, then the component
curl -s -o /dev/null -w 'parent delete: %{http_code}\n' -u "$EMAIL:$TOKEN" \
  -X DELETE "$SITE/rest/api/3/issue/$TICKET?deleteSubtasks=true"
curl -s -o /dev/null -w 'component delete: %{http_code}\n' -u "$EMAIL:$TOKEN" \
  -X DELETE "$SITE/rest/api/3/component/$E2E_COMPONENT_ID"

# 2. relay-flow server
$RF stop

# 3. Herdr session
herdr --session $E2E_SESSION server stop
sleep 2
herdr session delete $E2E_SESSION

# 4. The preserved worktree (cleanup deliberately left it behind)
rm -rf ~/.herdr/worktrees/herdr-e2e
rmdir  ~/.herdr/worktrees 2>/dev/null

# 5. The temporary tree, including the binary
rm -rf $E2E_ROOT
```

```text
parent delete: 204
component delete: 204
stopped
deleted session relay-e2e-XXXXXX
```

Verify nothing survived:

```bash
herdr session list
pgrep -af "relay-flow serve|$E2E_SESSION" || echo 'no stray processes'
ls -d $E2E_ROOT 2>/dev/null || echo 'temp tree removed'
herdr api snapshot | python3 -c \
 'import json,sys;d=json.load(sys.stdin)["result"]["snapshot"];print("default workspaces:",[(w["workspace_id"],w.get("label")) for w in d["workspaces"]])'
```

```text
name       status   directory                socket
default    running  /home/raj/.config/herdr  /home/raj/.config/herdr/herdr.sock
no stray processes
temp tree removed
default workspaces: [('w1', 'herdr')]
```

The default session must show only the workspaces it had before the test.

## Troubleshooting

| Symptom | Cause | Action |
| --- | --- | --- |
| `repo register` fails with `not a git worktree` | `--path` is not a repository root | Register the root, not a subdirectory |
| Snapshot shows workspaces right after registration | Adapter is provisioning during validation | Defect — `ValidateRepo` must create nothing |
| Run never leaves `starting` | JQL matches nothing | Check component name, issue type, status, and `filters.assignees` |
| `ensure-environment` fails with an unresolvable base | Repo has no `origin` and no local `main`/`master` | Create a `main` branch with at least one commit |
| Pane exists but the run stalls in `waiting` | Agent never delivered a report | Attach with `herdr session attach $E2E_SESSION` and read the pane |
| Two runs fight over one ticket | A second relay-flow server polls the same project/component | Stop the other server; assignee isolation is per machine |
| `worktree_create_failed` | The checkout path already exists | `rm -rf ~/.herdr/worktrees/<repo>/<slug>` and rerun |
| Panes remain after `completed` | `cleanupRunnerOnEnd` is not set | Set it in the workflow |
| Worktree missing after cleanup | Adapter removed a checkout | Defect — cleanup must never remove a worktree |

## Watching it live

```bash
herdr session attach $E2E_SESSION
```

Each node appears as its own tab titled `<ticket>:<node>` inside the ticket's
workspace. Detach without disturbing the run using Herdr's detach key.
