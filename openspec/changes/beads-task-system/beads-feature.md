# Full plan: add Beads as a task-system plugin

## Scope

This plan adds:

```text
  taskPlugin: beads
```

It does not change:

- runner behavior;
- harness behavior;
- workflow routing;
- durable execution;
- server API;
- report handling;
- SQLite schema.

The existing architecture remains:

```text
  one registered repo
      ↓
  one Beads task.System
      ↓
  one RepoPoller
      ↓
  bd list --ready ...
```

Each registered repo may use a different Beads workspace.

────────────────────────────────────────────────────────────────────────────────

## Mandatory pre-implementation live Beads preflight (completed; reference)

The live `bd` CLI preflight has already been completed for this change. The implementer MUST read `bd-cli-research.md` and use the existing disposable environment and dependency demo before writing adapter code. If `/tmp/beads-demo` is recreated, repeat the setup below and update the recorded disposable IDs; never initialize or mutate `/home/raj/raj/beads`.

First verify the tools and create an isolated Dolt server:

```sh
bd --version
dolt version

DEMO=/tmp/beads-demo
if [ -f "$DEMO/dolt-server.pid" ]; then
  old_pid=$(cat "$DEMO/dolt-server.pid")
  kill "$old_pid" 2>/dev/null || true
  sleep 1
fi
rm -rf "$DEMO"
mkdir -p "$DEMO/dolt-data"
nohup dolt sql-server \
  --data-dir "$DEMO/dolt-data" \
  --host 127.0.0.1 \
  --port 13307 \
  > "$DEMO/dolt-server.log" 2>&1 &
echo $! > "$DEMO/dolt-server.pid"

until dolt --no-tls \
  --host 127.0.0.1 \
  --port 13307 \
  sql -q 'SELECT 1' >/dev/null 2>&1; do
  kill -0 "$(cat "$DEMO/dolt-server.pid")" 2>/dev/null || {
    cat "$DEMO/dolt-server.log"
    exit 1
  }
  sleep 0.25
done
```

Initialize the real server-backed Beads workspace with no auto-started local server:

```sh
cd /tmp/beads-demo
BEADS_DOLT_SERVER_TLS=0 \
BEADS_DOLT_PASSWORD= \
bd init \
  --server \
  --external \
  --server-host 127.0.0.1 \
  --server-port 13307 \
  --server-user root \
  --prefix demo \
  --non-interactive \
  --skip-hooks \
  --skip-agents
```

Then exercise it from the relay-flow checkout, using the same external-workspace arrangement the adapter will use:

```sh
RELAY_FLOW_ROOT=<relay-flow-checkout>
export BEADS_DIR=/tmp/beads-demo/.beads
export BEADS_DOLT_SERVER_TLS=0
export BEADS_DOLT_PASSWORD=
cd "$RELAY_FLOW_ROOT"

bd where --json
bd info --json
bd create 'Relay-flow Beads demo parent' \
  --description 'Disposable parent for the relay-flow Beads preflight.' \
  --json
# Capture the returned Beads issue ID as PARENT_ID; do not derive it from the title.

printf '%s\n' 'Disposable mailbox description.' | \
bd create "$PARENT_ID:implement" \
  --type task \
  --parent "$PARENT_ID" \
  --no-inherit-labels \
  --labels wf:demo \
  --stdin \
  --json
# Capture the returned child issue ID as CHILD_ID; use the ID for later commands.

bd update "$PARENT_ID" --add-label wf:demo --json
printf '%s\n' 'preflight marker: beads-feature-live-check' | \
bd comment "$PARENT_ID" --stdin --json
bd comments "$PARENT_ID" --json
bd show "$PARENT_ID" --json
bd list --ready --no-parent --limit 0 --json
bd list --no-parent --status open,in_progress,blocked,deferred --label-pattern 'wf:*' --limit 0 --json
bd list --parent "$PARENT_ID" --all --limit 0 --json
bd update "$PARENT_ID" --status in_progress --json
bd update "$PARENT_ID" --status open --json
bd close "$CHILD_ID" --reason 'preflight close/reopen check' --json
bd reopen "$CHILD_ID" --reason 'preflight reset' --json
```

The implementer must record the selected `bd` and `dolt` versions and sanitized real command/output fixtures under the Beads tests. Beads status reconciliation intentionally does not depend on `--if-status`: the adapter reads the current issue with `bd show`, classifies target/expected/incompatible state, and performs an unconditional `bd update` only after an expected-state read. The narrow read/write race is an accepted Beads-specific last-writer-wins trade-off; it must not be hidden behind a fallback.

The `--no-parent` flag is an optimization, not the adapter's correctness boundary. The implementation MUST inspect the normalized `parent` field and defensively exclude every child issue even if a selected `bd` version returns a child unexpectedly. The preflight must record the actual output shape and any version-specific discrepancies.

The preflight passes only when the real commands prove all of the following:

- the Dolt server is running from `/tmp/beads-demo/dolt-data`;
- `bd init --server --external` creates a usable workspace at `/tmp/beads-demo/.beads`;
- commands launched from the relay-flow checkout honor `BEADS_DIR=/tmp/beads-demo/.beads`;
- parent creation, child creation, labels, comments, `show`, ready listing, claimed listing, child listing, close, and reopen work;
- the adapter can identify parents from the returned issue fields and exclude child issues from top-level polling regardless of CLI filtering quirks;
- read-before-write status behavior is known for the exact selected `bd` binary.

Use direct SQL only for the `SELECT 1` server-readiness probe. Never read or write the `issues` table or any other Beads table directly, and never read `.beads/issues.jsonl`.

Leave the isolated server running for the implementation work and retain its PID/log paths. At feature completion, stop only that PID and remove `/tmp/beads-demo`; never clean up a process or directory outside this disposable path.

────────────────────────────────────────────────────────────────────────────────

## 1. Decisions

### 1.1 Use the `bd` CLI, not the Beads Go module

Use a small subprocess wrapper around:

```text
  bd
```

Reasons:

- Beads already has the exact commands we need.
- All required operations have JSON output.
- Beads manages its own Dolt/storage behavior.
- No direct SQL or JSONL parsing is necessary.
- The current Beads checkout requires Go 1.26.5; relay-flow uses Go 1.24.6.
- The Beads public Storage interface is very large and would add unnecessary coupling.
- No new relay-flow dependency is required.

The adapter should not import:

```go
  github.com/steveyegge/beads/internal/...
```

and should not add the Beads module to go.mod.

### 1.2 Store the Beads workspace in repo task config

Use:

```yaml
  repos:
    payments:
      path: /work/payments
      taskConfig:
        beadsDir: /var/lib/beads/payments/.beads
```

The two paths have separate meanings:

```text
  path:
    code/runner repository

  beadsDir:
    Beads workspace/configuration context
```

For a normal local Beads repository:

```yaml
  repos:
    payments:
      path: /work/payments
      taskConfig:
        beadsDir: /work/payments/.beads
```

For a server-backed Beads workspace:

```yaml
  repos:
    payments:
      path: /work/payments
      taskConfig:
        beadsDir: /var/lib/beads/payments/.beads
```

The code repository does not need to contain .beads in the second case.

This avoids changing the current TaskScopeKey function signature.

### 1.3 beadsDir is required for the first multi-repo implementation

The Beads factory will declare:

```go
  RequiredRepoKeys: func() []string {
      return []string{"beadsDir"}
  }
```

This makes the task scope explicit and prevents two registered repos from pointing to the same Beads workspace.

Registration:

```sh
  relay-flow repo register \
    --name payments \
    --path /work/payments \
    --set beadsDir=/var/lib/beads/payments/.beads
```

The existing generic --set key=value support already handles this.

We should not invent a fake component field or use the Beads prefix as the scope key.

### 1.4 Do not use bd serve initially

Beads can run with a Dolt SQL server, but relay-flow should still invoke bd directly.

The Beads CLI will transparently use:

- embedded Dolt; or
- server-backed Dolt selected by the workspace metadata.

bd serve is not needed because it would require:

- a configured HTTP endpoint;
- server startup/lifecycle handling;
- readiness checks;
- possible bearer-token handling;
- one running HTTP server per workspace;
- another transport implementation.

That is unnecessary for the first version.

### 1.5 Do not use bd ready --claim

Relay-flow must resolve the workflow and add the wf:<workflow> label before creating the durable run.

Therefore use:

```sh
  bd list --ready ...
```

for reading work, and:

```sh
  bd update <id> --add-label wf:<workflow> --json
```

for relay-flow claiming.

### 1.6 Beads status transitions use read-before-write

Beads deliberately does not use `--if-status` for the initial adapter. For every status transition, the adapter reads the current issue with `bd show` and applies this small state table:

```text
current == target status
  → success; the durable activity already completed, so do nothing

current != expected source status
  → return retry.ConflictError; a manual state is incompatible

current == expected source status
  → issue an unconditional bd update to the target status
```

The read/write race between the last two steps is an explicitly accepted Beads-specific trade-off. Do not add a compatibility fallback around status writes. This mirrors Jira's existing behavior: `jira.system` delegates to its REST client's `Transition`, and that client checks the current target state before issuing a transition.

────────────────────────────────────────────────────────────────────────────────

## 2. Package layout

Add:

```text
  internal/task/beads/
      beads.go
      beads_test.go

  internal/task/beads/bdcli/
      bdcli.go
      bdcli_test.go
      testdata/
```

This follows the existing adapter pattern:

```text
  internal/task/jira/
  internal/runner/orca/orcacli/
```

bdcli is the narrow external-process seam. The Beads task adapter owns the task-system behavior and translates it to task.System.

────────────────────────────────────────────────────────────────────────────────

## 3. internal/task/beads/bdcli

### 3.1 CLI client responsibilities

The client should:

- execute bd;
- set the child process working directory;
- set BEADS_DIR;
- capture stdout/stderr separately;
- parse JSON;
- expose exit-code information;
- provide a narrow fakeable interface;
- serialize commands for one Beads workspace.

Never call:

```go
  os.Chdir(...)
```

The current process runs multiple pollers and activities concurrently. Use:

```go
  exec.Cmd.Dir
```

for each child process.

### 3.2 Client construction

Possible shape:

```go
  type CLI struct {
      repoPath string
      beadsDir string
      mu       sync.Mutex
  }
```

```go
  func New(repoPath, beadsDir string) *CLI
```

Every command should execute with:

```text
  cmd.Dir = repoPath
  BEADS_DIR = beadsDir
```

The adapter should explicitly control the Beads selector environment so an ambient BEADS_DIR, BEADS_DB, or BD_DB cannot redirect the command to an unrelated workspace.

### 3.3 CLI-domain values

Define only the fields relay-flow needs:

```go
  type Issue struct {
      ID          string   `json:"id"`
      Title       string   `json:"title"`
      Description string   `json:"description,omitempty"`
      Status      string   `json:"status"`
      IssueType   string   `json:"issue_type"`
      Priority    int      `json:"priority"`
      Assignee    string   `json:"assignee,omitempty"`
      Labels      []string `json:"labels,omitempty"`
      Parent      string   `json:"parent,omitempty"`
      IsBlocked   bool     `json:"is_blocked,omitempty"`
  }
```

```go
  type Comment struct {
      ID   string `json:"id"`
      Text string `json:"text"`
  }
```

Do not use Beads’ internal Go types.

### 3.4 Narrow client interface

```go
  type Client interface {
      Probe(ctx context.Context) error

      ListReady(ctx context.Context) ([]Issue, error)
      ListClaimed(ctx context.Context) ([]Issue, error)

      ListChildren(ctx context.Context, parentID string) ([]Issue, error)
      Show(ctx context.Context, issueID string) (Issue, error)

      ListComments(ctx context.Context, issueID string) ([]Comment, error)

      CreateChild(
          ctx context.Context,
          parentID string,
          title string,
          description string,
          label string,
      ) (Issue, error)

      Update(ctx context.Context, issueID string, input UpdateInput) error
      AddComment(ctx context.Context, issueID, body string) error
  }
```

```go
  type UpdateInput struct {
      Description    *string
      Status         string
      AddLabels      []string
      ClearDefer     bool
      Force          bool
  }
```

### 3.5 Internal client helpers

```go
  func (c *CLI) run(
      ctx context.Context,
      args []string,
      stdin io.Reader,
  ) ([]byte, error)

  func (c *CLI) runJSON(
      ctx context.Context,
      args []string,
      stdin io.Reader,
      destination any,
  ) error

  func commandEnvironment(repoPath, beadsDir string) []string
  func parseJSON(data []byte, destination any) error
```

The JSON parser should tolerate informational output before the JSON value while keeping stderr separate.

Define a command error carrying:

```go
  type CommandError struct {
      Args     []string
      ExitCode int
      Stderr   string
      Stdout   string
  }
```

Status mismatches are classified by the Beads adapter after `Show`; the CLI client only exposes ordinary command failures and exit codes.

### 3.6 Exact CLI commands

#### Probe

Use a harmless read:

```sh
  bd list --ready --limit 1 --no-parent --json
```

An empty result is success. Failure means the Beads CLI/workspace is unusable.

#### Ready parents

```sh
  bd list \
    --ready \
    --no-parent \
    --limit 0 \
    --json
```

#### Claimed active parents

```sh
  bd list \
    --no-parent \
    --status open,in_progress,blocked,hooked \
    --label-pattern wf:* \
    --limit 0 \
    --json
```

The two result sets are deduplicated by issue ID.

#### Child mailboxes

```sh
  bd list \
    --parent <parent-id> \
    --all \
    --limit 0 \
    --json
```

#### Individual issue

```sh
  bd show <issue-id> --json
```

#### Add claim label

```sh
  bd update <issue-id> \
    --add-label wf:<workflow> \
    --json
```

#### Create mailbox

```sh
  bd create "<parent-id>:<node>" \
    --type task \
    --parent <parent-id> \
    --no-inherit-labels \
    --labels wf:<workflow> \
    --stdin \
    --json
```

Description goes over stdin.

#### Reconcile mailbox

```sh
  bd update <mailbox-id> \
    --description=- \
    --add-label wf:<workflow> \
    --json
```

#### Status update

Read the issue first with `bd show <id> --json`. If it is already the target, return success; if it is not the expected source, return a task conflict; otherwise issue the unconditional update:

```sh
  bd update <id> \
    --status in_progress \
    --json
```

#### Add comment

Use the singular command because it supports stdin:

```sh
  bd comment <id> --stdin --json
```

#### Read comments

```sh
  bd comments <id> --json
```

#### Close mailbox

Read the mailbox first. If it is already `closed`, succeed as an idempotent no-op; if it is not `in_progress`, return a task conflict; otherwise issue the unconditional update:

```sh
  bd update <mailbox-id> \
    --status closed \
    --json
```

#### Reopen/reset

```sh
  bd update <id> \
    --status open \
    --defer "" \
    --json
```

────────────────────────────────────────────────────────────────────────────────

## 4. internal/task/beads/beads.go

### 4.1 Adapter-owned configuration

```go
  type Config struct {
      BeadsDir  string       `yaml:"beadsDir"`
      Filters   Filters      `yaml:"filters,omitempty"`
      Status    StatusConfig `yaml:"status,omitempty"`
      Templates Templates    `yaml:"templates,omitempty"`
  }
```

```go
  type Filters struct {
      ParentStatuses []string `yaml:"parentStatuses,omitempty"`
      IssueTypes     []string `yaml:"issueTypes,omitempty"`
      Labels         []string `yaml:"labels,omitempty"`
      Assignees      []string `yaml:"assignees,omitempty"`
  }
```

```go
  type StatusConfig struct {
      Parent  string `yaml:"parent,omitempty"`
      Mailbox string `yaml:"mailbox,omitempty"`
  }
```

```go
  type Templates struct {
      MailboxDescription string `yaml:"mailboxDescription"`
      SummaryComment     string `yaml:"summaryComment"`
      FeedbackComment    string `yaml:"feedbackComment"`
  }
```

Do not support Jira-specific fields:

```text
  project
  component
  transitionTo
```

### 4.2 Defaults

Add:

```go
  func DefaultConfig() config.RawValues
```

Defaults should contain only task-system text templates.

Example defaults:

```text
  mailboxDescription:
    Parent ticket: {{ticket}}
    Workflow: {{workflow}}
    Node: {{node}}
    Node type: {{nodeType}}
    Agent: {{agent}}
    Mailbox: {{mailbox}}

    Node work:
    {{nodeDescription}}

    Read this mailbox's comments for feedback from previous nodes.

  summaryComment:
    Summary for {{node}}

    {{summaryReport}}

  feedbackComment:
    Feedback from {{sourceNode}} to {{targetNode}} mailbox {{mailbox}}

    {{feedbackReport}}
```

The fixed report contract and routes continue to be appended by relay-flow core.

### 4.3 Factory registration

```go
  func init() {
      task.Register("beads", task.Factory{
          RequiredRepoKeys: func() []string {
              return []string{"beadsDir"}
          },
          TaskScopeKey: beadsTaskScopeKey,
          Auth:         beadsAuth,
          DefaultConfig: DefaultConfig,
          ValidateTextConfig: validateTextConfig,
          New: newSystem,
      })
  }
```

### 4.4 Scope calculation

```go
  func beadsTaskScopeKey(
      rootConfig config.RawValues,
      repoConfig config.RawValues,
  ) (string, error)
```

It should:

1. read repoConfig["beadsDir"];
2. reject missing/empty values;
3. require the path to identify a directory;
4. canonicalize it;
5. return it as the opaque scope key.

Conceptually:

```text
  scope = canonical(beadsDir)
```

This gives:

```text
  payments → /var/lib/beads/payments/.beads
  platform  → /var/lib/beads/platform/.beads
```

and rejects:

```text
  payments → /var/lib/beads/shared/.beads
  platform  → /var/lib/beads/shared/.beads
```

The Beads prefix is not used here.

### 4.5 System construction

```go
  type system struct {
      cli       bdcli.Client
      repoName  string
      repoPath  string
      beadsDir  string
      base      config.RawValues
      effective Config
  }
```

```go
  func newSystem(
      ctx context.Context,
      spec task.RepoSpec,
  ) (task.System, error)
```

Responsibilities:

- merge defaults/root/repo config;
- strictly decode Beads config;
- validate beadsDir;
- construct the CLI client;
- run Probe;
- validate templates;
- return a concurrent-safe system.

Do not create or initialize a Beads workspace automatically.

### 4.6 No-op authentication

```go
  func beadsAuth(
      ctx context.Context,
      args []string,
      stdin io.Reader,
  ) error
```

Since Beads has no relay-flow task credentials, this should succeed with no arguments and reject unsupported authentication arguments rather than creating a credentials file.

No changes to:

```text
  internal/credentials
  credentials.yaml
```

are needed.

────────────────────────────────────────────────────────────────────────────────

## 5. task.System method implementation

### Poll

```go
  func (s *system) Poll(ctx context.Context) ([]task.Ticket, error)
```

Steps:

1. call ListReady;
2. call ListClaimed;
3. deduplicate by Beads issue ID;
4. convert issues to task.Ticket;
5. return only top-level parent issues.

The adapter should not apply workflow filters here.

### Issue normalization

```go
  func issueToTicket(issue bdcli.Issue) task.Ticket
  func normalizeFields(issue bdcli.Issue) map[string]any
  func extractWorkflowClaims(labels []string) []string
```

Mapping:

```text
  Beads issue.id          → Ticket.ID and Ticket.Key
  Beads issue.title       → Ticket.Title
  Beads issue.labels      → Ticket.WorkflowClaims + Fields["labels"]
  Beads issue.status      → Fields["status"]
  Beads issue.issue_type  → Fields["issueType"]
  Beads issue.assignee    → Fields["assignee"]
  Beads issue.priority    → Fields["priority"]
  Beads issue.description → Fields["description"]
```

Ticket.Key should be the Beads issue ID.

### CompileFilter

```go
  func (s *system) CompileFilter(
      workflowTaskConfig config.RawValues,
  ) (func(task.Ticket) bool, error)
```

Merge:

```text
  root task config
      ↓
  repo task config
      ↓
  workflow task config
```

Support only the Beads-owned structured filters:

```text
  parentStatuses
  issueTypes
  labels
  assignees
```

Matching is local and case rules should be simple:

- statuses/types/labels: exact;
- assignees: case-insensitive;
- multiple labels: all required.

No arbitrary Beads query language or SQL.

### ValidateConfig

```go
  func (s *system) ValidateConfig(
      ctx context.Context,
      workflowTaskConfig config.RawValues,
      nodeTaskConfigs map[string]config.RawValues,
  ) error
```

Validate:

- unknown fields;
- explicit nulls;
- filter types;
- configured status values;
- task templates;
- required summaryReport and feedbackReport placeholders.

This is local validation. No network/API call is needed.

### RenderText

```go
  func (s *system) RenderText(
      kind task.TextKind,
      data task.TextData,
  ) (string, error)
```

Supported template variables:

```text
  runID
  ticket
  workflow
  repo
  node
  nodeType
  agent
  nodeDescription
  nextSteps
  successRoutes
  failureRoutes
  mailbox
  sourceNode
  targetNode
  summaryReport
  feedbackReport
```

Use the same simple replacement approach as the current Jira adapter.

### Claim

```go
  func (s *system) Claim(
      ctx context.Context,
      ticket task.TicketRef,
      workflow string,
  ) error
```

Call:

```sh
  bd update <ticket-id> --add-label wf:<workflow> --json
```

Do not modify status or assignee.

### EnsureMailboxes

```go
  func (s *system) EnsureMailboxes(
      ctx context.Context,
      parent task.TicketRef,
      workflow string,
      specs []task.MailboxSpec,
  ) (map[string]task.Mailbox, error)
```

Steps:

1. list all children of the parent;
2. match each requested mailbox by stable title:
   ```text
     <parent-id>:<node>
   ```
3. reject duplicate matching children;
4. update existing descriptions and labels;
5. create missing children;
6. return the complete node-to-mailbox map.

Helpers:

```go
  func mailboxTitle(parentID, node string) string
  func findMailbox(children []bdcli.Issue, title string) (bdcli.Issue, error)
  func issueToMailbox(issue bdcli.Issue, node string) task.Mailbox
```

Use the Beads issue ID as both:

```text
  Mailbox.ID
  Mailbox.Key
```

The title remains the stable human identity.

### ApplyTaskConfig

```go
  func (s *system) ApplyTaskConfig(
      ctx context.Context,
      target task.Target,
      raw config.RawValues,
  ) error
```

Recommended semantics:

- parent target:
    - apply status.parent when configured;
- mailbox target:
    - apply status.mailbox;
    - optionally apply status.parent if explicitly configured;
- no configured status:
    - no-op.

Status updates should:

1. read the current issue;
2. treat already-target status as success;
3. reject known incompatible manual states as retry.ConflictError;
4. issue an unconditional `bd update` only when the observed status is the expected source status;
5. accept the documented read/write race for Beads rather than adding a conditional-update fallback.

### Lifecycle defaults

Implement:

```go
  func (s *system) StartDefaults() config.RawValues
  func (s *system) WorkDefaults() config.RawValues
  func (s *system) EndDefaults() config.RawValues
```

Recommended defaults:

```text
  start:
    no parent status change

  work node:
    mailbox → in_progress

  end:
    parent → closed
```

Leaving the parent open during work keeps it visible to the claimed-ticket poll query.

### CompleteMailbox

```go
  func (s *system) CompleteMailbox(
      ctx context.Context,
      mailbox task.Mailbox,
  ) error
```

Behavior:

- close only the specified mailbox;
- no comment;
- no parent update;
- no runner operation;
- no routing;
- idempotent if already closed;
- incompatible manual state becomes a conflict.

### HasComment

```go
  func (s *system) HasComment(
      ctx context.Context,
      target task.Target,
      marker string,
  ) (bool, error)
```

Resolve the issue ID from:

```text
  target.Mailbox.Key
```

or:

```text
  target.Parent.Key
```

Then call:

```sh
  bd comments <id> --json
```

Search comment.text for the marker.

### Comment

```go
  func (s *system) Comment(
      ctx context.Context,
      target task.Target,
      body string,
      marker string,
  ) error
```

Steps:

1. read comments;
2. return success if marker exists;
3. append marker to body;
4. send body through stdin:
   ```sh
     bd comment <id> --stdin --json
   ```

Example stored body:

```text
  Summary content

  <!-- visit-id:summary -->
```

### ResetForRecovery

```go
  func (s *system) ResetForRecovery(
      ctx context.Context,
      parent task.TicketRef,
      mailboxes []task.Mailbox,
      taskConfig config.RawValues,
  ) error
```

Explicit recovery should:

- reset parent to open;
- reset every mailbox to open;
- clear deferred state;
- preserve comments;
- preserve labels;
- preserve descriptions;
- preserve Beads history;
- never delete issues.

The parent cancellation marker remains in its comments.

────────────────────────────────────────────────────────────────────────────────

## 6. Polling behavior

The existing RepoPoller remains unchanged.

For each registered repo:

```text
  RepoPoller.Run
      ↓
  task.System.Poll
      ↓
  bd list --ready --no-parent ...
      ↓
  bd list --label-pattern wf:* ...
      ↓
  deduplicate
      ↓
  handleBatch
      ↓
  router.ResolveWorkflow
      ↓
  RunManager.EnsureRun
```

There is no Beads-specific timer or polling goroutine.

If three workflows target the same registered Beads repo:

```text
  one Beads poll
      ↓
  one batch
      ↓
  three in-memory workflow matcher checks
```

No per-workflow Beads reads are added.

────────────────────────────────────────────────────────────────────────────────

## 7. Server-backed deployment

### Local workspace

```text
  code repo:  /work/payments
  beadsDir:   /work/payments/.beads
```

Commands run with:

```text
  cwd = /work/payments
  BEADS_DIR = /work/payments/.beads
```

### External/server-backed workspace

```text
  code repo:  /work/payments
  beadsDir:   /var/lib/beads/payments/.beads
```

Commands run with:

```text
  cwd = /work/payments
  BEADS_DIR = /var/lib/beads/payments/.beads
```

The external Beads workspace metadata selects the Dolt server/database.

The server-backed database can be shared by many writers, but the relay-flow adapter still treats each configured beadsDir as one task scope.

### Prefix convention

Optionally initialize each Beads workspace with a repo-specific prefix:

```text
  payments-...
  platform-...
```

This helps make ticket IDs globally recognizable.

It is not used for:

- workspace selection;
- task scope;
- poller selection;
- filtering a shared database.

────────────────────────────────────────────────────────────────────────────────

## 8. Wiring changes

Add the adapter imports:

```text
  cmd/relay-flow/main.go
  cmd/relay-flow/serve.go
```

```go
  _ "github.com/rajpopat27/relay-flow/internal/task/beads"
```

No changes are required to:

```text
  internal/config/machine.go
  internal/task/task.go
  internal/task/factory.go
  internal/repo/poller.go
  internal/repo/service.go
  internal/router
  internal/run
  internal/execution/goworkflows
  internal/server
```

The existing generic code will automatically expose:

```text
  beads
```

through:

```go
  task.Names()
```

and initialization:

```sh
  relay-flow init --task-plugin beads ...
```

repo register will obtain:

```text
  beadsDir
```

from:

```go
  task.RequiredRepoKeys("beads")
```

────────────────────────────────────────────────────────────────────────────────

## 9. Test plan

### 9.1 bdcli tests

Use a strict fake bd executable.

Verify exact arguments and stdin for:

- ready query;
- claimed query;
- child listing;
- issue show;
- claim label update;
- mailbox create;
- mailbox description update;
- status update;
- read-before-write status handling;
- comments list;
- comment stdin;
- recovery reset.

Also verify:

- cmd.Dir;
- BEADS_DIR;
- ambient Beads environment isolation;
- JSON array/object parsing;
- empty result behavior;
- target/expected/incompatible status classification;
- stderr propagation;
- multiline description/comment preservation.

### 9.2 Beads adapter tests

Cover:

- factory registration as "beads";
- unknown config field rejection;
- explicit null rejection;
- missing beadsDir rejection;
- canonical task-scope calculation;
- different Beads directories produce different scopes;
- same Beads directory is rejected by repo registration;
- no required authentication;
- issue normalization;
- wf:* claim extraction;
- ready/claimed deduplication;
- child issues excluded from polling;
- in-memory filter matching;
- mailbox creation;
- mailbox reuse;
- duplicate mailbox title rejection;
- description/label reconciliation;
- summary rendering;
- feedback rendering;
- comment marker idempotency;
- status transitions;
- target/expected/incompatible status handling;
- the accepted read/write race;
- mailbox completion;
- recovery resets state while preserving comment/label semantics.

### 9.3 Repository service tests

Add cases proving:

```text
  repo A + beadsDir A → succeeds
  repo B + beadsDir B → succeeds
  repo B + beadsDir A → rejected
  repo B + different path but same beadsDir → rejected
```

The existing duplicate path check should continue to work independently.

### 9.4 Composition test

Exercise the real composition path with:

- a temporary code repository;
- a temporary Beads workspace path;
- a strict fake bd;
- the real Beads task factory;
- the real repo service;
- the existing fake runner/harness;
- the real durable engine if practical.

Verify:

```text
  selected taskPlugin = beads
  repo path reaches bd command cwd
  beadsDir reaches BEADS_DIR
  poll returns ready parent
  mailboxes are children
  wf label is added before run creation
  comments are written to the right issue
```

### 9.5 Manual smoke test

After installing a released bd binary:

1. Create a disposable Beads workspace.
2. Initialize it with a test prefix.
3. Create:
    - one ready parent;
    - one blocked parent;
    - one child issue;
    - one closed issue.
4. Configure relay-flow with:
   ```yaml
     beadsDir: <temporary-workspace>/.beads
   ```
5. Verify:
    - only the ready parent is selected;
    - children are not selected;
    - claims add wf:<workflow>;
    - mailboxes are created;
    - comments are idempotent;
    - end closes the parent.

Do not run bd init, bd create, or other writes in the production Beads checkout without explicitly choosing that behavior.

────────────────────────────────────────────────────────────────────────────────

## 10. Documentation updates

Update only user-facing documentation needed for the new adapter:

```text
  README.md
  examples/
```

Document:

- installing bd;
- initializing a Beads workspace;
- configuring beadsDir;
- local versus server-backed Beads;
- relay-flow repo register --set beadsDir=...;
- Beads status names;
- Beads-specific workflow filter examples;
- no task auth credentials are needed;
- one poller per registered repo.

Do not document Beads prefixes as components or database selectors.

────────────────────────────────────────────────────────────────────────────────

## 11. Explicit non-goals

Do not add:

- direct Beads Go-module dependency;
- direct Dolt SQL queries;
- .beads/issues.jsonl parsing;
- automatic bd init;
- automatic bd serve startup;
- a Beads-specific poller;
- prefix-based multi-tenant routing;
- a fake component abstraction;
- Beads-specific workflow syntax in core;
- task-system logic in repo.Service;
- a second retry system;
- a new database schema;
- a new server API;
- rollback/deletion of Beads issues during recovery.

────────────────────────────────────────────────────────────────────────────────

## 12. Implementation order

1. Add bdcli command/error/JSON seam.
2. Add strict bdcli command-shape tests.
3. Add Beads config and factory registration.
4. Add beadsDir scope calculation.
5. Add New and workspace probe.
6. Add ticket normalization and Poll.
7. Add structured filter compilation.
8. Add task text templates.
9. Add claim and comment operations.
10. Add mailbox discovery/create/reconciliation.
11. Add status/application lifecycle behavior.
12. Add recovery reset.
13. Add blank-import wiring.
14. Add repository scope tests.
15. Add composition test.
16. Update README/examples.
17. Run:

```sh
  gofmt -w <changed Go files>
  go test ./...
  go test -race ./...
  go vet ./...
  cd plugin && bun test
```

The expected production change should remain limited to:

```text
  internal/task/beads/
  internal/task/beads/bdcli/
  cmd/relay-flow/main.go
  cmd/relay-flow/serve.go
  README.md
  examples/
```

plus tests.
