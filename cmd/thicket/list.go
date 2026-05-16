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
		fmt.Fprintln(cmd.OutOrStdout(), "no workspaces")
		return nil
	}
	writeWorkspaceTable(cmd.OutOrStdout(), workspaces)
	return nil
}

// Visible-cell widths for the `thicket list` table. The old layout used
// tabwriter and showed both SLUG and BRANCH at their natural width, which
// blew past 200 columns on real workspaces and misaligned rows whose
// nickname carried an emoji (tabwriter counts bytes, emoji are two cells).
// We now render with runewidth-backed pad/truncate (the same helpers the
// edit-workspace picker uses) and drop SLUG: NAME falls back to the slug
// when no nickname is set, and the slug fragment otherwise reappears in
// BRANCH.
const (
	listNameW   = 25 // matches workspace.NicknameMaxChars
	listIDW     = 10
	listBranchW = 30
	listReposW  = 5
	listWhenW   = 16
)

func writeWorkspaceTable(out io.Writer, workspaces []workspace.ManagedWorkspace) {
	cols := []struct {
		title string
		width int
	}{
		{"NAME", listNameW},
		{"TICKET", listIDW},
		{"BRANCH", listBranchW},
		{"REPOS", listReposW},
		{"CREATED", listWhenW},
	}
	for i, c := range cols {
		if i > 0 {
			fmt.Fprint(out, "  ")
		}
		fmt.Fprint(out, tui.PadRight(c.title, c.width))
	}
	fmt.Fprintln(out)
	for i, c := range cols {
		if i > 0 {
			fmt.Fprint(out, "  ")
		}
		fmt.Fprint(out, strings.Repeat("─", c.width))
	}
	fmt.Fprintln(out)

	for _, ws := range workspaces {
		fmt.Fprint(out, tui.PadRight(tui.Truncate(ws.DisplayName(), listNameW), listNameW))
		fmt.Fprint(out, "  ")
		fmt.Fprint(out, tui.PadRight(tui.Truncate(ws.State.TicketID, listIDW), listIDW))
		fmt.Fprint(out, "  ")
		fmt.Fprint(out, tui.PadRight(tui.Truncate(ws.State.Branch, listBranchW), listBranchW))
		fmt.Fprint(out, "  ")
		fmt.Fprint(out, tui.PadRight(fmt.Sprintf("%d", len(ws.State.Repos)), listReposW))
		fmt.Fprint(out, "  ")
		fmt.Fprint(out, tui.PadRight(ws.State.CreatedAt.Local().Format("2006-01-02 15:04"), listWhenW))
		fmt.Fprintln(out)
	}
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
