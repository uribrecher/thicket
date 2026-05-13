// Package launcher exec's the configured Claude binary inside a workspace,
// replacing the calling thicket process so the user lands in Claude Code
// with no stray parent shells. If the binary isn't on PATH, the caller
// is told how to reach the workspace manually.
package launcher

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
)

// ErrMissingBinary is returned when the configured Claude binary can't be
// resolved on PATH. The caller should fall back to printing a cd hint.
var ErrMissingBinary = errors.New("claude binary not found on PATH")

// LookPathFn resolves a binary name to an absolute path. Defaults to
// exec.LookPath; tests inject a stub.
type LookPathFn func(name string) (string, error)

// ExecFn replaces the current process with another. Defaults to
// syscall.Exec; tests inject a stub so the test binary doesn't get
// replaced. The default returns only on error.
type ExecFn func(argv0 string, argv []string, envv []string) error

// Launcher carries the configurable injection points.
type Launcher struct {
	BinaryName string
	// ExtraArgs are appended to the binary invocation, after argv[0].
	// thicket sets these to `--name <slug>` so each workspace's Claude
	// session is labelled in the prompt box, /resume list, and the
	// terminal window title.
	ExtraArgs []string
	LookPath  LookPathFn
	Exec      ExecFn
	Stderr    io.Writer
}

// New returns a Launcher with production defaults.
func New(binaryName string) *Launcher {
	if binaryName == "" {
		binaryName = "claude"
	}
	return &Launcher{
		BinaryName: binaryName,
		LookPath:   exec.LookPath,
		Exec:       syscall.Exec,
		Stderr:     os.Stderr,
	}
}

// Launch changes to workspaceDir and exec's the configured Claude binary.
// On Unix this replaces the thicket process. On any error before exec, it
// returns. After a successful exec, control never returns; nil is returned
// only by the injected ExecFn in tests.
func (l *Launcher) Launch(workspaceDir string) error {
	if err := os.Chdir(workspaceDir); err != nil {
		return fmt.Errorf("chdir %s: %w", workspaceDir, err)
	}
	path, err := l.LookPath(l.BinaryName)
	if err != nil {
		return ErrMissingBinary
	}
	argv := append([]string{l.BinaryName}, l.ExtraArgs...)
	if err := l.Exec(path, argv, os.Environ()); err != nil {
		return fmt.Errorf("exec claude: %w", err)
	}
	return nil
}

// PrintFallback writes a cd hint to w. Use when Launch returns
// ErrMissingBinary.
func PrintFallback(w io.Writer, workspaceDir string) {
	fmt.Fprintf(w, "claude binary not found on PATH.\n"+
		"Workspace ready at:\n  %s\n"+
		"cd in and launch your editor / AI session of choice.\n", workspaceDir)
}
