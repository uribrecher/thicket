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
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/uribrecher/thicket/internal/catalog"
	"github.com/uribrecher/thicket/internal/config"
	"github.com/uribrecher/thicket/internal/detector"
	gitops "github.com/uribrecher/thicket/internal/git"
	"github.com/uribrecher/thicket/internal/launcher"
	"github.com/uribrecher/thicket/internal/memory"
	"github.com/uribrecher/thicket/internal/secrets"
	thicketterm "github.com/uribrecher/thicket/internal/term"
	"github.com/uribrecher/thicket/internal/ticket"
	"github.com/uribrecher/thicket/internal/ticket/rank"
	"github.com/uribrecher/thicket/internal/ticket/shortcut"
	"github.com/uribrecher/thicket/internal/tui"
	"github.com/uribrecher/thicket/internal/tui/wizard"
	"github.com/uribrecher/thicket/internal/tui/wizard/start"
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

	// cwd shortcut: if the user runs `thicket start` (no positional id)
	// from inside an existing workspace, skip the ticket picker
	// entirely and re-launch Claude on that workspace. Explicit
	// `thicket start <id>` overrides — it's a direct request to open a
	// specific ticket's workspace, not whatever happens to be in pwd.
	// `--dry-run` falls through to no-launch (print the cd line, no
	// exec) so it stays true to "do nothing externally".
	if len(args) == 0 {
		if cwd, getwdErr := os.Getwd(); getwdErr == nil && cwd != "" {
			ws, findErr := workspace.FindContainingWorkspace(cfg.WorkspaceRoot, cwd)
			switch {
			case findErr == nil:
				if ws.State.Nickname != "" {
					fmt.Fprintf(out, "✓ using existing workspace %q (%s)\n", ws.State.Nickname, ws.Slug)
				} else {
					fmt.Fprintf(out, "✓ using existing workspace %s\n", ws.Slug)
				}
				return launchClaudeIn(out, cfg,
					nicknameOrSlug(ws.State.Nickname, ws.Slug),
					ws.State.Color, ws.Path, flags.noLaunch || flags.dryRun)
			case errors.Is(findErr, workspace.ErrNoState):
				// Normal "not in a workspace" — fall through silently.
			default:
				// Real error (corrupt manifest, permission denied).
				// Surface but still fall through to the normal flow.
				fmt.Fprintf(errOut, "warning: cwd-shortcut check: %v\n", findErr)
			}
		}
	}

	src, err := buildTicketSource(cmd.Context(), cfg)
	if err != nil {
		return err
	}

	// The wizard needs a TTY for bubbletea. --no-interactive and
	// --dry-run keep today's plain-stdout flow since callers in those
	// modes are typically scripted and benefit from line-oriented
	// output. Non-TTY stdin (CI, pipes) also drops to legacy because
	// bubbletea can't read input.
	useWizard := !flags.noInteractive && !flags.dryRun && term.IsTerminal(int(os.Stdin.Fd()))
	if !useWizard {
		return runStartLegacy(cmd, cfg, flags, src, args, out, errOut)
	}
	return runStartWizard(cmd, cfg, flags, src, args, out, errOut)
}

// runStartWizard is the interactive TTY path: a single Bubble Tea
// wizard with three pages (Ticket / Repos / Plan) replaces the old
// stack of sequential prompts.
func runStartWizard(cmd *cobra.Command, cfg *config.Config, flags startFlags,
	src ticket.Source, args []string, out, errOut io.Writer) error {

	// Pre-load the catalog OUTSIDE the wizard so the catalog-refresh
	// spinner doesn't fight bubbletea for the terminal.
	repos, err := loadCatalog(cfg, errOut)
	if err != nil {
		return err
	}

	// Args-path: parse + fetch + existing-workspace short-circuit
	// before entering the wizard. The wizard's Ticket page is
	// pre-completed in this case so the user lands on Repos.
	var preselected *ticket.Ticket
	if len(args) > 0 {
		id, err := src.Parse(args[0])
		if err != nil {
			return err
		}
		var tk ticket.Ticket
		err = withProgress(errOut, fmt.Sprintf("fetching ticket %s", id), func() error {
			var fetchErr error
			tk, fetchErr = src.Fetch(id)
			return fetchErr
		})
		if err != nil {
			return err
		}
		if existing := findWorkspaceForTicket(cfg, tk.SourceID, errOut); existing != nil {
			fmt.Fprintf(out, "reusing existing workspace at %s\n", existing.Path)
			return launchClaudeIn(out, cfg,
				nicknameOrSlug(existing.State.Nickname, workspace.Slug(tk.SourceID, tk.Title)),
				existing.State.Color, existing.Path, flags.noLaunch)
		}
		preselected = &tk
	}

	// Build the per-ticket Detect closure once so the wizard doesn't
	// import cmd/thicket.
	detectFn := func(ctx context.Context, tk ticket.Ticket, repos []catalog.Repo) ([]detector.RepoMatch, error) {
		return detectRepos(ctx, cfg, errOut, flags, tk, repos)
	}

	// Build the summarizer once and wrap as a closure for the wizard.
	// On builder error we skip wiring it (Summarize stays nil); the
	// wizard renderer then keeps its dumb first-N-lines fallback —
	// a missing/misconfigured backend shouldn't break the picker.
	var summarizeFn func(ctx context.Context, tk ticket.Ticket) ([]string, error)
	if sum, sumErr := buildClaudeSummarizer(cmd.Context(), cfg); sumErr == nil && sum != nil {
		summarizeFn = func(ctx context.Context, tk ticket.Ticket) ([]string, error) {
			return sum.Summarize(ctx, tk.Title, tk.Body)
		}
	}

	// One-time workspace scan: feeds both the Ticket-picker's
	// "Workspace" column AND the nickname suggester's existing-color
	// differentiation hint. Order matters — build the scan first so
	// the nickFn closure can capture the color list at construction
	// time (the suggester's signature wants the colors per call, but
	// the slice doesn't change during one wizard run).
	existingByTicket := make(map[string]workspace.ManagedWorkspace)
	var existingColors []string
	if wsList, warnings, listErr := workspace.ListManaged(cfg.WorkspaceRoot); listErr == nil {
		// wsList comes back newest-first; we want the freshest
		// colors at the top of the list when we cap inside the
		// suggester's renderExistingColorsClause helper.
		for _, w := range wsList {
			existingByTicket[w.State.TicketID] = w
			if w.State.Color != "" {
				existingColors = append(existingColors, w.State.Color)
			}
		}
		for _, warn := range warnings {
			fmt.Fprintf(errOut, "warning: %v\n", warn)
		}
	}
	findExisting := func(id string) *workspace.ManagedWorkspace {
		if w, ok := existingByTicket[id]; ok {
			return &w
		}
		return nil
	}

	// Same shape for the nickname+color suggester. Failure to build
	// leaves nickFn nil → the Plan page's input starts empty and the
	// launcher leaves the iTerm2 tab uncolored. The existing-colors
	// list is captured here so the LLM picks a contrasting hue.
	var nickFn func(ctx context.Context, tk ticket.Ticket) (detector.NicknameSuggestion, error)
	if ns, nsErr := buildClaudeNicknameSuggester(cmd.Context(), cfg); nsErr == nil && ns != nil {
		colors := existingColors
		nickFn = func(ctx context.Context, tk ticket.Ticket) (detector.NicknameSuggestion, error) {
			return ns.Suggest(ctx, tk.Title, tk.Body, colors)
		}
	}

	deps := wizard.Deps{
		Ctx:                   cmd.Context(),
		Cfg:                   cfg,
		Src:                   src,
		Repos:                 repos,
		Detect:                detectFn,
		Summarize:             summarizeFn,
		Nickname:              nickFn,
		Git:                   gitops.New(),
		Flags:                 wizard.Flags{Branch: flags.branch},
		FindExistingWorkspace: findExisting,
	}
	if l, ok := src.(ticket.Lister); ok {
		deps.Lister = l
	}
	if preselected != nil {
		deps.Preselected = preselected
	}

	res, err := start.Run(deps)
	if err != nil {
		if errors.Is(err, tui.ErrCancelled) {
			fmt.Fprintln(out, "cancelled.")
			return nil
		}
		return err
	}

	// Reuse exit (wizard short-circuited because an existing workspace
	// matched the picked ticket): launch Claude in the existing dir.
	if res.ReuseDir != "" {
		fmt.Fprintf(out, "reusing existing workspace at %s\n", res.ReuseDir)
		name := workspace.Slug(res.Ticket.SourceID, res.Ticket.Title)
		color := ""
		if st, err := workspace.ReadState(res.ReuseDir); err == nil {
			if st.Nickname != "" {
				name = st.Nickname
			}
			color = st.Color
		}
		return launchClaudeIn(out, cfg, name, color, res.ReuseDir, flags.noLaunch)
	}

	// Surface any skipped/failed clones from the wizard's clone phase
	// (proceed-without-failed-repo policy).
	for _, s := range res.Skipped {
		fmt.Fprintf(errOut, "skipped %s: %s\n", s.Name, s.Reason)
	}

	// workspace.Create runs in plain stdout (the wizard has already
	// exited and torn down its UI), streaming ✓ lines for each
	// worktree + memory file + state manifest.
	plan := res.Plan
	plan.Progress = out
	w := workspace.New(gitops.New())
	if err := w.Create(plan); err != nil {
		return err
	}
	fmt.Fprintf(out, "\nworkspace ready at %s\n", plan.WorkspaceDir)
	return launchClaudeIn(out, cfg,
		nicknameOrSlug(plan.Nickname, workspace.Slug(res.Ticket.SourceID, res.Ticket.Title)),
		plan.Color, plan.WorkspaceDir, flags.noLaunch)
}

// runStartLegacy preserves the pre-wizard CLI flow for the cases
// where bubbletea can't run: --no-interactive, --dry-run, and non-TTY
// stdin. Same behavior thicket has had since 0.1; just split out so
// the wizard path can stay focused.
func runStartLegacy(cmd *cobra.Command, cfg *config.Config, flags startFlags,
	src ticket.Source, args []string, out, errOut io.Writer) error {

	var tk ticket.Ticket
	if len(args) == 0 {
		// Legacy interactive picker — still uses tui.PickOne under the
		// hood. Reached when the user passes --dry-run on a TTY.
		picked, err := pickAssignedTicketLegacy(cmd.Context(), src, cfg, errOut)
		if err != nil {
			if errors.Is(err, tui.ErrCancelled) {
				fmt.Fprintln(out, "cancelled.")
				return nil
			}
			return err
		}
		id, err := src.Parse(picked.SourceID)
		if err != nil {
			return err
		}
		tk, err = src.Fetch(id)
		if err != nil {
			return err
		}
		if tk.State == "" && picked.State != "" {
			tk.State = picked.State
		}
		fmt.Fprintf(out, "  %s — %s\n", tui.HyperlinkForWriter(out, tk.URL, tk.SourceID), tk.Title)
	} else {
		id, err := src.Parse(args[0])
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "fetching ticket %s...\n", id)
		var err2 error
		tk, err2 = src.Fetch(id)
		if err2 != nil {
			return err2
		}
		fmt.Fprintf(out, "  %s — %s\n", tui.HyperlinkForWriter(out, tk.URL, tk.SourceID), tk.Title)
	}
	if existing := findWorkspaceForTicket(cfg, tk.SourceID, errOut); existing != nil {
		fmt.Fprintf(out, "reusing existing workspace at %s\n", existing.Path)
		return launchClaudeIn(out, cfg,
			nicknameOrSlug(existing.State.Nickname, workspace.Slug(tk.SourceID, tk.Title)),
			existing.State.Color, existing.Path, flags.noLaunch)
	}

	repos, err := loadCatalog(cfg, errOut)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "\n%s %d active repos across %v\n",
		catalogLabelStyle.Render("catalog:"), len(repos), cfg.GithubOrgs)

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

	chosen, err := resolveOrCloneLegacy(cmd.Context(), cfg, errOut, repos, chosenNames, selector, flags.dryRun)
	if err != nil {
		return err
	}
	if len(chosen) == 0 {
		return errors.New("no repos remain after the clone gate")
	}

	plan, err := buildPlanLegacy(cfg, flags, src, tk, chosen)
	if err != nil {
		return err
	}
	printPlanLegacy(out, plan, flags.dryRun)
	if flags.dryRun {
		return nil
	}

	plan.Progress = out
	w := workspace.New(gitops.New())
	if err := w.Create(plan); err != nil {
		return err
	}
	fmt.Fprintf(out, "\nworkspace ready at %s\n", plan.WorkspaceDir)
	return launchClaudeIn(out, cfg,
		nicknameOrSlug(plan.Nickname, workspace.Slug(tk.SourceID, tk.Title)),
		plan.Color, plan.WorkspaceDir, flags.noLaunch)
}

// findWorkspaceForTicket scans the workspace root for a managed
// workspace whose manifest's TicketID matches the given id. Returns
// nil when none exists. Read errors are tolerated silently — the
// caller will hit the same dir again on the create path.
func findWorkspaceForTicket(cfg *config.Config, ticketID string, errOut io.Writer) *workspace.ManagedWorkspace {
	workspaces, warnings, err := workspace.ListManaged(cfg.WorkspaceRoot)
	if err != nil {
		// Treat as "no existing workspace" — workspace.Create will hit
		// the same dir read and surface the underlying error then.
		return nil
	}
	// Surface per-manifest warnings here too: if a corrupt manifest
	// hides an existing workspace from this lookup, the user is about
	// to hit a confusing ErrExists from workspace.Create. Telling them
	// "warning: ws-xyz: parse state: ..." right now is far more useful
	// than the eventual mkdir-collision error.
	for _, w := range warnings {
		fmt.Fprintf(errOut, "warning: %v\n", w)
	}
	for i := range workspaces {
		if workspaces[i].State.TicketID == ticketID {
			return &workspaces[i]
		}
	}
	return nil
}

// launchClaudeIn opens the configured Claude binary in workspaceDir,
// passing `--name <name>` so the session is distinguishable in
// Claude's prompt box, /resume picker, and the terminal title.
// Honors --no-launch by printing the cd line instead.
//
// Before the syscall.Exec hand-off, when running under iTerm2 AND
// stdout is a TTY, also emits OSC escapes to set the tab title
// (= name), the tab badge (= name), and the tab background color
// (= color, when valid). These persist for the lifetime of the tab
// so concurrent workspace sessions stay visually distinct. Piped
// stdout (`thicket start … | tee …`) skips the escapes so the pipe
// doesn't see gibberish.
//
// Callers pass the workspace's nickname when set (short, human-
// friendly), falling back to the slug. See nicknameOrSlug. We
// re-run name through workspace.SanitizeNickname here so a hand-
// edited or freshly-typed value can't smuggle escape characters
// into the `--name` arg or the OSC stream.
func launchClaudeIn(out io.Writer, cfg *config.Config, name, color,
	workspaceDir string, noLaunch bool) error {

	// Defensive: SanitizeNickname drops control chars / ANSI
	// escapes. writeState already does this on persistence, but
	// the wizard's Plan-page input flows straight into launchClaudeIn
	// without going through writeState in the reuse paths.
	name = workspace.SanitizeNickname(name)

	if noLaunch {
		fmt.Fprintf(out, "cd %s\n", workspaceDir)
		return nil
	}
	if thicketterm.IsITerm2() && term.IsTerminal(int(os.Stdout.Fd())) {
		// Write to stdout — that's the terminal fd we'll hand off
		// to claude via syscall.Exec, so the escapes are
		// interpreted by the parent iTerm2 tab before claude takes
		// over.
		thicketterm.WriteTabTitle(os.Stdout, name)
		thicketterm.WriteBadge(os.Stdout, name)
		thicketterm.WriteTabColor(os.Stdout, color)
	}
	l := launcher.New(cfg.ClaudeBinary)
	l.ExtraArgs = []string{"--name", name}
	if err := l.Launch(workspaceDir); err != nil {
		if errors.Is(err, launcher.ErrMissingBinary) {
			launcher.PrintFallback(out, workspaceDir)
			return nil
		}
		return err
	}
	return nil
}

// nicknameOrSlug returns the nickname when non-empty, the slug
// otherwise — the resolution used for Claude's --name flag and other
// human-facing labels.
func nicknameOrSlug(nickname, slug string) string {
	if nickname != "" {
		return nickname
	}
	return slug
}

// ----- helpers below -----

type startFlags struct {
	only          []string
	branch        string
	nickname      string
	noInteractive bool
	noLaunch      bool
	dryRun        bool
}

func readStartFlags(cmd *cobra.Command) (startFlags, error) {
	f := cmd.Flags()
	only, _ := f.GetStringSlice("only")
	branch, _ := f.GetString("branch")
	nickname, _ := f.GetString("nickname")
	noInteractive, _ := f.GetBool("no-interactive")
	noLaunch, _ := f.GetBool("no-launch")
	dryRun, _ := f.GetBool("dry-run")
	return startFlags{
		only:          only,
		branch:        branch,
		nickname:      nickname,
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
// Ns" elapsed-time spinner. Used by the legacy path and by the
// wizard's pre-flight (catalog refresh) where bubbletea isn't running
// yet.
func withProgress(w io.Writer, label string, fn func() error) error {
	start := time.Now()
	done := make(chan struct{})
	stopped := make(chan struct{})
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
				fmt.Fprintf(w, "\r\033[K%s… %ds", label, secs)
			}
		}
	}()
	err := fn()
	close(done)
	<-stopped
	fmt.Fprint(w, "\r\033[K")
	if err == nil {
		fmt.Fprintf(w, "%s — %.1fs\n", label, time.Since(start).Seconds())
	}
	return err
}

// pickAssignedTicketLegacy is the pre-wizard ticket picker, still used
// by the legacy non-interactive / dry-run path. The wizard's Ticket
// page replaces it for the main interactive flow.
func pickAssignedTicketLegacy(ctx context.Context, src ticket.Source, cfg *config.Config,
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

	workspaces, warnings, listErr := workspace.ListManaged(cfg.WorkspaceRoot)
	if listErr != nil {
		fmt.Fprintf(errOut, "warning: could not enumerate existing workspaces: %v\n", listErr)
	}
	for _, w := range warnings {
		fmt.Fprintf(errOut, "warning: %v\n", w)
	}
	slugByTicket := make(map[string]string, len(workspaces))
	for _, w := range workspaces {
		slugByTicket[w.State.TicketID] = w.Slug
	}

	// Re-rank tickets using the cross-source ranker. The shortcut
	// source still returns them in its own order; rank.Sort imposes
	// the state-dominant scoring described in
	// docs/superpowers/specs/2026-05-16-ticket-ranking-design.md.
	rank.Sort(tickets, func(sourceID string) bool {
		return slugByTicket[sourceID] != ""
	})

	columns := []tui.Column{
		{Title: "Ticket", Width: 10},
		{Title: "State", Width: 18},
		{Title: "Title", Width: 50},
		{Title: "Workspace", Width: 36},
		{Title: "Iter", Width: 5},
	}
	rows := make([]tui.Row, len(tickets))
	byID := make(map[string]ticket.Ticket, len(tickets))
	for i, tk := range tickets {
		ws := slugByTicket[tk.SourceID]
		iter := rank.FormatIterationDistance(tk.IterationDistance)
		rows[i] = tui.Row{
			Key:    tk.SourceID,
			Cells:  []string{tk.SourceID, tk.State, tk.Title, ws, iter},
			Filter: tk.SourceID + " " + tk.State + " " + tk.Title + " " + ws,
			URL:    tk.URL, // Ticket is column 0 — URLColumn defaults to 0.
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
// available: env var → password manager.
func fetchSecret(ctx context.Context, cfg *config.Config, kind secretKind) (string, error) {
	if v := os.Getenv(envVarFor(kind)); v != "" {
		return v, nil
	}
	if cfg.Passwords.Manager == "" {
		return "", errors.New("no password manager configured — run `thicket config`")
	}
	var ref, account string
	switch kind {
	case secretShortcut:
		ref, account = cfg.Passwords.ShortcutTokenRef, cfg.Passwords.ShortcutTokenAccount
	case secretAnthropic:
		ref, account = cfg.Passwords.AnthropicKeyRef, cfg.Passwords.AnthropicKeyAccount
	}
	if ref == "" {
		return "", fmt.Errorf("reference not configured — set $%s or run `thicket config`",
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

// buildClaudeSummarizer mirrors buildClaudeDetector — same backend
// selection, same secret-fetch path, same defaults. Kept separate from
// the detector builder so an interface change to one doesn't cascade.
func buildClaudeSummarizer(ctx context.Context, cfg *config.Config) (detector.Summarizer, error) {
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
		return detector.NewClaudeCLISummarizer(bin, cfg.ClaudeModel), nil
	case "api":
		key, err := fetchSecret(ctx, cfg, secretAnthropic)
		if err != nil {
			return nil, fmt.Errorf("fetch anthropic key: %w", err)
		}
		return detector.NewAnthropicSummarizer(key, "", anthropic.Model(cfg.ClaudeModel)), nil
	default:
		return nil, fmt.Errorf("unknown claude_backend %q (want \"cli\" or \"api\")", backend)
	}
}

// buildClaudeNicknameSuggester mirrors buildClaudeSummarizer — same
// backend selection, same secret-fetch path. Separate from the
// summarizer builder so an interface change to one doesn't cascade.
func buildClaudeNicknameSuggester(ctx context.Context, cfg *config.Config) (detector.NicknameSuggester, error) {
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
		return detector.NewClaudeCLINicknameSuggester(bin, cfg.ClaudeModel), nil
	case "api":
		key, err := fetchSecret(ctx, cfg, secretAnthropic)
		if err != nil {
			return nil, fmt.Errorf("fetch anthropic key: %w", err)
		}
		return detector.NewAnthropicNicknameSuggester(key, "", anthropic.Model(cfg.ClaudeModel)), nil
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

// resolveOrCloneLegacy walks the chosen names, surfaces a per-repo
// clone confirmation, and clones any missing locals. Wizard mode does
// this in-page with concurrent cmds; this lives on for --no-interactive
// and --dry-run.
func resolveOrCloneLegacy(_ context.Context, cfg *config.Config, errOut io.Writer,
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

// buildPlanLegacy mirrors the wizard's plan-build logic for the
// legacy path. Kept separate so behavioral changes inside the wizard
// don't leak into --dry-run output and vice versa.
func buildPlanLegacy(cfg *config.Config, flags startFlags, src ticket.Source, tk ticket.Ticket,
	chosen []catalog.Repo) (workspace.Plan, error) {

	branch := flags.branch
	if branch == "" {
		branch = src.BranchName(tk)
	}
	if branch == "" {
		branch = workspace.Slug(tk.SourceID, tk.Title)
	}
	slug := workspace.Slug(tk.SourceID, tk.Title)
	wsDir := filepath.Join(cfg.WorkspaceRoot, slug)

	g := gitops.New()
	planRepos := make([]workspace.PlanRepo, 0, len(chosen))
	memRepos := make([]memory.RepoEntry, 0, len(chosen))
	for _, r := range chosen {
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
		Nickname:     flags.nickname,
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

// planTitleStyle highlights the "plan:" header in legacy output.
var planTitleStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("214")).
	Bold(true)

// catalogLabelStyle paints the "catalog:" label yellow in legacy output.
var catalogLabelStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("214"))

func printPlanLegacy(w io.Writer, p workspace.Plan, dryRun bool) {
	label := "plan:"
	if dryRun {
		label = "(dry-run) plan:"
	}
	fmt.Fprintf(w, "\n%s\n", planTitleStyle.Render(label))
	fmt.Fprintf(w, "  workspace dir: %s\n", AbbrevHome(p.WorkspaceDir))
	fmt.Fprintf(w, "  branch:        %s\n", p.Branch)
	fmt.Fprintf(w, "  worktrees:     %d\n", len(p.Repos))
	for _, r := range p.Repos {
		mode := "create branch"
		if r.BranchExists {
			mode = "checkout existing"
		}
		fmt.Fprintf(w, "    • %s (%s) src=%s\n",
			r.Name, mode, AbbrevHome(r.SourcePath))
	}
	fmt.Fprintln(w)
}

// AbbrevHome collapses an absolute path under $HOME to a leading `~`.
func AbbrevHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+string(os.PathSeparator)) {
		return "~" + path[len(home):]
	}
	return path
}
