package detector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// ClaudeCLIDetector shells out to the local `claude` binary instead of
// calling the Anthropic API directly. This lets users on a Claude
// Enterprise subscription get repo detection "for free" — no separate
// API key required, since `claude -p` reuses Claude Code's existing
// authentication.
//
// Output format: we ask Claude to return a bare JSON array; if it wraps
// it in prose or a fenced code block, we extract the first valid array.
type ClaudeCLIDetector struct {
	BinaryPath string
	Model      string
	Runner     CLIRunner
}

// CLIRunner is the injection seam used by tests to avoid shelling out.
type CLIRunner interface {
	Run(ctx context.Context, name string, args []string, stdin io.Reader) (stdout, stderr []byte, err error)
}

// DefaultCLIRunner shells out via os/exec.
type DefaultCLIRunner struct{}

func (DefaultCLIRunner) Run(ctx context.Context, name string, args []string, stdin io.Reader) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // we control name & args
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// NewClaudeCLI builds a CLI-backed detector. binary is "claude" by
// default; model may be empty (the CLI then uses its own default).
func NewClaudeCLI(binary, model string) *ClaudeCLIDetector {
	if binary == "" {
		binary = "claude"
	}
	return &ClaudeCLIDetector{BinaryPath: binary, Model: model, Runner: DefaultCLIRunner{}}
}

func (d *ClaudeCLIDetector) Detect(ctx context.Context, in Input) ([]RepoMatch, error) {
	if len(in.Repos) == 0 {
		return nil, errors.New("detector: empty repo catalog")
	}
	prompt, err := renderPrompt(in)
	if err != nil {
		return nil, err
	}
	// Force structured output: ask for JSON ONLY, no prose, no fences.
	// We still defend against fences in extractJSONArray.
	prompt += "\n\nReturn ONLY a JSON array. No prose, no markdown fences, no explanations.\n" +
		"Schema: [{\"name\":string,\"confidence\":number 0-1,\"reason\":string}]"

	args := []string{"-p"}
	if d.Model != "" {
		args = append(args, "--model", d.Model)
	}
	args = append(args, prompt)

	stdout, stderr, err := d.Runner.Run(ctx, d.BinaryPath, args, nil)
	if err != nil {
		return nil, fmt.Errorf("claude -p: %w (%s)", err, strings.TrimSpace(string(stderr)))
	}
	payload, err := extractJSONArray(stdout)
	if err != nil {
		return nil, fmt.Errorf("parse claude output: %w (raw: %s)", err,
			strings.TrimSpace(string(stdout)))
	}
	var matches []RepoMatch
	if err := json.Unmarshal(payload, &matches); err != nil {
		return nil, fmt.Errorf("decode repos: %w", err)
	}
	return matches, nil
}

// extractJSONArray pulls the outermost JSON array out of Claude's
// (potentially noisy) response. We scan for a top-level '[' and find its
// matching ']' by depth-tracking — ignoring '[' inside JSON strings.
func extractJSONArray(b []byte) ([]byte, error) {
	start := bytes.IndexByte(b, '[')
	if start < 0 {
		return nil, errors.New("no '[' in claude output")
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(b); i++ {
		c := b[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return b[start : i+1], nil
			}
		}
	}
	return nil, errors.New("unbalanced '[' in claude output")
}
