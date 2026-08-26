// Command relay-flow is the entrypoint. It is a thin command parser
// (standard flag package) with zero business logic: every command
// delegates to the server client (internal/server.Client) or the
// section-5 composition root for init/serve. The testable entry is
// run(args, stdin) int (allowed seam e).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/mattn/go-isatty"
	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/execution/goworkflows"
	"github.com/rajpopat27/relay-flow/internal/harness"
	"github.com/rajpopat27/relay-flow/internal/logging"
	"github.com/rajpopat27/relay-flow/internal/paths"
	"github.com/rajpopat27/relay-flow/internal/repo"
	runsvc "github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/runner"
	"github.com/rajpopat27/relay-flow/internal/server"
	"github.com/rajpopat27/relay-flow/internal/task"

	// Adapter registrations (factories registered via init for plugin
	// name validation at init-time).
	_ "github.com/rajpopat27/relay-flow/internal/harness/opencode"
	_ "github.com/rajpopat27/relay-flow/internal/runner/orca"
	_ "github.com/rajpopat27/relay-flow/internal/task/jira"
)

// Exit codes per specs/workflow-repo-management "CLI exit codes are
// stable": 0 success, 2 command/flag usage, 1 server/validation/operation.
const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

func main() { os.Exit(run(os.Args[1:], os.Stdin)) }

// home returns the relay-flow root, honoring RELAY_FLOW_HOME (tests and
// non-standard installs) and falling back to ~/.relay-flow.
func home() (paths.Paths, error) {
	if env := os.Getenv("RELAY_FLOW_HOME"); env != "" {
		root := env
		return paths.Paths{
			Root:      root,
			Config:    filepath.Join(root, "config.yaml"),
			Workflows: filepath.Join(root, "workflows"),
			Database:  filepath.Join(root, "state.db"),
			Socket:    filepath.Join(root, "server.sock"),
			Lock:      filepath.Join(root, "server.lock"),
			ServerLog: filepath.Join(root, "server.log"),
			PluginLog: filepath.Join(root, "plugin.log"),
		}, nil
	}
	return paths.ForUserHome()
}

// run parses args and dispatches one command. It is the test seam: stdin
// is injectable so the report command can be driven in-process.
func run(args []string, stdin io.Reader) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return exitUsage
	}
	p, err := home()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitFail
	}
	client := server.NewClient(p.Socket)

	switch args[0] {
	case "init":
		return cmdInit(p, args[1:], stdin)
	case "serve":
		return cmdServe(p, args[1:])
	case "stop":
		fs := flag.NewFlagSet("stop", flag.ContinueOnError)
		if err := fs.Parse(args[1:]); err != nil {
			return exitUsage
		}
		if err := client.Stop(context.Background()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitFail
		}
		return exitOK
	case "report":
		fs := flag.NewFlagSet("report", flag.ContinueOnError)
		if err := fs.Parse(args[1:]); err != nil {
			return exitUsage
		}
		return cmdReport(client, stdin)
	case "runtime-register":
		fs := flag.NewFlagSet("runtime-register", flag.ContinueOnError)
		if err := fs.Parse(args[1:]); err != nil {
			return exitUsage
		}
		return cmdRuntimeRegister(client, stdin)
	case "workflow":
		return cmdWorkflow(client, args[1:])
	case "repo":
		return cmdRepo(client, args[1:], stdin)
	case "run":
		return cmdRun(client, args[1:])
	default:
		usage(os.Stderr)
		return exitUsage
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `relay-flow — durable ticket runner

Usage:
  relay-flow init [--task-plugin <name> --runner-plugin <name> --harness-plugin <name>]
  relay-flow serve [--recover] [--debug]
  relay-flow stop
  relay-flow report

  relay-flow workflow submit --file <path>
  relay-flow workflow remove --name <name>
  relay-flow workflow list
  relay-flow workflow get --name <name>

  relay-flow repo register [--name <name> --path <path> --set key=value ...]
  relay-flow repo remove --name <name>
  relay-flow repo list
  relay-flow repo get --name <name>

  relay-flow run list
  relay-flow run get --ticket <key>
  relay-flow run cancel --ticket <key>`)
}

// --- init / serve delegate to the section-5 composition root ---

// cmdInit is the init composition root (docs lines 950/1028): select the
// three plugin names, atomically write the machine config, and initialize
// the SQLite database. Refuses to overwrite existing config or history.
// Plugin selection precedence: --task-plugin/--runner-plugin/--harness-plugin
// flags (all three required for a fully non-interactive run) → huh form on
// a TTY → three stdin lines (task, runner, harness), the documented test
// seam and script path.
func cmdInit(p paths.Paths, args []string, stdin io.Reader) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	taskName := fs.String("task-plugin", "", "task plugin name (non-interactive)")
	runnerName := fs.String("runner-plugin", "", "runner plugin name (non-interactive)")
	harnessName := fs.String("harness-plugin", "", "harness plugin name (non-interactive)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	flagged := *taskName != "" || *runnerName != "" || *harnessName != ""
	if flagged && (*taskName == "" || *runnerName == "" || *harnessName == "") {
		fmt.Fprintln(os.Stderr, "init: --task-plugin, --runner-plugin, and --harness-plugin must be given together")
		return exitUsage
	}
	// Refuse to overwrite existing state.
	if _, err := os.Stat(p.Config); err == nil {
		fmt.Fprintln(os.Stderr, "init: already initialized (config exists): "+p.Config)
		return exitFail
	}
	if _, err := os.Stat(p.Database); err == nil {
		fmt.Fprintln(os.Stderr, "init: already initialized (database exists): "+p.Database)
		return exitFail
	}
	if err := os.MkdirAll(p.Root, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitFail
	}
	_ = os.Chmod(p.Root, 0o700)

	// Selection precedence: flags → TTY form → stdin lines. The stdin path
	// is the documented test seam and script path.
	var names []string
	switch {
	case flagged:
		names = []string{*taskName, *runnerName, *harnessName}
	case isTTY(stdin):
		var err error
		names, err = pickPluginsInteractive()
		if err != nil {
			fmt.Fprintln(os.Stderr, "init: "+err.Error())
			return exitFail
		}
	default:
		names = readPluginLines(stdin)
		if names == nil {
			fmt.Fprintln(os.Stderr, "init: expected three plugin selections (task, runner, harness) on stdin")
			return exitFail
		}
	}
	// Validate against the registered factories; unknown names list the
	// registered set per design (no silent acceptance).
	if err := task.ValidateName(names[0]); err != nil {
		fmt.Fprintln(os.Stderr, "init: task plugin: "+err.Error())
		return exitFail
	}
	if err := runner.ValidateName(names[1]); err != nil {
		fmt.Fprintln(os.Stderr, "init: runner plugin: "+err.Error())
		return exitFail
	}
	if err := harness.ValidateName(names[2]); err != nil {
		fmt.Fprintln(os.Stderr, "init: harness plugin: "+err.Error())
		return exitFail
	}

	cfg := &config.Machine{
		TaskPlugin:         names[0],
		RunnerPlugin:       names[1],
		HarnessPlugin:      names[2],
		KeepTerminalsAlive: true,
		KeepSessionsAlive:  true,
	}
	if err := config.SaveMachine(p.Config, cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitFail
	}
	if err := goworkflows.InitDatabase(p.Database); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitFail
	}
	return exitOK
}

// isTTY reports whether stdin is an interactive terminal.
func isTTY(stdin io.Reader) bool {
	f, ok := stdin.(*os.File)
	return ok && isatty.IsTerminal(f.Fd())
}

// pickPluginsInteractive runs the huh form: one searchable select per
// plugin (task, runner, harness); enter advances/submits.
func pickPluginsInteractive() ([]string, error) {
	names := make([]string, 3)
	form := huh.NewForm(
		huh.NewGroup(huh.NewSelect[string]().
			Title("Task plugin").
			Options(huh.NewOptions(task.Names()...)...).
			Filtering(true).
			Value(&names[0])),
		huh.NewGroup(huh.NewSelect[string]().
			Title("Runner plugin").
			Options(huh.NewOptions(runner.Names()...)...).
			Filtering(true).
			Value(&names[1])),
		huh.NewGroup(huh.NewSelect[string]().
			Title("Harness plugin").
			Options(huh.NewOptions(harness.Names()...)...).
			Filtering(true).
			Value(&names[2])),
	)
	if err := form.Run(); err != nil {
		return nil, err
	}
	return names, nil
}

// readPluginLines reads the three plugin names from stdin, one per line
// (task, runner, harness); trailing whitespace tolerated. Returns nil when
// fewer than three non-empty lines are present.
func readPluginLines(stdin io.Reader) []string {
	names := make([]string, 0, 3)
	scanner := bufio.NewScanner(stdin)
	for len(names) < 3 && scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		names = append(names, line)
	}
	if len(names) != 3 {
		return nil
	}
	return names
}

func cmdServe(p paths.Paths, args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	recover := fs.Bool("recover", false, "treat execution state as lost and rebuild from the task system")
	debug := fs.Bool("debug", false, "enable debug logging (overrides RELAY_FLOW_LOG_LEVEL)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	logCloser, err := logging.Setup(p.ServerLog, logging.Options{
		Debug: *debug,
		Env:   os.Getenv("RELAY_FLOW_LOG_LEVEL"),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitFail
	}
	defer logCloser.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serveRoot(ctx, p, *recover); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitFail
	}
	return exitOK
}

// --- report ---

// cmdReport reads one JSON object from stdin and posts it to /reports.
// Exit 0 on any ack (including duplicate/stale), 1 on server/validation
// failure or malformed JSON.
func cmdReport(c *server.Client, stdin io.Reader) int {
	body, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitFail
	}
	var req runsvc.ReportRequest
	if err := json.Unmarshal(body, &req); err != nil {
		fmt.Fprintln(os.Stderr, "report: malformed JSON: "+err.Error())
		return exitFail
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := c.SubmitReport(ctx, req); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitFail
	}
	return exitOK
}

// cmdRuntimeRegister reads one plugin-owned JSON session registration from
// stdin and forwards it to the server over the existing Unix socket.
func cmdRuntimeRegister(c *server.Client, stdin io.Reader) int {
	body, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitFail
	}
	var registration runsvc.NodeRuntimeRegistration
	if err := json.Unmarshal(body, &registration); err != nil {
		fmt.Fprintln(os.Stderr, "runtime-register: malformed JSON: "+err.Error())
		return exitFail
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := c.RegisterNodeSession(ctx, registration); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitFail
	}
	return exitOK
}

// --- workflow ---

func cmdWorkflow(c *server.Client, args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return exitUsage
	}
	switch args[0] {
	case "submit":
		fs := flag.NewFlagSet("workflow submit", flag.ContinueOnError)
		file := fs.String("file", "", "workflow YAML file")
		if err := fs.Parse(args[1:]); err != nil {
			return exitUsage
		}
		if *file == "" {
			fmt.Fprintln(os.Stderr, "workflow submit: --file is required")
			return exitUsage
		}
		body, err := os.ReadFile(*file)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitFail
		}
		wf, err := c.SubmitWorkflow(context.Background(), body)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitFail
		}
		fmt.Println(wf.Name)
		return exitOK
	case "remove":
		fs := flag.NewFlagSet("workflow remove", flag.ContinueOnError)
		name := fs.String("name", "", "workflow name")
		if err := fs.Parse(args[1:]); err != nil {
			return exitUsage
		}
		if *name == "" {
			fmt.Fprintln(os.Stderr, "workflow remove: --name is required")
			return exitUsage
		}
		if err := c.RemoveWorkflow(context.Background(), *name); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitFail
		}
		return exitOK
	case "list":
		fs := flag.NewFlagSet("workflow list", flag.ContinueOnError)
		if err := fs.Parse(args[1:]); err != nil {
			return exitUsage
		}
		wfs, err := c.ListWorkflows(context.Background())
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitFail
		}
		for _, wf := range wfs {
			fmt.Println(wf.Name)
		}
		return exitOK
	case "get":
		fs := flag.NewFlagSet("workflow get", flag.ContinueOnError)
		name := fs.String("name", "", "workflow name")
		if err := fs.Parse(args[1:]); err != nil {
			return exitUsage
		}
		if *name == "" {
			fmt.Fprintln(os.Stderr, "workflow get: --name is required")
			return exitUsage
		}
		wf, err := c.GetWorkflow(context.Background(), *name)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitFail
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(wf)
		return exitOK
	}
	usage(os.Stderr)
	return exitUsage
}

// --- repo ---

func cmdRepo(c *server.Client, args []string, stdin io.Reader) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return exitUsage
	}
	switch args[0] {
	case "register":
		fs := flag.NewFlagSet("repo register", flag.ContinueOnError)
		name := fs.String("name", "", "repo name (non-interactive)")
		path := fs.String("path", "", "repo path (non-interactive)")
		sets := kvFlags{}
		fs.Var(&sets, "set", "required task repo key value, key=value (repeatable)")
		if err := fs.Parse(args[1:]); err != nil {
			return exitUsage
		}
		return cmdRepoRegister(c, *name, *path, sets, stdin)
	case "remove":
		fs := flag.NewFlagSet("repo remove", flag.ContinueOnError)
		name := fs.String("name", "", "repo name")
		if err := fs.Parse(args[1:]); err != nil {
			return exitUsage
		}
		if *name == "" {
			fmt.Fprintln(os.Stderr, "repo remove: --name is required")
			return exitUsage
		}
		if err := c.RemoveRepo(context.Background(), *name); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitFail
		}
		return exitOK
	case "list":
		fs := flag.NewFlagSet("repo list", flag.ContinueOnError)
		if err := fs.Parse(args[1:]); err != nil {
			return exitUsage
		}
		infos, err := c.ListRepos(context.Background())
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitFail
		}
		for _, info := range infos {
			fmt.Println(info.Name)
		}
		return exitOK
	case "get":
		fs := flag.NewFlagSet("repo get", flag.ContinueOnError)
		name := fs.String("name", "", "repo name")
		if err := fs.Parse(args[1:]); err != nil {
			return exitUsage
		}
		if *name == "" {
			fmt.Fprintln(os.Stderr, "repo get: --name is required")
			return exitUsage
		}
		info, err := c.GetRepo(context.Background(), *name)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitFail
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(info)
		return exitOK
	}
	usage(os.Stderr)
	return exitUsage
}

// cmdRepoRegister registers one repo. Fully-flagged invocation
// (--name/--path plus --set for every required task repo key) never
// prompts and produces the same repo entry as the interactive run.
// With no flags, a TTY drives the interactive flow: discover runner
// repos, huh searchable select, then prompts for name, path, and each
// required key with the selected candidate's name/path as defaults.
func cmdRepoRegister(c *server.Client, flagName, flagPath string, sets kvFlags, stdin io.Reader) int {
	ctx := context.Background()
	flagged := flagName != "" || flagPath != "" || len(sets) > 0

	if flagged {
		// Presence validation BEFORE any server contact.
		if flagName == "" || flagPath == "" {
			fmt.Fprintln(os.Stderr, "repo register: --name and --path are required for non-interactive registration")
			return exitUsage
		}
		taskCfg := config.RawValues{}
		for k, v := range sets {
			if v == "" {
				fmt.Fprintf(os.Stderr, "repo register: --set %s requires a non-empty value\n", k)
				return exitUsage
			}
			taskCfg[k] = v
		}
		fields, err := c.RepoTaskFields(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitFail
		}
		required := map[string]bool{}
		for _, f := range fields {
			required[f] = true
		}
		for k := range taskCfg {
			if !required[k] {
				fmt.Fprintf(os.Stderr, "repo register: unknown task key %q (required keys: %s)\n",
					k, strings.Join(fields, ", "))
				return exitUsage
			}
		}
		var missing []string
		for _, f := range fields {
			if _, ok := taskCfg[f]; !ok {
				missing = append(missing, f)
			}
		}
		if len(missing) > 0 {
			fmt.Fprintf(os.Stderr, "repo register: missing required task keys: %s (pass --set %s=<value>)\n",
				strings.Join(missing, ", "), missing[0])
			return exitUsage
		}
		info, err := c.RegisterRepo(ctx, repo.RegisterInput{Name: flagName, Path: flagPath, TaskConfig: taskCfg})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitFail
		}
		fmt.Println(info.Name)
		return exitOK
	}

	fields, err := c.RepoTaskFields(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitFail
	}

	if !isTTY(stdin) {
		fmt.Fprintln(os.Stderr, "repo register: interactive registration requires a TTY (or pass --name/--path/--set)")
		return exitFail
	}
	candidates, err := c.DiscoverRepos(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitFail
	}
	if len(candidates) == 0 {
		fmt.Fprintln(os.Stderr, "repo register: runner discovered no repos")
		return exitFail
	}

	// Searchable select over discovered candidates.
	selected := 0
	options := make([]huh.Option[int], len(candidates))
	for i, cand := range candidates {
		options[i] = huh.NewOption(cand.Name+"  ("+cand.Path+")", i)
	}
	pick := huh.NewForm(huh.NewGroup(huh.NewSelect[int]().
		Title("Repository").
		Options(options...).
		Filtering(true).
		Value(&selected)))
	if err := pick.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "repo register: "+err.Error())
		return exitFail
	}

	// Name/path inputs default to the SELECTED candidate, then one input
	// per required task repo key. Values pass through as entered; the
	// service validates required keys.
	cand := candidates[selected]
	name := cand.Name
	path := cand.Path
	inputs := []huh.Field{
		huh.NewInput().Title("Name").Value(&name),
		huh.NewInput().Title("Path").Value(&path),
	}
	values := make([]string, len(fields))
	for i, f := range fields {
		inputs = append(inputs, huh.NewInput().Title(f).Value(&values[i]))
	}
	if err := huh.NewForm(huh.NewGroup(inputs...)).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "repo register: "+err.Error())
		return exitFail
	}
	taskCfg := config.RawValues{}
	for i, f := range fields {
		taskCfg[f] = values[i]
	}
	info, err := c.RegisterRepo(ctx, repo.RegisterInput{
		Name:       name,
		Path:       path,
		TaskConfig: taskCfg,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitFail
	}
	fmt.Println(info.Name)
	return exitOK
}

// kvFlags collects repeated key=value flags (registration task config).
// Parse rejects malformed pairs, empty keys/values, and duplicates.
type kvFlags map[string]string

func (k kvFlags) String() string { return "" }
func (k kvFlags) Set(s string) error {
	kv := strings.SplitN(s, "=", 2)
	if len(kv) != 2 || kv[0] == "" || kv[1] == "" {
		return fmt.Errorf("expected key=value with non-empty key and value, got %q", s)
	}
	if _, dup := k[kv[0]]; dup {
		return fmt.Errorf("duplicate --set for key %q", kv[0])
	}
	k[kv[0]] = kv[1]
	return nil
}

// --- run ---

func cmdRun(c *server.Client, args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return exitUsage
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("run list", flag.ContinueOnError)
		repoName := fs.String("repo", "", "filter by repo")
		workflowName := fs.String("workflow", "", "filter by workflow")
		ticket := fs.String("ticket", "", "filter by ticket")
		if err := fs.Parse(args[1:]); err != nil {
			return exitUsage
		}
		runs, err := c.ListRuns(context.Background(), runsvc.Filter{
			Repo:     *repoName,
			Workflow: *workflowName,
			Ticket:   *ticket,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitFail
		}
		for _, r := range runs {
			fmt.Println(formatRunListRow(r))
		}
		return exitOK
	case "get":
		fs := flag.NewFlagSet("run get", flag.ContinueOnError)
		ticket := fs.String("ticket", "", "ticket key")
		if err := fs.Parse(args[1:]); err != nil {
			return exitUsage
		}
		if *ticket == "" {
			fmt.Fprintln(os.Stderr, "run get: --ticket is required")
			return exitUsage
		}
		rn, err := c.GetRunByTicket(context.Background(), *ticket)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitFail
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rn)
		return exitOK
	case "cancel":
		fs := flag.NewFlagSet("run cancel", flag.ContinueOnError)
		ticket := fs.String("ticket", "", "ticket key")
		reason := fs.String("reason", "", "cancel reason")
		if err := fs.Parse(args[1:]); err != nil {
			return exitUsage
		}
		if *ticket == "" {
			fmt.Fprintln(os.Stderr, "run cancel: --ticket is required")
			return exitUsage
		}
		if err := c.CancelRun(context.Background(), *ticket, *reason); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitFail
		}
		return exitOK
	}
	usage(os.Stderr)
	return exitUsage
}

func formatRunListRow(r runsvc.Run) string {
	row := fmt.Sprintf("%s\t%s\t%s\t%s", r.ID, r.Ticket.Key, r.Workflow, r.State)
	if r.Retry != nil {
		row += fmt.Sprintf("\tretrying attempt=%d next=%s error=%q",
			r.Retry.Attempt, r.Retry.NextRetryAt.Format(time.RFC3339), r.Retry.LastError)
	}
	return row
}
