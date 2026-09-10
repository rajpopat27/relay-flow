# Structs, Methods, and Interfaces

This is the target Go design for the features in `docs/feature-tracker.md`. It replaces the current per-workflow daemon with repo-scoped polling and durable ticket runs while preserving the useful Jira, Orca, Unix-socket, and registry seams.

## Design Rules

- Keep domain names human-readable: Repo Poller, Ticket Router, Run Manager, Workflow Worker, Activity Worker, node, mailbox, report, and next step.
- Use concrete structs by default. Add interfaces only where relay-flow consumes a replaceable task system, runner, harness, or durable executor.
- Task, runner, and harness packages own their small stable plugin contracts. Other interfaces live with their consumers.
- Use one generic durable workflow interpreter for all YAML workflows. Do not register a generated Go workflow per YAML file.
- Store workflow YAML on disk, execution history in SQLite, and workflow assignment in the task system as `wf:<name>`.
- Pass serializable value structs across durable boundaries. Never persist interfaces, clients, functions, or engine-specific values.
- Keep `go-workflows` types inside `internal/execution/goworkflows`.
- Treat external effects as idempotent, retryable activities. SQLite cannot make Jira or runner calls atomic.
- Recover by rolling forward. Do not implement rollback compensation.
- Do not add an event bus, dependency-injection framework, generic repository layer, or worker per ticket.

## Packages

```text
cmd/relay-flow/                    command parsing and server startup only

internal/paths/                    ~/.relay-flow paths
internal/config/                   machine config, raw plugin config, strict decode
internal/identity/                 RunID and NodeVisitID only

internal/workflow/                 workflow, node, route, report, validation, file store
internal/task/                     task-system contract and registry
internal/task/jira/                Jira task system
internal/task/jira/acli/           Jira CLI wrapper
internal/runner/                   runner contract and registry
internal/runner/orca/              Orca runner
internal/runner/orca/orcacli/      Orca CLI wrapper
internal/harness/                  harness contract and registry
internal/harness/opencode/         OpenCode launch-time harness

internal/repo/                     registrations and repo service
internal/repo/poller.go            Repo Poller and bounded polling group
internal/router/                   Ticket Router
internal/run/                      run values, Run Manager, executor boundary
internal/execution/goworkflows/    go-workflows, SQLite, interpreter, activities
internal/retry/                    one error classification and backoff policy

internal/server/                   Unix-socket HTTP handlers and client
```

### Dependency Direction

```text
server
  -> workflow.Service, repo.Service, run.Manager

repo    -> task.System
repo    -> runner.Runner (registration/discovery only)
router  -> repo.Repo, task.Ticket, workflow.Workflow
run     -> repo.Repo, task.System, workflow.Workflow

execution/goworkflows
  -> run values
  -> repo registry
  -> task.System
  -> runner.Runner
  -> harness.Harness

task/jira       -> task contract, task/jira/acli
runner/orca     -> runner contract, runner/orca/orcacli
harness/opencode -> harness contract
```

`workflow`, `task`, `runner`, and `harness` never import the server or durable engine.

`identity` imports no relay-flow package. `task`, `runner`, `harness`, and `run` may import it without creating cycles.

## Common Packages

### `internal/paths`

```go
type Paths struct {
	Root       string
	Config     string
	Workflows  string
	Database   string
	Socket     string
	Lock       string
	ServerLog  string
	PluginLog  string
}

func ForUserHome() (Paths, error)
func Ensure(Paths) error
```

All process artifacts stay under `~/.relay-flow/`. This replaces path construction spread across `config`, `discovery`, and the plugin.

### `internal/config`

Raw plugin values are validated during startup or submission and remain serializable across durable workflow boundaries. Task adapters decode the effective values when executing an operation; do not add typed runtime caches that complicate restart or workflow replay.

```go
type RawValues map[string]any

type Machine struct {
	PollIntervalSeconds int             `yaml:"pollIntervalSeconds,omitempty"`
	CompletedRunRetentionDays int       `yaml:"completedRunRetentionDays,omitempty"`
	TaskPlugin    string          `yaml:"taskPlugin"`
	TaskConfig    RawValues       `yaml:"taskConfig,omitempty"`
	RunnerPlugin  string          `yaml:"runnerPlugin"`
	RunnerConfig  RawValues       `yaml:"runnerConfig,omitempty"`
	HarnessPlugin string          `yaml:"harnessPlugin"`
	HarnessConfig RawValues       `yaml:"harnessConfig,omitempty"`
	Repos         map[string]Repo `yaml:"repos,omitempty"`
}

type Repo struct {
	Path       string `yaml:"path"`
	TaskConfig RawValues `yaml:"taskConfig,omitempty"`
}

func LoadMachine(path string) (*Machine, error)
func SaveMachine(path string, cfg *Machine) error
func Merge(values ...RawValues) RawValues
func DecodeStrict(values RawValues, dst any) error
func WriteAtomic(path string, data []byte, mode fs.FileMode) error
```

`PollIntervalSeconds` defaults to `15` when omitted and must be positive. It is machine-wide because Repo Pollers are shared across workflows; per-workflow and per-repo intervals are unsupported.

`CompletedRunRetentionDays` defaults to `30` when omitted and must be positive. Cleanup removes completed or canceled engine histories and matching `relay_runs` projection rows; non-terminal runs are preserved and task-system markers remain.

`Merge` applies root, repo, workflow, then node precedence and recursively merges maps. A task adapter validates and decodes the result into one type such as `jira.Config`; it does not define four scope-specific config structs.

`WriteAtomic` creates a temporary sibling, writes and syncs it, sets permissions, renames it over the destination, and syncs the parent directory. Machine config uses `0600`; workflow files use `0644`.

Task, runner, and harness each keep a small package-local named factory map. Duplicate registration panics because it is a programmer error. Unknown configured names return a user-facing error listing registered names. A shared registry abstraction is unnecessary.

### `internal/identity`

```go
type RunID string
type NodeVisitID string

func NewRunID(repo, workflow, ticket string) RunID
func NewNodeVisitID() NodeVisitID
```

`RunID` has deterministic delimiter-safe encoding. `NodeVisitID` is generated once through a durable replay-safe side effect for each node entry. Consumers treat both as opaque and never parse them.

## Workflow Domain

### Values

```go
package workflow

type NodeType string

const (
	NodeAgent NodeType = "agent"
	NodeHITL  NodeType = "hitl"
)

type Outcome string

const (
	OutcomeSuccess Outcome = "success"
	OutcomeFailure Outcome = "failure"
)

type Workflow struct {
	Name              string          `yaml:"name" json:"name"`
	Repos             []string        `yaml:"repos" json:"repos"`
	CleanupRunnerOnEnd bool           `yaml:"cleanupRunnerOnEnd" json:"cleanupRunnerOnEnd"`
	TaskConfig        config.RawValues `yaml:"taskConfig,omitempty" json:"taskConfig,omitempty"`
	Nodes             map[string]Node `yaml:"nodes" json:"nodes"`
}

type Node struct {
	Type        NodeType      `yaml:"type,omitempty" json:"type,omitempty"`
	Agent       string        `yaml:"agent,omitempty" json:"agent,omitempty"`
	Description string        `yaml:"description,omitempty" json:"description,omitempty"`
	NudgePrompt string        `yaml:"nudgePrompt,omitempty" json:"nudgePrompt,omitempty"`
	TaskConfig  config.RawValues `yaml:"taskConfig,omitempty" json:"taskConfig,omitempty"`
	OnSuccess   []Route       `yaml:"onSuccess,omitempty" json:"onSuccess,omitempty"`
	OnFailure   []Route       `yaml:"onFailure,omitempty" json:"onFailure,omitempty"`
}

type Route struct {
	Target string `yaml:"target" json:"target"`
	When   string `yaml:"when,omitempty" json:"when,omitempty"`
}
```

`start` and `end` are reserved lifecycle node names. The word `terminal` is used only for runner terminals that host agents:

- `start` has no type, agent, description, nudge, or failure routes. It has exactly one success target.
- `end` has no type, agent, description, nudge, or routes.
- Every other node is `agent` or `hitl`, has an agent and description, and declares at least one valid route for every permitted outcome.
- HITL changes only nudge behavior. It uses the same report and route contract as an agent node.
- Only agent and HITL nodes receive mailbox subtasks. `start` and `end` are orchestration-only and do not receive subtasks or sessions.
- A mailbox is the node subtask's description and comment section. If the selected next node is `end`, feedback fields must be `None` and the feedback comment is skipped because `end` has no mailbox.

### Structured Report

```go
type Summary struct {
	Completed        string `json:"completed"`
	NotCompleted     string `json:"notCompleted"`
	IssuesDiscovered string `json:"issuesDiscovered"`
	Verification     string `json:"verification"`
	Notes            string `json:"notes"`
}

type Feedback struct {
	ReasonForNextStep string `json:"reasonForNextStep"`
	RequiredActions   string `json:"requiredActions"`
	RelevantContext   string `json:"relevantContext"`
	ExpectedResult    string `json:"expectedResult"`
}

type Report struct {
	Status   Outcome  `json:"status"`
	NextStep string   `json:"nextStep"`
	Summary  Summary  `json:"summary"`
	Feedback Feedback `json:"feedback"`
}
```

All strings are required. The literal `None` represents an intentionally empty section. `nodeVisitID` and `runID` are transport metadata, not LLM-generated report fields.

### Workflow Methods

```go
func Parse(name string, yamlBytes []byte) (*Workflow, error)
func (w *Workflow) Validate() error
func (w *Workflow) StartTarget() (string, error)
func (w *Workflow) Routes(node string, outcome Outcome) ([]Route, error)
func (w *Workflow) ValidateReport(node string, report Report) error
func (w *Workflow) RenderNudge(node string, data NudgeTemplateData) (string, error)

type NudgeTemplateData struct {
	Ticket   string
	Workflow string
	Repo     string
	Node     string
	NextSteps string
}
```

`ValidateReport` verifies every section, outcome, and that `NextStep` names exactly one configured target for that outcome. It is pure and is called both at the server boundary and inside durable execution.

### Workflow Storage

```go
type Store struct {
	Dir string
}

func (s *Store) LoadAll() ([]*Workflow, error)
func (s *Store) Get(name string) (*Workflow, error)
func (s *Store) Put(name string, yamlBytes []byte) error
func (s *Store) Remove(name string) error

type Registry struct {
	// private mutex and name map
}

func (r *Registry) Get(name string) (*Workflow, bool)
func (r *Registry) List() []*Workflow
func (r *Registry) ReferencesRepo(repo string) bool
func (r *Registry) Replace(wf *Workflow)
func (r *Registry) Remove(name string)
```

The workflow file is the durable definition. The workflow registry has no repo index; repo bindings are the only derived repo-to-workflow index.

### Workflow Service

```go
type ActiveRuns interface {
	HasActiveWorkflow(context.Context, string) (bool, error)
}

type RepoLookup interface {
	Exists(string) bool
}

type Service struct {
	// store, registry, repos, active runs
}

func (s *Service) Submit(ctx context.Context, yamlBytes []byte) (*Workflow, error)
func (s *Service) Remove(ctx context.Context, name string) error
func (s *Service) Get(name string) (*Workflow, error)
func (s *Service) List() []*Workflow
```

`Submit` means create or replace. Replacement is rejected while any run of that workflow is active. Validation completes before the file is atomically replaced; the in-memory replacement cannot fail afterward. There is no workflow versioning.

## Task-System Contract

### Values

```go
package task

type Ticket struct {
	ID        string
	Key       string
	Title     string
	WorkflowClaims []string
	Fields    map[string]any
}

type TicketRef struct {
	ID    string `json:"id"`
	Key   string `json:"key"`
	Title string `json:"title"`
}

func (t Ticket) Ref() TicketRef

type Mailbox struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Node string `json:"node"`
}

type MailboxSpec struct {
	Node        string        `json:"node"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	TaskConfig  config.RawValues `json:"taskConfig,omitempty"`
}

type Target struct {
	Parent  TicketRef
	Mailbox *Mailbox
}
```

The task package contains task-system values only. Run IDs, node visits, reports, and orchestration inputs belong to `run`.

### Runtime Interface

One `System` instance is created per registered repo and is safe for concurrent use.

```go
type System interface {
	Poll(ctx context.Context) ([]Ticket, error)
	CompileFilter(workflowTaskConfig config.RawValues) (func(Ticket) bool, error)
	Claim(ctx context.Context, ticket TicketRef, workflow string) error

	ValidateConfig(ctx context.Context, workflowTaskConfig config.RawValues, nodeTaskConfigs map[string]config.RawValues) error
	EnsureMailboxes(ctx context.Context, parent TicketRef, workflow string, specs []MailboxSpec) (map[string]Mailbox, error)
	ApplyTaskConfig(ctx context.Context, target Target, taskConfig config.RawValues) error
	CompleteMailbox(ctx context.Context, mailbox Mailbox) error
	HasComment(ctx context.Context, target Target, marker string) (bool, error)
	Comment(ctx context.Context, target Target, body, marker string) error
	ResetForRecovery(ctx context.Context, parent TicketRef, mailboxes []Mailbox, taskConfig config.RawValues) error
}
```

`Poll` returns active parent tickets only; mailbox subtasks are never routed as workflow runs. `EnsureMailboxes` is the sole mailbox discovery operation: given a parent and node specs, it finds existing child mailboxes, creates only missing ones, and returns the complete map. Recovery polls parents first, then calls `EnsureMailboxes` before resetting mailbox state.

These are task-system primitives:

- `ApplyTaskConfig` applies the adapter-owned `taskConfig` to a parent and optional mailbox. Jira transitions statuses; Linear may assign users or update fields.
- `CompleteMailbox` marks the current node mailbox complete using task-system semantics; Jira moves it to `Done`. `HasComment` checks a stable relay-flow marker without requiring core to understand provider comment formats. `Comment` writes human-readable summary, feedback, or cancellation text to the selected item.
- `EnsureMailboxes` finds existing mailboxes by workflow/node identity, creates only missing ones, ensures their workflow labels, and returns the complete node-to-mailbox map. Normal startup and database-loss recovery use the same operation.
- Run orchestration decides the order of `ApplyTaskConfig` and `Comment`; task adapters do not know start, node, or end lifecycle steps.
- Relay-flow comments carry a stable marker derived from `nodeVisitID` and comment type; adapters check for it before posting. Rare duplicates remain acceptable after ambiguous failures.
- Each method must be idempotent where the task system permits it. If one method requires multiple remote calls, that adapter owns reconciliation inside the method.

After a report is durably accepted, run orchestration schedules these separate activities in this exact order:

```text
Comment(current mailbox, SUMMARY)
-> Comment(next mailbox, FEEDBACK) when next step is not end
-> CompleteMailbox(current mailbox)
-> ApplyTaskConfig(next target, next node config)
-> start or reconcile the next node terminal
```

`CompleteMailbox` performs no comment, feedback, routing, next-node processing, or runner work.

### Factory

```go
type RepoSpec struct {
	Name       string
	Path       string
	RootConfig config.RawValues
	RepoConfig config.RawValues
}

type Factory struct {
	RequiredRepoKeys func() []string
	TaskScopeKey     func(rootConfig, repoConfig config.RawValues) (string, error)
	New              func(context.Context, RepoSpec) (System, error)
}

func Register(name string, factory Factory)
func New(ctx context.Context, name string, spec RepoSpec) (System, error)
func RequiredRepoKeys(name string) ([]string, error)
func TaskScopeKey(name string, rootConfig, repoConfig config.RawValues) (string, error)
func Names() []string
```

`repo register` requests the explicit YAML keys returned by `RequiredRepoKeys`, such as `project` and `component`. `TaskScopeKey` returns an opaque canonical physical task scope, such as Jira site/project/component; registration rejects a scope already assigned to another repo. Prompt metadata and reflection are unnecessary. The Jira adapter validates merged values against the same `jira.Config` during loading and decodes the operation's effective values when called. Core never imports `jira.Config`.

The Jira-owned type, located in `internal/task/jira`, can contain all supported scopes without separate config models:

```go
type Config struct {
	Assignee    string       `yaml:"assignee,omitempty"`
	Project     string       `yaml:"project,omitempty"`
	Component   string       `yaml:"component,omitempty"`
	Filters     Filters      `yaml:"filters,omitempty"`
	Transition TransitionTo `yaml:"transitionTo,omitempty"`
}
```

## Runner Contract

### Values And Interface

```go
package runner

type RepoCandidate struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type Environment struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

type Terminal struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type Command struct {
	Executable string            `json:"executable"`
	Args       []string          `json:"args,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
}

type RunSpec struct {
	RunID     identity.RunID
	RepoName  string
	RepoPath  string
	TicketKey string
}

type Runner interface {
	DiscoverRepos(ctx context.Context) ([]RepoCandidate, error)
	ValidateRepo(ctx context.Context, name, path string) error
	EnsureEnvironment(ctx context.Context, spec RunSpec) (Environment, error)
	FindTerminal(ctx context.Context, env Environment, title string) (Terminal, bool, error)
	CloseTerminal(ctx context.Context, terminal Terminal) error
	EnsureTerminal(ctx context.Context, env Environment, title string, command Command) (Terminal, error)
	CloseTerminals(ctx context.Context, spec RunSpec) error
	CleanupRun(ctx context.Context, spec RunSpec) error
}
```

The runner owns workspaces/worktrees, terminals, process handles, terminal liveness, and internal IDs. It does not know task-system fields, workflow routes, report contents, or OpenCode command syntax. `FindTerminal` returns only a live usable terminal. `CloseTerminals` closes agent terminals while preserving the environment/workspace; `CleanupRun` removes all runner-owned run resources at `end`. Both reconstruct identity from `RunSpec` without SQLite IDs.

The terminal title contains only `<ticket>:<node>`, for example `PAY-101:coding`. It never contains `nodeVisitID`, workflow name, agent name, or other changing metadata. `nodeVisitID` stays in harness environment/report metadata and changes on every revisit. Each node visit checkpoints closing the previous terminal, then calls idempotent `EnsureTerminal` with current visit metadata; a lost start acknowledgement finds the newly created terminal rather than creating another.

### Factory

```go
type Factory func(config.RawValues) (Runner, error)

func Register(name string, factory Factory)
func New(name string, cfg config.RawValues) (Runner, error)
func Names() []string
```

No repo-level runner config exists. The runner resolves internal workspace IDs from the registered repo name and path.

## Harness Contract

### Values And Interface

```go
package harness

type Session struct {
	ID    string
	Title string
}

type LaunchSpec struct {
	RunID        identity.RunID
	NodeVisitID  identity.NodeVisitID
	RepoName     string
	RepoPath     string
	Workflow     string
	Ticket       string
	Node         string
	NodeType     workflow.NodeType
	Agent        string
	Title        string
	Prompt       string
	NudgePrompt  string
	NextSteps    []workflow.Route
	ResumeID     string
}

type Harness interface {
	ValidateAgent(ctx context.Context, repoPath, agent string) error
	FindSession(ctx context.Context, repoPath, title string) (Session, bool, error)
	BuildCommand(spec LaunchSpec) (runner.Command, error)
}
```

The harness owns agent validation, session discovery, resume syntax, and launch command construction. The runner executes the returned command.

The OpenCode runtime plugin implements the other half of the harness contract, including sending nudges through OpenCode's own session API:

1. Read the last completed assistant message on idle.
2. Parse the complete report contract.
3. For an agent node, use the rendered nudge when output is invalid.
4. For HITL, remain silent when output is absent or invalid.
5. Send `{runID, nodeVisitID, report}` as JSON through `relay-flow report`.
6. Retry the exact report with exponential backoff and jitter until acknowledged.

### Factory

```go
type Factory func(config.RawValues) (Harness, error)

func Register(name string, factory Factory)
func New(name string, cfg config.RawValues) (Harness, error)
func Names() []string
```

## Registered Repos

### In-Memory Repo

```go
package repo

type WorkflowBinding struct {
	Workflow *workflow.Workflow
	Match    func(task.Ticket) bool
}

type Info struct {
	Name       string        `json:"name"`
	Path       string        `json:"path"`
	TaskConfig config.RawValues `json:"taskConfig,omitempty"`
}

type Repo struct {
	Name       string
	Path       string
	TaskConfig config.RawValues
	TaskSystem task.System
	Workflows  []WorkflowBinding
}

func (r *Repo) Info() Info

type Registry struct {
	// private mutex and name map
}

func (r *Registry) Get(name string) (*Repo, bool)
func (r *Registry) List() []*Repo
func (r *Registry) Replace(repo *Repo)
func (r *Registry) Remove(name string)
func (r *Registry) BindWorkflows(workflows []*workflow.Workflow) error
```

`Workflow.Repos` is the source of truth. `Repo.Workflows` is a derived in-memory index rebuilt at startup and after workflow submission/removal.

### Repo Service

```go
type ActiveRuns interface {
	HasActiveRepo(context.Context, string) (bool, error)
}

type WorkflowRefs interface {
	ReferencesRepo(string) bool
}

type Service struct {
	// machine config, registry, runner, task factory, active runs, workflow refs
}

type RegisterInput struct {
	Name       string
	Path       string
	TaskConfig config.RawValues
}

func (s *Service) Discover(ctx context.Context) ([]runner.RepoCandidate, error)
func (s *Service) RequiredRepoKeys() []string
func (s *Service) Register(ctx context.Context, input RegisterInput) (Info, error)
func (s *Service) Remove(ctx context.Context, name string) error
func (s *Service) Get(name string) (Info, error)
func (s *Service) List() []Info
```

`RequiredRepoKeys` delegates to the task factory's method of the same name. Registration validates the runner repo, required task values, task-system connectivity, duplicate names, and duplicate canonical paths before atomically writing machine config. Removal is rejected while a run or stored workflow references the repo.

## Repo Pollers

```go
package repo

type BatchHandler func(context.Context, *Repo, []task.Ticket)

type RepoPoller struct {
	Repo     *Repo
	Interval time.Duration
	Handle   BatchHandler
}

func (p *RepoPoller) Run(ctx context.Context)

type PollerGroup struct {
	// pollers and a semaphore limiting active polls
}

func NewPollerGroup(maxConcurrent int, handle BatchHandler) *PollerGroup
func (g *PollerGroup) ReplaceRepos(repos []*Repo)
func (g *PollerGroup) Run(ctx context.Context)
```

There is one lightweight timer goroutine per repo. A semaphore limits actual task-system polls to 10. Repo Pollers only fetch batches and call `BatchHandler`; they do not match, claim, or start runs.
Every Repo Poller uses `Machine.PollIntervalSeconds`.

## Ticket Router

```go
package router

var ErrNoMatch = errors.New("ticket matches no workflow")

type AmbiguousError struct {
	Ticket    string
	Workflows []string
}

type InvalidClaimError struct {
	Ticket   string
	Workflow string
	Repo     string
}

func ResolveWorkflow(registered *repo.Repo, ticket task.Ticket) (*workflow.Workflow, error)
```

Routing order:

1. If the ticket carries more than one workflow claim, return `InvalidClaimError`.
2. If exactly one workflow claim exists, resolve it directly from repo bindings without applying filters.
3. If the claim names an unknown workflow or one not registered for the repo, return `InvalidClaimError`.
4. Otherwise execute the precompiled matchers.
5. Zero matches returns `ErrNoMatch`; the polling handler ignores it.
6. One match returns the workflow.
7. Multiple matches returns `AmbiguousError`; no ticket mutation occurs.

The Ticket Router is pure and has no Jira, SQLite, runner, or goroutine dependency.

## Runs And Run Manager

### IDs And State

```go
package run

type ID = identity.RunID
type NodeVisitID = identity.NodeVisitID

type State string

const (
	StateStarting  State = "starting"
	StateRunning   State = "running"
	StateWaiting   State = "waiting"
	StateBlocked   State = "blocked"
	StateCompleted State = "completed"
	StateCanceling State = "canceling"
	StateCanceled  State = "canceled"
)

type Start struct {
	ID       ID                 `json:"id"`
	Repo     string             `json:"repo"`
	RepoPath string             `json:"repoPath"`
	Workflow workflow.Workflow  `json:"workflow"`
	Ticket   task.TicketRef     `json:"ticket"`
}

type Work struct {
	RunID       ID
	Repo        string
	Workflow    string
	Parent      task.TicketRef
	WorkflowTaskConfig config.RawValues
}

type NodeWork struct {
	Work
	Node        string
	NodeVisitID NodeVisitID
	Mailbox     task.Mailbox
	NodeTaskConfig config.RawValues
}

type CommentWork struct {
	Item   task.Target
	Body   string
	Marker string
}

type Run struct {
	ID                   ID          `json:"id"`
	Repo                 string      `json:"repo"`
	Workflow             string      `json:"workflow"`
	Ticket               task.TicketRef `json:"ticket"`
	State                State       `json:"state"`
	CurrentNode          string      `json:"currentNode,omitempty"`
	CurrentNodeVisitID   NodeVisitID `json:"currentNodeVisitId,omitempty"`
	LastError            string      `json:"lastError,omitempty"`
	StartedAt            time.Time   `json:"startedAt"`
	UpdatedAt            time.Time   `json:"updatedAt"`
	FinishedAt           *time.Time  `json:"finishedAt,omitempty"`
}

type ReportRequest struct {
	RunID       ID             `json:"runId"`
	NodeVisitID NodeVisitID    `json:"nodeVisitId"`
	Report      workflow.Report `json:"report"`
}

type ReportAck struct {
	Accepted  bool `json:"accepted"`
	Duplicate bool `json:"duplicate"`
}

type Filter struct {
	Repo     string
	Workflow string
	Ticket   string
	Active   *bool
}
```

`NodeVisitID` is generated once through a durable replay-safe side effect whenever the graph enters a work node. It remains stable across normal replay and changes on revisits or explicit database recovery.

### Durable Executor Boundary

This is the replacement boundary for `go-workflows`, Temporal, or another durable engine.

```go
type Executor interface {
	EnsureRun(ctx context.Context, start Start) (created bool, err error)
	SubmitReport(ctx context.Context, report ReportRequest) (ReportAck, error)
	CancelRun(ctx context.Context, id ID, reason string) error
}

type RunQueries interface {
	GetRun(ctx context.Context, id ID) (Run, error)
	FindRunByTicket(ctx context.Context, ticket string) (Run, error)
	ListRuns(ctx context.Context, filter Filter) ([]Run, error)
	HasActiveWorkflow(ctx context.Context, workflow string) (bool, error)
	HasActiveRepo(ctx context.Context, repo string) (bool, error)
}
```

No `go-workflows` context, instance, backend, queue, or error crosses this interface.

### Run Manager

```go
type RunManager struct {
	Executor Executor
	Runs     RunQueries
}

func (m *RunManager) EnsureRun(ctx context.Context, repo *repo.Repo, wf *workflow.Workflow, ticket task.Ticket) error
func (m *RunManager) CancelByTicket(ctx context.Context, ticket, reason string) error
```

`EnsureRun` performs only assignment and durable-run creation:

1. If unassigned, call `repo.TaskSystem.Claim(ticket.Ref(), wf.Name)`.
2. Only continue after the claim succeeds.
3. If already assigned to the selected workflow, skip claiming.
4. Build deterministic ID `repo/workflow/ticket`.
5. Call `Executor.EnsureRun` with a value snapshot of the workflow.

Repeated polls are harmless. A crash after claim but before `EnsureRun` is repaired by the next repo poll. Workflow labels are applied to the parent and every mailbox subtask. Loss or deletion of SQLite execution state is recovered only through explicit `serve --recover`, described below.

`CancelByTicket` resolves the active run through `RunQueries.FindRunByTicket`, then calls `Executor.CancelRun`.

## `go-workflows` Implementation

### Main Types

```go
package goworkflows

type Engine struct {
	// SQLite backend, go-workflows client/workers, read model, dependencies
}

type Activities struct {
	Repos   *repo.Registry
	Runner  runner.Runner
	Harness harness.Harness
	Runs    *RunProjection
}

type RunProjection struct {
	DB *sql.DB
}
```

`go-workflows` creates its own backend tables. Relay-flow adds one small derived `relay_runs` projection in the same SQLite database because the engine does not expose application-level queries for ticket lookup, active workflow checks, or `run list/get`.

```sql
CREATE TABLE relay_runs (
    id TEXT PRIMARY KEY,
    repo TEXT NOT NULL,
    workflow TEXT NOT NULL,
    ticket_id TEXT NOT NULL,
    ticket_key TEXT NOT NULL,
    state TEXT NOT NULL,
    current_node TEXT,
    current_node_visit_id TEXT,
    last_error TEXT,
    started_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    finished_at DATETIME
);
```

Indexes cover `(ticket_key)`, `(workflow, state)`, and `(repo, state)`. This table is a derived API projection only. Durable workflow history is authoritative for reports, selected routes, and activity progress. Projection updates are idempotent durable activities, so replay repairs interrupted updates.

### Workers

Create one `go-workflows` Workflow Worker object and one Activity Worker object:

```go
worker.NewWorkflowWorker(backend, &worker.WorkflowWorkerOptions{
	MaxParallelWorkflowTasks: 10,
})

worker.NewActivityWorker(backend, &worker.ActivityWorkerOptions{
	MaxParallelActivityTasks: 20,
})
```

“10 Workflow Workers” and “20 Activity Workers” mean concurrency limits, not 30 independently constructed workers. Waiting for a signal is durable and does not reserve an Activity Worker or one relay-flow goroutine per ticket.

### Generic Interpreter

Register one function:

```go
func TicketWorkflow(ctx goworkflow.Context, start run.Start) error
```

Logical flow:

```text
ensure all work-node mailboxes
process reserved start taskConfig
choose start.onSuccess target

for each work node:
  generate nodeVisitID through a durable side effect
  update the relay_runs projection
  process node in task system
  ensure runner environment
  find and checkpoint-close the previous terminal by stable title
  find prior harness session by stable title
  build harness command with report metadata
  ensure terminal with stable title and current visit command
  wait on signal "report/<nodeVisitID>"
  validate status, next step, summary, and feedback
  persist the accepted report and selected next node in workflow history
  write summary to current mailbox
  write feedback to selected mailbox
  complete current node
  continue with the already-selected next node

at end:
  process end taskConfig
  if cleanupRunnerOnEnd: clean all runner-owned run resources
  update the relay_runs projection as completed
```

Each task, runner, harness, and read-model call is a separate activity checkpoint. If an activity returns after performing a remote effect but before its result is persisted, it may be retried; adapters must read-before-write, use provider idempotency keys where available, or tolerate occasional duplicate comments.

### Report Delivery

`Engine.SubmitReport`:

1. Resolve the durable workflow instance by `runID`; use the projection only to locate `runID` when the caller supplies a ticket key.
2. If `nodeVisitID` is no longer current, acknowledge it as an old duplicate and do nothing.
3. Validate a current visit's report against the current workflow node.
4. Call `SignalWorkflow(runID, "report/"+nodeVisitID, report)`.
5. Return acknowledgement only after `SignalWorkflow` has durably appended the signal to SQLite.

No report-deduplication table or payload hash is needed. The signal channel name includes the visit ID, and the workflow consumes only the first report for that visit before advancing. A duplicate arriving while the visit is current may remain unused in workflow history; a later retry is acknowledged as an old duplicate. Neither case repeats mailbox comments, task changes, or runner work.

### Durable Activity Retries

`go-workflows` has finite retries and no jitter in its native retry calculation. To implement the approved common policy, the adapter schedules each external activity once, classifies the result, and uses a durable timer before retrying.

Use a private typed closure-based loop around each activity call. Do not expose a generic helper with `activity any` or variadic arguments.

- Transient: retry indefinitely with exponential backoff, jitter, and five-minute cap.
- Conflict/manual task-system change: mark the run blocked, retry the expected operation on the capped schedule, and automatically continue when the external state is compatible again.
- Startup configuration, credential, permission, and connectivity errors are rejected before workflows begin. Errors encountered by an existing runtime activity keep retrying because dependencies and credentials can recover.
- Cancellation: stop scheduling normal activities and run cancellation cleanup on a disconnected workflow context.

Jitter is generated through `workflow.SideEffect` so replay returns the same delay. The native activity retry count is one; private typed retry loops use the common policy.

### Cancellation

Cancellation cannot interrupt an already-running `go-workflows` activity. It prevents later activities from being scheduled.

On cancellation, a disconnected workflow context schedules only:

1. runner `CloseTerminals`, preserving the run environment/workspace;
2. task-system `Comment` on the parent using marker `runID + cancellation`, accepting rare duplicates after ambiguous task-system failures;
3. read-model state `canceled`.

Subtask statuses and mailbox history remain unchanged. No rollback compensation runs.

### Database-Loss Recovery

`relay-flow serve --recover` is an explicit destructive recovery mode used only when SQLite execution state was deleted or became unusable:

- `relay-flow init` initializes the SQLite database.
- Normal `serve` requires a valid existing database and refuses to start if it is missing or unusable.
- With a healthy database, a labeled ticket whose deterministic run is missing represents a crash after claiming but before run creation; `EnsureRun` creates it normally.
- Database loss is never inferred from a missing run. Only the explicit `--recover` flag creates fresh execution state and restarts labeled tickets from `start`.

1. Load machine config, registered repos, and stored workflow YAML from disk.
2. Start Repo Pollers against active tickets; task systems already exclude parents completed through `end`.
3. Select parents and mailbox subtasks carrying `wf:<name>`.
4. Build `runner.RunSpec` from repo, workflow, and parent ticket, then call `CloseTerminals`; this requires no SQLite run state and preserves the workspace/code.
5. Call `EnsureMailboxes` to find existing subtasks and create only missing ones.
6. Reset those mailbox subtasks to `To Do`; keep their comments, labels, worktrees, and code.
7. Create a fresh deterministic durable run and execute the reserved `start` node.

Recovery never runs automatically. Repeated agent work and occasional duplicate comments are accepted. The task-system contract needs one explicit `ResetForRecovery` operation; its implementation is adapter-specific.
Parents containing the stable cancellation marker are skipped during recovery.
All previous execution progress is treated as unknown. Recovery creates a new durable run using the same deterministic logical `RunID`, generates fresh `NodeVisitID`s, and processes from `start`; it never resumes an old node, route, report, timer, or activity.

### Engine Lifecycle

```go
func New(path string, deps Dependencies) (*Engine, error)
func (e *Engine) Start(ctx context.Context) error
func (e *Engine) Shutdown(ctx context.Context) error
```

`Engine` implements both `run.Executor` and `run.RunQueries`; lifecycle remains on the concrete engine because application services do not need to mock it.

Startup opens the SQLite backend, migrates `relay_runs`, registers `TicketWorkflow` and `Activities`, starts workers, and resumes pending engine tasks automatically. Shutdown cancels worker polling, waits a bounded time for active tasks, and closes SQLite.

Before workers or Repo Pollers start, validate task-system, runner, and harness configuration, credentials, permissions, connectivity, registered repos, and every configured agent.

### Required Compatibility Spike

Before implementation, pin an exact `go-workflows` release and compile a small spike covering SQLite startup, explicit instance IDs, duplicate start handling, separate Workflow/Activity Workers, signals, durable timers, cancellation cleanup, and restart recovery. The latest stable release inspected during this design, `v1.4.2`, requires Go `1.24.5`; this repo currently declares Go `1.21.11`, so adopting it requires an explicit toolchain upgrade. Do not add an adapter against unpinned nightly APIs.

## Shared Retry Package

```go
package retry

type Kind string

const (
	Transient Kind = "transient"
	Conflict  Kind = "conflict"
)

type Failure struct {
	Kind    Kind   `json:"kind"`
	Message string `json:"message"`
}

type BackoffPolicy struct {
	Initial time.Duration
	Maximum time.Duration
	Factor  float64
	Jitter  float64
}

var DefaultBackoffPolicy = BackoffPolicy{
	Initial: 2 * time.Second,
	Maximum: 5 * time.Minute,
	Factor:  2,
	Jitter:  0.2,
}

func ConflictError(err error) error
func Classify(err error) Failure
func (p BackoffPolicy) Delay(attempt int, random float64) time.Duration
func Do(ctx context.Context, policy BackoffPolicy, fn func() error) error
```

`Do` is a thin adapter for non-durable loops such as Repo Pollers. Durable activities use the same `BackoffPolicy.Delay` through workflow timers. The TypeScript harness plugin mirrors the same constants with a thin `setTimeout` adapter because it cannot import Go code. Backoff calculation is tested centrally; adapters test only their execution mechanism.

## Server API And Services

`internal/server` translates Unix-socket JSON to services. It contains no Jira, Orca, workflow graph, or SQLite logic.

`relay-flow init` runs before the server is required: it selects the three plugin names and atomically writes machine config. `relay-flow serve` acquires the existing flock, builds the startup wiring below, opens the Unix socket, and blocks until shutdown.

### Client

```go
type Client struct {
	Socket string
}

func (c *Client) Stop(ctx context.Context) error

func (c *Client) SubmitWorkflow(ctx context.Context, yaml []byte) (*workflow.Workflow, error)
func (c *Client) RemoveWorkflow(ctx context.Context, name string) error
func (c *Client) ListWorkflows(ctx context.Context) ([]*workflow.Workflow, error)
func (c *Client) GetWorkflow(ctx context.Context, name string) (*workflow.Workflow, error)

func (c *Client) DiscoverRepos(ctx context.Context) ([]runner.RepoCandidate, error)
func (c *Client) RegisterRepo(ctx context.Context, input repo.RegisterInput) (repo.Info, error)
func (c *Client) RemoveRepo(ctx context.Context, name string) error
func (c *Client) ListRepos(ctx context.Context) ([]repo.Info, error)
func (c *Client) GetRepo(ctx context.Context, name string) (repo.Info, error)

func (c *Client) SubmitReport(ctx context.Context, report run.ReportRequest) (run.ReportAck, error)
func (c *Client) CancelRun(ctx context.Context, ticket, reason string) error
func (c *Client) ListRuns(ctx context.Context, filter run.Filter) ([]run.Run, error)
func (c *Client) GetRunByTicket(ctx context.Context, ticket string) (run.Run, error)
```

Suggested routes:

```text
POST   /stop

POST   /workflows
GET    /workflows
GET    /workflows/{name}
DELETE /workflows/{name}

GET    /repos/discover
GET    /repos/task-fields
POST   /repos
GET    /repos
GET    /repos/{name}
DELETE /repos/{name}

POST   /reports
GET    /runs
GET    /runs/by-ticket/{key}
POST   /runs/by-ticket/{key}/cancel
```

`relay-flow report` reads one JSON object from stdin and calls `SubmitReport`; multiline fields never become shell arguments.

## Startup Wiring

The server composition root is explicit Go code, not a container:

```text
load machine config
select task, runner, and harness factories
construct shared runner and harness
load registered repos and one task.System per repo
load workflow files
validate each workflow against every referenced repo task system
bind workflows and compiled matchers to repos
open go-workflows SQLite executor and workers
construct Run Manager
start Repo Poller group
start Unix-socket server
```

Polling callback stays simple and does not introduce another named dispatcher:

```go
func handleBatch(ctx context.Context, repo *repo.Repo, tickets []task.Ticket) {
	for _, ticket := range tickets {
		wf, err := router.ResolveWorkflow(repo, ticket)
		if errors.Is(err, router.ErrNoMatch) {
			continue
		}
		if err != nil {
			log.Error("route ticket", "ticket", ticket.Key, "error", err)
			continue
		}
		if err := runManager.EnsureRun(ctx, repo, wf, ticket); err != nil {
			log.Error("ensure run", "ticket", ticket.Key, "error", err)
		}
	}
}
```

## Mapping From Current Code

| Current | Target |
|---|---|
| `internal/config.Config` | move workflow schema/validation to `workflow.Workflow`; keep machine values/I/O in `config.Machine` |
| `internal/daemon.Daemon.PollLoop` | `repo.RepoPoller` |
| `Daemon.PollOnce` routing switch | `router.ResolveWorkflow` plus `run.RunManager` |
| daemon dispatch/bounce goroutines | durable `TicketWorkflow` activities |
| `tasks.Tasks` | repo-bound `task.System` |
| `tasks.Ticket.Node` | durable run current node, not poll output |
| `tasks.Report` | `run` orchestration using task `ApplyTaskConfig` and `Comment` primitives |
| `runner.Runner.Spawn` | `harness.BuildCommand` plus `runner.EnsureTerminal` |
| `runner/orca.buildCommand` | `harness/opencode.BuildCommand` |
| in-memory `nudged` map | harness plugin behavior plus durable node visit |
| `server.entries` | workflow/repo registries plus durable executor |
| flag-based legacy report | JSON `run.ReportRequest` |
| cwd-based submit repo discovery | registered repo names in workflow YAML |

Keep these existing seams:

- move `acli.Client` under `task/jira/acli`;
- move `orcacli.Client` under `runner/orca/orcacli`;
- move `opencode.Exists` under `harness/opencode`;
- Unix socket and flock single-instance behavior;
- strict YAML decoding;
- adapter registration;
- fake CLI interfaces in adapter tests.

Replace `internal/daemon`; incrementally expanding it would retain the wrong per-workflow polling and in-memory execution ownership.

## KISS Decisions

- One task plugin, runner plugin, and harness plugin are selected per machine.
- One task-system instance and Repo Poller exist per registered repo.
- One generic durable workflow function handles every ticket run.
- One workflow worker object and one activity worker object use bounded concurrency.
- One derived `relay_runs` table supports query APIs; workflow history remains the transition authority and no generic ORM is added.
- No workflow versions; updates require zero active runs.
- No parallel graph branches; every report selects one next node.
- No manual pause/resume API.
- No automatic progression from manual subtask status changes.
- No rollback compensation.
- No plugin access to SQLite and no JSONL report outbox.
- No repo-level runner or harness config.
- No dynamic external Go plugin loading in this redesign.

## Cross-Validation Notes

These tracker statements must be interpreted as follows during implementation:

- A node transition means ordered, separately durable activities and roll-forward recovery, not an ACID transaction across Jira and the runner.
- “SQLite-backed queues” means `go-workflows` persists workflow/activity tasks in its SQLite backend; relay-flow does not implement another queue.
- “10 Workflow Workers / 20 Activity Workers” are maximum parallel tasks on two worker objects.
- `go-workflows` native retries are finite and have no jitter; private typed retry loops use the shared `BackoffPolicy` with durable workflow timers.
- Already-running activities cannot be interrupted by cancellation.
- A workflow snapshot is persisted as run input for deterministic replay, but users cannot maintain multiple workflow versions; submission replacement is blocked while runs are active.
- `wf:<name>` labels on parent and mailbox subtasks recover claim-before-run crashes and identify runs for explicit `serve --recover` after database loss.
