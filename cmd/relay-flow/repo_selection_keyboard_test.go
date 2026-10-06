package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/server"
	"github.com/rajpopat27/relay-flow/internal/task"
)

func repoKeyboardField(candidates []runner.RepoCandidate, selected *[]int) huh.Field {
	field := repoMultiSelect(repoSelectionOptions(candidates), selected)
	// Apply the same setup that Huh's form/group applies to a field.
	huh.NewForm(huh.NewGroup(field))
	field.Focus()
	return field
}

func sendRepoKeys(t *testing.T, field huh.Field, keys ...tea.KeyMsg) {
	t.Helper()
	for _, key := range keys {
		model, _ := field.Update(key)
		if model != field {
			t.Fatal("keyboard update replaced the production selection field")
		}
	}
}

func TestRepoSelectionFormCtrlAEnter(t *testing.T) {
	var selected []int
	field := repoMultiSelect(repoSelectionOptions([]runner.RepoCandidate{
		{Name: "payments", Path: "/work/payments"},
		{Name: "checkout", Path: "/work/checkout"},
	}), &selected)
	form := huh.NewForm(huh.NewGroup(field)).
		WithProgramOptions(tea.WithoutRenderer(), tea.WithoutSignalHandler()).
		WithInput(strings.NewReader("\x01\r")).
		WithOutput(io.Discard).
		WithTimeout(3 * time.Second)
	if err := form.Run(); err != nil {
		t.Fatal(err)
	}
	if form.State != huh.StateCompleted || fmt.Sprint(selected) != "[0 1]" {
		t.Fatalf("Ctrl+A/Enter: form state=%v selected=%v", form.State, selected)
	}
}

func TestRepoSelectionKeyboardBulkToggle(t *testing.T) {
	candidates := []runner.RepoCandidate{
		{Name: "payments", Path: "/work/payments"},
		{Name: "checkout", Path: "/work/checkout"},
	}
	for _, count := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("%d repositories", count), func(t *testing.T) {
			var selected []int
			field := repoKeyboardField(candidates[:count], &selected)
			all := make([]int, count)
			for i := range all {
				all[i] = i
			}
			for _, want := range []string{fmt.Sprint(all), "[]"} {
				sendRepoKeys(t, field, tea.KeyMsg{Type: tea.KeyCtrlA})
				if fmt.Sprint(selected) != want {
					t.Fatalf("Ctrl+A selected %v, want %s (Add must never be bulk-selected)", selected, want)
				}
			}
			sendRepoKeys(t, field, tea.KeyMsg{Type: tea.KeyEnter})
			if field.Error() == nil {
				t.Fatal("Enter accepted an empty repository selection")
			}
			// Explicit Space on Add remains available even without candidates.
			sendRepoKeys(t, field, tea.KeyMsg{Type: tea.KeyEnd}, tea.KeyMsg{Type: tea.KeySpace}, tea.KeyMsg{Type: tea.KeyEnter})
			if field.Error() != nil || fmt.Sprint(selected) != "[-1]" {
				t.Fatalf("explicit Add: selected=%v error=%v", selected, field.Error())
			}
		})
	}
}

func TestRepoSelectionKeyboardFilteredBulkToggle(t *testing.T) {
	candidates := []runner.RepoCandidate{
		{Name: "payments", Path: "/work/payments"},
		{Name: "checkout", Path: "/work/checkout"},
		{Name: "payments-api", Path: "/work/payments-api"},
	}
	for _, filter := range []string{"", "payments", "repository", "does-not-match"} {
		for _, editing := range []bool{false, true} {
			t.Run(fmt.Sprintf("filter=%s/editing=%t", filter, editing), func(t *testing.T) {
				selected := []int{1} // A selection outside the payments filter must survive.
				field := repoKeyboardField(candidates, &selected)
				sendRepoKeys(t, field, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(filter)})
				if !editing {
					sendRepoKeys(t, field, tea.KeyMsg{Type: tea.KeyEnter})
				}
				wants := []string{"[1]", "[1]"}
				switch filter {
				case "":
					wants = []string{"[0 1 2]", "[]"}
				case "payments":
					wants = []string{"[0 1 2]", "[1]"}
				case "does-not-match":
					if !editing {
						// Huh clears a no-match filter when Enter commits it.
						wants = []string{"[0 1 2]", "[]"}
					}
				}
				for _, want := range wants {
					sendRepoKeys(t, field, tea.KeyMsg{Type: tea.KeyCtrlA})
					if fmt.Sprint(selected) != want {
						t.Fatalf("Ctrl+A selected %v, want %s", selected, want)
					}
				}
			})
		}
	}
}

func TestRepoSelectionKeyboardBulkProceedsWithoutAdding(t *testing.T) {
	for _, count := range []int{1, 2} {
		t.Run(fmt.Sprintf("%d repositories", count), func(t *testing.T) {
			home := t.TempDir()
			deps := serveRegister(t, home, []string{"project", "component"})
			deps.candidates = []runner.RepoCandidate{
				{Name: "payments", Path: "/work/payments"},
				{Name: "checkout", Path: "/work/checkout"},
			}[:count]
			client := server.NewClient(filepath.Join(home, ".relay-flow", "server.sock"))
			candidates, selected, err := selectReposInteractiveWithPrompts(context.Background(), client, deps.candidates, nil,
				func(candidates []runner.RepoCandidate, selected []int) ([]int, error) {
					field := repoKeyboardField(candidates, &selected)
					sendRepoKeys(t, field, tea.KeyMsg{Type: tea.KeyCtrlA}, tea.KeyMsg{Type: tea.KeyEnter})
					return selected, field.Error()
				},
				func() (runner.RepoCandidate, error) {
					t.Fatal("Ctrl+A/Enter opened the path/name prompt")
					return runner.RepoCandidate{}, nil
				})
			if err != nil {
				t.Fatal(err)
			}
			registration := task.Registration{Fields: []task.RegistrationField{
				{Key: "project", Title: "Project"},
				{Key: "component", Title: "Component", Derived: true},
			}}
			if err := registerSelectedReposDynamic(context.Background(), client, candidates, selected, registration, kvFlags{"project": "PAY"}); err != nil {
				t.Fatal(err)
			}
			if len(deps.ensureCalls) != 0 || len(deps.successful) != count {
				t.Fatalf("ensure calls=%v registrations=%v", deps.ensureCalls, deps.successful)
			}
			for _, input := range deps.successful {
				if input.TaskConfig["project"] != "PAY" || input.TaskConfig["component"] != input.Name {
					t.Fatalf("incorrect shared mapping: %+v", input)
				}
			}
		})
	}
}

func TestRepoSelectionKeyboardSpacePreservesExplicitAdd(t *testing.T) {
	selected := []int{0}
	field := repoKeyboardField([]runner.RepoCandidate{{Name: "payments", Path: "/work/payments"}}, &selected)
	sendRepoKeys(t, field, tea.KeyMsg{Type: tea.KeyEnd}, tea.KeyMsg{Type: tea.KeySpace}, tea.KeyMsg{Type: tea.KeyCtrlA}, tea.KeyMsg{Type: tea.KeyEnter})
	if fmt.Sprint(selected) != "[-1]" || field.Error() != nil {
		t.Fatalf("explicit Add was lost: selected=%v error=%v", selected, field.Error())
	}
	if !strings.Contains(field.View(), "Add repository") {
		t.Fatal("Add action is no longer visible")
	}
}
