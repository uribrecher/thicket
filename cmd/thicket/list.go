package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/uribrecher/thicket/internal/config"
	"github.com/uribrecher/thicket/internal/tui"
	"github.com/uribrecher/thicket/internal/workspace"
)

func runList(cmd *cobra.Command, _ []string) error {
	cfg, err := loadConfigOrPointAtInit()
	if err != nil {
		return err
	}
	workspaces, warnings, err := workspace.ListManaged(cfg.WorkspaceRoot)
	if err != nil {
		return err
	}
	for _, w := range warnings {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v\n", w)
	}
	if len(workspaces) == 0 {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "no workspaces")
		return err
	}
	return writeWorkspaceTable(cmd.OutOrStdout(), workspaces)
}

// Visible-cell column widths for the `thicket list` table. The previous
// tabwriter layout showed SLUG and BRANCH at their natural width — past
// 200 columns on real workspaces — and counted bytes, so a nickname with
// an emoji shifted every following column by one cell. We render with
// tui.PadRight / tui.Truncate, which are backed by go-runewidth and so
// pad/clip by visible terminal cells. SLUG stays in the table because
// `thicket edit` and `thicket rm` accept a slug argument and the
// branch (truncated here, overridable via `--branch`) is not a reliable
// substitute. Total is 118 visible cells (108 of content + five 2-space
// gaps), which fits comfortably in modern terminal defaults while
// staying scannable; we no longer claim 80-column compatibility.
const (
	listNickW   = 25 // matches workspace.NicknameMaxChars
	listSlugW   = 30
	listIDW     = 8
	listBranchW = 24
	listReposW  = 5
	listWhenW   = 16
)

func writeWorkspaceTable(out io.Writer, workspaces []workspace.ManagedWorkspace) error {
	cols := []struct {
		title string
		width int
	}{
		{"NICKNAME", listNickW},
		{"SLUG", listSlugW},
		{"TICKET", listIDW},
		{"BRANCH", listBranchW},
		{"REPOS", listReposW},
		{"CREATED", listWhenW},
	}
	var b strings.Builder
	for i, c := range cols {
		if i > 0 {
			b.WriteString("  ")
		}
		b.WriteString(tui.PadRight(c.title, c.width))
	}
	b.WriteByte('\n')
	for i, c := range cols {
		if i > 0 {
			b.WriteString("  ")
		}
		b.WriteString(strings.Repeat("─", c.width))
	}
	b.WriteByte('\n')

	for _, ws := range workspaces {
		b.WriteString(tui.PadRight(tui.Truncate(ws.State.Nickname, listNickW), listNickW))
		b.WriteString("  ")
		b.WriteString(tui.PadRight(tui.Truncate(ws.Slug, listSlugW), listSlugW))
		b.WriteString("  ")
		// HyperlinkForWriter wraps the already padded+truncated cell
		// (so runewidth's column math stays correct — OSC bytes are
		// appended last) AND falls back to plain text when out isn't
		// a TTY, so `thicket list | tee log.txt` consumers don't see
		// raw escape bytes. State.URL is empty on legacy manifests,
		// in which case the helper also returns the label unchanged.
		b.WriteString(tui.HyperlinkForWriter(out, ws.State.URL,
			tui.PadRight(tui.Truncate(ws.State.TicketID, listIDW), listIDW)))
		b.WriteString("  ")
		b.WriteString(tui.PadRight(tui.Truncate(ws.State.Branch, listBranchW), listBranchW))
		b.WriteString("  ")
		b.WriteString(tui.PadRight(fmt.Sprintf("%d", len(ws.State.Repos)), listReposW))
		b.WriteString("  ")
		b.WriteString(tui.PadRight(ws.State.CreatedAt.Local().Format("2006-01-02 15:04"), listWhenW))
		b.WriteByte('\n')
	}
	_, err := io.WriteString(out, b.String())
	return err
}

func loadConfigOrPointAtInit() (*config.Config, error) {
	cfgPath, err := config.Path()
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(cfgPath)
	if errors.Is(err, config.ErrNoConfig) {
		return nil, errors.New("config not found — run `thicket config`")
	}
	return cfg, err
}
