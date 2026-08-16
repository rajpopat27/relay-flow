// Command orca-jira-loop automates the Jira <-> Orca <-> opencode workflow.
// `serve` is a central process hosting any number of configs submitted via
// `submit`. `report` is a one-shot call (invoked by the opencode plugin)
// that posts a comment and transitions Jira — no network transport, no
// shared daemon state with the server.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"

	"syscall"

	"orca-jira-loop/internal/acli"
	"orca-jira-loop/internal/config"
	"orca-jira-loop/internal/daemon"
	"orca-jira-loop/internal/discovery"
	"orca-jira-loop/internal/server"
)

// workflowConfigPath is always relative to cwd — `report` inherits cwd
// from the terminal it's invoked in, which is always the ticket's own
// worktree; `submit` runs from the repo the config governs.
func workflowConfigPath(configName string) string {
	return filepath.Join(".workflow", configName+".yaml")
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "init":
		cmdInit(os.Args[2:])
	case "stop":
		cmdStop(os.Args[2:])
	case "report":
		cmdReport(os.Args[2:])
	case "serve":
		cmdServe(os.Args[2:])
	case "submit":
		cmdSubmit(os.Args[2:])
	case "remove":
		cmdRemove(os.Args[2:])
	case "list":
		cmdList(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: orca-jira-loop init --assignee \"<your Jira display name or accountId>\"")
	fmt.Fprintln(os.Stderr, "       orca-jira-loop stop serve")
	fmt.Fprintln(os.Stderr, "       orca-jira-loop serve [--dry-run] [--foreground]")
	fmt.Fprintln(os.Stderr, "       orca-jira-loop submit [-f <yaml>]   (config name comes from the YAML's name field)")
	fmt.Fprintln(os.Stderr, "       orca-jira-loop remove <config-name>")
	fmt.Fprintln(os.Stderr, "       orca-jira-loop list")
	fmt.Fprintln(os.Stderr, "       orca-jira-loop report --config <name> --workflow <id> --ticket <key> --agent <name> --status <name> --summary <text>")
	fmt.Fprintln(os.Stderr, "  config is always read from .workflow/<config-name>.yaml (relative to cwd)")
	fmt.Fprintln(os.Stderr, "  server artifacts (lock/sock/log) are always under ~/.orca-jira-loop/")
}

// daemonize re-execs the binary detached with childArgs, log file attached
// as stdout+stderr. No supervisor, no IPC: the child IS the worker, the pid
// file it writes is the only state. Returns after spawning.
func daemonize(logPath string, childArgs ...string) {
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Fatalf("open log file: %v", err)
	}
	self, err := os.Executable()
	if err != nil {
		log.Fatalf("resolve executable: %v", err)
	}
	cmd := exec.Command(self, childArgs...)
	cmd.Stdout, cmd.Stderr = f, f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = append(os.Environ(), "ORCA_JIRA_LOOP_DAEMONIZED=1")
	if err := cmd.Start(); err != nil {
		log.Fatalf("daemonize: %v", err)
	}
	fmt.Printf("started (pid %d), logging to %s\n", cmd.Process.Pid, logPath)
}

// cmdInit writes the machine config (~/.orca-jira-loop/config.yaml) with
// this machine user's Jira identity, probe-validated against Jira.
func cmdInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	assignee := fs.String("assignee", "", "your Jira display name or accountId")
	fs.Parse(args)
	if *assignee == "" {
		log.Fatalf("usage: orca-jira-loop init --assignee \"<your Jira display name or accountId>\"")
	}
	if err := acli.New().ValidateAssignee(*assignee); err != nil {
		log.Fatalf("%v", err)
	}
	if err := (&config.MachineConfig{Assignee: *assignee}).Save(); err != nil {
		log.Fatalf("%v", err)
	}
	p, _ := config.MachineConfigPath()
	fmt.Printf("machine config written to %s (assignee=%q)\n", p, *assignee)
}

func cmdStop(args []string) {
	// `stop serve` asks the central server to shut down over its socket;
	// process exit releases the flock, so no pid file exists to clean up.
	if len(args) != 1 || args[0] != "serve" {
		log.Fatalf("usage: orca-jira-loop stop serve")
	}
	client, err := server.NewClient()
	if err != nil {
		log.Fatalf("%v", err)
	}
	if err := client.Shutdown(); err != nil {
		log.Fatalf("%v", err)
	}
	fmt.Println("server stopped")
}

// cmdReport is invoked by the opencode plugin, once per session.idle, from
// inside the agent's own terminal (cwd = the ticket's worktree). It is a
// one-shot, stateless call: no retry loop inside it, no IPC to the running
// `run` process — Jira is the only shared state.
func cmdReport(args []string) {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	configName := fs.String("config", "", "config name (matches .workflow/<name>.yaml)")
	workflowName := fs.String("workflow", "", "workflow ID inside the config")
	ticket := fs.String("ticket", "", "Jira ticket key")
	agentName := fs.String("agent", "", "opencode agent name")
	status := fs.String("status", "", "agent-reported status name (e.g. done)")
	summary := fs.String("summary", "", "agent's summary of what it did")
	fs.Parse(args)

	if *configName == "" || *workflowName == "" || *ticket == "" || *agentName == "" || *status == "" || *summary == "" {
		log.Fatalf("usage: orca-jira-loop report --config <name> --workflow <id> --ticket <key> --agent <name> --status <name> --summary <text>")
	}

	cfg, err := config.Load(workflowConfigPath(*configName))
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if _, ok := cfg.Workflows[*workflowName]; !ok {
		log.Fatalf("config: unknown workflow %q in %s", *workflowName, workflowConfigPath(*configName))
	}

	acliClient := acli.New()

	result, err := daemon.Report(cfg, acliClient, *workflowName, *ticket, *agentName, *status, *summary)
	// A non-nil err alongside Action=="transitioned" means: the comment
	// (the actual work report) landed, but the Jira transition itself
	// failed — the daemon will retry the transition directly on a later
	// poll. The agent's job is done either way, so stdout must still
	// report "transitioned" for the plugin to close the terminal; only
	// log the transition failure, don't treat it as fatal.
	if err != nil && result.Action != "transitioned" {
		log.Fatalf("report %s: %v", *ticket, err)
	}
	if err != nil {
		log.Printf("report %s: %v", *ticket, err)
	}

	// The agent terminal is deliberately NOT closed here: the session
	// stays alive so the daemon can nudge it in place (preserving full
	// context) if the ticket ever comes back to one of this agent's
	// statuses — e.g. a review bounce. Terminals are closed only by the
	// daemon's closeLeftoverTerminals once the ticket reaches a workflow
	// closeOn status.
	// Printed as JSON on stdout (not log, which goes to stderr) so the
	// plugin can parse it. On "nudged", the plugin itself delivers
	// result.detail back into the session via its own opencode client —
	// this CLI never talks to Orca/opencode, only Jira.
	out, _ := json.Marshal(map[string]string{"action": result.Action, "detail": result.Detail})
	fmt.Println(string(out))
	log.Printf("report %s: workflow=%s agent=%s action=%s detail=%q", *ticket, *workflowName, *agentName, result.Action, result.Detail)
}

func sortedWorkflowNames(cfg *config.Config) []string {
	names := make([]string, 0, len(cfg.Workflows))
	for name := range cfg.Workflows {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// cmdServe runs the central server: one process hosting any number of
// configs submitted over its unix socket. Daemonizes by default like `run`.
func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "log every orca command instead of executing it")
	foreground := fs.Bool("foreground", false, "stay in the foreground")
	fs.Parse(args)

	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("%v", err)
	}
	logPath := filepath.Join(home, ".orca-jira-loop", "server.log")

	if !*foreground {
		// Acquire the single-instance lock in the PARENT, before spawning:
		// this is where the user is watching, so "already running" must
		// fail here, not silently in the detached child. The child
		// re-acquires (re-exec can't inherit the flock), a tiny race we
		// accept: two simultaneous fresh `serve` invocations may both
		// spawn, but the loser's child exits immediately on its own
		// AcquireServerLock.
		release, err := discovery.AcquireServerLock()
		if err != nil {
			log.Fatalf("%v", err)
		}
		release()
		childArgs := []string{"serve", "--foreground"}
		if *dryRun {
			childArgs = append(childArgs, "--dry-run")
		}
		daemonize(logPath, childArgs...)
		return
	}

	// Tee logs to server.log when run interactively; daemonized child
	// already has stderr attached to the log file.
	if os.Getenv("ORCA_JIRA_LOOP_DAEMONIZED") == "" {
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			log.SetOutput(io.MultiWriter(os.Stderr, f))
			defer f.Close()
		}
	}

	// Single instance enforcement via flock: the kernel holds the lock for
	// this process's life and releases it on ANY exit (clean, crash,
	// kill -9), so there is never stale state to clean up. No pid file.
	releaseLock, err := discovery.AcquireServerLock()
	if err != nil {
		log.Fatalf("%v", err)
	}
	defer releaseLock()

	sockPath, err := discovery.SocketPath()
	if err != nil {
		log.Fatalf("%v", err)
	}
	os.Remove(sockPath) // stale socket from a crashed server
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		log.Fatalf("listen %s: %v", sockPath, err)
	}
	defer os.Remove(sockPath)

	srv := server.New(*dryRun, server.ProdDeps(*dryRun))
	log.Printf("orca-jira-loop serve: socket=%s dry-run=%v", sockPath, *dryRun)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		log.Printf("shutting down")
		srv.Shutdown()
	}()
	if err := srv.Serve(ln); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

// cmdSubmit reads a workflow YAML and sends it to the running server.
// cwd must be inside the repo the config governs (repo resolved here,
// client-side, before submit). The config's identity is the `name` field
// inside the YAML — submit takes no name argument.
func cmdSubmit(args []string) {
	fs := flag.NewFlagSet("submit", flag.ExitOnError)
	file := fs.String("f", "", "path to workflow YAML (default .workflow/workflow.yaml)")
	fs.Parse(args)
	if fs.NArg() != 0 {
		log.Fatalf("usage: orca-jira-loop submit [-f <yaml>]  (config name comes from the YAML's `name` field)")
	}
	path := *file
	if path == "" {
		path = workflowConfigPath("workflow")
	}
	yamlBytes, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("read %s: %v", path, err)
	}
	// Parse client-side too, so the confirmation prints the real name and
	// obvious YAML errors fail before touching the server.
	cfg, err := config.Parse(path, yamlBytes)
	if err != nil {
		log.Fatalf("%v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("%v", err)
	}
	client, err := server.NewClient()
	if err != nil {
		log.Fatalf("%v", err)
	}
	if err := client.Submit(cwd, yamlBytes); err != nil {
		log.Fatalf("submit: %v", err)
	}
	fmt.Printf("submitted %s\n", cfg.Name)
}

func cmdRemove(args []string) {
	if len(args) != 1 {
		log.Fatalf("usage: orca-jira-loop remove <config-name>")
	}
	client, err := server.NewClient()
	if err != nil {
		log.Fatalf("%v", err)
	}
	if err := client.Remove(args[0]); err != nil {
		log.Fatalf("remove: %v", err)
	}
	fmt.Println("removed")
}

func cmdList(args []string) {
	client, err := server.NewClient()
	if err != nil {
		log.Fatalf("%v", err)
	}
	infos, err := client.List()
	if err != nil {
		log.Fatalf("list: %v", err)
	}
	if len(infos) == 0 {
		fmt.Println("no configs running")
		return
	}
	for _, in := range infos {
		fmt.Printf("%s\trepo=%s\tsince=%s\n", in.ConfigName, in.RepoPath, in.StartedAt.Format("15:04:05"))
	}
}
