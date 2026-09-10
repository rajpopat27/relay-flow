package herdr

import (
	"fmt"
	"os/exec"
	"strings"
)

// originBaseRef resolves the branch new ticket worktrees are created from.
// The base is always the repository's origin branch; the ladder is
// origin/HEAD, then origin/main, then origin/master. A repository without an
// origin remote falls back to its local main or master.
//
// The base only applies when the ticket branch does not exist yet: Herdr
// checks out an existing branch as-is, so prior agent commits are preserved.
func originBaseRef(repoPath string) (string, error) {
	if ref, ok := gitValue(repoPath, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); ok && ref != "" {
		return ref, nil
	}
	for _, ref := range []string{"origin/main", "origin/master"} {
		if gitHasRef(repoPath, "refs/remotes/"+ref) {
			return ref, nil
		}
	}
	if !gitHasOrigin(repoPath) {
		for _, ref := range []string{"main", "master"} {
			if gitHasRef(repoPath, "refs/heads/"+ref) {
				return ref, nil
			}
		}
	}
	return "", fmt.Errorf("herdr: repository %q has no origin/HEAD, origin/main, origin/master, or local main/master to branch from; set it with: git -C %s remote set-head origin -a", repoPath, repoPath)
}

func gitValue(repoPath string, args ...string) (string, bool) {
	cmd := exec.Command("git", append([]string{"-C", repoPath}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

func gitHasRef(repoPath, ref string) bool {
	_, ok := gitValue(repoPath, "rev-parse", "--verify", "--quiet", ref)
	return ok
}

func gitHasOrigin(repoPath string) bool {
	out, ok := gitValue(repoPath, "remote")
	if !ok {
		return false
	}
	for _, remote := range strings.Fields(out) {
		if remote == "origin" {
			return true
		}
	}
	return false
}
