package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/uribrecher/thicket/internal/git"
	"github.com/uribrecher/thicket/internal/workspace"
)

func runRm(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfigOrPointAtInit()
	if err != nil {
		return err
	}
	slug := args[0]
	dir := filepath.Join(cfg.WorkspaceRoot, slug)
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("workspace %q not found at %s", slug, dir)
		}
		return err
	}
	force, _ := cmd.Flags().GetBool("force")

	w := workspace.New(git.New())
	if err := w.Remove(dir, force); err != nil {
		if errors.Is(err, workspace.ErrNoState) {
			fmt.Printf("warning: no manifest, removed dir only\n")
			return nil
		}
		return err
	}
	fmt.Printf("removed %s\n", dir)
	return nil
}
