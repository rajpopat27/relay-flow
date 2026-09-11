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
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/mattn/go-isatty"
	"github.com/rajpopat27/relay-flow/internal/config"
	"github.com/rajpopat27/relay-flow/internal/execution/projection"
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
	_ "github.com/rajpopat27/relay-flow/internal/harness/pi"
	_ "github.com/rajpopat27/relay-flow/internal/runner/herdr"
	_ "github.com/rajpopat27/relay-flow/internal/runner/orca"
	_ "github.com/rajpopat27/relay-flow/internal/task/beads"
	_ "github.com/rajpopat27/relay-flow/internal/task/jira"
)

// Exit codes per specs/workflow-repo-management "CLI exit codes are
// stable": 0 success, 2 command/flag usage, 1 server/validation/operation.
const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

// Build metadata has safe local-build defaults and can be replaced by release
// builds with -ldflags -X main.version=... and friends.
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() { os.Exit(run(os.Args[1:], os.Stdin)) }

// home returns the relay-flow root, honoring RELAY_FLOW_HOME (tests and
// non-standard installs) and falling back to ~/.relay-flow.
func home() (paths.Paths, error) {
	if env := os.Getenv("RELAY_FLOW_HOME"); env != "" {
		root := env
		return paths.Paths{
			Root:        root,
			Config:      filepath.Join(root, "config.yaml"),
			Credentials: filepath.Join(root, "credentials.yaml"),
			Workflows:   filepath.Join(root, "workflows"),
			Database:    filepath.Join(root, "state.db"),
			Socket:      filepath.Join(root, "server.sock"),
			Lock:        filepath.Join(root, "server.lock"),
			ServerLog:   filepath.Join(root, "server.log"),
			PluginLog:   filepath.Join(root, "plugin.log"),
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
	if isVersionArgs(args) {
		printVersion(os.Stdout, len(args) == 1 && args[0] == "version")
		return exitOK
	}
	if hasHelpArg(args) {
		return printScopedHelp(args, os.Stdout)
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
	case "task":
		return cmdTask(p, args[1:], stdin)
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
  relay-flow [command] [flags]

Setup and server:
  relay-flow init [--force] [--task-plugin <name> --runner-plugin <name> --harness-plugin <name>]
                   [--executor-plugin <goworkflows|temporal>]
                   [--temporal-address <host:port>] [--temporal-namespace <name>]
                   (interactive first run also authenticates and optionally registers repos)
  relay-flow task auth [task-plugin options]
  relay-flow serve [--recover] [--debug] [--background]
  relay-flow stop
  relay-flow report
  relay-flow version | --version | -v

Workflow:
  relay-flow workflow submit --file <path>
  relay-flow workflow remove --name <name>
  relay-flow workflow list [--json]
  relay-flow workflow get --name <name> [--json]

Repository:
  relay-flow repo register [--name <name> --path <path> --set key=value ...]
  relay-flow repo remove --name <name>
  relay-flow repo list
  relay-flow repo get --name <name>

Run:
  relay-flow run list [--repo <name>] [--workflow <name>] [--ticket <key>] [--active] [--json]
  relay-flow run get --ticket <key> [--json]
  relay-flow run restart --ticket <key>
  relay-flow run cancel --ticket <key>

Use relay-flow <command> --help for command-specific details.

Examples:
  relay-flow workflow list --json
  relay-flow run get --ticket PAY-101
Example: relay-flow run get --ticket PAY-101`)
}

func isVersionArgs(args []string) bool {
	return len(args) == 1 && (args[0] == "version" || args[0] == "--version" || args[0] == "-v")
}

func hasHelpArg(args []string) bool {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}

func printVersion(w io.Writer, long bool) {
	if !long {
		fmt.Fprintf(w, "relay-flow %s\n", version)
		return
	}
	fmt.Fprintf(w, "relay-flow %s\n", version)
	fmt.Fprintf(w, "Commit: %s\n", commit)
	fmt.Fprintf(w, "Build date: %s\n", buildDate)
	fmt.Fprintf(w, "Go: %s\n", runtime.Version())
	fmt.Fprintf(w, "Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
}

func printScopedHelp(args []string, w io.Writer) int {
	// Help is handled before home/config/client creation. Keep each block
	// scoped to the requested command instead of dumping the entire surface.
	path := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			continue
		}
		// Command names are positional; once a flag appears, the remaining
		// tokens are command arguments and must not change the help scope.
		if strings.HasPrefix(arg, "-") {
			break
		}
		path = append(path, arg)
	}
	if len(path) == 0 {
		usage(w)
		return exitOK
	}
	name := strings.Join(path, " ")
	switch name {
	case "init":
		fmt.Fprintln(w, "Usage: relay-flow init [flags]")
		fmt.Fprintln(w, "\nInitialize relay-flow and its durable state.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --task-plugin <name>              Task-system plugin (required with runner and harness plugin flags).")
		fmt.Fprintln(w, "  --runner-plugin <name>            Runner plugin (required with task and harness plugin flags).")
		fmt.Fprintln(w, "  --harness-plugin <name>           Harness plugin (required with task and runner plugin flags).")
		fmt.Fprintln(w, "  --executor-plugin <name>          Durable executor: goworkflows or temporal.")
		fmt.Fprintln(w, "  --temporal-address <host:port>    Temporal server address; applies only to --executor-plugin temporal (optional, defaults to localhost:7233).")
		fmt.Fprintln(w, "  --temporal-namespace <name>       Temporal namespace/team name; applies only to --executor-plugin temporal (required).")
		fmt.Fprintln(w, "  --force                           Update plugin selections while preserving existing durable state.")
		fmt.Fprintln(w, "Example: relay-flow init --task-plugin jira --runner-plugin orca --harness-plugin opencode")
	case "serve":
		fmt.Fprintln(w, "Usage: relay-flow serve [--recover] [--debug] [--background]")
		fmt.Fprintln(w, "\nStart the relay-flow server.")
		fmt.Fprintln(w, "Example: relay-flow serve --background")
	case "stop":
		fmt.Fprintln(w, "Usage: relay-flow stop")
		fmt.Fprintln(w, "\nStop the running relay-flow server.")
		fmt.Fprintln(w, "Example: relay-flow stop")
	case "report":
		fmt.Fprintln(w, "Usage: relay-flow report < report.json")
		fmt.Fprintln(w, "\nSubmit one complete structured node report.")
		fmt.Fprintln(w, "Example: relay-flow report < report.json")
	case "task":
		fmt.Fprintln(w, "Usage: relay-flow task <command>")
		fmt.Fprintln(w, "\nRun task-system commands through the configured task plugin.")
		fmt.Fprintln(w, "Commands:")
		fmt.Fprintln(w, "  auth  Authenticate the configured task-system plugin.")
		fmt.Fprintln(w, "Use relay-flow task <subcommand> --help for command-specific details.")
		fmt.Fprintln(w, "Example: relay-flow task auth")
	case "task auth":
		fmt.Fprintln(w, "Usage: relay-flow task auth [task-plugin options]")
		fmt.Fprintln(w, "\nAuthenticate the configured task-system plugin.")
		fmt.Fprintln(w, "Arguments: task-plugin options are passed through unchanged; relay-flow adds no flags.")
		fmt.Fprintln(w, "Example: relay-flow task auth")
	case "runtime-register":
		fmt.Fprintln(w, "Usage: relay-flow runtime-register < session.json")
		fmt.Fprintln(w, "\nRegister a harness runtime session with the server.")
		fmt.Fprintln(w, "Example: relay-flow runtime-register < session.json")
	case "version":
		fmt.Fprintln(w, "Usage: relay-flow version")
		fmt.Fprintln(w, "\nPrint release and build metadata without contacting the server.")
		fmt.Fprintln(w, "Example: relay-flow version")
	case "workflow":
		fmt.Fprintln(w, "Usage: relay-flow workflow <command>")
		fmt.Fprintln(w, "\nManage validated workflow definitions and inspect their runs.")
		fmt.Fprintln(w, "Commands:")
		fmt.Fprintln(w, "  submit  Create or replace a workflow definition.")
		fmt.Fprintln(w, "    Usage: relay-flow workflow submit --file <path>")
		fmt.Fprintln(w, "    Example: relay-flow workflow submit --file workflow.yaml")
		fmt.Fprintln(w, "  remove  Remove a workflow with no active runs.")
		fmt.Fprintln(w, "    Usage: relay-flow workflow remove --name <name>")
		fmt.Fprintln(w, "    Example: relay-flow workflow remove --name basicFlow")
		fmt.Fprintln(w, "  list    List configured workflows and latest execution summaries.")
		fmt.Fprintln(w, "    Usage: relay-flow workflow list [--json] [--no-color]")
		fmt.Fprintln(w, "    Example: relay-flow workflow list --json")
		fmt.Fprintln(w, "  get     Show a workflow graph and recent runs.")
		fmt.Fprintln(w, "    Usage: relay-flow workflow get --name <name> [--json] [--no-color]")
		fmt.Fprintln(w, "    Example: relay-flow workflow get --name basicFlow")
		fmt.Fprintln(w, "Use relay-flow workflow <subcommand> --help for flag details.")
	case "workflow submit":
		fmt.Fprintln(w, "Usage: relay-flow workflow submit --file <path>")
		fmt.Fprintln(w, "\nCreate or replace a validated workflow definition.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --file <path>  Workflow YAML file to submit (required).")
		fmt.Fprintln(w, "Example: relay-flow workflow submit --file workflow.yaml")
	case "workflow remove":
		fmt.Fprintln(w, "Usage: relay-flow workflow remove --name <name>")
		fmt.Fprintln(w, "\nRemove a workflow with no active runs.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --name <name>  Workflow name (required).")
		fmt.Fprintln(w, "Example: relay-flow workflow remove --name basicFlow")
	case "workflow list":
		fmt.Fprintln(w, "Usage: relay-flow workflow list [--json] [--no-color]")
		fmt.Fprintln(w, "\nList configured workflows and their latest execution summaries.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --json      Print stable JSON instead of the human-readable table.")
		fmt.Fprintln(w, "  --no-color  Disable terminal color decoration.")
		fmt.Fprintln(w, "Example: relay-flow workflow list --json")
	case "workflow get":
		fmt.Fprintln(w, "Usage: relay-flow workflow get --name <name> [--json] [--no-color]")
		fmt.Fprintln(w, "\nShow a workflow's validated static graph and recent runs.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --name <name>  Workflow name (required).")
		fmt.Fprintln(w, "  --json         Print stable JSON instead of the human-readable detail view.")
		fmt.Fprintln(w, "  --no-color     Disable terminal color decoration.")
		fmt.Fprintln(w, "Example: relay-flow workflow get --name basicFlow")
	case "repo":
		fmt.Fprintln(w, "Usage: relay-flow repo <command>")
		fmt.Fprintln(w, "\nManage registered runner repositories and task-system configuration.")
		fmt.Fprintln(w, "Commands:")
		fmt.Fprintln(w, "  register  Register a runner repository and its task-system configuration.")
		fmt.Fprintln(w, "    Usage: relay-flow repo register [--name <name>] [--path <path>] [--set key=value ...]")
		fmt.Fprintln(w, "    Example (Beads): relay-flow repo register --name payments --path /work/payments --set beadsDir=/work/payments/.beads")
		fmt.Fprintln(w, "  remove    Remove a registered repository.")
		fmt.Fprintln(w, "    Usage: relay-flow repo remove --name <name>")
		fmt.Fprintln(w, "    Example: relay-flow repo remove --name payments")
		fmt.Fprintln(w, "  list      List registered repositories.")
		fmt.Fprintln(w, "    Usage: relay-flow repo list")
		fmt.Fprintln(w, "    Example: relay-flow repo list")
		fmt.Fprintln(w, "  get       Show one registered repository as JSON.")
		fmt.Fprintln(w, "    Usage: relay-flow repo get --name <name>")
		fmt.Fprintln(w, "    Example: relay-flow repo get --name payments")
		fmt.Fprintln(w, "Use relay-flow repo <subcommand> --help for flag details.")
	case "repo register":
		fmt.Fprintln(w, "Usage: relay-flow repo register [--name <name>] [--path <path>] [--set key=value ...]")
		fmt.Fprintln(w, "\nRegister a runner repository and task-system configuration.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --name <name>       Repository name. Optional for interactive registration; required in flagged/non-interactive mode.")
		fmt.Fprintln(w, "  --path <path>       Local repository path. Optional for interactive registration; required in flagged/non-interactive mode.")
		fmt.Fprintln(w, "  --set key=value     Optional, repeatable task-system configuration override. Key and value must be non-empty; duplicate keys are rejected.")
		fmt.Fprintln(w, "\nInteractive mode: omit --name, --path, and --set; a TTY is required.")
		fmt.Fprintln(w, "Flagged mode: --name and --path are required. Repeat --set once per task-system key; the selected plugin validates keys and values, and derived keys cannot be overridden.")
		fmt.Fprintln(w, "Example (Beads): relay-flow repo register --name payments --path /work/payments --set beadsDir=/work/payments/.beads")
	case "repo remove":
		fmt.Fprintln(w, "Usage: relay-flow repo remove --name <name>")
		fmt.Fprintln(w, "\nRemove a registered repository.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --name <name>  Registered repository name (required).")
		fmt.Fprintln(w, "No other flags are supported.")
		fmt.Fprintln(w, "Example: relay-flow repo remove --name payments")
	case "repo list":
		fmt.Fprintln(w, "Usage: relay-flow repo list")
		fmt.Fprintln(w, "\nList registered repositories.")
		fmt.Fprintln(w, "Flags: none.")
		fmt.Fprintln(w, "Example: relay-flow repo list")
	case "repo get":
		fmt.Fprintln(w, "Usage: relay-flow repo get --name <name>")
		fmt.Fprintln(w, "\nShow one registered repository and its task-system configuration as JSON.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --name <name>  Registered repository name (required).")
		fmt.Fprintln(w, "No other flags are supported.")
		fmt.Fprintln(w, "Example: relay-flow repo get --name payments")
	case "run":
		fmt.Fprintln(w, "Usage: relay-flow run <command>")
		fmt.Fprintln(w, "\nInspect and control durable ticket runs.")
		fmt.Fprintln(w, "Commands:")
		fmt.Fprintln(w, "  list     List runs, optionally filtered by repository, workflow, ticket, or active state.")
		fmt.Fprintln(w, "    Usage: relay-flow run list [--repo <name>] [--workflow <name>] [--ticket <key>] [--active] [--json] [--no-color]")
		fmt.Fprintln(w, "    Example: relay-flow run list --active --json")
		fmt.Fprintln(w, "  get      Show one run's execution detail and actual step visits.")
		fmt.Fprintln(w, "    Usage: relay-flow run get --ticket <key> [--json] [--no-color]")
		fmt.Fprintln(w, "    Example: relay-flow run get --ticket PAY-101")
		fmt.Fprintln(w, "  restart  Start a fresh attempt for a canceled run.")
		fmt.Fprintln(w, "    Usage: relay-flow run restart --ticket <key>")
		fmt.Fprintln(w, "    Example: relay-flow run restart --ticket PAY-101")
		fmt.Fprintln(w, "  cancel   Permanently cancel the current execution attempt.")
		fmt.Fprintln(w, "    Usage: relay-flow run cancel --ticket <key> [--reason <text>]")
		fmt.Fprintln(w, "    Example: relay-flow run cancel --ticket PAY-101 --reason "+`"operator request"`)
		fmt.Fprintln(w, "Use relay-flow run <subcommand> --help for flag details.")
	case "run list":
		fmt.Fprintln(w, "Usage: relay-flow run list [--repo <name>] [--workflow <name>] [--ticket <key>] [--active] [--json] [--no-color]")
		fmt.Fprintln(w, "\nList durable runs, with optional filters.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --repo <name>      Filter by registered repository name.")
		fmt.Fprintln(w, "  --workflow <name>  Filter by workflow name.")
		fmt.Fprintln(w, "  --ticket <key>     Filter by task ticket key.")
		fmt.Fprintln(w, "  --active           Show only active runs.")
		fmt.Fprintln(w, "  --json             Print stable JSON instead of the human-readable table.")
		fmt.Fprintln(w, "  --no-color         Disable terminal color decoration.")
		fmt.Fprintln(w, "Example: relay-flow run list --active --json")
	case "run get":
		fmt.Fprintln(w, "Usage: relay-flow run get --ticket <key> [--json] [--no-color]")
		fmt.Fprintln(w, "\nShow an Argo-shaped execution detail view and actual step visits.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --ticket <key>  Task ticket key (required).")
		fmt.Fprintln(w, "  --json          Print stable JSON instead of the human-readable detail view.")
		fmt.Fprintln(w, "  --no-color      Disable terminal color decoration.")
		fmt.Fprintln(w, "Example: relay-flow run get --ticket PAY-101")
	case "run restart":
		fmt.Fprintln(w, "Usage: relay-flow run restart --ticket <key>")
		fmt.Fprintln(w, "\nStart a fresh attempt for a canceled run.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --ticket <key>  Task ticket key (required).")
		fmt.Fprintln(w, "No other flags are supported.")
		fmt.Fprintln(w, "Example: relay-flow run restart --ticket PAY-101")
	case "run cancel":
		fmt.Fprintln(w, "Usage: relay-flow run cancel --ticket <key> [--reason <text>]")
		fmt.Fprintln(w, "\nPermanently cancel the current execution attempt.")
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, "  --ticket <key>    Task ticket key (required).")
		fmt.Fprintln(w, "  --reason <text>   Optional cancellation reason recorded with the request.")
		fmt.Fprintln(w, "Example: relay-flow run cancel --ticket PAY-101 --reason "+`"operator request"`)
	default:
		fmt.Fprintf(os.Stderr, "unknown help target: %s\n", name)
		return exitUsage
	}
	return exitOK
}

// --- init / serve delegate to the section-5 composition root ---

// cmdInit is the init composition root (docs lines 950/1028): select the
// three plugin names, atomically write the machine config, and initialize
// the SQLite database. Refuses to overwrite existing config or history unless
// --force safely updates plugin selections while preserving durable state.
// Plugin selection precedence: --task-plugin/--runner-plugin/--harness-plugin
// flags (all three required for a fully non-interactive run) → huh form on
// a TTY → three stdin lines (task, runner, harness), the documented test
// seam and script path.
func cmdInit(p paths.Paths, args []string, stdin io.Reader) int {
	if err := recoverFirstRunPublication(p); err != nil {
		fmt.Fprintln(os.Stderr, "init: "+err.Error())
		return exitFail
	}
	if err := cleanupFirstRunStaging(p); err != nil {
		fmt.Fprintln(os.Stderr, "init: "+err.Error())
		return exitFail
	}
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	taskName := fs.String("task-plugin", "", "task plugin name (non-interactive)")
	runnerName := fs.String("runner-plugin", "", "runner plugin name (non-interactive)")
	harnessName := fs.String("harness-plugin", "", "harness plugin name (non-interactive)")
	executorName := fs.String("executor-plugin", "", "durable executor name (goworkflows or temporal)")
	temporalAddress := fs.String("temporal-address", "", "Temporal server address")
	temporalNamespace := fs.String("temporal-namespace", "", "Temporal namespace/team name")
	force := fs.Bool("force", false, "update plugin selections while preserving existing state")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	flagged := *taskName != "" || *runnerName != "" || *harnessName != ""
	if flagged && (*taskName == "" || *runnerName == "" || *harnessName == "") {
		fmt.Fprintln(os.Stderr, "init: --task-plugin, --runner-plugin, and --harness-plugin must be given together")
		return exitUsage
	}
	configExists, err := pathExists(p.Config)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitFail
	}
	databaseExists, err := pathExists(p.Database)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitFail
	}
	if !*force && configExists {
		fmt.Fprintln(os.Stderr, "init: already initialized (config exists): "+p.Config)
		return exitFail
	}
	if !*force && databaseExists {
		fmt.Fprintln(os.Stderr, "init: already initialized (database exists): "+p.Database)
		return exitFail
	}
	if *force && configExists && !databaseExists {
		fmt.Fprintln(os.Stderr, "init: database is missing; refusing to recreate existing durable state")
		return exitFail
	}
	// A first interactive setup keeps the final home untouched until the
	// selected task plugin has authenticated successfully. Scripted setup and
	// --force retain the existing eager directory creation behavior.
	firstRunInteractive := !*force && !configExists && !databaseExists && !flagged && interactiveInitTTY(stdin)
	if !firstRunInteractive {
		if err := os.MkdirAll(p.Root, 0o700); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitFail
		}
		_ = os.Chmod(p.Root, 0o700)
	}
	var unlock func()
	if *force {
		unlock, err = lockForForcedInit(p.Lock)
		if err != nil {
			fmt.Fprintln(os.Stderr, "init: "+err.Error())
			return exitFail
		}
		defer unlock()
		if databaseExists {
			active, err := projection.HasNonterminalRuns(p.Database)
			if err != nil {
				fmt.Fprintln(os.Stderr, "init: "+err.Error())
				return exitFail
			}
			if active {
				fmt.Fprintln(os.Stderr, "init: cannot use --force while a run is nonterminal")
				return exitFail
			}
		}
	}

	// Selection precedence: flags → TTY form → legacy stdin lines. The
	// three-line stdin path remains embedded-mode input and never consumes
	// Temporal answers.
	var names []string
	executor := strings.TrimSpace(*executorName)
	switch {
	case flagged:
		names = []string{*taskName, *runnerName, *harnessName}
	case interactiveInitTTY(stdin):
		var err error
		names, err = interactiveInitPluginPick()
		if err != nil {
			fmt.Fprintln(os.Stderr, "init: "+err.Error())
			return exitFail
		}
		if len(names) == 4 {
			executor = names[3]
			names = names[:3]
		}
	default:
		names = readInitLines(stdin)
		if names == nil {
			fmt.Fprintln(os.Stderr, "init: expected task, runner, and harness plugin selections on stdin")
			return exitFail
		}
	}
	if executor == executorTemporal && interactiveInitTTY(stdin) {
		address, namespace, err := promptTemporalSettings(*temporalAddress, *temporalNamespace)
		if err != nil {
			fmt.Fprintln(os.Stderr, "init: "+err.Error())
			return exitFail
		}
		*temporalAddress = address
		*temporalNamespace = namespace
	}
	if executor == "" {
		executor = executorGoworkflows
	}
	if executor != executorGoworkflows && executor != executorTemporal {
		fmt.Fprintln(os.Stderr, "init: unknown executor plugin "+executor)
		return exitUsage
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
		PollIntervalSeconds: 15, CompletedRunRetentionDays: 30,
		KeepTerminalsAlive: true, KeepSessionsAlive: true,
		ExecutorPlugin: executor,
	}
	if *force && configExists {
		cfg, err = config.LoadMachine(p.Config)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitFail
		}
		if strings.TrimSpace(*executorName) == "" {
			executor = cfg.ExecutorPlugin
		}
		cfg.ExecutorPlugin = executor
	}
	if executor == executorTemporal {
		if strings.TrimSpace(*temporalAddress) != "" {
			cfg.TemporalAddress = strings.TrimSpace(*temporalAddress)
		} else if cfg.TemporalAddress == "" {
			cfg.TemporalAddress = defaultTemporalAddr
		}
		if strings.TrimSpace(*temporalNamespace) != "" {
			cfg.TemporalNamespace = strings.TrimSpace(*temporalNamespace)
		}
		if cfg.TemporalNamespace == "" {
			fmt.Fprintln(os.Stderr, "init: --temporal-namespace is required for executor-plugin temporal")
			return exitUsage
		}
	} else {
		if strings.TrimSpace(*temporalAddress) != "" || strings.TrimSpace(*temporalNamespace) != "" {
			fmt.Fprintln(os.Stderr, "init: Temporal address/namespace require executor-plugin temporal")
			return exitUsage
		}
		cfg.TemporalAddress = ""
		cfg.TemporalNamespace = ""
	}
	cfg.TaskPlugin = names[0]
	cfg.RunnerPlugin = names[1]
	cfg.HarnessPlugin = names[2]
	taskDefaults, err := task.Defaults(names[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "init: task plugin: "+err.Error())
		return exitFail
	}
	if !*force || !configExists {
		cfg.TaskConfig = taskDefaults
	} else {
		cfg.TaskConfig = config.Merge(taskDefaults, cfg.TaskConfig)
	}
	if err := task.ValidateTextConfig(names[0], cfg.TaskConfig); err != nil {
		fmt.Fprintln(os.Stderr, "init: task plugin config: "+err.Error())
		return exitFail
	}
	harnessDefaults, err := harness.Defaults(names[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, "init: harness plugin: "+err.Error())
		return exitFail
	}
	if !*force || !configExists {
		cfg.HarnessConfig = harnessDefaults
	} else {
		cfg.HarnessConfig = config.Merge(harnessDefaults, cfg.HarnessConfig)
	}
	var stagedAuthDir string
	if firstRunInteractive {
		if names[0] == "beads" {
			fmt.Println("Beads authentication is managed by the Beads workspace and Dolt setup; relay-flow does not store task credentials.")
		}
		var authErr error
		cfg, stagedAuthDir, authErr = stageFirstRunAuthentication(p, cfg, names[0], stdin)
		if authErr != nil {
			fmt.Fprintln(os.Stderr, "init: task auth: "+authErr.Error())
			return exitFail
		}
		defer os.RemoveAll(stagedAuthDir)
	}
	identity := projection.ExecutorIdentity{
		ExecutorPlugin:    executor,
		TemporalAddress:   cfg.TemporalAddress,
		TemporalNamespace: cfg.TemporalNamespace,
	}
	// Force mode verifies the immutable durable identity before contacting
	// Temporal or replacing configuration. Legacy marker-less homes may only
	// be adopted by goworkflows.
	if databaseExists {
		if err := verifyExecutorIdentity(p.Database, identity); err != nil {
			fmt.Fprintln(os.Stderr, "init: "+err.Error())
			return exitFail
		}
	}
	if executor == executorTemporal {
		if err := ensureTemporalNamespace(context.Background(), cfg.TemporalAddress, cfg.TemporalNamespace, cfg.CompletedRunRetentionDays); err != nil {
			fmt.Fprintln(os.Stderr, "init: "+err.Error())
			return exitFail
		}
	}
	if !firstRunInteractive && !databaseExists {
		if err := projection.InitDatabaseWithIdentity(p.Database, identity); err != nil {
			fmt.Fprintln(os.Stderr, "init: "+err.Error())
			return exitFail
		}
	}
	if firstRunInteractive {
		if err := commitFirstRun(p, stagedAuthDir, identity); err != nil {
			fmt.Fprintln(os.Stderr, "init: "+err.Error())
			return exitFail
		}
		register, err := interactiveInitRepoPrompt()
		if err != nil {
			fmt.Fprintln(os.Stderr, "init: "+err.Error())
			return exitFail
		}
		if register {
			if err := interactiveInitRepoSetup(p, stdin); err != nil {
				fmt.Fprintln(os.Stderr, "init: "+err.Error())
				return exitFail
			}
			fmt.Println("Repository setup complete. The server is running; stop it with `relay-flow stop`.")
		}
	} else if err := config.SaveMachine(p.Config, cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitFail
	}
	fmt.Printf("Task system: %s\nRunner: %s\nHarness: %s\nExecutor: %s\nRelay-flow initialized\n", names[0], names[1], names[2], executor)
	return exitOK
}

func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("stat %s: %w", path, err)
}

func lockForForcedInit(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open server lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("cannot use --force while the server is running")
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// isTTY reports whether stdin is an interactive terminal.
func isTTY(stdin io.Reader) bool {
	f, ok := stdin.(*os.File)
	return ok && isatty.IsTerminal(f.Fd())
}

// pickPluginsInteractive runs one searchable select per plugin type.
func pickPluginsInteractive() ([]string, error) {
	names := make([]string, 4)
	groups := make([]*huh.Group, 0, 4)
	for _, selection := range []struct {
		title   string
		options []string
		value   *string
	}{
		{"Select task system", task.Names(), &names[0]},
		{"Select runner", runner.Names(), &names[1]},
		{"Select harness", harness.Names(), &names[2]},
	} {
		field, err := pluginSelectField(selection.title, selection.options, selection.value)
		if err != nil {
			return nil, err
		}
		if field != nil {
			groups = append(groups, huh.NewGroup(field))
		}
	}
	executorField, err := executorSelectField(&names[3])
	if err != nil {
		return nil, err
	}
	groups = append(groups, huh.NewGroup(executorField))
	if len(groups) == 0 {
		return names, nil
	}
	form := huh.NewForm(groups...)
	if err := form.Run(); err != nil {
		return nil, err
	}
	return names, nil
}

func pluginSelectField(title string, options []string, value *string) (huh.Field, error) {
	if len(options) == 0 {
		return nil, fmt.Errorf("%s: no plugins registered", title)
	}
	return huh.NewSelect[string]().
		Title(title).
		Description("Press / to filter available plugins.").
		Options(huh.NewOptions(options...)...).
		Value(value), nil
}

// readInitLines reads the three plugin selections.
func readInitLines(stdin io.Reader) []string {
	values := make([]string, 0, 3)
	scanner := bufio.NewScanner(stdin)
	for len(values) < 3 && scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		values = append(values, line)
	}
	if len(values) < 3 {
		return nil
	}
	return values
}

// cmdTask dispatches task-system commands to the selected task plugin.
func cmdTask(p paths.Paths, args []string, stdin io.Reader) int {
	if len(args) == 0 || args[0] != "auth" {
		usage(os.Stderr)
		return exitUsage
	}
	return cmdTaskAuth(p, args[1:], stdin)
}

func cmdTaskAuth(p paths.Paths, args []string, stdin io.Reader) int {
	cfg, err := config.LoadMachine(p.Config)
	if err != nil {
		fmt.Fprintln(os.Stderr, "task auth: "+err.Error())
		return exitFail
	}
	if err := task.Auth(context.Background(), cfg.TaskPlugin, args, stdin); err != nil {
		fmt.Fprintln(os.Stderr, "task auth: "+err.Error())
		return exitFail
	}
	return exitOK
}

func cmdServe(p paths.Paths, args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	recover := fs.Bool("recover", false, "treat execution state as lost and rebuild from the task system")
	debug := fs.Bool("debug", false, "enable debug logging (overrides RELAY_FLOW_LOG_LEVEL)")
	background := fs.Bool("background", false, "start the server detached and return after readiness")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *background {
		if err := startBackgroundServe(p, *recover, *debug); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitFail
		}
		fmt.Println("Relay-flow server started")
		return exitOK
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
		slog.Error("server startup failed", "error", err)
		fmt.Fprintln(os.Stderr, err)
		return exitFail
	}
	return exitOK
}

var (
	backgroundServeStartupTimeout = 5 * time.Minute
	backgroundServePollInterval   = 250 * time.Millisecond
)

func startBackgroundServe(p paths.Paths, recover, debug bool) error {
	client := server.NewClient(p.Socket)
	if serverResponding(client, 200*time.Millisecond) {
		return fmt.Errorf("serve --background: server is already running")
	}
	deadline := time.Now().Add(backgroundServeStartupTimeout)
	lockHeld, err := serverLockHeld(p.Lock)
	if err != nil {
		return fmt.Errorf("serve --background: inspect server lock: %w", err)
	}

	var (
		cmd           *exec.Cmd
		wait          chan error
		observedOwner = lockHeld
		devNull       *os.File
	)
	if !lockHeld {
		executable, resolveErr := os.Executable()
		if resolveErr != nil {
			return fmt.Errorf("serve --background: resolve executable: %w", resolveErr)
		}
		childArgs := []string{"serve"}
		if recover {
			childArgs = append(childArgs, "--recover")
		}
		if debug {
			childArgs = append(childArgs, "--debug")
		}
		devNull, err = os.OpenFile(os.DevNull, os.O_RDWR, 0)
		if err != nil {
			return fmt.Errorf("serve --background: open %s: %w", os.DevNull, err)
		}
		defer devNull.Close()
		cmd = exec.Command(executable, childArgs...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = devNull, devNull, devNull
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("serve --background: start: %w; see %s", err, p.ServerLog)
		}
		wait = make(chan error, 1)
		go func() { wait <- cmd.Wait() }()
	}

	ticker := time.NewTicker(backgroundServePollInterval)
	defer ticker.Stop()
	for {
		if serverResponding(client, 200*time.Millisecond) {
			return nil
		}
		if !time.Now().Before(deadline) {
			if cmd != nil && wait != nil {
				_ = cmd.Process.Kill()
				<-wait
			}
			return fmt.Errorf("serve --background: startup timed out; see %s", p.ServerLog)
		}
		if observedOwner && wait == nil {
			held, lockErr := serverLockHeld(p.Lock)
			if lockErr != nil {
				return fmt.Errorf("serve --background: inspect server lock: %w", lockErr)
			}
			if !held {
				return fmt.Errorf("serve --background: server exited before readiness; see %s", p.ServerLog)
			}
		}
		select {
		case childErr := <-wait:
			held, lockErr := serverLockHeld(p.Lock)
			if lockErr != nil {
				return fmt.Errorf("serve --background: inspect server lock: %w", lockErr)
			}
			if held {
				// Another process acquired the lock while this child was
				// starting. Wait for that owner instead of spawning again.
				observedOwner = true
				wait = nil
				cmd = nil
				continue
			}
			return fmt.Errorf("serve --background: server exited before readiness: %v; see %s", childErr, p.ServerLog)
		case <-ticker.C:
		}
	}
}

func serverLockHeld(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return true, nil
		}
		return false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		return false, err
	}
	return false, nil
}

func serverResponding(client *server.Client, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, err := client.ListRepos(ctx)
	return err == nil
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
		jsonOutput := fs.Bool("json", false, "print stable JSON")
		fs.Bool("no-color", false, "disable terminal colors")
		if err := fs.Parse(args[1:]); err != nil {
			return exitUsage
		}
		finishLoading := beginLoading("Loading workflows", *jsonOutput)
		summaries, err := c.ListWorkflowSummaries(context.Background())
		finishLoading(err)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitFail
		}
		if *jsonOutput {
			return encodeJSON(summaries)
		}
		renderWorkflowSummaries(os.Stdout, summaries, cliRenderOptions())
		return exitOK
	case "get":
		fs := flag.NewFlagSet("workflow get", flag.ContinueOnError)
		name := fs.String("name", "", "workflow name")
		jsonOutput := fs.Bool("json", false, "print stable JSON")
		fs.Bool("no-color", false, "disable terminal colors")
		if err := fs.Parse(args[1:]); err != nil {
			return exitUsage
		}
		if *name == "" {
			fmt.Fprintln(os.Stderr, "workflow get: --name is required")
			return exitUsage
		}
		finishLoading := beginLoading("Loading workflow", *jsonOutput)
		if *jsonOutput {
			wf, err := c.GetWorkflow(context.Background(), *name)
			finishLoading(err)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return exitFail
			}
			return encodeJSON(wf)
		}
		detail, err := c.GetWorkflowDetail(context.Background(), *name)
		finishLoading(err)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitFail
		}
		renderWorkflowDetail(os.Stdout, detail, cliRenderOptions())
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

// cmdRepoRegister registers one flagged repo or multiple interactively selected
// runner repos using registration metadata supplied by the selected task plugin.
func cmdRepoRegister(c *server.Client, flagName, flagPath string, sets kvFlags, stdin io.Reader) int {
	ctx := context.Background()
	flagged := flagName != "" || flagPath != "" || len(sets) > 0

	if flagged {
		// Presence validation BEFORE any server contact.
		if flagName == "" || flagPath == "" {
			fmt.Fprintln(os.Stderr, "repo register: --name and --path are required for non-interactive registration")
			return exitUsage
		}
		registration, err := loadRepoRegistration(ctx, c, sets)
		if err != nil {
			fmt.Fprintln(os.Stderr, standaloneRepoServerHint(err))
			return exitFail
		}
		taskCfg, err := registrationTaskConfig(registration, sets, flagName)
		if err != nil {
			fmt.Fprintln(os.Stderr, "repo register: "+err.Error())
			return exitUsage
		}
		info, err := c.RegisterRepo(ctx, repo.RegisterInput{Name: flagName, Path: flagPath, TaskConfig: taskCfg})
		if err != nil {
			fmt.Fprintln(os.Stderr, standaloneRepoServerHint(err))
			return exitFail
		}
		fmt.Println(info.Name)
		return exitOK
	}

	registration, err := c.RepoRegistrationFields(ctx, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, standaloneRepoServerHint(err))
		return exitFail
	}

	if !isTTY(stdin) {
		fmt.Fprintln(os.Stderr, "repo register: interactive registration requires a TTY (or pass --name/--path/--set)")
		return exitFail
	}
	candidates, err := c.DiscoverRepos(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, standaloneRepoServerHint(err))
		return exitFail
	}
	registered, err := c.ListRepos(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, standaloneRepoServerHint(err))
		return exitFail
	}
	candidates, selected, err := selectReposInteractive(ctx, c, candidates, registered)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repo register: "+standaloneRepoServerHint(err).Error())
		return exitFail
	}

	shared := kvFlags{}
	if err := promptRepoRegistration(registration, shared); err != nil {
		fmt.Fprintln(os.Stderr, "repo register: "+err.Error())
		return exitFail
	}
	if len(shared) > 0 {
		dependent, err := c.RepoRegistrationFields(ctx, flatRegistrationValues(shared))
		if err != nil {
			fmt.Fprintln(os.Stderr, standaloneRepoServerHint(err))
			return exitFail
		}
		registration = mergeRegistration(registration, dependent)
		if err := promptRepoRegistration(registration, shared); err != nil {
			fmt.Fprintln(os.Stderr, "repo register: "+err.Error())
			return exitFail
		}
	}
	if err := registerSelectedReposDynamic(ctx, c, candidates, selected, registration, shared); err != nil {
		fmt.Fprintln(os.Stderr, "repo register: "+standaloneRepoServerHint(err).Error())
		return exitFail
	}
	return exitOK
}

func repoMultiSelect(options []huh.Option[int], selected *[]int) *huh.MultiSelect[int] {
	return huh.NewMultiSelect[int]().
		Title("Select repositories").
		Description("Press / to filter repositories; select [+] Add repository to add a runner resource.").
		Filterable(true).
		Options(options...).
		Value(selected).
		Validate(func(values []int) error {
			if len(values) == 0 {
				return fmt.Errorf("select at least one repository")
			}
			return nil
		})
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
		activeOnly := fs.Bool("active", false, "show only active runs")
		jsonOutput := fs.Bool("json", false, "print stable JSON")
		fs.Bool("no-color", false, "disable terminal colors")
		if err := fs.Parse(args[1:]); err != nil {
			return exitUsage
		}
		var active *bool
		if *activeOnly {
			value := true
			active = &value
		}
		filter := runsvc.Filter{Repo: *repoName, Workflow: *workflowName, Ticket: *ticket, Active: active}
		finishLoading := beginLoading("Loading runs", *jsonOutput)
		runs, err := c.ListRuns(context.Background(), filter)
		finishLoading(err)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitFail
		}
		if *jsonOutput {
			return encodeJSON(runs)
		}
		renderRunsFiltered(os.Stdout, runs, cliRenderOptions(), filter)
		return exitOK
	case "get":
		fs := flag.NewFlagSet("run get", flag.ContinueOnError)
		ticket := fs.String("ticket", "", "ticket key")
		jsonOutput := fs.Bool("json", false, "print stable JSON")
		fs.Bool("no-color", false, "disable terminal colors")
		if err := fs.Parse(args[1:]); err != nil {
			return exitUsage
		}
		if *ticket == "" {
			fmt.Fprintln(os.Stderr, "run get: --ticket is required")
			return exitUsage
		}
		finishLoading := beginLoading("Loading run", *jsonOutput)
		detail, err := c.GetRunDetailByTicket(context.Background(), *ticket)
		finishLoading(err)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitFail
		}
		if *jsonOutput {
			return encodeJSON(detail)
		}
		renderRunDetail(os.Stdout, detail, cliRenderOptions())
		return exitOK
	case "restart":
		fs := flag.NewFlagSet("run restart", flag.ContinueOnError)
		ticket := fs.String("ticket", "", "ticket key")
		if err := fs.Parse(args[1:]); err != nil {
			return exitUsage
		}
		if *ticket == "" {
			fmt.Fprintln(os.Stderr, "run restart: --ticket is required")
			return exitUsage
		}
		rn, err := c.RestartRun(context.Background(), *ticket)
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
	attempt := r.AttemptID
	if attempt == 0 {
		attempt = 1
	}
	row := fmt.Sprintf("%s\t%s\t%s\t%s\tattempt=%d", r.ID, r.Ticket.Key, r.Workflow, r.State, attempt)
	if r.Retry != nil {
		row += fmt.Sprintf("\tretrying attempt=%d next=%s error=%q",
			r.Retry.Attempt, r.Retry.NextRetryAt.Format(time.RFC3339), r.Retry.LastError)
	}
	return row
}
