package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/uribrecher/thicket/internal/config"
	"github.com/uribrecher/thicket/internal/workspace"
)

func runList(cmd *cobra.Command, _ []string) error {
	cfg, err := loadConfigOrPointAtInit()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(cfg.WorkspaceRoot)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(cmd.OutOrStdout(), "no workspaces (workspace_root does not exist yet)")
			return nil
		}
		return fmt.Errorf("read %s: %w", cfg.WorkspaceRoot, err)
	}

	type row struct {
		slug     string
		ticketID string
		branch   string
		repos    int
		when     string
	}
	var rows []row
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		ws := filepath.Join(cfg.WorkspaceRoot, e.Name())
		st, err := workspace.ReadState(ws)
		switch {
		case errors.Is(err, workspace.ErrNoState):
			// Not a thicket workspace — skip silently.
			continue
		case err != nil:
			// Corrupted/unreadable manifest: surface it as a warning so
			// users can investigate (instead of quietly hiding the row).
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s: %v\n", e.Name(), err)
			continue
		}
		rows = append(rows, row{
			slug:     e.Name(),
			ticketID: st.TicketID,
			branch:   st.Branch,
			repos:    len(st.Repos),
			when:     st.CreatedAt.Local().Format("2006-01-02 15:04"),
		})
	}
	if len(rows) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "no workspaces")
		return nil
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].when > rows[j].when })

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SLUG\tTICKET\tBRANCH\tREPOS\tCREATED")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n", r.slug, r.ticketID, r.branch, r.repos, r.when)
	}
	return w.Flush()
}

func loadConfigOrPointAtInit() (*config.Config, error) {
	cfgPath, err := config.Path()
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(cfgPath)
	if errors.Is(err, config.ErrNoConfig) {
		return nil, errors.New("config not found — run `thicket init`")
	}
	return cfg, err
}
