// Package discovery resolves the current Orca repo and manages the
// central relayflow server's fixed-location artifacts under ~/.relayflow/
// (socket + flock). Single-instance enforcement is flock-based: the
// kernel releases the lock on any process exit, so there is no stale
// state and no pid files anywhere.
package discovery

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func dir(workflowName string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(home, ".relayflow", workflowName)
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
// on: ~/.relayflow/server.sock.
func SocketPath() (string, error) {
	d, err := dir("")
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "server.sock"), nil
}

// ServerLockPath returns the flock file enforcing a single `serve`
// process: ~/.relayflow/server.lock.
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


