// Command relay-flow is the entrypoint. It is a thin command parser
// (standard flag package) with zero business logic: every command
// delegates to the server client (internal/server.Client) or the
// section-5 composition root for init/serve. The testable entry is
// run(args, stdin) int (allowed seam e).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rajpopat27/relay-flow/internal/paths"
	runsvc "github.com/rajpopat27/relay-flow/internal/run"
	"github.com/rajpopat27/relay-flow/internal/server"
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
	case "workflow":
		return cmdWorkflow(client, args[1:])
	case "repo":
		return cmdRepo(client, args[1:])
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
  relay-flow init
  relay-flow serve [--recover]
  relay-flow stop
  relay-flow report

  relay-flow workflow submit --file <path>
  relay-flow workflow remove --name <name>
  relay-flow workflow list
  relay-flow workflow get --name <name>

  relay-flow repo register
  relay-flow repo remove --name <name>
  relay-flow repo list
  relay-flow repo get --name <name>

  relay-flow run list
  relay-flow run get --ticket <key>
  relay-flow run cancel --ticket <key>`)
}

// --- init / serve delegate to the section-5 composition root ---

func cmdInit(p paths.Paths, args []string, stdin io.Reader) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	// Composition root for init is task 5.x. The CLI only parses; wiring
	// runs later. Returning failure here keeps 3.31 red until 5.x lands.
	_ = p
	_ = stdin
	fmt.Fprintln(os.Stderr, "init: composition root not wired yet")
	return exitFail
}

func cmdServe(p paths.Paths, args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	recover := fs.Bool("recover", false, "treat execution state as lost and rebuild from the task system")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
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

func cmdRepo(c *server.Client, args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return exitUsage
	}
	switch args[0] {
	case "register":
		fs := flag.NewFlagSet("repo register", flag.ContinueOnError)
		name := fs.String("name", "", "repo name (skips interactive selection)")
		path := fs.String("path", "", "repo path (skips interactive selection)")
		if err := fs.Parse(args[1:]); err != nil {
			return exitUsage
		}
		// Interactive huh-based selection is wired by the composition root
		// when --name/--path are absent; the CLI stays parse-only.
		_ = name
		_ = path
		fmt.Fprintln(os.Stderr, "repo register: interactive selection not wired yet")
		return exitFail
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
			fmt.Printf("%s\t%s\t%s\t%s\n", r.ID, r.Ticket.Key, r.Workflow, r.State)
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
