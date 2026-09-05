package temporal

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rajpopat27/relay-flow/internal/execution/projection"
	"github.com/rajpopat27/relay-flow/internal/identity"
	"github.com/rajpopat27/relay-flow/internal/repo"
	"github.com/rajpopat27/relay-flow/internal/retry"
	"github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
	workflowservice "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	temporalSDK "go.temporal.io/sdk/temporal"
	"google.golang.org/protobuf/types/known/durationpb"
	_ "modernc.org/sqlite"
)

func TestTemporalNewRejectsIdentityMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	if err := projection.InitDatabaseWithIdentity(path, projection.ExecutorIdentity{ExecutorPlugin: "temporal", TemporalAddress: "localhost:7233", TemporalNamespace: "identity-test"}); err != nil {
		t.Fatal(err)
	}
	_, err := New(path, Dependencies{Repos: repo.NewRegistry(), Runner: &lagRunner{}, Harness: &lagHarness{}, TemporalAddress: "other-host:7233", TemporalNamespace: "identity-test"})
	if !errors.Is(err, projection.ErrIdentityMismatch) {
		t.Fatalf("identity mismatch error = %v", err)
	}
}

func TestTemporalNewRejectsLegacyMarkerlessProjection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	if err := projection.InitDatabase(path); err != nil {
		t.Fatal(err)
	}
	_, err := New(path, Dependencies{Repos: repo.NewRegistry(), Runner: &lagRunner{}, Harness: &lagHarness{}, TemporalAddress: "localhost:7233", TemporalNamespace: "legacy-test"})
	if !errors.Is(err, projection.ErrIdentityMissing) {
		t.Fatalf("legacy markerless error = %v", err)
	}
}

func TestTemporalWorkflowAndActivityRegistrationAreSeparate(t *testing.T) {
	if _, ok := reflect.TypeOf(Activities{}).MethodByName("TicketWorkflow"); ok {
		t.Fatal("Temporal activity struct exposes TicketWorkflow")
	}
	if reflect.TypeOf(TicketWorkflow).Kind() != reflect.Func {
		t.Fatal("TicketWorkflow is not a package-level function")
	}
}

func TestTemporalWorkerAndActivityOptionsMatchMVP(t *testing.T) {
	opts := workerOptions(nil, false)
	if opts.MaxConcurrentWorkflowTaskExecutionSize != 10 || opts.MaxConcurrentActivityExecutionSize != 20 {
		t.Fatalf("execution limits = workflow %d/activity %d", opts.MaxConcurrentWorkflowTaskExecutionSize, opts.MaxConcurrentActivityExecutionSize)
	}
	if opts.MaxConcurrentWorkflowTaskPollers != 2 || opts.MaxConcurrentActivityTaskPollers != 2 {
		t.Fatalf("poller limits = workflow %d/activity %d", opts.MaxConcurrentWorkflowTaskPollers, opts.MaxConcurrentActivityTaskPollers)
	}
	if opts.WorkerStopTimeout != 30*time.Second || opts.LocalActivityWorkerOnly {
		t.Fatalf("normal worker options = %+v", opts)
	}
	activity := temporalActivityOptions
	if activity.StartToCloseTimeout != 5*time.Minute || !activity.WaitForCancellation || activity.RetryPolicy == nil || activity.RetryPolicy.MaximumAttempts != 1 {
		t.Fatalf("activity options = %+v", activity)
	}
}

func TestRuntimeBindingCleanupUpdatesTemporalSnapshot(t *testing.T) {
	state := &workflowState{bindings: map[string]NodeRuntimeBinding{
		"node": {Node: "node", TerminalID: "term", SessionID: "session", NodeVisitID: "visit"},
	}}
	applyRuntimePolicy(state, "node", run.RuntimePolicy{})
	binding := state.bindings["node"]
	if binding.TerminalID != "" || binding.SessionID != "" {
		t.Fatalf("cleared runtime binding = %+v", binding)
	}
	state.bindings["node"] = NodeRuntimeBinding{Node: "node", TerminalID: "term", SessionID: "session", NodeVisitID: "visit"}
	applyRuntimePolicy(state, "node", run.RuntimePolicy{KeepTerminalsAlive: true, KeepSessionsAlive: true})
	if got := state.bindings["node"]; got.TerminalID != "term" || got.SessionID != "session" {
		t.Fatalf("preserved runtime binding = %+v", got)
	}
}

func TestTemporalClientOptionsUseConfiguredIdentity(t *testing.T) {
	options := temporalClientOptions(Dependencies{TemporalAddress: "127.0.0.1:7233", TemporalNamespace: "relay-test"})
	if options.HostPort != "127.0.0.1:7233" || options.Namespace != "relay-test" {
		t.Fatalf("Temporal client options = %+v", options)
	}
}

func TestClassifyTemporalActivityErrors(t *testing.T) {
	conflict := temporalSDK.NewApplicationError("manual conflict", string(retry.Conflict))
	if got := classifyTemporalError(conflict); got.Kind != retry.Conflict {
		t.Fatalf("Temporal conflict classification = %+v", got)
	}
	transient := temporalSDK.NewApplicationError("temporary failure", string(retry.Transient))
	if got := classifyTemporalError(transient); got.Kind != retry.Transient {
		t.Fatalf("Temporal transient classification = %+v", got)
	}
	wrapped := retry.ConflictError(errors.New("wrapped conflict"))
	if got := classifyTemporalError(wrapped); got.Kind != retry.Conflict {
		t.Fatalf("wrapped conflict classification = %+v", got)
	}
}

func TestTemporalRetentionSeparatesLocalAndNamespacePolicies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	if err := projection.InitDatabaseWithIdentity(path, projection.ExecutorIdentity{ExecutorPlugin: "temporal", TemporalAddress: "localhost:7233", TemporalNamespace: "retention-separation"}); err != nil {
		t.Fatal(err)
	}
	engine, err := New(path, Dependencies{Repos: repo.NewRegistry(), Runner: &lagRunner{}, Harness: &lagHarness{}, RetentionDays: 1, TemporalAddress: "localhost:7233", TemporalNamespace: "retention-separation"})
	if err != nil {
		t.Fatal(err)
	}
	if engine.retention != 24*time.Hour || engine.namespaceRetention != 30*24*time.Hour {
		t.Fatalf("retention policies = local %s/namespace %s", engine.retention, engine.namespaceRetention)
	}
	if err := engine.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestTemporalStartRejectsShortRetentionNamespace(t *testing.T) {
	if os.Getenv("RELAY_FLOW_TEMPORAL_LIVE") != "1" {
		t.Skip("set RELAY_FLOW_TEMPORAL_LIVE=1 to run namespace retention startup coverage")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	namespace := "relay-flow-short-retention-" + string(identity.NewNodeVisitID())[:12]
	manager, err := client.NewNamespaceClient(client.Options{HostPort: "localhost:7233"})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := manager.Register(ctx, &workflowservice.RegisterNamespaceRequest{
		Namespace: namespace, Description: "short retention test", WorkflowExecutionRetentionPeriod: durationpb.New(24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.db")
	if err := projection.InitDatabaseWithIdentity(path, projection.ExecutorIdentity{ExecutorPlugin: "temporal", TemporalAddress: "localhost:7233", TemporalNamespace: namespace}); err != nil {
		t.Fatal(err)
	}
	engine, err := New(path, Dependencies{Repos: repo.NewRegistry(), Runner: &lagRunner{}, Harness: &lagHarness{}, TemporalAddress: "localhost:7233", TemporalNamespace: namespace})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(ctx); err == nil || !strings.Contains(err.Error(), "below required") {
		t.Fatalf("Start short-retention namespace error = %v", err)
	}
	if engine.worker != nil {
		t.Fatal("short-retention startup created a worker")
	}
	if err := engine.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if db, err := sql.Open("sqlite", path); err != nil {
		t.Fatal(err)
	} else {
		_ = db.Close()
	}
}

func TestTemporalUnavailableFailsWithoutEmbeddedFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	if err := projection.InitDatabaseWithIdentity(path, projection.ExecutorIdentity{ExecutorPlugin: "temporal", TemporalAddress: "127.0.0.1:1", TemporalNamespace: "unavailable-test"}); err != nil {
		t.Fatal(err)
	}
	engine, err := New(path, Dependencies{Repos: repo.NewRegistry(), Runner: &lagRunner{}, Harness: &lagHarness{}, TemporalAddress: "127.0.0.1:1", TemporalNamespace: "unavailable-test"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := engine.Start(ctx); err == nil {
		t.Fatal("unavailable Temporal endpoint unexpectedly started")
	}
	if engine.worker != nil {
		t.Fatal("unavailable Temporal endpoint started a worker")
	}
	if err := engine.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var embedded int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='instances'`).Scan(&embedded); err != nil {
		t.Fatal(err)
	}
	if embedded != 0 {
		t.Fatal("unavailable Temporal startup created embedded go-workflows tables")
	}
}

func TestTemporalLocalRetentionKeepsActiveRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	if err := projection.InitDatabaseWithIdentity(path, projection.ExecutorIdentity{ExecutorPlugin: "temporal", TemporalAddress: "localhost:7233", TemporalNamespace: "retention-test"}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	proj := &projection.RunProjection{DB: db}
	old := run.Start{ID: "repo/wf/old", Repo: "repo", Workflow: workflow.Workflow{Name: "wf"}, Ticket: task.TicketRef{ID: "old", Key: "OLD"}}
	active := run.Start{ID: "repo/wf/active", Repo: "repo", Workflow: workflow.Workflow{Name: "wf"}, Ticket: task.TicketRef{ID: "active", Key: "ACTIVE"}}
	if err := proj.InsertStart(context.Background(), old, time.Now().UTC().Add(-72*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := proj.InsertStart(context.Background(), active, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	finished := time.Now().UTC().Add(-48 * time.Hour)
	if err := proj.UpdateState(context.Background(), old.ID, run.StateCompleted, "", &finished); err != nil {
		t.Fatal(err)
	}
	if err := proj.UpdateState(context.Background(), active.ID, run.StateWaiting, "", nil); err != nil {
		t.Fatal(err)
	}
	removed, err := proj.SweepRetention(context.Background(), time.Now().UTC().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != string(old.ID) {
		t.Fatalf("retention removed = %v", removed)
	}
	if _, err := proj.Get(context.Background(), active.ID); err != nil {
		t.Fatalf("active row removed by retention: %v", err)
	}
	if _, err := proj.Get(context.Background(), old.ID); !projection.IsNotFound(err) {
		t.Fatalf("old row remains after retention: %v", err)
	}
}

func TestTemporalEngineShutdownIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	if err := projection.InitDatabaseWithIdentity(path, projection.ExecutorIdentity{
		ExecutorPlugin: "temporal", TemporalAddress: "localhost:7233", TemporalNamespace: "shutdown-test",
	}); err != nil {
		t.Fatal(err)
	}
	deps := Dependencies{
		Repos: repo.NewRegistry(), Runner: &lagRunner{}, Harness: &lagHarness{},
		TemporalAddress: "localhost:7233", TemporalNamespace: "shutdown-test",
	}
	engine, err := New(path, deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := engine.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown = %v", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var one int
	if err := db.QueryRow(`SELECT 1`).Scan(&one); err != nil || one != 1 {
		t.Fatalf("database after idempotent shutdown = %d, %v", one, err)
	}
}
