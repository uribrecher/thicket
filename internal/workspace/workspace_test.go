package workspace

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/uribrecher/thicket/internal/git"
	"github.com/uribrecher/thicket/internal/memory"
)

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

func initRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sh(t, dir, "git", "init", "-q", "-b", "main")
	sh(t, dir, "git", "config", "user.email", "t@example.com")
	sh(t, dir, "git", "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	sh(t, dir, "git", "add", ".")
	sh(t, dir, "git", "commit", "-q", "-m", "init")
}

func basePlan(root string) Plan {
	srcA := filepath.Join(root, "src", "alpha")
	srcB := filepath.Join(root, "src", "beta")
	wsDir := filepath.Join(root, "tasks", "sc-1-x")
	return Plan{
		WorkspaceDir: wsDir,
		Branch:       "u/sc-1-x",
		Repos: []PlanRepo{
			{Name: "alpha", SourcePath: srcA, WorktreePath: filepath.Join(wsDir, "alpha"), BranchExists: false},
			{Name: "beta", SourcePath: srcB, WorktreePath: filepath.Join(wsDir, "beta"), BranchExists: false},
		},
		Memory: memory.Input{
			TicketID: "sc-1", Title: "X", Body: "body",
			Branch: "u/sc-1-x", WorkspaceDir: wsDir,
			Repos: []memory.RepoEntry{
				{Name: "alpha", Branch: "u/sc-1-x", WorktreePath: filepath.Join(wsDir, "alpha"), DefaultBranch: "main"},
				{Name: "beta", Branch: "u/sc-1-x", WorktreePath: filepath.Join(wsDir, "beta"), DefaultBranch: "main"},
			},
			CreatedAt: time.Date(2026, 5, 13, 12, 30, 0, 0, time.UTC),
		},
	}
}

func TestCreate_happyPath(t *testing.T) {
	root := t.TempDir()
	p := basePlan(root)
	initRepo(t, p.Repos[0].SourcePath)
	initRepo(t, p.Repos[1].SourcePath)

	w := New(git.New())
	if err := w.Create(p); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Worktrees exist and are on the branch
	g := git.New()
	for _, r := range p.Repos {
		got, err := g.CurrentBranch(r.WorktreePath)
		if err != nil {
			t.Errorf("current branch %s: %v", r.Name, err)
		}
		if got != p.Branch {
			t.Errorf("%s branch = %q, want %q", r.Name, got, p.Branch)
		}
	}
	// CLAUDE.local.md exists
	if _, err := os.Stat(filepath.Join(p.WorkspaceDir, memory.FileName)); err != nil {
		t.Errorf("memory file missing: %v", err)
	}
	// State manifest written
	st, err := ReadState(p.WorkspaceDir)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if st.TicketID != "sc-1" || st.Branch != "u/sc-1-x" || len(st.Repos) != 2 {
		t.Errorf("state mismatch: %+v", st)
	}
}

func TestCreate_writesProgressLines(t *testing.T) {
	root := t.TempDir()
	p := basePlan(root)
	initRepo(t, p.Repos[0].SourcePath)
	initRepo(t, p.Repos[1].SourcePath)

	var buf bytes.Buffer
	p.Progress = &buf

	if err := New(git.New()).Create(p); err != nil {
		t.Fatalf("create: %v", err)
	}

	got := buf.String()
	// One ✓ per worktree + memory file + state manifest. The
	// ordering matters — it matches the user-visible sequence
	// and the rollback contract (worktrees first, then memory,
	// then manifest). Walk through with strings.Index so each
	// expected line MUST appear after the previous one.
	wantOrdered := []string{
		"✓ worktree: " + p.Repos[0].Name,
		"✓ worktree: " + p.Repos[1].Name,
		"✓ wrote " + memory.FileName,
		"✓ wrote .thicket/state.json",
	}
	cursor := 0
	for _, want := range wantOrdered {
		idx := strings.Index(got[cursor:], want)
		if idx < 0 {
			t.Errorf("progress output missing (or out of order) %q after cursor %d\nfull output:\n%s",
				want, cursor, got)
			return
		}
		cursor += idx + len(want)
	}
}

func TestCreate_nilProgressIsSilent(t *testing.T) {
	root := t.TempDir()
	p := basePlan(root)
	initRepo(t, p.Repos[0].SourcePath)
	initRepo(t, p.Repos[1].SourcePath)
	// Plan.Progress left nil — must not panic.
	if err := New(git.New()).Create(p); err != nil {
		t.Fatalf("create: %v", err)
	}
}

func TestCreate_collisionReturnsErrExists(t *testing.T) {
	root := t.TempDir()
	p := basePlan(root)
	if err := os.MkdirAll(p.WorkspaceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	err := New(git.New()).Create(p)
	if !errors.Is(err, ErrExists) {
		t.Fatalf("want ErrExists, got %v", err)
	}
}

func TestCreate_rollbackOnFailure(t *testing.T) {
	root := t.TempDir()
	p := basePlan(root)
	initRepo(t, p.Repos[0].SourcePath)
	// beta's source intentionally not initialised → second AddWorktree
	// fails → rollback should clean alpha + workspace dir.
	if err := os.MkdirAll(p.Repos[1].SourcePath, 0o755); err != nil {
		t.Fatal(err)
	}

	err := New(git.New()).Create(p)
	if err == nil {
		t.Fatal("expected failure")
	}
	if _, err := os.Stat(p.WorkspaceDir); !os.IsNotExist(err) {
		t.Errorf("workspace dir should be removed after rollback, stat=%v", err)
	}
	// alpha's worktree should also be gone
	if _, err := os.Stat(p.Repos[0].WorktreePath); !os.IsNotExist(err) {
		t.Errorf("alpha worktree should be gone, stat=%v", err)
	}
}

func TestRemove_cleansWorktreesAndDir(t *testing.T) {
	root := t.TempDir()
	p := basePlan(root)
	initRepo(t, p.Repos[0].SourcePath)
	initRepo(t, p.Repos[1].SourcePath)
	w := New(git.New())
	if err := w.Create(p); err != nil {
		t.Fatal(err)
	}
	if err := w.Remove(p.WorkspaceDir, true, nil); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(p.WorkspaceDir); !os.IsNotExist(err) {
		t.Errorf("workspace dir should be gone, stat=%v", err)
	}
	// Source repos should NOT list the worktrees anymore
	for _, r := range p.Repos {
		out, err := exec.Command("git", "-C", r.SourcePath, "worktree", "list").Output()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), r.WorktreePath) {
			t.Errorf("%s still lists worktree: %s", r.Name, out)
		}
	}
}

func TestRemove_preservesWorkspaceWhenWorktreeRemovalFails(t *testing.T) {
	root := t.TempDir()
	p := basePlan(root)
	initRepo(t, p.Repos[0].SourcePath)
	initRepo(t, p.Repos[1].SourcePath)
	w := New(git.New())
	if err := w.Create(p); err != nil {
		t.Fatal(err)
	}
	// Dirty up the first worktree so a non-force remove will refuse it.
	if err := os.WriteFile(filepath.Join(p.Repos[0].WorktreePath, "dirty.txt"),
		[]byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := w.Remove(p.WorkspaceDir, false, nil); err == nil {
		t.Fatal("expected error when worktree removal fails")
	}
	// Critically: the workspace dir must still exist with the user's file.
	if _, err := os.Stat(p.WorkspaceDir); err != nil {
		t.Errorf("workspace should be preserved when removal fails: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.Repos[0].WorktreePath, "dirty.txt")); err != nil {
		t.Errorf("uncommitted file should survive: %v", err)
	}
}

func TestRemove_writesProgressLines(t *testing.T) {
	root := t.TempDir()
	p := basePlan(root)
	initRepo(t, p.Repos[0].SourcePath)
	initRepo(t, p.Repos[1].SourcePath)
	w := New(git.New())
	if err := w.Create(p); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := w.Remove(p.WorkspaceDir, true, &buf); err != nil {
		t.Fatalf("remove: %v", err)
	}

	got := buf.String()
	for _, want := range []string{
		"✓ removed worktree " + p.Repos[0].Name,
		"✓ removed worktree " + p.Repos[1].Name,
		"✓ deleted workspace directory: " + p.WorkspaceDir,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("progress output missing %q\nfull output:\n%s", want, got)
		}
	}
}

func TestRemove_writesFailureProgress(t *testing.T) {
	root := t.TempDir()
	p := basePlan(root)
	initRepo(t, p.Repos[0].SourcePath)
	initRepo(t, p.Repos[1].SourcePath)
	w := New(git.New())
	if err := w.Create(p); err != nil {
		t.Fatal(err)
	}
	// Dirty the first worktree so a non-force remove will refuse it.
	if err := os.WriteFile(filepath.Join(p.Repos[0].WorktreePath, "dirty.txt"),
		[]byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := w.Remove(p.WorkspaceDir, false, &buf); err == nil {
		t.Fatal("expected error when worktree removal fails")
	}

	got := buf.String()
	for _, want := range []string{
		"✗ could not remove worktree " + p.Repos[0].Name,
		"workspace directory preserved",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("failure-progress output missing %q\nfull output:\n%s", want, got)
		}
	}
	// The final-delete ✓ must NOT appear — the workspace was preserved.
	if strings.Contains(got, "deleted workspace directory") {
		t.Errorf("output should not announce delete on failure path:\n%s", got)
	}
}

func TestRemove_noManifest_refusesWithoutForce(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "ws")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	err := New(git.New()).Remove(dir, false, nil)
	if !errors.Is(err, ErrNoState) {
		t.Fatalf("want ErrNoState, got %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("dir should be preserved when refused, stat=%v", err)
	}
}

func TestRemove_noManifest_forceDeletes(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "ws")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := New(git.New()).Remove(dir, true, nil); err != nil {
		t.Fatalf("force remove: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("dir should be removed under force")
	}
}

func TestListManaged_skipsNonWorkspacesAndWarnsOnCorruptManifests(t *testing.T) {
	root := t.TempDir()

	// Bare directory with no .thicket/state.json — should be skipped silently.
	if err := os.MkdirAll(filepath.Join(root, "not-a-thicket-ws"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Regular file at the top level — should be ignored entirely.
	if err := os.WriteFile(filepath.Join(root, "stray.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Two valid workspaces with different CreatedAt values to verify ordering.
	older := filepath.Join(root, "ws-older")
	newer := filepath.Join(root, "ws-newer")
	writeFakeState(t, older, State{
		TicketID: "sc-1", Branch: "a", CreatedAt: time.Now().Add(-2 * time.Hour),
	})
	writeFakeState(t, newer, State{
		TicketID: "sc-2", Branch: "b", CreatedAt: time.Now(),
	})
	// Corrupt manifest — should surface as a warning, not a panic.
	corrupt := filepath.Join(root, "ws-corrupt")
	if err := os.MkdirAll(filepath.Join(corrupt, ".thicket"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corrupt, ".thicket", "state.json"),
		[]byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, warnings, err := ListManaged(root)
	if err != nil {
		t.Fatalf("unexpected fatal error: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("want 2 workspaces, got %d: %+v", len(got), got)
	}
	if got[0].Slug != "ws-newer" || got[1].Slug != "ws-older" {
		t.Errorf("ordering wrong: %s then %s (want ws-newer then ws-older)",
			got[0].Slug, got[1].Slug)
	}
	if got[0].State.TicketID != "sc-2" {
		t.Errorf("newer state not propagated: %+v", got[0].State)
	}
	if len(warnings) != 1 {
		t.Fatalf("want 1 warning for corrupt manifest, got %d: %v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0].Error(), "ws-corrupt") {
		t.Errorf("warning should name the corrupt slug, got %v", warnings[0])
	}
}

func TestFindContainingWorkspace(t *testing.T) {
	root := t.TempDir()
	slug := "sc-1-fix-thing"
	wsDir := filepath.Join(root, slug)
	st := State{TicketID: "sc-1", Branch: "u/sc-1-fix-thing", CreatedAt: time.Now()}
	writeFakeState(t, wsDir, st)

	// A worktree subdir, mimicking `cd workspace/<repo>` — the real
	// common case.
	repoSub := filepath.Join(wsDir, "some-repo")
	if err := os.MkdirAll(repoSub, 0o755); err != nil {
		t.Fatal(err)
	}
	// A sibling under root with no manifest — pwd here must NOT match.
	bareSibling := filepath.Join(root, "not-a-thicket-ws")
	if err := os.MkdirAll(bareSibling, 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		cwd       string
		wantSlug  string
		wantMatch bool
	}{
		{"cwd is workspace dir", wsDir, slug, true},
		{"cwd inside worktree", repoSub, slug, true},
		{"cwd is root itself", root, "", false},
		{"cwd outside root", t.TempDir(), "", false},
		{"cwd in sibling without manifest", bareSibling, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := FindContainingWorkspace(root, tc.cwd)
			if tc.wantMatch {
				if err != nil {
					t.Fatalf("want match, got err: %v", err)
				}
				if got.Slug != tc.wantSlug {
					t.Errorf("slug = %q, want %q", got.Slug, tc.wantSlug)
				}
				if got.Path != filepath.Join(root, tc.wantSlug) {
					t.Errorf("path = %q, want %q", got.Path, filepath.Join(root, tc.wantSlug))
				}
				if got.State.TicketID != st.TicketID {
					t.Errorf("state.TicketID = %q, want %q", got.State.TicketID, st.TicketID)
				}
			} else if !errors.Is(err, ErrNoState) {
				t.Errorf("want ErrNoState, got err=%v ws=%+v", err, got)
			}
		})
	}
}

func TestFindContainingWorkspace_resolvesSymlinks(t *testing.T) {
	// Real root + a symlink that points at it. On macOS, $TMPDIR is
	// itself usually under /var → /private/var, so EvalSymlinks is
	// already exercised on the temp dirs themselves — but we also
	// stand up an explicit symlink to cover the case where the user
	// configured workspace_root via a symlinked path (e.g. ~/work →
	// /Volumes/...).
	realRoot := t.TempDir()
	slug := "sc-2-x"
	wsDir := filepath.Join(realRoot, slug)
	writeFakeState(t, wsDir, State{TicketID: "sc-2", Branch: "b", CreatedAt: time.Now()})
	// Create the worktree subdir on disk so EvalSymlinks can resolve a
	// cwd that points into it via the link.
	repoSub := filepath.Join(wsDir, "repo")
	if err := os.MkdirAll(repoSub, 0o755); err != nil {
		t.Fatal(err)
	}

	linkRoot := filepath.Join(t.TempDir(), "link-to-root")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Skipf("cannot create symlink (likely filesystem restriction): %v", err)
	}
	// Look up via the link path (root) + a cwd that goes through the
	// link.
	got, err := FindContainingWorkspace(linkRoot, filepath.Join(linkRoot, slug, "repo"))
	if err != nil {
		t.Fatalf("want match via symlink, got err: %v", err)
	}
	if got.Slug != slug {
		t.Errorf("slug = %q, want %q", got.Slug, slug)
	}
	// The returned Path must use the user-facing (link) root, not
	// the resolved real path — so the dir we hand back is the path
	// the user would type themselves.
	wantPath := filepath.Join(linkRoot, slug)
	if got.Path != wantPath {
		t.Errorf("path = %q, want %q (must use link-root, not resolved)", got.Path, wantPath)
	}
}

func TestFindContainingWorkspace_corruptManifestReturnsError(t *testing.T) {
	root := t.TempDir()
	slug := "ws-corrupt"
	corrupt := filepath.Join(root, slug)
	if err := os.MkdirAll(filepath.Join(corrupt, ".thicket"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corrupt, ".thicket", "state.json"),
		[]byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := FindContainingWorkspace(root, corrupt)
	if err == nil {
		t.Fatal("want error on corrupt manifest, got nil")
	}
	if errors.Is(err, ErrNoState) {
		t.Errorf("corrupt manifest should NOT be conflated with ErrNoState (got %v)", err)
	}
}

func TestFindContainingWorkspace_emptyInputs(t *testing.T) {
	if _, err := FindContainingWorkspace("", "/some/cwd"); !errors.Is(err, ErrNoState) {
		t.Errorf("empty root: want ErrNoState, got %v", err)
	}
	if _, err := FindContainingWorkspace("/some/root", ""); !errors.Is(err, ErrNoState) {
		t.Errorf("empty cwd: want ErrNoState, got %v", err)
	}
}

func TestListManaged_missingRootIsNotAnError(t *testing.T) {
	got, warnings, err := ListManaged(filepath.Join(t.TempDir(), "does-not-exist"))
	if got != nil || warnings != nil || err != nil {
		t.Errorf("missing root should return nil/nil/nil, got %v / %v / %v",
			got, warnings, err)
	}
}

func TestListManaged_unreadableRootReturnsFatalError(t *testing.T) {
	// Create a directory we deliberately can't read by chmodding 0.
	root := filepath.Join(t.TempDir(), "no-access")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o000); err != nil {
		t.Skip("cannot remove read perms on this fs:", err)
	}
	defer func() { _ = os.Chmod(root, 0o755) }()

	got, warnings, err := ListManaged(root)
	if err == nil {
		t.Fatalf("want fatal error for unreadable root, got nil (workspaces=%v warnings=%v)",
			got, warnings)
	}
	if got != nil || warnings != nil {
		t.Errorf("on fatal err want nil workspaces/warnings, got %v / %v", got, warnings)
	}
}

func writeFakeState(t *testing.T, dir string, st State) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".thicket"), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".thicket", "state.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// ----- Add -----

// createBasePlanWorkspace runs Create against basePlan(root) and
// returns the live root. Used as the starting point for Add tests:
// the workspace already has alpha + beta as worktrees on branch
// u/sc-1-x, with a real CLAUDE.local.md + state.json on disk.
func createBasePlanWorkspace(t *testing.T) (string, Plan) {
	t.Helper()
	root := t.TempDir()
	p := basePlan(root)
	initRepo(t, p.Repos[0].SourcePath)
	initRepo(t, p.Repos[1].SourcePath)
	if err := New(git.New()).Create(p); err != nil {
		t.Fatalf("create: %v", err)
	}
	return root, p
}

func TestAdd_attachesNewWorktreesAndAppendsState(t *testing.T) {
	root, p := createBasePlanWorkspace(t)

	srcG := filepath.Join(root, "src", "gamma")
	initRepo(t, srcG)
	wtG := filepath.Join(p.WorkspaceDir, "gamma")

	ap := AddPlan{
		WorkspaceDir: p.WorkspaceDir,
		NewRepos: []PlanRepo{
			{Name: "gamma", SourcePath: srcG, WorktreePath: wtG, BranchExists: false},
		},
		Memory: memory.Input{
			TicketID: "sc-1", Title: "X", Body: "body",
			Branch: "u/sc-1-x", WorkspaceDir: p.WorkspaceDir,
			Repos: []memory.RepoEntry{
				{Name: "alpha", Branch: "u/sc-1-x", WorktreePath: filepath.Join(p.WorkspaceDir, "alpha"), DefaultBranch: "main"},
				{Name: "beta", Branch: "u/sc-1-x", WorktreePath: filepath.Join(p.WorkspaceDir, "beta"), DefaultBranch: "main"},
				{Name: "gamma", Branch: "u/sc-1-x", WorktreePath: wtG, DefaultBranch: "main"},
			},
			CreatedAt: time.Date(2026, 5, 13, 12, 30, 0, 0, time.UTC),
		},
	}

	res, err := New(git.New()).Add(ap)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(res.Added) != 1 || res.Added[0].Name != "gamma" {
		t.Errorf("Added = %+v", res.Added)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("Skipped = %+v", res.Skipped)
	}

	// Worktree is real and on the right branch.
	got, err := git.New().CurrentBranch(wtG)
	if err != nil || got != "u/sc-1-x" {
		t.Errorf("gamma worktree branch = %q (err=%v)", got, err)
	}

	// state.json now lists 3 repos.
	st, err := ReadState(p.WorkspaceDir)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if len(st.Repos) != 3 {
		t.Fatalf("state repos = %d, want 3 (%+v)", len(st.Repos), st.Repos)
	}
	names := []string{st.Repos[0].Name, st.Repos[1].Name, st.Repos[2].Name}
	if !reflect.DeepEqual(names, []string{"alpha", "beta", "gamma"}) {
		t.Errorf("state repo order = %v, want [alpha beta gamma]", names)
	}

	// CLAUDE.local.md mentions gamma now.
	body, err := os.ReadFile(filepath.Join(p.WorkspaceDir, memory.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "| gamma |") {
		t.Errorf("gamma row missing from CLAUDE.local.md:\n%s", body)
	}
}

func TestAdd_preservesStatusLog(t *testing.T) {
	root, p := createBasePlanWorkspace(t)

	// Append a status-log entry the way an agent would.
	memPath := filepath.Join(p.WorkspaceDir, memory.FileName)
	orig, err := os.ReadFile(memPath)
	if err != nil {
		t.Fatal(err)
	}
	appended := string(orig) + "\n### 2026-05-13 13:00\n- Triaged the regression\n"
	if err := os.WriteFile(memPath, []byte(appended), 0o644); err != nil {
		t.Fatal(err)
	}

	srcG := filepath.Join(root, "src", "gamma")
	initRepo(t, srcG)
	ap := AddPlan{
		WorkspaceDir: p.WorkspaceDir,
		NewRepos: []PlanRepo{
			{Name: "gamma", SourcePath: srcG, WorktreePath: filepath.Join(p.WorkspaceDir, "gamma")},
		},
		Memory: memory.Input{
			TicketID: "sc-1", Title: "X", Body: "body",
			Branch: "u/sc-1-x", WorkspaceDir: p.WorkspaceDir,
			Repos: []memory.RepoEntry{
				{Name: "alpha", Branch: "u/sc-1-x", WorktreePath: filepath.Join(p.WorkspaceDir, "alpha"), DefaultBranch: "main"},
				{Name: "beta", Branch: "u/sc-1-x", WorktreePath: filepath.Join(p.WorkspaceDir, "beta"), DefaultBranch: "main"},
				{Name: "gamma", Branch: "u/sc-1-x", WorktreePath: filepath.Join(p.WorkspaceDir, "gamma"), DefaultBranch: "main"},
			},
			CreatedAt: time.Date(2026, 5, 13, 12, 30, 0, 0, time.UTC),
		},
	}
	if _, err := New(git.New()).Add(ap); err != nil {
		t.Fatalf("add: %v", err)
	}

	body, err := os.ReadFile(memPath)
	if err != nil {
		t.Fatal(err)
	}
	bs := string(body)
	if !strings.Contains(bs, "### 2026-05-13 13:00") {
		t.Errorf("status-log heading lost:\n%s", bs)
	}
	if !strings.Contains(bs, "Triaged the regression") {
		t.Errorf("status-log body lost:\n%s", bs)
	}
	if !strings.Contains(bs, "| gamma |") {
		t.Errorf("new repo row not in refreshed file:\n%s", bs)
	}
}

func TestAdd_rejectsDuplicateRepo(t *testing.T) {
	_, p := createBasePlanWorkspace(t)
	ap := AddPlan{
		WorkspaceDir: p.WorkspaceDir,
		NewRepos: []PlanRepo{
			// "alpha" is already in the workspace.
			{Name: "alpha", SourcePath: p.Repos[0].SourcePath,
				WorktreePath: p.Repos[0].WorktreePath},
		},
		Memory: p.Memory,
	}
	_, err := New(git.New()).Add(ap)
	if err == nil {
		t.Fatal("want error for duplicate repo, got nil")
	}
	if !strings.Contains(err.Error(), "already in this workspace") {
		t.Errorf("error doesn't mention duplicate: %v", err)
	}
}

func TestAdd_partialFailureLeavesStateConsistent(t *testing.T) {
	root, p := createBasePlanWorkspace(t)

	srcG := filepath.Join(root, "src", "gamma")
	initRepo(t, srcG)
	// "delta" intentionally points at a non-existent source — AddWorktree will fail.
	ap := AddPlan{
		WorkspaceDir: p.WorkspaceDir,
		NewRepos: []PlanRepo{
			{Name: "gamma", SourcePath: srcG, WorktreePath: filepath.Join(p.WorkspaceDir, "gamma")},
			{Name: "delta", SourcePath: filepath.Join(root, "src", "no-such-repo"),
				WorktreePath: filepath.Join(p.WorkspaceDir, "delta")},
		},
		Memory: memory.Input{
			TicketID: "sc-1", Title: "X", Body: "body",
			Branch: "u/sc-1-x", WorkspaceDir: p.WorkspaceDir,
			Repos: []memory.RepoEntry{
				{Name: "alpha", Branch: "u/sc-1-x", WorktreePath: filepath.Join(p.WorkspaceDir, "alpha"), DefaultBranch: "main"},
				{Name: "beta", Branch: "u/sc-1-x", WorktreePath: filepath.Join(p.WorkspaceDir, "beta"), DefaultBranch: "main"},
				{Name: "gamma", Branch: "u/sc-1-x", WorktreePath: filepath.Join(p.WorkspaceDir, "gamma"), DefaultBranch: "main"},
			},
			CreatedAt: time.Date(2026, 5, 13, 12, 30, 0, 0, time.UTC),
		},
	}

	res, err := New(git.New()).Add(ap)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(res.Added) != 1 || res.Added[0].Name != "gamma" {
		t.Errorf("Added = %+v", res.Added)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Name != "delta" {
		t.Errorf("Skipped = %+v", res.Skipped)
	}

	// State has 3 repos: original 2 + gamma. delta was dropped.
	st, err := ReadState(p.WorkspaceDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Repos) != 3 {
		t.Errorf("state repos = %d, want 3", len(st.Repos))
	}
}

func TestSlug_alwaysPrefixesTicketID(t *testing.T) {
	cases := []struct {
		id, title, want string
	}{
		{"sc-65825", "freshness", "sc-65825-freshness"},
		{"SC-65825", "Freshness", "sc-65825-freshness"},
		{"sc-12345", "Fix inventory grouping!!", "sc-12345-fix-inventory-grouping"},
		{"sc-1", "", "sc-1"},
		{"", "just-a-title", "just-a-title"},
		{"", "", "workspace"},
		// Title contains the id — still safe: id wins as prefix.
		{"sc-1", "sc-1 follow-up", "sc-1-sc-1-follow-up"},
		// Non-ASCII letters are stripped; surrounding ASCII fuses.
		{"sc-1", "café / résumé", "sc-1-caf-rsum"},
	}
	for _, tc := range cases {
		t.Run(tc.id+"|"+tc.title, func(t *testing.T) {
			if got := Slug(tc.id, tc.title); got != tc.want {
				t.Errorf("Slug(%q,%q)=%q, want %q", tc.id, tc.title, got, tc.want)
			}
		})
	}
}

func TestSlugify_basic(t *testing.T) {
	cases := map[string]string{
		"Fix Inventory Grouping":   "fix-inventory-grouping",
		"  spaces  everywhere ":    "spaces-everywhere",
		"under_scores/and/slashes": "under-scores-and-slashes",
		"keep_123_digits":          "keep-123-digits",
		"!!!":                      "",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q)=%q, want %q", in, got, want)
		}
	}
}
