package main

import (
	"errors"
	"fmt"
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

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SLUG\tTICKET\tBRANCH\tREPOS\tCREATED")
	for _, ws := range workspaces {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n",
			ws.Slug, ws.State.TicketID, ws.State.Branch, len(ws.State.Repos),
			ws.State.CreatedAt.Local().Format("2006-01-02 15:04"))
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
		return nil, errors.New("config not found — run `thicket config`")
	}
	return cfg, err
}
