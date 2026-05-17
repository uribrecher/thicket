// Package memory renders the workspace's CLAUDE.local.md — the seed file
// thicket drops into every workspace so AI sessions inside it start with
// full ticket context that survives reboots, compactions, and new sessions.
//
// The output is deliberately structured so a Claude Code session can both
// (a) read the ticket at the top and (b) append progress notes to the
// "Status log" section across sessions without re-priming.
package memory

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"
)

// RepoEntry is one row in the rendered repos table.
type RepoEntry struct {
	Name          string
	Branch        string
	WorktreePath  string
	DefaultBranch string
}

// Input is the data the template consumes.
type Input struct {
	TicketID     string // e.g. "sc-12345"
	Title        string
	URL          string
	State        string // optional
	Owner        string // optional
	Body         string // markdown
	Branch       string
	WorkspaceDir string
	Repos        []RepoEntry
	CreatedAt    time.Time
}

// FileName is the file thicket writes inside every workspace.
const FileName = "CLAUDE.local.md"

// urlLinePrefix is what Render emits ahead of the ticket URL. Kept in
// one place so ExtractURL and the template stay in sync if the bullet
// label ever changes.
const urlLinePrefix = "- **URL:** "

// ExtractURL reads the workspace's CLAUDE.local.md and returns the
// ticket URL recorded in the "- **URL:** ..." line, or "" when the
// file is missing, malformed, or carries no URL. Best-effort: any
// I/O or parse failure yields "" so callers can use it to backfill
// workspace.State.URL on manifests written before that field
// existed without having to plumb an error path through every
// display site.
func ExtractURL(workspaceDir string) string {
	b, err := os.ReadFile(filepath.Join(workspaceDir, FileName))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		// The template emits the URL line near the top — we don't
		// short-circuit because callers might pass a hand-edited file
		// where blank lines moved it down.
		rest, ok := strings.CutPrefix(line, urlLinePrefix)
		if !ok {
			continue
		}
		return strings.TrimSpace(rest)
	}
	return ""
}

const tmpl = `# Ticket {{.TicketID}} — {{.Title}}

- **URL:** {{.URL}}
{{- if .State}}
- **State:** {{.State}}
{{- end}}
{{- if .Owner}}
- **Owner:** {{.Owner}}
{{- end}}
- **Branch:** ` + "`{{.Branch}}`" + `
- **Workspace:** ` + "`{{.WorkspaceDir}}`" + `
- **Created:** {{.CreatedAt.Format "2006-01-02 15:04 MST"}}

## Repos in this workspace

| Repo | Branch | Worktree | Default branch |
| ---- | ------ | -------- | -------------- |
{{- range .Repos}}
| {{.Name}} | ` + "`{{.Branch}}`" + ` | ` + "`{{.WorktreePath}}`" + ` | {{.DefaultBranch}} |
{{- end}}

## Ticket body

<details>
<summary>Click to expand the full ticket description</summary>

{{.Body}}

</details>

## Status log

<!--
Append-only notes Claude (or you) can leave across sessions. Add a new
` + "`### YYYY-MM-DD HH:MM`" + ` heading per entry. Survives reboots and
context compaction; do not delete prior entries.
-->

`

var t = template.Must(template.New("claude_local").Parse(tmpl))

// Render returns the file body for the given input.
func Render(in Input) ([]byte, error) {
	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now()
	}
	in.Body = strings.TrimSpace(in.Body)
	if in.Body == "" {
		in.Body = "_(ticket has no description)_"
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, in); err != nil {
		return nil, fmt.Errorf("render CLAUDE.local.md: %w", err)
	}
	return buf.Bytes(), nil
}

// statusLogMarker is the heading the template emits at the top of the
// "Status log" section. Used by RegenPreservingStatusLog to splice the
// freshly-rendered top half of the file with the existing file's
// status-log tail. The leading "\n" anchors it to a line start so an
// accidental substring inside the ticket body can't accidentally match.
const statusLogMarker = "\n## Status log\n"

// RegenPreservingStatusLog re-renders the memory file from `in` while
// preserving the existing file's `## Status log` section verbatim.
// `thicket edit` calls this so that adding a repo to a workspace
// refreshes the repos table (and any other top-half data the template
// produces) without throwing away the agent's prior progress notes.
//
// preserved is true when the existing file contained the marker and
// its tail was spliced in. When false, body is the unmodified full
// render (the caller should log a soft warning so the user knows the
// status log they may have appended was rolled over).
func RegenPreservingStatusLog(in Input, existing []byte) (body []byte, preserved bool, err error) {
	fresh, err := Render(in)
	if err != nil {
		return nil, false, err
	}
	if len(existing) == 0 {
		return fresh, false, nil
	}
	freshPrefix, _, ok := strings.Cut(string(fresh), statusLogMarker)
	if !ok {
		return fresh, false, nil
	}
	_, existingTail, ok := strings.Cut(string(existing), statusLogMarker)
	if !ok {
		return fresh, false, nil
	}
	return []byte(freshPrefix + statusLogMarker + existingTail), true, nil
}
