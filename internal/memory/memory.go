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
