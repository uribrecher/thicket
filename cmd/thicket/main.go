package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"

	"github.com/uribrecher/thicket/internal/updater"
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
		PersistentPreRun: func(cmd *cobra.Command, _ []string) {
			// Self-update probe. Synchronous but bounded by the
			// updater's 2s HTTP timeout, and cached for 24h so most
			// invocations pay no I/O at all. Soft-fails on any
			// network / cache issue. The Names() switch below
			// covers commands where running the probe is pointless
			// or would loop (`update` runs its own forced check,
			// and `version` / `help` are read-only diagnostics).
			switch cmd.Name() {
			case "update", "version", "help", "":
				return
			}
			if noCheck, _ := cmd.Flags().GetBool("no-update-check"); noCheck {
				return
			}
			v, _, _ := buildInfo()
			updater.CheckOnRun(v, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	root.PersistentFlags().Bool("no-update-check", false,
		"skip the daily self-update probe (also: THICKET_NO_UPDATE_CHECK=1)")
	root.AddCommand(newStartCmd())
	root.AddCommand(newEditCmd())
	root.AddCommand(newConfigCmd())
	root.AddCommand(newListCmd())
	root.AddCommand(newRmCmd())
	root.AddCommand(newCatalogCmd())
	root.AddCommand(newDoctorCmd())
	root.AddCommand(newUpdateCmd())
	root.AddCommand(newVersionCmd())
	return root
}

func newUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Check for a new thicket release and apply it",
		Long: "Force-fetches the latest release from GitHub, ignoring the 24h\n" +
			"update cache, and replaces the running thicket binary in place if\n" +
			"a newer version is available. No-op if you're already on latest.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			v, _, _ := buildInfo()
			return updater.CheckAndApplyNow(v, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
}

func newEditCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "edit [slug]",
		Short: "Add repos to an existing workspace (interactive picker if no slug)",
		Long: "Opens a 3-page wizard (Workspace -> Repos -> Submit) that lets you\n" +
			"add more git worktrees to a workspace you've already created.\n" +
			"Pre-existing repos are shown as locked rows on the Repos page —\n" +
			"removing repos isn't supported yet; use `thicket rm` + `thicket start`\n" +
			"for that.",
		Args: cobra.MaximumNArgs(1),
		RunE: runEdit,
	}
	return c
}

func newStartCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "start [ticket]",
		Short: "Spawn a workspace for a ticket (interactive picker if no id given)",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runStart,
	}
	c.Flags().StringSlice("only", nil, "use exactly these repos (skips LLM)")
	c.Flags().String("branch", "", "override branch name")
	c.Flags().String("nickname", "", "short workspace label (max 20 chars; overrides LLM suggestion)")
	c.Flags().Bool("no-interactive", false, "accept LLM suggestion without prompting")
	c.Flags().Bool("no-launch", false, "do not launch Claude after creating the workspace")
	c.Flags().Bool("dry-run", false, "print the plan, do not change anything on disk")
	return c
}

func newConfigCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Open the config wizard (first-run setup, or edit existing config)",
		Args:  cobra.NoArgs,
		RunE:  runConfig,
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
			fmt.Fprintf(cmd.OutOrStdout(), "thicket %s (%s, committed %s)\n", v, c, d)
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
