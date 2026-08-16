// Command relay automates tracker ↔ runner agent workflows.
// `serve` is a central process hosting any number of workflows submitted via
// `submit`. `report` is a one-shot socket client (invoked by the opencode
// plugin) that asks the server to record an agent outcome.
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

	"syscall"

	"relay/internal/acli"
	"relay/internal/config"
	"relay/internal/discovery"
	"relay/internal/server"
)

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
	case "serve":
		cmdServe(os.Args[2:])
	case "submit":
		cmdSubmit(os.Args[2:])
	case "report":
		cmdReport(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: relay init --assignee \"<your Jira display name or accountId>\"")
	fmt.Fprintln(os.Stderr, "       relay stop serve")
	fmt.Fprintln(os.Stderr, "       relay serve [--dry-run] [--foreground]")
	fmt.Fprintln(os.Stderr, "       relay submit [-f <yaml>]   (workflow name comes from the YAML's name field)")
	fmt.Fprintln(os.Stderr, "       relay report --workflow <name> --ticket <key> --node <node> --outcome <success|failure> --summary <text>")
	fmt.Fprintln(os.Stderr, "  server artifacts (lock/sock/log) are always under ~/.relay/")
}

// daemonize re-execs the binary detached with childArgs, log file attached
// as stdout+stderr. No supervisor, no IPC: the child IS the worker. Returns
// after spawning.
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
	cmd.Env = append(os.Environ(), "RELAY_DAEMONIZED=1")
	if err := cmd.Start(); err != nil {
		log.Fatalf("daemonize: %v", err)
	}
	fmt.Printf("started (pid %d), logging to %s\n", cmd.Process.Pid, logPath)
}

// cmdInit writes the machine config (~/.relay/config.yaml) with this machine
// user's tracker identity, probe-validated against the tracker.
func cmdInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	assignee := fs.String("assignee", "", "your tracker display name or accountId")
	fs.Parse(args)
	if *assignee == "" {
		log.Fatalf("usage: relay init --assignee \"<your tracker display name or accountId>\"")
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
		log.Fatalf("usage: relay stop serve")
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

// cmdServe runs the central server: one process hosting any number of
// workflows submitted over its unix socket. Daemonizes by default.
func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "log every runner command instead of executing it")
	foreground := fs.Bool("foreground", false, "stay in the foreground")
	fs.Parse(args)

	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("%v", err)
	}
	logPath := filepath.Join(home, ".relay", "server.log")

	if !*foreground {
		// Acquire the single-instance lock in the PARENT, before spawning:
		// this is where the user is watching, so "already running" must
		// fail here, not silently in the detached child.
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
	if os.Getenv("RELAY_DAEMONIZED") == "" {
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
	log.Printf("relay serve: socket=%s dry-run=%v", sockPath, *dryRun)

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

// cmdReport is invoked by the opencode plugin once per session.idle.
// Thin socket client: the server resolves the outcome edge and calls the
// tasks adapter — no config load, no tracker calls, no fallback. The
// agent terminal is deliberately NOT closed here: it stays alive for
// bounce nudges; only closeOn nodes close terminals.
func cmdReport(args []string) {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	workflow := fs.String("workflow", "", "workflow name")
	ticket := fs.String("ticket", "", "ticket key")
	node := fs.String("node", "", "node name")
	outcome := fs.String("outcome", "", "success or failure")
	summary := fs.String("summary", "", "agent's summary of what it did")
	fs.Parse(args)
	if *workflow == "" || *ticket == "" || *node == "" || *outcome == "" || *summary == "" {
		log.Fatalf("usage: relay report --workflow <name> --ticket <key> --node <node> --outcome <success|failure> --summary <text>")
	}
	client, err := server.NewClient()
	if err != nil {
		log.Fatalf("%v", err)
	}
	result, err := client.Report(*workflow, *ticket, *node, *outcome, *summary)
	if err != nil {
		log.Fatalf("report %s: %v", *ticket, err)
	}
	// JSON on stdout (log goes to stderr) so the plugin can parse it.
	out, _ := json.Marshal(map[string]string{"action": result.Action, "detail": result.Detail})
	fmt.Println(string(out))
	log.Printf("report %s: workflow=%s node=%s action=%s detail=%q", *ticket, *workflow, *node, result.Action, result.Detail)
}

// cmdSubmit reads a workflow YAML and sends it to the running server. cwd
// must be inside the repo the workflow governs (repo resolved client-side).
// The workflow's identity is the `name` field inside the YAML.
func cmdSubmit(args []string) {
	fs := flag.NewFlagSet("submit", flag.ExitOnError)
	file := fs.String("f", "", "path to workflow YAML (default .workflow/workflow.yaml)")
	fs.Parse(args)
	if fs.NArg() != 0 {
		log.Fatalf("usage: relay submit [-f <yaml>]  (workflow name comes from the YAML's `name` field)")
	}
	path := *file
	if path == "" {
		path = filepath.Join(".workflow", "workflow.yaml")
	}
	yamlBytes, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("read %s: %v", path, err)
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
	fmt.Println("submitted")
}
