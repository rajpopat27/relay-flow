# Temporal Beads/Pi/Orca end-to-end verification

This document records a real-process E2E for the Temporal executor. It uses a
real local Temporal Server, a real Beads workspace and `bd` CLI, a real Orca
repository/worktree/terminal, a real Pi session with the relay-flow plugin, and
the relay-flow binary built from this checkout.

The acceptance workflow has two work nodes in addition to the lifecycle nodes:

```text
start -> implement -> review -> end
```

The E2E also verifies explicit restart behavior:

```text
attempt 1 reaches implement
  -> cancel the run
  -> change the workflow definition
  -> resubmit the same workflow
  -> confirm no automatic restart
  -> explicitly restart the ticket
  -> attempt 2 starts from start
  -> implement succeeds
  -> review succeeds
  -> end completes the run
```

This is a disposable verification run, not a CI test. It must not use the
repository's normal relay-flow home, Beads workspace, or an installed
relay-flow binary.

## Scope

The recorded run verified:

| Behavior | Evidence |
|---|---|
| Temporal is selected during init and no embedded fallback starts | isolated machine config, Temporal identity marker, server log |
| One dedicated named namespace is used | namespace API response and config |
| Namespace retention is at least 30 days | Temporal namespace API response |
| Beads workspace is initialized externally by the operator | `bd init` and `bd list` |
| Repo registration uses a repo-scoped Beads workspace | `repo register` and `repo get` |
| A ready parent is claimed exactly once | `wf:<workflow>` label and repeated poll logs |
| A run is created with one deterministic application Workflow ID | `run get` and Temporal Visibility |
| Orca creates the ticket worktree and stable node terminal | runner log and Orca worktree/terminal inspection |
| Pi reads the parent/mailbox and runs in the Orca worktree | Pi terminal capture |
| Two work mailboxes are created | Beads child listing: `implement` and `review` |
| Implement report routes to review | `feedback written` log and review mailbox comment |
| Review report routes to end | review mailbox summary and final run state |
| Cancellation records a canceled first attempt | relay-flow run query and Temporal Visibility |
| Resubmission changes the stored workflow definition | `workflow get` and updated mailbox descriptions |
| Resubmission does not automatically restart a canceled run | run query before and after another poll |
| Explicit restart creates a distinct attempt-suffixed Workflow ID | `run restart` output and Temporal Visibility |
| Attempt 2 starts from `start` with fresh node visits | attempt-2 run state and server log |
| Attempt 2 reaches both `implement` and `review` | server log and two mailbox summaries |
| Existing mailbox/worktree is reused without duplicate children | Beads child listing and Orca environment logs |
| Temporal history contains the canceled and completed executions | public Temporal Visibility API |
| Final Beads parent and both mailboxes close | `bd show` and `bd list --parent` |
| Closed parent is not picked up again | ready/claimed Beads queries return `[]` |

## Prerequisites

The run requires:

- Go toolchain capable of building the checkout; use `GOTOOLCHAIN=auto` and
  `-buildvcs=false` in this worktree;
- `bd` 1.2.2 or a compatible Beads CLI;
- `orca-ide` with the Orca app running and ready;
- `pi` with the relay-flow Pi extension installed and a usable provider;
- a running local Temporal Server at `localhost:7233`;
- Temporal Web/API at `localhost:8233` if the public Visibility verification
  below is used;
- `jq`, `curl`, `python3`, and Git.

On Linux outside an Orca-managed terminal, use `orca-ide`, not bare `orca`:

```sh
ORCA=/home/raj/.local/bin/orca-ide
$ORCA status --json
```

The status must report a running, reachable, ready Orca runtime. Verify Pi and
its extension before starting the E2E:

```sh
bd --version
pi --version
pi list
```

The Pi list must contain `relay-flow-plugin`. Beads authentication belongs to
the Beads workspace; `relay-flow task auth` is a no-op for this setup.

Check Temporal before creating the test namespace:

```sh
(exec 3<>/dev/tcp/127.0.0.1/7233) && echo "Temporal gRPC open"
curl -fsS http://127.0.0.1:8233/api/v1/namespaces >/dev/null
```

Run this E2E alone. The local Temporal Server can time out when many live test
packages create namespaces and workers concurrently. A focused real-process
run is the acceptance target here.

## Disposable layout

Use one distinctive root and remove any previous copy only if it belongs to
the current E2E run:

```sh
export E2E=/tmp/relay-flow-temporal-e2e
rm -rf "$E2E"
mkdir -p "$E2E/bin" "$E2E/relay-home" "$E2E/evidence"
export RF="$E2E/bin/relay-flow"
export RELAY_FLOW_HOME="$E2E/relay-home"
```

Keep the normal `HOME` for Orca and Pi credentials. Isolate relay-flow with
`RELAY_FLOW_HOME`; do not point Pi at a new empty HOME unless its extension and
provider credentials have also been installed there.

## Step 1: create the disposable repository

```sh
mkdir -p "$E2E/hello-world/src"
cd "$E2E/hello-world"
git init -q .
git config user.email e2e@example.com
git config user.name 'Relay Flow E2E'
printf '# Hello World E2E\n\nDisposable Temporal E2E repository.\n' > README.md
printf '.beads/\n' > .gitignore
git add README.md .gitignore
git commit -q -m 'initial hello-world E2E repository'
```

The main repository is under `/tmp`. Orca creates the ticket worktree in its
managed worktree directory; that managed path is expected and is recorded in
the runner/server logs.

## Step 2: initialize Beads in the repository

Relay-flow does not initialize Beads or run Beads migrations. Initialize the
workspace first, with an explicit `BEADS_DIR` on every later command:

```sh
cd "$E2E/hello-world"
BEADS_DIR="$E2E/hello-world/.beads" \
  bd init --prefix e2e --non-interactive --skip-hooks --skip-agents
BEADS_DIR="$E2E/hello-world/.beads" \
  bd list --ready --no-parent --limit 0 --json
```

Expected initial result:

```json
[]
```

Do not use `bd --db <bare-name>` from the relay-flow checkout. `--db` accepts
a path and can bootstrap an accidental empty database in the current directory.

## Step 3: build the relay-flow binary

Build from the checkout under test. Do not use `go install` or an installed
binary:

```sh
cd /home/raj/orca/workspaces/relay-flow/relay-flow-2pq
GOTOOLCHAIN=auto go build -buildvcs=false -o "$RF" ./cmd/relay-flow
chmod 755 "$RF"
"$RF" --help
```

`-buildvcs=false` avoids the Git VCS-stamping failure that can occur in this
worktree.

## Step 4: register the repository with Orca

Register the main repository, not a manually created ticket worktree:

```sh
ORCA=/home/raj/.local/bin/orca-ide
ORCA_REPO_JSON=$($ORCA repo add --path "$E2E/hello-world" --json)
printf '%s\n' "$ORCA_REPO_JSON" > "$E2E/evidence/orca-repo.json"
ORCA_REPO_ID=$(printf '%s' "$ORCA_REPO_JSON" | jq -r '.result.repo.id')
printf '%s\n' "$ORCA_REPO_ID" > "$E2E/evidence/orca-repo-id.txt"
```

The display name must be `hello-world`, matching the relay-flow repo name.
The Orca runner creates the ticket-specific worktree later, when the workflow
enters a work node.

## Step 5: initialize relay-flow with Temporal

Generate one unique named namespace. Do not use Temporal's `default` namespace:

```sh
NAMESPACE="relay-flow-e2e-$(date +%s)"
printf '%s\n' "$NAMESPACE" > "$E2E/evidence/namespace.txt"

"$RF" init \
  --task-plugin beads \
  --runner-plugin orca \
  --harness-plugin pi \
  --executor-plugin temporal \
  --temporal-address localhost:7233 \
  --temporal-namespace "$NAMESPACE"

"$RF" task auth
```

Verify that `RELAY_FLOW_HOME/config.yaml` contains:

```yaml
executorPlugin: temporal
temporalAddress: localhost:7233
temporalNamespace: <the-generated-namespace>
taskPlugin: beads
runnerPlugin: orca
harnessPlugin: pi
```

The init command creates or verifies the namespace through the public Temporal
SDK and requires at least 30 days of retention. It must not start the embedded
executor.

## Step 6: start and register relay-flow

```sh
"$RF" serve --background --debug
for i in $(seq 1 30); do
  [ -S "$RELAY_FLOW_HOME/server.sock" ] && break
  sleep 1
done

"$RF" repo register \
  --name hello-world \
  --path "$E2E/hello-world" \
  --set "beadsDir=$E2E/hello-world/.beads"

"$RF" repo get --name hello-world
```

The server must be running before `repo register` and `workflow submit`. The
registered Beads directory must be an existing canonical directory.

## Step 7: submit the first workflow definition

For the cancellation attempt, create `$E2E/two-node-v1.yaml`:

```yaml
name: helloWorldTemporalTwoNodeE2E
repos:
  - hello-world
cleanupRunnerOnEnd: false

taskConfig:
  filters:
    parentStatuses: [open]
    issueTypes: [task]
    labels: [two-node-e2e]

nodes:
  start:
    onSuccess:
      - target: implement

  implement:
    type: agent
    agent: default
    description: |
      This is restart E2E attempt 1. Read the Beads ticket and mailbox, then
      wait for the operator to cancel this attempt. Do not change files or
      submit a report.
    onSuccess:
      - target: review
    onFailure:
      - target: implement

  review:
    type: agent
    agent: default
    description: This node should not be reached in attempt 1.
    onSuccess:
      - target: end
    onFailure:
      - target: implement

  end: {}
```

Submit it:

```sh
"$RF" workflow submit --file "$E2E/two-node-v1.yaml"
```

The graph must contain both `implement` and `review`; `start` and `end` are
reserved lifecycle nodes and do not count as work nodes.

## Step 8: create the Beads parent and cancel attempt 1

```sh
cd "$E2E/hello-world"
PARENT=$(BEADS_DIR="$E2E/hello-world/.beads" bd create \
  'Two-node Temporal restart E2E' \
  --type task \
  --description 'Cancel attempt 1, resubmit the changed workflow, explicitly restart, and complete implement plus review.' \
  --labels two-node-e2e \
  --silent)
printf '%s\n' "$PARENT" > "$E2E/evidence/parent-id.txt"
```

Poll until the run reaches the first work node:

```sh
for i in $(seq 1 40); do
  out=$("$RF" run get --ticket "$PARENT" 2>/dev/null || true)
  printf '%s\n' "$out"
  if printf '%s' "$out" | grep -q '"currentNode": "implement"'; then
    break
  fi
  sleep 3
done
```

Cancel attempt 1 before it reports:

```sh
"$RF" run cancel \
  --ticket "$PARENT" \
  --reason 'Two-node restart E2E attempt 1 cancellation'

"$RF" run get --ticket "$PARENT"
```

Expected state:

```text
attemptId: 1
state: canceled
currentNode: implement
```

The parent should remain `in_progress` and the implement mailbox should remain
present. Cancellation closes the run-owned terminal but does not delete the
worktree or mailbox history.

## Step 9: change and resubmit the workflow

Create `$E2E/two-node-v2.yaml` with the same workflow name and repository, but
with both work nodes active:

```yaml
name: helloWorldTemporalTwoNodeE2E
repos:
  - hello-world
cleanupRunnerOnEnd: false

taskConfig:
  filters:
    parentStatuses: [open]
    issueTypes: [task]
    labels: [two-node-e2e]

nodes:
  start:
    onSuccess:
      - target: implement

  implement:
    type: agent
    agent: default
    description: |
      This is the updated workflow, attempt 2. Create src/hello.py so that
      python3 src/hello.py prints exactly "Hello from the two-node restart!".
      Verify the output and report success with NEXT STEP: review.
    onSuccess:
      - target: review
    onFailure:
      - target: implement

  review:
    type: agent
    agent: default
    description: |
      Inspect src/hello.py, run python3 src/hello.py, and verify the exact
      output "Hello from the two-node restart!". Report success with
      NEXT STEP: end.
    onSuccess:
      - target: end
    onFailure:
      - target: implement

  end: {}
```

Submit the replacement while the only run is terminal:

```sh
"$RF" workflow submit --file "$E2E/two-node-v2.yaml"
```

Verify that another poll does not restart the canceled attempt:

```sh
sleep 20
"$RF" run get --ticket "$PARENT"
```

The result must still show `attemptId: 1` and `state: canceled`.

## Step 10: explicitly restart the ticket

```sh
"$RF" run restart --ticket "$PARENT"
"$RF" run get --ticket "$PARENT"
```

Expected response shape:

```text
id: .../<parent>~attempt~2
attemptId: 2
state: starting
```

The new application ID is distinct from the canceled attempt. The old
Temporal execution is never reused.

## Step 11: verify both work nodes complete

Poll until the current run is complete:

```sh
for i in $(seq 1 90); do
  out=$("$RF" run get --ticket "$PARENT" 2>/dev/null || true)
  printf '%s\n' "$out"
  if printf '%s' "$out" | grep -q '"state": "completed"'; then
    break
  fi
  sleep 5
done
```

Verify Beads children:

```sh
cd "$E2E/hello-world"
BEADS_DIR="$E2E/hello-world/.beads" \
  bd list --parent "$PARENT" --all --limit 0 --json
```

There must be exactly two workflow mailboxes:

```text
<parent>.1  <parent>:implement  closed
<parent>.2  <parent>:review     closed
```

The implement summary must be followed by feedback to the review mailbox. The
review summary must have no feedback to `end`.

## Step 12: verify the Orca worktree and generated program

Find the ticket worktree through Orca:

```sh
$ORCA worktree list --repo "id:$ORCA_REPO_ID" --json
```

Use the worktree whose display name is the Beads parent ID. Then inspect it:

```sh
WT=/home/raj/orca/workspaces/hello-world/<parent-id>
git -C "$WT" status --short
git -C "$WT" log --oneline -3
cat "$WT/src/hello.py"
python3 "$WT/src/hello.py"
```

Expected output:

```text
Hello from the two-node restart!
```

The Orca terminal list for the worktree must show the stable node titles while
the nodes run:

```text
<parent-id>:implement
<parent-id>:review
```

The Pi plugin log must show separate runtime registration for both nodes and
separate session IDs.

## Step 13: verify Temporal history through public APIs

Relay-flow does not access Temporal persistence. For verification, the local
Temporal Web/API can be queried by Workflow ID:

```sh
NS=$(cat "$E2E/evidence/namespace.txt")
OLD_ID="hello-world/helloWorldTemporalTwoNodeE2E/$PARENT"
NEW_ID="${OLD_ID}~attempt~2"
OLD_Q=$(python3 -c 'import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1], safe=""))' "$OLD_ID")
NEW_Q=$(python3 -c 'import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1], safe=""))' "$NEW_ID")

curl -fsS \
  "http://127.0.0.1:8233/api/v1/namespaces/$NS/workflows?query=WorkflowId%3D%27$OLD_Q%27"
curl -fsS \
  "http://127.0.0.1:8233/api/v1/namespaces/$NS/workflows?query=WorkflowId%3D%27$NEW_Q%27"
```

Expected statuses:

```text
old Workflow ID: CANCELED
new Workflow ID: COMPLETED
both task queues: relay-flow
```

The recorded run returned history lengths of 98 for the canceled execution and
207 for the completed two-node execution.

## Step 14: verify final Beads polling state

```sh
cd "$E2E/hello-world"
BEADS_DIR="$E2E/hello-world/.beads" \
  bd list --ready --no-parent --limit 0 --json
BEADS_DIR="$E2E/hello-world/.beads" \
  bd list --no-parent --status open,in_progress,blocked,deferred \
  --label-pattern 'wf:*' --limit 0 --json
```

Both queries must return `[]`. The parent is closed and must not re-enter the
poll loop.

## Step 15: cleanup

Stop relay-flow through its own socket and stop all E2E terminals through Orca:

```sh
"$RF" stop
$ORCA terminal stop \
  --worktree "id:$ORCA_REPO_ID::$WT" \
  --json
```

Do not use `pkill`. The recorded run preserved the disposable root and Orca
worktree for inspection:

```text
/tmp/relay-flow-temporal-e2e/
/home/raj/orca/workspaces/hello-world/<parent-id>/
```

After reviewing the evidence, remove the temporary root and managed worktree if
the host should be clean. The dedicated Temporal namespace is intentionally
not deleted by relay-flow; it is a server-side namespace with the configured
retention policy.

## Recorded run

The final two-work-node restart run used:

```text
Namespace:          relay-flow-e2e-1788607136
Workflow:           helloWorldTemporalTwoNodeE2E
Parent:             e2e-omb
Attempt 1:          hello-world/helloWorldTemporalTwoNodeE2E/e2e-omb
Attempt 1 status:   CANCELED
Attempt 2:          hello-world/helloWorldTemporalTwoNodeE2E/e2e-omb~attempt~2
Attempt 2 status:   COMPLETED
Implement visit:   df3a9dd824adede3bd9e02ec74428028
Review visit:      48f501d6d6d6d01d6f27076bc08a0e43
Worktree:           /home/raj/orca/workspaces/hello-world/e2e-omb
Commit:             90d9dec
Output:             Hello from the two-node restart!
```

The original relay-flow checkout was not modified by this E2E. Evidence from
all runs is retained under `/tmp/relay-flow-temporal-e2e/evidence/` until the
operator removes it.
