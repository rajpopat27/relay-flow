// Package discovery manages the poll loop's per-workflow pid/log files
// under the fixed default location ~/.orca-jira-loop/<workflow-name>/. No
// flags needed to locate these files. Keyed by workflow name (not repoId
// or cwd) since a workflow-name invocation of `run` may happen from any
// worktree/directory.
package discovery

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func dir(workflowName string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(home, ".orca-jira-loop", workflowName)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", err
	}
	return d, nil
}

// CurrentRepo resolves the repoId and the *repo's* displayName (not the
// worktree's own displayName, which is a different, worktree-scoped
// value) for the repo the CLI is running in, via `orca worktree current`
// followed by `orca repo show`.
func CurrentRepo() (repoID, repoDisplayName string, err error) {
	return RepoFromPath(".")
}

// RepoFromPath is CurrentRepo rooted at an arbitrary directory instead of
// the process cwd — used by the server, which receives the repo path from
// the submitting client.
func RepoFromPath(path string) (repoID, repoDisplayName string, err error) {
	cmd := exec.Command("orca", "worktree", "current", "--json")
	cmd.Dir = path
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("orca worktree current: %w", err)
	}
	var wres struct {
		Result struct {
			Worktree struct {
				RepoID string `json:"repoId"`
			} `json:"worktree"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &wres); err != nil || wres.Result.Worktree.RepoID == "" {
		return "", "", fmt.Errorf("orca worktree current: no repoId in output")
	}
	repoID = wres.Result.Worktree.RepoID

	repoCmd := exec.Command("orca", "repo", "show", "--repo", "id:"+repoID, "--json")
	repoCmd.Dir = path
	out, err = repoCmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("orca repo show: %w", err)
	}
	var rres struct {
		Result struct {
			Repo struct {
				DisplayName string `json:"displayName"`
			} `json:"repo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &rres); err != nil || rres.Result.Repo.DisplayName == "" {
		return "", "", fmt.Errorf("orca repo show: no displayName in output")
	}
	return repoID, rres.Result.Repo.DisplayName, nil
}

// SocketPath returns the unix socket the central `serve` process listens
// on: ~/.orca-jira-loop/server.sock.
func SocketPath() (string, error) {
	d, err := dir("")
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "server.sock"), nil
}

// ServerLockPath returns the flock file enforcing a single `serve`
// process: ~/.orca-jira-loop/server.lock.
func ServerLockPath() (string, error) {
	d, err := dir("")
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "server.lock"), nil
}

// AcquireServerLock takes an exclusive non-blocking flock on the server
// lock file. The lock is held by the kernel for the life of the returned
// file's descriptor: process exit (clean, crash, or kill -9) releases it
// automatically, so there is no stale-state cleanup. Returns a release
// func (also runs at process exit implicitly).
func AcquireServerLock() (release func(), err error) {
	path, err := ServerLockPath()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("server already running (lock held: %s)", path)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// PidPath returns the default pid file path for single-instance enforcement.
func PidPath(workflowName string) (string, error) {
	d, err := dir(workflowName)
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "daemon.pid"), nil
}

// AcquirePidFile writes the current PID, refusing if a live poll loop
// already owns this workflowName.
func AcquirePidFile(workflowName string) error {
	path, err := PidPath(workflowName)
	if err != nil {
		return err
	}
	if b, err := os.ReadFile(path); err == nil {
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(b))); perr == nil {
			if processAlive(pid) {
				return fmt.Errorf("orca-jira-loop already running for workflow %q, pid %d", workflowName, pid)
			}
		}
	}
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o644)
}

func ReleasePidFile(workflowName string) {
	if path, err := PidPath(workflowName); err == nil {
		os.Remove(path)
	}
}

// StopRunning sends SIGTERM to the poll loop owning workflowName's pid
// file, and waits briefly for it to exit and release the pid file itself.
func StopRunning(workflowName string) error {
	path, err := PidPath(workflowName)
	if err != nil {
		return err
	}
	return StopPidFile(path)
}

// StopPidFile sends SIGTERM to the process named by the pid file at path,
// waiting briefly for exit. The owning process removes the pid file itself
// on shutdown; a stale pid file (dead process) is removed here.
func StopPidFile(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("no orca-jira-loop running (pid file %s)", path)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || !processAlive(pid) {
		os.Remove(path)
		return fmt.Errorf("no orca-jira-loop running (pid file %s)", path)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal pid %d: %w", pid, err)
	}
	for i := 0; i < 50; i++ {
		if !processAlive(pid) {
			os.Remove(path)
			return nil
		}
		<-time.After(100 * time.Millisecond)
	}
	return fmt.Errorf("pid %d did not exit after SIGTERM", pid)
}

func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
