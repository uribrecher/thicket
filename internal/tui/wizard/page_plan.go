package wizard

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/uribrecher/thicket/internal/catalog"
	"github.com/uribrecher/thicket/internal/memory"
	"github.com/uribrecher/thicket/internal/workspace"
)

// cloneState tracks one clone's lifecycle for in-page rendering.
type cloneState struct {
	name      string
	cloneURL  string
	targetDir string
	started   time.Time
	done      bool
	err       error
}

type planPage struct {
	// Built lazily on first activation so the user sees branch-exists
	// info pulled from the actual local repos.
	built       bool
	buildErr    error
	builtForID  string // ticket id the current cloneInclude state belongs to
	branch      string
	workspace   string
	allRepos    []catalog.Repo // chosen, ordered (cloned + to-clone)
	toClone     []catalog.Repo // subset needing a clone
	branchExist map[string]bool

	// "Missing clones" checkbox state: name → include in workspace.
	// Defaults to true for every to-clone repo. Wiped on ticket
	// change (see initCmd) so a repo unchecked for ticket A doesn't
	// silently start unchecked when the user moves to ticket B.
	cloneInclude map[string]bool
	cursor       int  // index into a flat cursor space: 0..len(toClone)-1
	focusBtn     bool // true when the cursor is on the Create button (no toggleable rows or below the list)

	// Post-Create state.
	creating  bool
	clones    map[string]*cloneState
	cloneOrd  []string // for deterministic rendering
	startedAt time.Time
}

func newPlanPage() *planPage {
	return &planPage{
		cloneInclude: make(map[string]bool),
	}
}

func (p *planPage) Title() string { return "Plan" }

// Hints renders dynamically: while clones run we lock input and show
// nothing; otherwise show ↑/↓, and space if there are toggleable
// missing-clone rows, and enter for the Create action.
func (p *planPage) Hints() string {
	if p.creating {
		return ""
	}
	if len(p.toClone) > 0 {
		return "↑/↓ cursor · space toggles clone · enter creates"
	}
	return "enter creates"
}

// Complete is true once the plan is built without error. The page is
// its own commit gate via the Create button; → / Enter at the wizard
// level don't advance past Plan (it's the last page).
func (p *planPage) Complete() bool { return p.built && p.buildErr == nil }

// locked reports whether tab navigation should be blocked. We lock
// once Create starts so the user can't unwind a half-materialized
// workspace.
func (p *planPage) locked() bool { return p.creating }

// initCmd rebuilds the plan on EVERY activation. Earlier we tried to
// skip rebuilds when the chosen-repo list was unchanged, but that
// invited a class of stale-state bugs: a repo's LocalPath could
// change between activations (after a previous Create attempt
// cloned it), the branch-exists probe could now return a different
// answer, and so on. Rebuilding is cheap (a few BranchExists calls)
// so we always rebuild and trust the latest state.
func (p *planPage) initCmd(m *Model) tea.Cmd {
	// Drop checkbox state when the ticket changed — a repo unchecked
	// for ticket A would otherwise start unchecked for ticket B if
	// both happen to need it cloned, which is surprising.
	if p.builtForID != m.ticketID {
		p.cloneInclude = make(map[string]bool)
		p.builtForID = m.ticketID
	}
	p.built = false
	p.buildErr = nil
	return buildPlanCmd(m)
}

func buildPlanCmd(m *Model) tea.Cmd {
	return func() tea.Msg {
		branch := strings.TrimSpace(m.deps.Flags.Branch)
		if branch == "" {
			branch = m.deps.Src.BranchName(m.ticket)
		}
		if branch == "" {
			branch = workspace.Slug(m.ticket.SourceID, m.ticket.Title)
		}
		// BranchExists for already-cloned repos so the plan preview
		// shows "checkout existing" vs "create branch" accurately.
		// For un-cloned repos we can't probe yet — assume create.
		exist := make(map[string]bool, len(m.chosen))
		for _, r := range m.chosen {
			if r.LocalPath == "" {
				continue
			}
			ok, err := m.deps.Git.BranchExists(r.LocalPath, branch)
			if err != nil {
				return planBuiltMsg{err: fmt.Errorf("check branch in %s: %w", r.Name, err)}
			}
			exist[r.Name] = ok
		}
		var toClone []catalog.Repo
		for _, r := range m.chosen {
			if r.LocalPath == "" {
				toClone = append(toClone, r)
			}
		}
		// Build the workspace.Plan eagerly so the preview reflects
		// final state. Repos without LocalPath get a would-be
		// target so the preview's source path renders sensibly; the
		// real LocalPath lands after clone.
		slug := workspace.Slug(m.ticket.SourceID, m.ticket.Title)
		wsDir := filepath.Join(m.deps.Cfg.WorkspaceRoot, slug)
		planRepos := make([]workspace.PlanRepo, 0, len(m.chosen))
		memRepos := make([]memory.RepoEntry, 0, len(m.chosen))
		for _, r := range m.chosen {
			src := r.LocalPath
			if src == "" {
				src = filepath.Join(m.deps.Cfg.ReposRoot, r.Name)
			}
			wt := filepath.Join(wsDir, r.Name)
			planRepos = append(planRepos, workspace.PlanRepo{
				Name:         r.Name,
				SourcePath:   src,
				WorktreePath: wt,
				BranchExists: exist[r.Name],
			})
			memRepos = append(memRepos, memory.RepoEntry{
				Name:          r.Name,
				Branch:        branch,
				WorktreePath:  wt,
				DefaultBranch: r.DefaultBranch,
			})
		}
		plan := workspace.Plan{
			WorkspaceDir: wsDir,
			Branch:       branch,
			Repos:        planRepos,
			Memory: memory.Input{
				TicketID:     m.ticket.SourceID,
				Title:        m.ticket.Title,
				URL:          m.ticket.URL,
				State:        m.ticket.State,
				Owner:        m.ticket.Owner,
				Body:         m.ticket.Body,
				Branch:       branch,
				WorkspaceDir: wsDir,
				Repos:        memRepos,
				CreatedAt:    time.Now(),
			},
		}
		return planBuiltMsg{plan: plan, toClone: toClone}
	}
}

func (p *planPage) Update(m *Model, msg tea.Msg) (Page, tea.Cmd) {
	switch v := msg.(type) {
	case planBuiltMsg:
		if v.err != nil {
			p.built = false
			p.buildErr = v.err
			return p, nil
		}
		p.built = true
		p.buildErr = nil
		p.branch = v.plan.Branch
		p.workspace = v.plan.WorkspaceDir
		p.allRepos = append(p.allRepos[:0], m.chosen...)
		p.toClone = v.toClone
		// Persist the partial result on the model so the wizard's
		// post-clone exit has the plan handy.
		m.result.Plan = v.plan
		// Branch-exists map for in-place re-rendering.
		p.branchExist = make(map[string]bool, len(v.plan.Repos))
		for _, r := range v.plan.Repos {
			p.branchExist[r.Name] = r.BranchExists
		}
		// Default every to-clone repo to checked; preserve any
		// previous toggle state when the user navigates back & forth.
		for _, r := range p.toClone {
			if _, ok := p.cloneInclude[r.Name]; !ok {
				p.cloneInclude[r.Name] = true
			}
		}
		// Reset the cursor — focus the first toggleable row if any,
		// otherwise the Create button.
		if len(p.toClone) > 0 {
			p.cursor = 0
			p.focusBtn = false
		} else {
			p.focusBtn = true
		}
		return p, nil

	case cloneStartedMsg:
		if cs, ok := p.clones[v.name]; ok {
			cs.started = time.Now()
		}
		return p, p.tickCmd()

	case cloneDoneMsg:
		cs, ok := p.clones[v.name]
		if !ok {
			return p, nil
		}
		cs.done = true
		cs.err = v.err
		if v.err == nil {
			// Patch the corresponding allRepos entry with the new
			// local path so the result reflects it.
			for i := range p.allRepos {
				if p.allRepos[i].Name == v.name {
					p.allRepos[i].LocalPath = v.localPath
					break
				}
			}
		}
		// If every clone is finished, advance to workspace.Create.
		if p.allClonesDone() {
			return p, p.finalizeCmd(m)
		}
		return p, nil

	case tickMsg:
		return p, p.tickCmd()

	case tea.KeyMsg:
		if p.creating {
			return p, nil // input locked while clones run
		}
		switch v.String() {
		case "up", "k":
			if p.focusBtn && len(p.toClone) > 0 {
				p.focusBtn = false
				p.cursor = len(p.toClone) - 1
			} else if p.cursor > 0 {
				p.cursor--
			}
			return p, nil
		case "down", "j":
			if !p.focusBtn {
				if p.cursor < len(p.toClone)-1 {
					p.cursor++
				} else {
					p.focusBtn = true
				}
			}
			return p, nil
		case " ":
			if !p.focusBtn && len(p.toClone) > 0 {
				name := p.toClone[p.cursor].Name
				p.cloneInclude[name] = !p.cloneInclude[name]
			}
			return p, nil
		case "enter":
			if p.focusBtn || len(p.toClone) == 0 {
				return p, p.startCloneCmd(m)
			}
			// Enter on a missing-clone row toggles it (mirror "space").
			name := p.toClone[p.cursor].Name
			p.cloneInclude[name] = !p.cloneInclude[name]
			return p, nil
		}
	}
	return p, nil
}

// allClonesDone reports whether every started clone has finished.
func (p *planPage) allClonesDone() bool {
	if len(p.clones) == 0 {
		return false
	}
	for _, cs := range p.clones {
		if !cs.done {
			return false
		}
	}
	return true
}

func (p *planPage) tickCmd() tea.Cmd {
	if !p.creating {
		return nil
	}
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// startCloneCmd kicks off the clone phase. After it returns, the
// wizard receives one cloneStartedMsg + one cloneDoneMsg per repo.
// If nothing needs cloning, it skips straight to finalizeCmd.
func (p *planPage) startCloneCmd(m *Model) tea.Cmd {
	// Build the final repo set, dropping unchecked to-clones.
	final := make([]catalog.Repo, 0, len(p.allRepos))
	var pending []catalog.Repo
	for _, r := range p.allRepos {
		if r.LocalPath != "" {
			final = append(final, r)
			continue
		}
		if p.cloneInclude[r.Name] {
			final = append(final, r)
			pending = append(pending, r)
		} else {
			// Track this as an explicit user-skip for the summary.
			m.result.Skipped = append(m.result.Skipped, SkipReport{
				Name:   r.Name,
				Reason: "deselected before create",
			})
		}
	}
	if len(final) == 0 {
		p.buildErr = errors.New("no repos selected to clone — uncheck or remove repos to retry")
		return nil
	}
	p.allRepos = final
	p.creating = true
	p.startedAt = time.Now()
	p.clones = make(map[string]*cloneState, len(pending))
	p.cloneOrd = p.cloneOrd[:0]
	if len(pending) == 0 {
		// Nothing to clone; finalize immediately.
		return p.finalizeCmd(m)
	}
	cmds := make([]tea.Cmd, 0, 2*len(pending))
	for _, r := range pending {
		target := filepath.Join(m.deps.Cfg.ReposRoot, r.Name)
		cs := &cloneState{name: r.Name, cloneURL: r.CloneURL, targetDir: target}
		p.clones[r.Name] = cs
		p.cloneOrd = append(p.cloneOrd, r.Name)
		// Send the started msg immediately so the UI flips to "cloning…"
		// while the actual git call runs.
		started := func(name string) tea.Cmd {
			return func() tea.Msg { return cloneStartedMsg{name: name} }
		}(r.Name)
		clone := func(name, url, dir string) tea.Cmd {
			return func() tea.Msg {
				var buf bytes.Buffer
				err := m.deps.Git.Clone(url, dir, &buf, &buf)
				out := strings.TrimSpace(buf.String())
				if err != nil {
					return cloneDoneMsg{name: name, err: fmt.Errorf("%s: %w (git output: %s)", name, err, truncate(out, 200))}
				}
				return cloneDoneMsg{name: name, localPath: dir}
			}
		}(r.Name, r.CloneURL, target)
		cmds = append(cmds, started, clone)
	}
	cmds = append(cmds, p.tickCmd())
	return tea.Batch(cmds...)
}

// finalizeCmd builds the post-clone plan and emits createDoneMsg.
// workspace.Create itself runs AFTER the wizard exits (in runStart),
// so the result here carries a finalized plan ready to execute.
func (p *planPage) finalizeCmd(m *Model) tea.Cmd {
	return func() tea.Msg {
		// Filter out failed clones (proceed-without-failed-repo policy).
		var kept []catalog.Repo
		for _, r := range p.allRepos {
			if cs, ok := p.clones[r.Name]; ok && cs.err != nil {
				m.result.Skipped = append(m.result.Skipped, SkipReport{
					Name:   r.Name,
					Reason: cs.err.Error(),
				})
				continue
			}
			kept = append(kept, r)
		}
		if len(kept) == 0 {
			return createDoneMsg{err: errors.New("every clone failed — nothing to materialize")}
		}
		// Re-build the plan against the kept repos so PlanRepo.SourcePath
		// uses the freshly-cloned target dirs.
		planRepos := make([]workspace.PlanRepo, 0, len(kept))
		memRepos := make([]memory.RepoEntry, 0, len(kept))
		for _, r := range kept {
			src := r.LocalPath
			if src == "" {
				src = filepath.Join(m.deps.Cfg.ReposRoot, r.Name)
			}
			exists := p.branchExist[r.Name]
			// For freshly-cloned repos we never checked
			// BranchExists; re-probe so the worktree is added with
			// the right `-b` flag.
			if cs, ok := p.clones[r.Name]; ok && cs.err == nil {
				ok, err := m.deps.Git.BranchExists(src, p.branch)
				if err != nil {
					return createDoneMsg{err: fmt.Errorf("check branch in %s after clone: %w", r.Name, err)}
				}
				exists = ok
			}
			wt := filepath.Join(p.workspace, r.Name)
			planRepos = append(planRepos, workspace.PlanRepo{
				Name:         r.Name,
				SourcePath:   src,
				WorktreePath: wt,
				BranchExists: exists,
			})
			memRepos = append(memRepos, memory.RepoEntry{
				Name:          r.Name,
				Branch:        p.branch,
				WorktreePath:  wt,
				DefaultBranch: r.DefaultBranch,
			})
		}
		plan := workspace.Plan{
			WorkspaceDir: p.workspace,
			Branch:       p.branch,
			Repos:        planRepos,
			Memory:       m.result.Plan.Memory,
		}
		plan.Memory.Repos = memRepos
		return createDoneMsg{result: Result{Plan: plan, Skipped: m.result.Skipped}}
	}
}

func (p *planPage) View(m *Model) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Review and create workspace"))
	b.WriteString("\n\n")
	if !p.built {
		if p.buildErr != nil {
			b.WriteString("  " + errStyle.Render(fmtErr(p.buildErr)) + "\n")
			return indent(b.String(), 2)
		}
		b.WriteString("  " + hintStyle.Render("building plan…") + "\n")
		return indent(b.String(), 2)
	}

	// Missing clones (only if non-empty).
	if len(p.toClone) > 0 && !p.creating {
		b.WriteString("  " + sectionStyle.Render("Missing clones") + "\n")
		for i, r := range p.toClone {
			check := "[ ]"
			if p.cloneInclude[r.Name] {
				check = "[x]"
			}
			cursor := " "
			line := fmt.Sprintf("%s  %s %-32s → %s",
				cursor, check, r.Name,
				abbrevHome(filepath.Join(m.deps.Cfg.ReposRoot, r.Name)))
			if !p.focusBtn && i == p.cursor {
				cursor = cursorStyle.Render("▶")
				line = fmt.Sprintf("%s  %s %s",
					cursor,
					cursorStyle.Render(check+" "+padRight(r.Name, 32)),
					dimStyle.Render("→ "+abbrevHome(filepath.Join(m.deps.Cfg.ReposRoot, r.Name))))
			}
			b.WriteString("  " + line + "\n")
		}
		b.WriteString("\n")
	}

	// Plan preview (when not in the middle of cloning).
	if !p.creating {
		// "Will be cloned" subsection: lists the checked missing
		// clones with the target work dir so the user can see
		// "before the workspace happens, these clones will land
		// here". Hidden when there's nothing to clone.
		toClone := p.checkedClones()
		if len(toClone) > 0 {
			b.WriteString("  " + planHeaderStyle.Render(
				fmt.Sprintf("The following repos will be cloned into %s:",
					abbrevHome(m.deps.Cfg.ReposRoot))) + "\n")
			for _, r := range toClone {
				b.WriteString(fmt.Sprintf("      • %s\n", r.Name))
			}
			b.WriteString("\n")
		}

		b.WriteString("  " + planHeaderStyle.Render("The following will be created:") + "\n")
		b.WriteString(fmt.Sprintf("    workspace dir: %s\n", abbrevHome(p.workspace)))
		b.WriteString(fmt.Sprintf("    branch:        %s\n", p.branch))
		final := p.finalSelection()
		b.WriteString(fmt.Sprintf("    worktrees:     %d\n", len(final)))
		for _, r := range final {
			mode := "create branch"
			if p.branchExist[r.Name] {
				mode = "checkout existing"
			}
			b.WriteString(fmt.Sprintf("      • %s (%s)\n", r.Name, mode))
		}
		b.WriteString("\n")
		// Create button.
		btn := createBtnIdle.Render("[ Create workspace ]")
		if p.focusBtn {
			btn = createBtnStyle.Render("Create workspace")
		}
		b.WriteString("  " + btn + "\n")
	} else {
		// Clone log.
		b.WriteString("  " + sectionStyle.Render("Cloning…") + "\n")
		for _, name := range p.cloneOrd {
			cs := p.clones[name]
			switch {
			case cs.done && cs.err == nil:
				b.WriteString("    " + selectedTagStyle.Render("✓") +
					fmt.Sprintf(" cloned %s → %s\n", name, abbrevHome(cs.targetDir)))
			case cs.done && cs.err != nil:
				b.WriteString("    " + errStyle.Render("✗") +
					fmt.Sprintf(" clone failed for %s: %s — skipping\n", name, cs.err.Error()))
			default:
				elapsed := 0
				if !cs.started.IsZero() {
					elapsed = int(time.Since(cs.started).Seconds())
				}
				b.WriteString("    " +
					fmt.Sprintf("cloning %s → %s… %ds\n", name, abbrevHome(cs.targetDir), elapsed))
			}
		}
	}
	return indent(b.String(), 2)
}

// checkedClones returns the missing-clone repos the user has left
// checked in the "Missing clones" section — i.e. the repos that will
// actually be cloned when the user hits Create. Used by the plan
// preview to surface the clone destination up front.
func (p *planPage) checkedClones() []catalog.Repo {
	out := make([]catalog.Repo, 0, len(p.toClone))
	for _, r := range p.toClone {
		if p.cloneInclude[r.Name] {
			out = append(out, r)
		}
	}
	return out
}

// finalSelection returns the repos that will end up in the workspace
// given current toggles — used by the plan preview.
func (p *planPage) finalSelection() []catalog.Repo {
	out := make([]catalog.Repo, 0, len(p.allRepos))
	for _, r := range p.allRepos {
		if r.LocalPath != "" || p.cloneInclude[r.Name] {
			out = append(out, r)
		}
	}
	return out
}

