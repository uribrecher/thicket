// Package git wraps the few git plumbing/porcelain commands thicket needs.
// We shell out to the system `git` binary rather than depending on a
// library: the surface area is small, error messages stay native, and the
// install requirement (a working git) is already implied by the tool.
package git

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Runner abstracts command execution so tests can stub it out. Production
// uses DefaultRunner; tests can inject a fake to assert arguments without
// shelling out.
type Runner interface {
	Run(dir, name string, args []string, stdout, stderr io.Writer) error
}

// DefaultRunner shells out to real commands via os/exec.
type DefaultRunner struct{}

func (DefaultRunner) Run(dir, name string, args []string, stdout, stderr io.Writer) error {
	cmd := exec.Command(name, args...) //nolint:gosec // we control name & args
	cmd.Dir = dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// Git wraps Runner with the high-level operations thicket uses.
type Git struct {
	Runner Runner
}

// New returns a Git that shells out via os/exec.
func New() *Git { return &Git{Runner: DefaultRunner{}} }

// AddWorktree creates a new worktree at target from source. If createBranch
// is true, a new branch <branch> is created at HEAD of source; otherwise
// <branch> must already exist in source and is checked out into target.
//
// `git -C <source> worktree add <target> [-b] <branch>`
func (g *Git) AddWorktree(source, target, branch string, createBranch bool) error {
	if branch == "" {
		return errors.New("AddWorktree: branch is required")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}
	args := []string{"-C", source, "worktree", "add"}
	if createBranch {
		args = append(args, target, "-b", branch)
	} else {
		args = append(args, target, branch)
	}
	return g.run("", "git", args)
}

// RemoveWorktree removes the worktree at target. force=true tolerates a
// dirty working copy.
func (g *Git) RemoveWorktree(source, target string, force bool) error {
	args := []string{"-C", source, "worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, target)
	// `git worktree remove` errors if target doesn't exist; tolerate that
	// so rollback can be best-effort.
	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return g.run("", "git", args)
}

// BranchExists reports whether <branch> exists in <source> (local OR
// origin/<branch>). Used to decide whether to pass -b on AddWorktree.
//
// A non-nil error means the source isn't a usable git repository or some
// other unexpected failure happened — distinct from "the ref simply isn't
// there", which is reported as (false, nil).
func (g *Git) BranchExists(source, branch string) (bool, error) {
	// Probe whether the source is a git repo at all. This separates
	// "this repo doesn't have the branch" (false, nil) from "this isn't
	// a git repo / permission denied / git missing" (return error).
	if err := g.run("", "git", []string{"-C", source, "rev-parse", "--git-dir"}); err != nil {
		return false, fmt.Errorf("not a git repo at %s: %w", source, err)
	}
	// Local branch check first.
	if err := g.run("", "git", []string{"-C", source, "show-ref", "--verify",
		"--quiet", "refs/heads/" + branch}); err == nil {
		return true, nil
	}
	// Remote-tracking branch check.
	if err := g.run("", "git", []string{"-C", source, "show-ref", "--verify",
		"--quiet", "refs/remotes/origin/" + branch}); err == nil {
		return true, nil
	}
	return false, nil
}

// Clone clones <sshURL> into <targetDir>, streaming git's stdout/stderr to
// the caller-provided writers (typically os.Stdout/Stderr) so progress is
// visible during a long clone.
func (g *Git) Clone(sshURL, targetDir string, stdout, stderr io.Writer) error {
	if err := os.MkdirAll(filepath.Dir(targetDir), 0o755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}
	return g.Runner.Run("", "git", []string{"clone", sshURL, targetDir}, stdout, stderr)
}

// CurrentBranch returns the currently checked-out branch in source.
func (g *Git) CurrentBranch(source string) (string, error) {
	var out bytes.Buffer
	if err := g.Runner.Run("", "git", []string{"-C", source, "symbolic-ref",
		"--short", "HEAD"}, &out, io.Discard); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}

// run is a stdout/stderr-discarding convenience around Runner.Run with a
// fail-with-stderr error wrap.
func (g *Git) run(dir, name string, args []string) error {
	var stderr bytes.Buffer
	if err := g.Runner.Run(dir, name, args, io.Discard, &stderr); err != nil {
		s := strings.TrimSpace(stderr.String())
		if s == "" {
			return err
		}
		return fmt.Errorf("%s %s: %w (%s)", name, strings.Join(args, " "), err, s)
	}
	return nil
}
