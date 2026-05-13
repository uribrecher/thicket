package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"
)

var (
	version = "dev"
	commit  = ""
	date    = ""
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "thicket",
		Short:         "Ticket-driven multi-repo workspace bootstrapper",
		Long:          "thicket spawns an isolated workspace of git worktrees for one ticket.\nSee https://github.com/uribrecher/thicket for the full guide.",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(newStartCmd())
	root.AddCommand(newInitCmd())
	root.AddCommand(newListCmd())
	root.AddCommand(newRmCmd())
	root.AddCommand(newCatalogCmd())
	root.AddCommand(newDoctorCmd())
	root.AddCommand(newVersionCmd())
	return root
}

func newStartCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "start <ticket>",
		Short: "Spawn a workspace for a ticket",
		Args:  cobra.ExactArgs(1),
		RunE:  runStart,
	}
	c.Flags().StringSlice("only", nil, "use exactly these repos (skips LLM)")
	c.Flags().String("branch", "", "override branch name")
	c.Flags().Bool("no-interactive", false, "accept LLM suggestion without prompting")
	c.Flags().Bool("no-launch", false, "do not launch Claude after creating the workspace")
	c.Flags().Bool("dry-run", false, "print the plan, do not change anything on disk")
	return c
}

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "First-run setup wizard",
		Args:  cobra.NoArgs,
		RunE:  runInit,
	}
}

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List active workspaces",
		Args:  cobra.NoArgs,
		RunE:  runList,
	}
}

func newRmCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "rm [slug]",
		Short: "Remove a workspace and its worktrees (interactive picker if no slug)",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runRm,
	}
	c.Flags().Bool("force", false, "remove even if worktrees have local changes")
	c.Flags().Bool("yes", false, "skip the confirmation prompt (for scripts)")
	return c
}

func newCatalogCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "catalog",
		Short: "Show or refresh the GitHub-org repo cache",
		Args:  cobra.NoArgs,
		RunE:  runCatalog,
	}
	c.Flags().Bool("refresh", false, "force-refresh the cache")
	return c
}

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose configuration, tokens, and required external tools",
		Args:  cobra.NoArgs,
		RunE:  runDoctor,
	}
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version info",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			v, c, d := buildInfo()
			fmt.Fprintf(cmd.OutOrStdout(), "thicket %s (%s, built %s)\n", v, c, d)
		},
	}
}

func buildInfo() (v, c, d string) {
	v, c, d = version, commit, date
	if c == "" || d == "" {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, s := range info.Settings {
				switch s.Key {
				case "vcs.revision":
					if c == "" {
						c = s.Value
					}
				case "vcs.time":
					if d == "" {
						d = s.Value
					}
				}
			}
		}
	}
	if c == "" {
		c = "unknown"
	}
	if d == "" {
		d = "unknown"
	}
	return
}
