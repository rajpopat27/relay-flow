// Command orca-jira-loop automates the Jira <-> Orca <-> opencode workflow
// described in docs/jira-workflow-architecture.md. `run` polls Jira and
// dispatches agents; `report` is a one-shot call (invoked by the opencode
// plugin) that parses an agent's final message and transitions Jira — no
// network transport, no shared daemon state between the two.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"orca-jira-loop/internal/acli"
	"orca-jira-loop/internal/config"
	"orca-jira-loop/internal/daemon"
	"orca-jira-loop/internal/discovery"
)

// workflowConfigPath is always relative to cwd — the poll loop must be run
// from inside the repo it governs; `report` likewise inherits cwd from the
// terminal it's invoked in, which is always the ticket's own worktree.
func workflowConfigPath(workflowName string) string {
	return filepath.Join(".workflow", workflowName+".yaml")
}

// logPathFor returns the workflow's log file under
// ~/.orca-jira-loop/<workflow>/daemon.log, creating the dir if needed.
func logPathFor(workflowName string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".orca-jira-loop", workflowName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.log"), nil
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "run":
		cmdRun(os.Args[2:])
	case "stop":
		cmdStop(os.Args[2:])
	case "report":
		cmdReport(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: orca-jira-loop run [--dry-run] <workflow-name>")
	fmt.Fprintln(os.Stderr, "       orca-jira-loop stop <workflow-name>")
	fmt.Fprintln(os.Stderr, "       orca-jira-loop report --workflow <name> --ticket <key> --agent <name> --status <name> --summary <text>")
	fmt.Fprintln(os.Stderr, "  config is always read from .workflow/<workflow-name>.yaml (relative to cwd)")
	fmt.Fprintln(os.Stderr, "  pid/log files are always under ~/.orca-jira-loop/<workflow-name>/")
}

func cmdStop(args []string) {
	if len(args) < 1 {
		log.Fatalf("usage: orca-jira-loop stop <workflow-name>")
	}
	workflowName := args[0]
	if err := discovery.StopRunning(workflowName); err != nil {
		log.Fatalf("%v", err)
	}
	fmt.Println("stopped")
}

func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "log every orca command instead of executing it (acli/Jira calls always run)")
	foreground := fs.Bool("foreground", false, "stay in the foreground (default: self-daemonize, logs to ~/.orca-jira-loop/<workflow>/daemon.log)")
	fs.Parse(args)
	// Flags must come before the workflow name: `run [--dry-run] <name>`.
	if fs.NArg() != 1 {
		log.Fatalf("usage: orca-jira-loop run [--dry-run] [--foreground] <workflow-name>")
	}
	workflowName := fs.Arg(0)

	if !*foreground {
		// Re-exec ourselves detached with --foreground, log file attached
		// as stdout+stderr. No supervisor, no IPC: the child IS `run`,
		// the pid file it writes is the only state.
		logPath, err := logPathFor(workflowName)
		if err != nil {
			log.Fatalf("%v", err)
		}
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			log.Fatalf("open log file: %v", err)
		}
		self, err := os.Executable()
		if err != nil {
			log.Fatalf("resolve executable: %v", err)
		}
		cmd := exec.Command(self, "run", "--foreground")
		if *dryRun {
			cmd.Args = append(cmd.Args, "--dry-run")
		}
		cmd.Args = append(cmd.Args, workflowName)
		cmd.Stdout, cmd.Stderr = f, f
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		cmd.Env = append(os.Environ(), "ORCA_JIRA_LOOP_DAEMONIZED=1")
		if err := cmd.Start(); err != nil {
			log.Fatalf("daemonize: %v", err)
		}
		fmt.Printf("started (pid %d), logging to %s\n", cmd.Process.Pid, logPath)
		return
	}

	// Foreground worker (also what the daemonized parent re-execs): when
	// launched directly by a human (interactive stderr), tee logs to the
	// workflow's log file as well; when re-execed by the daemonizing
	// parent, stderr already IS the log file — plain output, no tee.
	if os.Getenv("ORCA_JIRA_LOOP_DAEMONIZED") == "" {
		if logPath, err := logPathFor(workflowName); err == nil {
			if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
				log.SetOutput(io.MultiWriter(os.Stderr, f))
				defer f.Close()
			}
		}
	}

	cfg, err := config.Load(workflowConfigPath(workflowName))
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	repoID, repoDisplayName, err := discovery.CurrentRepo()
	if err != nil {
		log.Fatalf("resolve current repo (orca worktree current): %v", err)
	}

	if err := discovery.AcquirePidFile(workflowName); err != nil {
		log.Fatalf("%v", err)
	}
	defer discovery.ReleasePidFile(workflowName)

	log.Printf("orca-jira-loop starting: workflow=%s repo=%s dry-run=%v", workflowName, repoDisplayName, *dryRun)
	log.Printf("config = %s", workflowConfigPath(workflowName))
	log.Printf("jql (base) = %q", cfg.JQL)
	log.Printf("poll_interval_seconds=%d", cfg.PollIntervalSeconds)

	d := daemon.New(workflowName, cfg, repoID, repoDisplayName, *dryRun)

	// Fail fast on YAML status typos: verify every status name the
	// workflow references (statuses map, jira_status_on targets,
	// close_on_statuses) is a real status in the Jira project. Jira's
	// JQL parser rejects unknown status values, so one cheap search per
	// distinct name catches "DO Done"-style typos before the loop starts
	// dispatching on a config that can never match.
	projectKey, err := daemon.ProjectKeyFromJQL(cfg.JQL)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if bad, err := daemon.ValidateStatuses(cfg, acli.New(), projectKey); err != nil {
		log.Fatalf("config: %v", err)
	} else if len(bad) > 0 {
		log.Fatalf("config: invalid Jira statuses in %s: %s", workflowConfigPath(workflowName), strings.Join(bad, ", "))
	}
	log.Printf("config: all referenced Jira statuses verified against project %s", projectKey)

	stop := make(chan struct{})
	go d.PollLoop(stop)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Printf("shutting down")
	close(stop)
}

// cmdReport is invoked by the opencode plugin, once per session.idle, from
// inside the agent's own terminal (cwd = the ticket's worktree). It is a
// one-shot, stateless call: no retry loop inside it, no IPC to the running
// `run` process — Jira is the only shared state.
func cmdReport(args []string) {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	workflowName := fs.String("workflow", "", "workflow name (matches .workflow/<name>.yaml)")
	ticket := fs.String("ticket", "", "Jira ticket key")
	agentName := fs.String("agent", "", "opencode agent name")
	status := fs.String("status", "", "agent-reported status name (e.g. done)")
	summary := fs.String("summary", "", "agent's summary of what it did")
	fs.Parse(args)

	if *workflowName == "" || *ticket == "" || *agentName == "" || *status == "" || *summary == "" {
		log.Fatalf("usage: orca-jira-loop report --workflow <name> --ticket <key> --agent <name> --status <name> --summary <text>")
	}

	cfg, err := config.Load(workflowConfigPath(*workflowName))
	if err != nil {
		log.Fatalf("config: %v", err)
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
	// daemon's closeLeftoverTerminals once the ticket reaches a status
	// with no mapped agent.
	// Printed as JSON on stdout (not log, which goes to stderr) so the
	// plugin can parse it. On "nudged", the plugin itself delivers
	// result.detail back into the session via its own opencode client —
	// this CLI never talks to Orca/opencode, only Jira.
	out, _ := json.Marshal(map[string]string{"action": result.Action, "detail": result.Detail})
	fmt.Println(string(out))
	log.Printf("report %s: agent=%s action=%s detail=%q", *ticket, *agentName, result.Action, result.Detail)
}
