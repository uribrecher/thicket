package launcher

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunch_callsExecWithResolvedPath(t *testing.T) {
	tmp := t.TempDir()
	cwdBefore, _ := os.Getwd()
	defer os.Chdir(cwdBefore)

	var gotArgv0 string
	var gotArgv []string
	l := &Launcher{
		BinaryName: "claude",
		LookPath:   func(name string) (string, error) { return "/opt/claude/claude", nil },
		Exec: func(argv0 string, argv []string, envv []string) error {
			gotArgv0, gotArgv = argv0, argv
			return nil
		},
	}
	if err := l.Launch(tmp); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if gotArgv0 != "/opt/claude/claude" {
		t.Errorf("argv0 = %q", gotArgv0)
	}
	if len(gotArgv) != 1 || gotArgv[0] != "claude" {
		t.Errorf("argv = %v", gotArgv)
	}
	cwd, _ := os.Getwd()
	if cwd != evalSymlinks(t, tmp) {
		t.Errorf("cwd = %q, want %q", cwd, tmp)
	}
}

func TestLaunch_missingBinary_returnsSentinel(t *testing.T) {
	tmp := t.TempDir()
	cwdBefore, _ := os.Getwd()
	defer os.Chdir(cwdBefore)

	l := &Launcher{
		BinaryName: "claude",
		LookPath: func(string) (string, error) {
			return "", exec.ErrNotFound
		},
		Exec: func(string, []string, []string) error {
			t.Fatal("Exec should not be called")
			return nil
		},
	}
	err := l.Launch(tmp)
	if !errors.Is(err, ErrMissingBinary) {
		t.Fatalf("want ErrMissingBinary, got %v", err)
	}
}

func TestLaunch_badWorkspaceDir(t *testing.T) {
	l := &Launcher{
		BinaryName: "claude",
		LookPath:   func(string) (string, error) { return "/x", nil },
		Exec:       func(string, []string, []string) error { return nil },
	}
	err := l.Launch(filepath.Join(t.TempDir(), "no-such-dir"))
	if err == nil || !strings.Contains(err.Error(), "chdir") {
		t.Errorf("want chdir error, got %v", err)
	}
}

func TestPrintFallback_mentionsWorkspaceDir(t *testing.T) {
	var b bytes.Buffer
	PrintFallback(&b, "/path/to/ws")
	out := b.String()
	if !strings.Contains(out, "/path/to/ws") {
		t.Errorf("missing workspace path: %s", out)
	}
	if !strings.Contains(out, "cd") {
		t.Errorf("missing cd hint: %s", out)
	}
}

// evalSymlinks resolves symlinks so macOS's /var → /private/var dance doesn't
// confuse the cwd comparison.
func evalSymlinks(t *testing.T, p string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
