package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/spf13/cobra"

	"github.com/uribrecher/thicket/internal/catalog"
	"github.com/uribrecher/thicket/internal/config"
	"github.com/uribrecher/thicket/internal/detector"
	gitops "github.com/uribrecher/thicket/internal/git"
	"github.com/uribrecher/thicket/internal/launcher"
	"github.com/uribrecher/thicket/internal/memory"
	"github.com/uribrecher/thicket/internal/secrets"
	"github.com/uribrecher/thicket/internal/ticket"
	"github.com/uribrecher/thicket/internal/ticket/shortcut"
	"github.com/uribrecher/thicket/internal/tui"
	"github.com/uribrecher/thicket/internal/workspace"
)

func runStart(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfigOrPointAtInit()
	if err != nil {
		return err
	}
	flags, err := readStartFlags(cmd)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()

	src, err := buildTicketSource(cmd.Context(), cfg)
	if err != nil {
		return err
	}

	var tk ticket.Ticket
	if len(args) == 0 {
		// Interactive picker over the user's active assigned tickets.
		tk, err = pickAssignedTicket(cmd.Context(), src, cfg, errOut)
		if err != nil {
			if errors.Is(err, tui.ErrCancelled) {
				fmt.Fprintln(out, "cancelled.")
				return nil
			}
			return err
		}
		fmt.Fprintf(out, "  %s — %s\n", tk.SourceID, tk.Title)
	} else {
		id, err := src.Parse(args[0])
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "fetching ticket %s...\n", id)
		tk, err = src.Fetch(id)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "  %s — %s\n", tk.SourceID, tk.Title)
	}
	if strings.TrimSpace(tk.Body) == "" {
		fmt.Fprintln(out, "  ⚠ ticket has no description — LLM routing will lack context;\n"+
			"    consider \"thicket start <ticket> --only repo1,repo2\" instead.")
	}

	repos, err := loadCatalog(cfg, errOut)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "catalog: %d active repos across %v\n", len(repos), cfg.GithubOrgs)

	var picks []detector.RepoMatch
	err = withProgress(errOut, "looking for relevant repos", func() error {
		var detErr error
		picks, detErr = detectRepos(cmd.Context(), cfg, errOut, flags, tk, repos)
		return detErr
	})
	if err != nil {
		return err
	}

	selector := pickSelector(flags.noInteractive)
	chosenNames, err := selector.SelectRepos(repos, picks)
	if err != nil {
		if errors.Is(err, tui.ErrCancelled) {
			fmt.Fprintln(out, "cancelled.")
			return nil
		}
		return err
	}
	if len(chosenNames) == 0 {
		return errors.New("no repos selected — nothing to do")
	}

	chosen, err := resolveOrClone(cmd.Context(), cfg, errOut, repos, chosenNames, selector, flags.dryRun)
	if err != nil {
		return err
	}
	if len(chosen) == 0 {
		return errors.New("no repos remain after the clone gate")
	}

	plan, err := buildPlan(cfg, flags, src, tk, chosen)
	if err != nil {
		return err
	}

	if flags.dryRun {
		printPlan(out, plan)
		return nil
	}

	w := workspace.New(gitops.New())
	if err := w.Create(plan); err != nil {
		return err
	}
	fmt.Fprintf(out, "workspace ready at %s\n", plan.WorkspaceDir)

	if flags.noLaunch {
		fmt.Fprintf(out, "cd %s\n", plan.WorkspaceDir)
		return nil
	}
	// `--name` labels the Claude session in its prompt box, /resume
	// picker, and the terminal window title — useful when juggling
	// several open workspaces.
	l := launcher.New(cfg.ClaudeBinary)
	l.ExtraArgs = []string{"--name", workspace.Slug(tk.SourceID, tk.Title)}
	if err := l.Launch(plan.WorkspaceDir); err != nil {
		if errors.Is(err, launcher.ErrMissingBinary) {
			launcher.PrintFallback(out, plan.WorkspaceDir)
			return nil
		}
		return err
	}
	return nil
}

// ----- helpers below -----

type startFlags struct {
	only          []string
	branch        string
	noInteractive bool
	noLaunch      bool
	dryRun        bool
}

func readStartFlags(cmd *cobra.Command) (startFlags, error) {
	f := cmd.Flags()
	only, _ := f.GetStringSlice("only")
	branch, _ := f.GetString("branch")
	noInteractive, _ := f.GetBool("no-interactive")
	noLaunch, _ := f.GetBool("no-launch")
	dryRun, _ := f.GetBool("dry-run")
	return startFlags{
		only:          only,
		branch:        branch,
		noInteractive: noInteractive,
		noLaunch:      noLaunch,
		dryRun:        dryRun,
	}, nil
}

// secretKind identifies which logical secret we're resolving.
type secretKind int

const (
	secretShortcut secretKind = iota
	secretAnthropic
)

// envVarFor returns the canonical env-var name for a secret. When that
// env var is set, it short-circuits the password-manager fetch — useful
// for CI, one-off debugging, and Claude-Enterprise users who only need
// to set ANTHROPIC_API_KEY for a single run.
func envVarFor(k secretKind) string {
	switch k {
	case secretShortcut:
		return "SHORTCUT_API_TOKEN"
	case secretAnthropic:
		return "ANTHROPIC_API_KEY"
	}
	return ""
}

// withProgress runs fn while printing a single-line, in-place "label …
// Ns" elapsed-time spinner. Keeps the user from thinking the CLI is
// stuck and gives them a sense of how long the underlying call took
// (the LLM in particular can take 5–30s). Clears the line on completion
// so subsequent output starts on a clean row.
//
// The spinner goroutine acks shutdown via `stopped` so the final clear
// line can't race with one last in-flight tick frame.
func withProgress(w io.Writer, label string, fn func() error) error {
	start := time.Now()
	done := make(chan struct{})
	stopped := make(chan struct{})
	// Print the initial frame immediately so the user sees something
	// even if fn returns in <1s.
	fmt.Fprintf(w, "%s… 0s", label)
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case t := <-ticker.C:
				secs := int(t.Sub(start).Seconds())
				// \r returns to column 0; \033[K clears to end of line
				// so the previous frame's tail (e.g. when seconds shrink
				// in digit count) doesn't leak through.
				fmt.Fprintf(w, "\r\033[K%s… %ds", label, secs)
			}
		}
	}()
	err := fn()
	close(done)
	<-stopped // ensure the goroutine is done before we finalize the line
	// Wipe the spinner line so the next print starts clean.
	fmt.Fprint(w, "\r\033[K")
	if err == nil {
		fmt.Fprintf(w, "%s — %.1fs\n", label, time.Since(start).Seconds())
	}
	return err
}

// pickAssignedTicket fetches the user's active assigned tickets from
// the configured source (if it implements ticket.Lister) and shows a
// fuzzy-search PickOne over them. Rows are annotated with the
// existing workspace slug when one already exists on disk — handy for
// switching back to in-flight work without re-creating the workspace.
func pickAssignedTicket(ctx context.Context, src ticket.Source, cfg *config.Config,
	errOut io.Writer) (ticket.Ticket, error) {

	lister, ok := src.(ticket.Lister)
	if !ok {
		return ticket.Ticket{}, fmt.Errorf(
			"ticket source %q does not support listing — pass a ticket id explicitly",
			src.Name())
	}

	var tickets []ticket.Ticket
	err := withProgress(errOut, "fetching your open assigned tickets", func() error {
		var listErr error
		tickets, listErr = lister.ListAssigned(ctx)
		return listErr
	})
	if err != nil {
		return ticket.Ticket{}, err
	}
	if len(tickets) == 0 {
		return ticket.Ticket{}, errors.New("no open assigned tickets found")
	}

	// Cross-reference with existing managed workspaces so the picker
	// can surface 'already has a workspace' inline.
	workspaces, _ := listManagedWorkspaces(cfg)
	slugByTicket := make(map[string]string, len(workspaces))
	for _, w := range workspaces {
		slugByTicket[w.ticket] = w.slug
	}

	columns := []tui.Column{
		{Title: "Ticket", Width: 10},
		{Title: "State", Width: 18},
		{Title: "Title", Width: 50},
		{Title: "Workspace", Width: 36},
	}
	rows := make([]tui.Row, len(tickets))
	byID := make(map[string]ticket.Ticket, len(tickets))
	for i, tk := range tickets {
		ws := slugByTicket[tk.SourceID]
		rows[i] = tui.Row{
			Key:    tk.SourceID,
			Cells:  []string{tk.SourceID, tk.State, tk.Title, ws},
			Filter: tk.SourceID + " " + tk.State + " " + tk.Title + " " + ws,
		}
		byID[tk.SourceID] = tk
	}
	key, err := tui.PickOne("Pick a ticket to start a workspace for", columns, rows)
	if err != nil {
		return ticket.Ticket{}, err
	}
	return byID[key], nil
}

// isDir reports whether path exists and is a directory.
func isDir(path string) bool {
	if path == "" {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

// fetchSecret resolves a secret using the highest-priority source
// available: env var → password manager. Each manager call is
// constructed fresh with the secret's own 1Password account so different
// secrets can live in different accounts.
func fetchSecret(ctx context.Context, cfg *config.Config, kind secretKind) (string, error) {
	if v := os.Getenv(envVarFor(kind)); v != "" {
		return v, nil
	}
	if cfg.Passwords.Manager == "" {
		return "", errors.New("no password manager configured — run `thicket init`")
	}
	var ref, account string
	switch kind {
	case secretShortcut:
		ref, account = cfg.Passwords.ShortcutTokenRef, cfg.Passwords.ShortcutTokenAccount
	case secretAnthropic:
		ref, account = cfg.Passwords.AnthropicKeyRef, cfg.Passwords.AnthropicKeyAccount
	}
	if ref == "" {
		return "", fmt.Errorf("reference not configured — set $%s or run `thicket init`",
			envVarFor(kind))
	}
	mgr, err := secrets.New(cfg.Passwords.Manager, secrets.Options{
		OnePasswordAccount: account,
	})
	if err != nil {
		return "", err
	}
	return mgr.Get(ctx, ref)
}

func buildTicketSource(ctx context.Context, cfg *config.Config) (ticket.Source, error) {
	switch cfg.TicketSource {
	case "shortcut":
		token, err := fetchSecret(ctx, cfg, secretShortcut)
		if err != nil {
			return nil, fmt.Errorf("fetch shortcut token: %w", err)
		}
		return shortcut.New(token, ""), nil
	default:
		return nil, fmt.Errorf("unknown ticket_source %q (only \"shortcut\" is implemented)", cfg.TicketSource)
	}
}

func loadCatalog(cfg *config.Config, errOut io.Writer) ([]catalog.Repo, error) {
	cachePath, err := catalog.Path()
	if err != nil {
		return nil, err
	}
	repos, age, err := catalog.Load(cachePath)
	// Refresh if cache is missing, expired, or — defensively — empty
	// (an earlier version of thicket could cache `repos: null`).
	needsRefresh := errors.Is(err, catalog.ErrNoCache) ||
		age >= catalog.DefaultCacheTTL || len(repos) == 0
	if needsRefresh {
		err = withProgress(errOut,
			fmt.Sprintf("fetching repo catalog from GitHub (%v)", cfg.GithubOrgs),
			func() error {
				var buildErr error
				repos, buildErr = catalog.Build(cfg.GithubOrgs, catalog.GHFetcher{})
				return buildErr
			})
		if err != nil {
			return nil, err
		}
		if err := catalog.Save(cachePath, repos); err != nil {
			fmt.Fprintf(errOut, "warning: could not cache catalog: %v\n", err)
		}
	} else if err != nil {
		return nil, err
	}
	return catalog.WithLocalPaths(repos, cfg.ReposRoot), nil
}

func detectRepos(ctx context.Context, cfg *config.Config, _ io.Writer, flags startFlags,
	tk ticket.Ticket, repos []catalog.Repo) ([]detector.RepoMatch, error) {

	catRepos := make([]detector.CatalogRepo, len(repos))
	for i, r := range repos {
		catRepos[i] = detector.CatalogRepo{Name: r.Name, Description: r.Description}
	}

	// --only short-circuit: deterministic resolution against the catalog.
	if len(flags.only) > 0 {
		aliases := make(map[string]string)
		for _, a := range cfg.RepoAliases {
			for _, alias := range a.Aliases {
				aliases[strings.ToLower(alias)] = a.Name
			}
		}
		d := &detector.RuleDetector{Catalog: catRepos, Aliases: aliases}
		return d.Detect(ctx, detector.Input{
			TicketBody: strings.Join(flags.only, ","),
		})
	}

	d, err := buildClaudeDetector(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return d.Detect(ctx, detector.Input{
		TicketTitle: tk.Title,
		TicketBody:  tk.Body,
		Repos:       catRepos,
	})
}

// buildClaudeDetector picks between the API-backed detector (uses an
// Anthropic API key from the password manager) and the CLI-backed
// detector (shells out to `claude -p`, no API key needed — handy for
// users on a Claude Enterprise subscription). The choice is driven by
// cfg.ClaudeBackend; CLI is the default when not set.
func buildClaudeDetector(ctx context.Context, cfg *config.Config) (detector.Detector, error) {
	backend := cfg.ClaudeBackend
	if backend == "" {
		backend = "cli"
	}
	switch backend {
	case "cli":
		bin := cfg.ClaudeBinary
		if bin == "" {
			bin = "claude"
		}
		return detector.NewClaudeCLI(bin, cfg.ClaudeModel), nil
	case "api":
		key, err := fetchSecret(ctx, cfg, secretAnthropic)
		if err != nil {
			return nil, fmt.Errorf("fetch anthropic key: %w", err)
		}
		return detector.NewAnthropic(key, "", anthropic.Model(cfg.ClaudeModel)), nil
	default:
		return nil, fmt.Errorf("unknown claude_backend %q (want \"cli\" or \"api\")", backend)
	}
}

func pickSelector(noInteractive bool) tui.Selector {
	if noInteractive {
		return tui.AutoSelector{AutoClone: true}
	}
	return tui.HuhSelector{}
}

func resolveOrClone(_ context.Context, cfg *config.Config, errOut io.Writer,
	repos []catalog.Repo, chosen []string, selector tui.Selector, dryRun bool) ([]catalog.Repo, error) {

	byName := make(map[string]catalog.Repo, len(repos))
	for _, r := range repos {
		byName[r.Name] = r
	}
	g := gitops.New()
	var out []catalog.Repo
	for _, name := range chosen {
		r, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("internal: %q not in catalog", name)
		}
		if r.Cloned() {
			out = append(out, r)
			continue
		}
		target := filepath.Join(cfg.ReposRoot, r.Name)
		yes, err := selector.ConfirmClone(r.Name, target)
		if err != nil {
			return nil, err
		}
		if !yes {
			fmt.Fprintf(errOut, "skipping %s (no local clone)\n", r.Name)
			continue
		}
		if dryRun {
			fmt.Fprintf(errOut, "(dry-run) would clone %s → %s\n", r.CloneURL, target)
			r.LocalPath = target
			out = append(out, r)
			continue
		}
		// Buffer git's output so the spinner has the line to itself,
		// then dump the buffered output only on error so failed clones
		// stay diagnosable.
		var gitOut bytes.Buffer
		err = withProgress(errOut, fmt.Sprintf("cloning %s → %s", r.CloneURL, target),
			func() error {
				return g.Clone(r.CloneURL, target, &gitOut, &gitOut)
			})
		if err != nil {
			fmt.Fprintln(errOut, strings.TrimSpace(gitOut.String()))
			return nil, fmt.Errorf("clone %s: %w", r.Name, err)
		}
		r.LocalPath = target
		out = append(out, r)
	}
	return out, nil
}

func buildPlan(cfg *config.Config, flags startFlags, src ticket.Source, tk ticket.Ticket,
	chosen []catalog.Repo) (workspace.Plan, error) {

	branch := flags.branch
	if branch == "" {
		branch = src.BranchName(tk)
	}
	if branch == "" {
		// Last-resort default if the source has no opinion.
		branch = workspace.Slug(tk.SourceID, tk.Title)
	}
	// Slug is always ticket-id-prefixed, decoupled from the branch name.
	// Branch may come from Shortcut as e.g. "uri/freshness" (no id) —
	// we still want the workspace folder to carry "sc-65825-freshness".
	slug := workspace.Slug(tk.SourceID, tk.Title)
	wsDir := filepath.Join(cfg.WorkspaceRoot, slug)

	g := gitops.New()
	planRepos := make([]workspace.PlanRepo, 0, len(chosen))
	memRepos := make([]memory.RepoEntry, 0, len(chosen))
	for _, r := range chosen {
		// In --dry-run, repos that the user agreed to clone have
		// r.LocalPath set to the *would-be* target — there's no git
		// repo there yet, so BranchExists would fail and abort the
		// preview. Treat them as branch-doesn't-exist for plan
		// rendering; the real run probes for real after cloning.
		var exists bool
		if !flags.dryRun || isDir(r.LocalPath) {
			var err error
			exists, err = g.BranchExists(r.LocalPath, branch)
			if err != nil {
				return workspace.Plan{}, fmt.Errorf("check branch in %s: %w", r.Name, err)
			}
		}
		wt := filepath.Join(wsDir, r.Name)
		planRepos = append(planRepos, workspace.PlanRepo{
			Name:         r.Name,
			SourcePath:   r.LocalPath,
			WorktreePath: wt,
			BranchExists: exists,
		})
		memRepos = append(memRepos, memory.RepoEntry{
			Name:          r.Name,
			Branch:        branch,
			WorktreePath:  wt,
			DefaultBranch: r.DefaultBranch,
		})
	}
	return workspace.Plan{
		WorkspaceDir: wsDir,
		Branch:       branch,
		Repos:        planRepos,
		Memory: memory.Input{
			TicketID:     tk.SourceID,
			Title:        tk.Title,
			URL:          tk.URL,
			State:        tk.State,
			Owner:        tk.Owner,
			Body:         tk.Body,
			Branch:       branch,
			WorkspaceDir: wsDir,
			Repos:        memRepos,
			CreatedAt:    time.Now(),
		},
	}, nil
}

func printPlan(w io.Writer, p workspace.Plan) {
	fmt.Fprintf(w, "\n(dry-run) plan:\n")
	fmt.Fprintf(w, "  workspace: %s\n", p.WorkspaceDir)
	fmt.Fprintf(w, "  branch:    %s\n", p.Branch)
	fmt.Fprintf(w, "  repos:\n")
	for _, r := range p.Repos {
		mode := "create branch"
		if r.BranchExists {
			mode = "checkout existing"
		}
		fmt.Fprintf(w, "    - %s (%s) src=%s\n", r.Name, mode, r.SourcePath)
	}
}
