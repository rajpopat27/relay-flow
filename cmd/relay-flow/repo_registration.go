package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/repo"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/server"
	"github.com/rajpopat27/relay-flow/internal/task"
)

const addRepositorySelection = -1

func repoSelectionOptions(candidates []runner.RepoCandidate) []huh.Option[int] {
	options := make([]huh.Option[int], 0, len(candidates)+1)
	for i, candidate := range candidates {
		label := strings.TrimSpace(candidate.Name)
		if label == "" {
			label = candidate.Path
		}
		options = append(options, huh.NewOption(label+"  ("+candidate.Path+")", i))
	}
	options = append(options, huh.NewOption("[+] Add repository", addRepositorySelection))
	return options
}

func canonicalRepoPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	absolute = filepath.Clean(absolute)
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return filepath.Clean(resolved)
	}
	return absolute
}

func selectedRepoPaths(candidates []runner.RepoCandidate, selected []int) []string {
	paths := make([]string, 0, len(selected))
	seen := map[string]bool{}
	for _, index := range selected {
		if index < 0 || index >= len(candidates) {
			continue
		}
		path := canonicalRepoPath(candidates[index].Path)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	return paths
}

func selectedRepoIndices(candidates []runner.RepoCandidate, paths []string) []int {
	wanted := map[string]bool{}
	for _, path := range paths {
		if path != "" {
			wanted[path] = true
		}
	}
	selected := make([]int, 0, len(wanted))
	for i, candidate := range candidates {
		if wanted[canonicalRepoPath(candidate.Path)] {
			selected = append(selected, i)
		}
	}
	return selected
}

func candidateAtPath(candidates []runner.RepoCandidate, path string) bool {
	path = canonicalRepoPath(path)
	for _, candidate := range candidates {
		if canonicalRepoPath(candidate.Path) == path {
			return true
		}
	}
	return false
}

func mergeRepoCandidateNames(candidates []runner.RepoCandidate, names map[string]string) []runner.RepoCandidate {
	out := make([]runner.RepoCandidate, len(candidates))
	copy(out, candidates)
	for i := range out {
		if name := names[canonicalRepoPath(out[i].Path)]; name != "" {
			out[i].Name = name
		}
	}
	return out
}

func validateAddedRepo(candidate runner.RepoCandidate, candidates []runner.RepoCandidate, registered []repo.Info) error {
	candidate.Name = strings.TrimSpace(candidate.Name)
	candidate.Path = canonicalRepoPath(candidate.Path)
	if candidate.Path == "" {
		return fmt.Errorf("repository path is required")
	}
	if candidate.Name == "" {
		return fmt.Errorf("repository name is required")
	}
	for _, existing := range candidates {
		if canonicalRepoPath(existing.Path) == candidate.Path {
			return fmt.Errorf("repository path %q is already discovered as %q", candidate.Path, existing.Name)
		}
		if strings.TrimSpace(existing.Name) == candidate.Name {
			return fmt.Errorf("repository name %q is already discovered", candidate.Name)
		}
	}
	for _, existing := range registered {
		if canonicalRepoPath(existing.Path) == candidate.Path {
			return fmt.Errorf("repository path %q is already registered as %q", candidate.Path, existing.Name)
		}
		if strings.TrimSpace(existing.Name) == candidate.Name {
			return fmt.Errorf("repository name %q is already registered", candidate.Name)
		}
	}
	return nil
}

func promptAddedRepo() (runner.RepoCandidate, error) {
	var path, name string
	pathInput := huh.NewInput().Title("Repository path").Value(&path).Validate(func(value string) error {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("repository path is required")
		}
		return nil
	})
	nameInput := huh.NewInput().Title("Registered name").Value(&name).Validate(func(value string) error {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("repository name is required")
		}
		return nil
	})
	if err := huh.NewForm(huh.NewGroup(pathInput, nameInput)).Run(); err != nil {
		return runner.RepoCandidate{}, err
	}
	return runner.RepoCandidate{Name: strings.TrimSpace(name), Path: canonicalRepoPath(path)}, nil
}

type repoSelectionPrompt func([]runner.RepoCandidate, []int) ([]int, error)
type repoAddPrompt func() (runner.RepoCandidate, error)

func selectReposInteractive(ctx context.Context, c *server.Client, candidates []runner.RepoCandidate, registered []repo.Info) ([]runner.RepoCandidate, []int, error) {
	return selectReposInteractiveWithPrompts(ctx, c, candidates, registered, promptRepoSelection, promptAddedRepo)
}

func promptRepoSelection(candidates []runner.RepoCandidate, selected []int) ([]int, error) {
	pick := huh.NewForm(huh.NewGroup(repoMultiSelect(repoSelectionOptions(candidates), &selected)))
	if err := pick.Run(); err != nil {
		return nil, err
	}
	return selected, nil
}

func selectReposInteractiveWithPrompts(ctx context.Context, c *server.Client, candidates []runner.RepoCandidate, registered []repo.Info, selectPrompt repoSelectionPrompt, addPrompt repoAddPrompt) ([]runner.RepoCandidate, []int, error) {
	selectedPaths := []string{}
	nameOverrides := map[string]string{}
	addedCandidates := []runner.RepoCandidate{}
	for {
		candidates = mergeRepoCandidateNames(candidates, nameOverrides)
		selected, err := selectPrompt(candidates, selectedRepoIndices(candidates, selectedPaths))
		if err != nil {
			return nil, nil, err
		}
		selectedPaths = selectedRepoPaths(candidates, selected)
		add := false
		for _, value := range selected {
			if value == addRepositorySelection {
				add = true
				break
			}
		}
		if !add {
			if len(selectedPaths) == 0 {
				return nil, nil, fmt.Errorf("select at least one repository")
			}
			return candidates, selectedRepoIndices(candidates, selectedPaths), nil
		}

		added, err := addPrompt()
		if err != nil {
			return nil, nil, err
		}
		knownCandidates := append(append([]runner.RepoCandidate(nil), candidates...), addedCandidates...)
		if err := validateAddedRepo(added, knownCandidates, registered); err != nil {
			return nil, nil, err
		}
		if err := c.EnsureRepo(ctx, added); err != nil {
			return nil, nil, fmt.Errorf("ensure repository %q: %w", added.Name, err)
		}
		path := canonicalRepoPath(added.Path)
		nameOverrides[path] = added.Name
		addedCandidates = append(addedCandidates, added)
		selectedPaths = append(selectedPaths, path)

		refreshed, err := c.DiscoverRepos(ctx)
		if err != nil {
			return nil, nil, err
		}
		refreshed = mergeRepoCandidateNames(refreshed, nameOverrides)
		// Preserve selected entries across a runner refresh even if the
		// external runner's listing is briefly eventually consistent.
		for _, old := range candidates {
			oldPath := canonicalRepoPath(old.Path)
			if containsPath(selectedPaths, oldPath) && !candidateAtPath(refreshed, oldPath) {
				refreshed = append(refreshed, old)
			}
		}
		if !candidateAtPath(refreshed, path) {
			refreshed = append(refreshed, added)
		}
		candidates = refreshed
	}
}

func containsPath(paths []string, want string) bool {
	for _, path := range paths {
		if path == want {
			return true
		}
	}
	return false
}

// loadRepoRegistration asks the selected task plugin for the fields needed by
// registration. A second pass lets a plugin expose dependent choices after a
// value such as a project has been supplied.
func loadRepoRegistration(ctx context.Context, c *server.Client, supplied kvFlags) (task.Registration, error) {
	initial, err := c.RepoRegistrationFields(ctx, nil)
	if err != nil {
		return task.Registration{}, err
	}
	if len(supplied) == 0 {
		return initial, nil
	}
	dependent, err := c.RepoRegistrationFields(ctx, flatRegistrationValues(supplied))
	if err != nil {
		return task.Registration{}, err
	}
	return mergeRegistration(initial, dependent), nil
}

func flatRegistrationValues(values kvFlags) config.RawValues {
	out := make(config.RawValues, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func mergeRegistration(first, second task.Registration) task.Registration {
	fields := make([]task.RegistrationField, 0, len(first.Fields)+len(second.Fields))
	positions := map[string]int{}
	for _, field := range append(append([]task.RegistrationField(nil), first.Fields...), second.Fields...) {
		if index, ok := positions[field.Key]; ok {
			fields[index] = field
			continue
		}
		positions[field.Key] = len(fields)
		fields = append(fields, field)
	}
	return task.Registration{Fields: fields}
}

// promptRepoRegistration renders only metadata returned by the task plugin.
// The core CLI does not know which values are Jira statuses or how they are
// validated.
func promptRepoRegistration(reg task.Registration, values kvFlags) error {
	for _, field := range reg.Fields {
		if field.Derived || values[field.Key] != "" {
			continue
		}
		title := field.Title
		if title == "" {
			title = field.Key
		}
		if len(field.Options) > 0 {
			selected := field.Default
			if err := huh.NewForm(huh.NewGroup(
				huh.NewSelect[string]().Title(title).Options(registrationSelectOptions(field)...).Value(&selected),
			)).Run(); err != nil {
				return err
			}
			if strings.TrimSpace(selected) == "" {
				return fmt.Errorf("task field %q requires a value", field.Key)
			}
			values[field.Key] = selected
			continue
		}
		value := field.Default
		input := huh.NewInput().Title(title).Value(&value).Validate(func(value string) error {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("%s is required", title)
			}
			return nil
		})
		if err := huh.NewForm(huh.NewGroup(input)).Run(); err != nil {
			return err
		}
		values[field.Key] = value
	}
	return nil
}

func registrationSelectOptions(field task.RegistrationField) []huh.Option[string] {
	options := make([]huh.Option[string], 0, len(field.Options)+1)
	if field.Default == "" {
		// Huh selects the first option when Enter is pressed. Keep an explicit
		// empty choice first so a missing conventional default cannot silently
		// become the first real Jira status.
		options = append(options, huh.NewOption("Select a value...", ""))
	}
	for _, option := range field.Options {
		options = append(options, huh.NewOption(option, option))
	}
	return options
}

func registrationTaskConfig(reg task.Registration, supplied kvFlags, repoName string) (config.RawValues, error) {
	known := make(map[string]task.RegistrationField, len(reg.Fields))
	for _, field := range reg.Fields {
		if strings.TrimSpace(field.Key) == "" {
			return nil, errorsForRegistration("task plugin returned an empty registration key")
		}
		if _, exists := known[field.Key]; exists {
			return nil, errorsForRegistration(fmt.Sprintf("duplicate task registration key %q", field.Key))
		}
		known[field.Key] = field
	}
	out := config.RawValues{}
	for key, value := range supplied {
		field, ok := known[key]
		if !ok {
			return nil, errorsForRegistration(fmt.Sprintf("unknown task key %q", key))
		}
		if field.Derived {
			return nil, errorsForRegistration(fmt.Sprintf("task key %q is derived and cannot be overridden", key))
		}
		if strings.TrimSpace(value) == "" {
			return nil, errorsForRegistration(fmt.Sprintf("task key %q requires a non-empty value", key))
		}
		if len(field.Options) > 0 && !containsRegistrationOption(field.Options, value) {
			return nil, errorsForRegistration(fmt.Sprintf("task key %q value %q is not one of the available choices", key, value))
		}
		if err := setRegistrationValue(out, key, value); err != nil {
			return nil, err
		}
	}
	for _, field := range reg.Fields {
		if field.Derived {
			if err := setRegistrationValue(out, field.Key, repoName); err != nil {
				return nil, err
			}
			continue
		}
		if registrationValue(out, field.Key) == "" {
			return nil, errorsForRegistration(fmt.Sprintf("missing required task key %q", field.Key))
		}
	}
	return out, nil
}

func errorsForRegistration(message string) error { return fmt.Errorf("%s", message) }

func containsRegistrationOption(options []string, value string) bool {
	for _, option := range options {
		if option == value {
			return true
		}
	}
	return false
}

func registrationValue(values config.RawValues, key string) string {
	parts := strings.Split(key, ".")
	var current any = map[string]any(values)
	for _, part := range parts {
		var next map[string]any
		switch value := current.(type) {
		case map[string]any:
			next = value
		case config.RawValues:
			next = map[string]any(value)
		default:
			return ""
		}
		value, ok := next[part]
		if !ok {
			return ""
		}
		current = value
	}
	value, _ := current.(string)
	return strings.TrimSpace(value)
}

func setRegistrationValue(values config.RawValues, key, value string) error {
	parts := strings.Split(key, ".")
	if len(parts) == 0 || parts[0] == "" {
		return errorsForRegistration("task plugin returned an invalid registration key")
	}
	current := values
	for _, part := range parts[:len(parts)-1] {
		if part == "" {
			return errorsForRegistration(fmt.Sprintf("task plugin returned an invalid registration key %q", key))
		}
		existing, ok := current[part]
		if !ok {
			nested := map[string]any{}
			current[part] = nested
			current = nested
			continue
		}
		var nested map[string]any
		switch typed := existing.(type) {
		case map[string]any:
			nested = typed
		case config.RawValues:
			nested = map[string]any(typed)
			current[part] = nested
		default:
			return errorsForRegistration(fmt.Sprintf("task registration key %q conflicts with another value", key))
		}
		current = nested
	}
	current[parts[len(parts)-1]] = value
	return nil
}

func registerSelectedReposDynamic(ctx context.Context, c *server.Client, candidates []runner.RepoCandidate, selected []int, reg task.Registration, values kvFlags) error {
	for _, index := range selected {
		candidate := candidates[index]
		taskCfg, err := registrationTaskConfig(reg, values, candidate.Name)
		if err != nil {
			return fmt.Errorf("%s: %w", candidate.Name, err)
		}
		info, err := c.RegisterRepo(ctx, repo.RegisterInput{Name: candidate.Name, Path: candidate.Path, TaskConfig: taskCfg})
		if err != nil {
			return fmt.Errorf("%s: %w", candidate.Name, err)
		}
		fmt.Println(info.Name)
	}
	return nil
}
