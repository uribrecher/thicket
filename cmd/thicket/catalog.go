package main

import (
	"errors"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/uribrecher/thicket/internal/catalog"
)

func runCatalog(cmd *cobra.Command, _ []string) error {
	cfg, err := loadConfigOrPointAtInit()
	if err != nil {
		return err
	}
	refresh, _ := cmd.Flags().GetBool("refresh")

	cachePath, err := catalog.Path()
	if err != nil {
		return err
	}

	var repos []catalog.Repo
	if !refresh {
		repos, age, err := catalog.Load(cachePath)
		if err != nil && !errors.Is(err, catalog.ErrNoCache) {
			return err
		}
		if !errors.Is(err, catalog.ErrNoCache) && age < catalog.DefaultCacheTTL {
			repos = catalog.WithLocalPaths(repos, cfg.ReposRoot)
			printCatalog(cmd, repos, age)
			return nil
		}
	}

	fmt.Fprintln(cmd.ErrOrStderr(), "fetching catalog via gh...")
	repos, err = catalog.Build(cfg.GithubOrgs, catalog.GHFetcher{})
	if err != nil {
		return err
	}
	if err := catalog.Save(cachePath, repos); err != nil {
		return fmt.Errorf("save cache: %w", err)
	}
	repos = catalog.WithLocalPaths(repos, cfg.ReposRoot)
	printCatalog(cmd, repos, 0)
	return nil
}

func printCatalog(cmd *cobra.Command, repos []catalog.Repo, age time.Duration) {
	if age > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "catalog cached %v ago — use --refresh to re-fetch\n",
			age.Round(time.Minute))
	}
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "REPO\tCLONED\tDEFAULT\tDESCRIPTION")
	for _, r := range repos {
		c := "no"
		if r.Cloned() {
			c = "yes"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Name, c, r.DefaultBranch, r.Description)
	}
	_ = w.Flush()
	fmt.Fprintf(cmd.ErrOrStderr(), "\n%d repos total\n", len(repos))
}
