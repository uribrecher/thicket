package git

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initRepo creates a git repo at dir with one initial commit on `main`.
func initRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sh(t, dir, "git", "init", "-q", "-b", "main")
	sh(t, dir, "git", "config", "user.email", "test@example.com")
	sh(t, dir, "git", "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	sh(t, dir, "git", "add", ".")
	sh(t, dir, "git", "commit", "-q", "-m", "init")
}

func sh(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = io.Discard
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, stderr.String())
	}
}

func TestAddWorktree_createsNewBranch(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	tgt := filepath.Join(root, "wt")
	initRepo(t, src)

	g := New()
	if err := g.AddWorktree(src, tgt, "feat/test", true); err != nil {
		t.Fatalf("add worktree: %v", err)
	}
	// Verify branch was checked out in target
	got, err := g.CurrentBranch(tgt)
	if err != nil {
		t.Fatalf("current branch: %v", err)
	}
	if got != "feat/test" {
		t.Errorf("got branch %q, want feat/test", got)
	}
}

func TestAddWorktree_checksOutExistingBranch(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	tgt := filepath.Join(root, "wt")
	initRepo(t, src)
	// Create branch in source first
	sh(t, src, "git", "branch", "preexisting")

	g := New()
	if err := g.AddWorktree(src, tgt, "preexisting", false); err != nil {
		t.Fatalf("add worktree: %v", err)
	}
	got, _ := g.CurrentBranch(tgt)
	if got != "preexisting" {
		t.Errorf("got branch %q, want preexisting", got)
	}
}

func TestAddWorktree_emptyBranch(t *testing.T) {
	if err := New().AddWorktree("/tmp/src", "/tmp/tgt", "", false); err == nil {
		t.Error("expected error for empty branch")
	}
}

func TestBranchExists(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	initRepo(t, src)
	sh(t, src, "git", "branch", "alpha")

	g := New()
	got, err := g.BranchExists(src, "alpha")
	if err != nil || !got {
		t.Errorf("alpha should exist; got=%v err=%v", got, err)
	}
	got, err = g.BranchExists(src, "no-such-branch")
	if err != nil || got {
		t.Errorf("no-such-branch should not exist; got=%v err=%v", got, err)
	}
}

func TestRemoveWorktree_roundtrip(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	tgt := filepath.Join(root, "wt")
	initRepo(t, src)

	g := New()
	if err := g.AddWorktree(src, tgt, "x", true); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := g.RemoveWorktree(src, tgt, true); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(tgt); !os.IsNotExist(err) {
		t.Errorf("worktree dir still exists: %v", err)
	}
}

func TestRemoveWorktree_tolerantOfMissing(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	initRepo(t, src)
	if err := New().RemoveWorktree(src, filepath.Join(root, "absent"), false); err != nil {
		t.Errorf("remove of absent target should be no-op, got %v", err)
	}
}

func TestClone_localRepo(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	tgt := filepath.Join(root, "clone")
	initRepo(t, src)
	var out bytes.Buffer
	if err := New().Clone(src, tgt, &out, &out); err != nil {
		t.Fatalf("clone: %v\n%s", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(tgt, "README.md")); err != nil {
		t.Errorf("README missing after clone: %v", err)
	}
}

// fakeRunner records the arguments it was given.
type fakeRunner struct {
	gotDir, gotName string
	gotArgs         []string
	exit            error
}

func (f *fakeRunner) Run(dir, name string, args []string, _, _ io.Writer) error {
	f.gotDir, f.gotName, f.gotArgs = dir, name, args
	return f.exit
}

func TestAddWorktree_argsShape(t *testing.T) {
	fr := &fakeRunner{}
	g := &Git{Runner: fr}
	if err := g.AddWorktree("/src", "/dst", "br", true); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(fr.gotArgs, " ")
	want := "-C /src worktree add /dst -b br"
	if got != want {
		t.Errorf("args: got %q, want %q", got, want)
	}
	fr2 := &fakeRunner{}
	g2 := &Git{Runner: fr2}
	if err := g2.AddWorktree("/src", "/dst", "br", false); err != nil {
		t.Fatal(err)
	}
	got2 := strings.Join(fr2.gotArgs, " ")
	want2 := "-C /src worktree add /dst br"
	if got2 != want2 {
		t.Errorf("args: got %q, want %q", got2, want2)
	}
}
