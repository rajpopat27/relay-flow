package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/repo"
	"github.com/rajpopat27/relay-flow/internal/task"
	"github.com/rajpopat27/relay-flow/internal/workflow"
)

type rejectingSubmissionTask struct {
	*scenarioTaskSystem
	failure error
}

func (s *rejectingSubmissionTask) ValidateConfig(ctx context.Context, root config.RawValues, nodes map[string]config.RawValues) error {
	if s.failure != nil {
		return s.failure
	}
	return s.scenarioTaskSystem.ValidateConfig(ctx, root, nodes)
}

type rejectingSubmissionRunner struct {
	*scenarioRunner
	failure error
}

func (r *rejectingSubmissionRunner) ValidateRepo(ctx context.Context, name, path string) error {
	if r.failure != nil {
		return r.failure
	}
	return r.scenarioRunner.ValidateRepo(ctx, name, path)
}

type rejectingSubmissionHarness struct {
	*scenarioHarness
	failure error
}

func (h *rejectingSubmissionHarness) ValidateAgent(ctx context.Context, path, agent string) error {
	if h.failure != nil {
		return h.failure
	}
	return h.scenarioHarness.ValidateAgent(ctx, path, agent)
}

type submissionActiveRuns struct{}

func (submissionActiveRuns) HasActiveWorkflow(context.Context, string) (bool, error) {
	return false, nil
}

func TestStartupPrerequisitesDoNotBypassSubmissionValidationOrReplaceAcceptedState(t *testing.T) {
	for _, stage := range []string{"task-config", "runner-repo", "harness-agent"} {
		t.Run(stage, func(t *testing.T) {
			f := newPrerequisiteFixture(t, nil)
			sys := &rejectingSubmissionTask{scenarioTaskSystem: newScenarioTaskSystem(f.log)}
			rnr := &rejectingSubmissionRunner{scenarioRunner: newScenarioRunner(f.log)}
			hrn := &rejectingSubmissionHarness{scenarioHarness: newScenarioHarness(f.log)}
			registry := repo.NewRegistry()
			registered := &repo.Repo{Name: scenarioRepo, Path: scenarioRepoPath, TaskSystem: sys}
			registry.Replace(registered)
			store := &workflow.Store{Dir: f.paths.Workflows}
			service := workflow.NewService(store, submissionActiveRuns{}, repoExists{registry})
			service.ValidateTaskConfig = workflowConfigValidator(registry)
			service.ValidateSubmission = workflowSubmissionValidator(registry, rnr, hrn)
			service.Rebind = func() error { return registry.BindWorkflows(service.Registry().List()) }
			accepted, err := service.Submit(context.Background(), scenarioWorkflowYAML)
			if err != nil {
				t.Fatal(err)
			}
			bindings := registered.Bindings()
			yamlPath := filepath.Join(store.Dir, scenarioWorkflowName+".yaml")
			hashPath := filepath.Join(store.Dir, scenarioWorkflowName+".sha256")
			beforeYAML := readFile(t, yamlPath)
			beforeHash := readFile(t, hashPath)
			failure := errors.New("candidate-specific validation rejected")
			switch stage {
			case "task-config":
				sys.failure = failure
			case "runner-repo":
				rnr.failure = failure
			case "harness-agent":
				hrn.failure = failure
			}
			// Basic machine health still succeeds; it does not authorize a
			// candidate that fails the full repo/task/agent validation path.
			if err := checkStartupPrerequisites(context.Background(), f.cfg, rnr, hrn); err != nil {
				t.Fatal(err)
			}
			candidate := []byte(strings.Replace(string(scenarioWorkflowYAML), "Implement the requested change.", "Rejected replacement description.", 1))
			if _, err := service.Submit(context.Background(), candidate); !errors.Is(err, failure) {
				t.Fatalf("submission bypassed %s validation: %v", stage, err)
			}
			if readFile(t, yamlPath) != beforeYAML || readFile(t, hashPath) != beforeHash {
				t.Fatal("failed submission replaced accepted YAML/hash")
			}
			current, err := service.Get(scenarioWorkflowName)
			if err != nil || current != accepted || !current.IsRoutable() {
				t.Fatalf("failed submission replaced accepted in-memory definition: %v", err)
			}
			after := registered.Bindings()
			if len(bindings) != 1 || len(after) != 1 || after[0].Workflow != bindings[0].Workflow || after[0].Workflow != accepted {
				t.Fatal("failed submission changed routing bindings")
			}
			if matches := after[0].Match(task.Ticket{Key: scenarioTicket}); !matches {
				t.Fatal("accepted routing matcher no longer works")
			}
			if transactions, err := filepath.Glob(filepath.Join(store.Dir, "*.txn")); err != nil || len(transactions) != 0 {
				t.Fatalf("failed validation attempted publication: %v, %v", transactions, err)
			}
			if _, err := os.Stat(yamlPath); err != nil {
				t.Fatal(err)
			}
		})
	}
}
