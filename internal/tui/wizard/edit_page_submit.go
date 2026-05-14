package wizard

import (
	"bytes"
	"context"
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

// editSubmitPage is the third page of the edit wizard: build an
// AddPlan from m.additions + m.selectedWorkspace, show the user what
// will be cloned and what worktrees will be attached, then run the
// clones in-page. workspace.Add itself runs AFTER the wizard exits
// (in cmd/thicket/edit.go), mirroring start's post-wizard
// workspace.Create.
type editSubmitPage struct {
	// Built lazily on first activation.
	built      bool
	buildErr   error
	builtForID string // slug + len(additions) — re-init when this changes

	branch    string
	workspace string
	allRepos  []catalog.Repo
	toClone   []catalog.Repo

	// Per-name decisions populated by the plan build.
	branchExist map[string]bool

	cloneInclude map[string]bool
	cursor       int
	focusBtn     bool

	creating  bool
	clones    map[string]*cloneState
	cloneOrd  []string
	startedAt time.Time
}

func newEditSubmitPage() *editSubmitPage {
	return &editSubmitPage{
		cloneInclude: make(map[string]bool),
	}
}

func (p *editSubmitPage) Title() string { return "Submit" }

func (p *editSubmitPage) Hints() string {
	if p.creating {
		return ""
	}
	if len(p.toClone) > 0 {
		return "↑/↓ cursor · space toggles clone · enter adds"
	}
	return "enter adds"
}

func (p *editSubmitPage) Complete() bool { return p.built && p.buildErr == nil }

func (p *editSubmitPage) locked() bool { return p.creating }

func (p *editSubmitPage) initCmd(m *Model) tea.Cmd {
	key := ""
	if m.selectedWorkspace != nil {
		key = m.selectedWorkspace.Slug
	}
	key += fmt.Sprintf("|%d", len(m.additions))
	if p.builtForID != key {
		p.cloneInclude = make(map[string]bool)
		p.builtForID = key
	}
	p.built = false
	p.buildErr = nil
	return buildEditPlanCmd(m)
}

func buildEditPlanCmd(m *Model) tea.Cmd {
	return func() tea.Msg {
		ws := m.selectedWorkspace
		if ws == nil {
			return editPlanBuiltMsg{err: errors.New("no workspace selected")}
		}
		branch := ws.State.Branch
		// Probe BranchExists for additions that are already cloned;
		// repos that still need cloning get assumed-create.
		exist := make(map[string]bool, len(m.additions))
		for _, r := range m.additions {
			if r.LocalPath == "" {
				continue
			}
			ok, err := m.editDeps.Git.BranchExists(r.LocalPath, branch)
			if err != nil {
				return editPlanBuiltMsg{err: fmt.Errorf("check branch in %s: %w", r.Name, err)}
			}
			exist[r.Name] = ok
		}
		var toClone []catalog.Repo
		for _, r := range m.additions {
			if r.LocalPath == "" {
				toClone = append(toClone, r)
			}
		}
		// Build the AddPlan eagerly so the preview reflects final
		// state. Sources for uncloned repos point at the expected
		// post-clone target so the preview reads sensibly; the real
		// LocalPath lands after clone.
		newPlanRepos := make([]workspace.PlanRepo, 0, len(m.additions))
		for _, r := range m.additions {
			src := r.LocalPath
			if src == "" {
				src = filepath.Join(m.editDeps.Cfg.ReposRoot, r.Name)
			}
			wt := filepath.Join(ws.Path, r.Name)
			newPlanRepos = append(newPlanRepos, workspace.PlanRepo{
				Name:         r.Name,
				SourcePath:   src,
				WorktreePath: wt,
				BranchExists: exist[r.Name],
			})
		}
		// memory.Input is the FULL post-add repo set so the regen
		// produces the right table.
		memRepos := make([]memory.RepoEntry, 0, len(ws.State.Repos)+len(m.additions))
		for _, r := range ws.State.Repos {
			memRepos = append(memRepos, memory.RepoEntry{
				Name:         r.Name,
				Branch:       branch,
				WorktreePath: r.WorktreePath,
				// DefaultBranch is unknown at this point — left empty.
				// The fresh render will show empty under "Default
				// branch" for the pre-existing rows; not perfect but
				// not load-bearing.
			})
		}
		for _, r := range m.additions {
			memRepos = append(memRepos, memory.RepoEntry{
				Name:          r.Name,
				Branch:        branch,
				WorktreePath:  filepath.Join(ws.Path, r.Name),
				DefaultBranch: r.DefaultBranch,
			})
		}
		addPlan := workspace.AddPlan{
			WorkspaceDir: ws.Path,
			NewRepos:     newPlanRepos,
			Memory: memory.Input{
				TicketID:     ws.State.TicketID,
				Branch:       branch,
				WorkspaceDir: ws.Path,
				Repos:        memRepos,
				CreatedAt:    ws.State.CreatedAt,
				// Title / Body / URL / State / Owner are filled by
				// the caller (cmd/thicket/edit.go) after re-fetching
				// the ticket post-wizard.
			},
		}
		return editPlanBuiltMsg{addPlan: addPlan, toClone: toClone, branch: branch}
	}
}

func (p *editSubmitPage) Update(m *Model, msg tea.Msg) (Page, tea.Cmd) {
	switch v := msg.(type) {
	case editPlanBuiltMsg:
		if v.err != nil {
			p.buildErr = v.err
			return p, nil
		}
		p.built = true
		p.branch = v.branch
		p.workspace = v.addPlan.WorkspaceDir
		p.allRepos = append(p.allRepos[:0], m.additions...)
		p.toClone = v.toClone
		p.branchExist = make(map[string]bool, len(v.addPlan.NewRepos))
		for _, r := range v.addPlan.NewRepos {
			p.branchExist[r.Name] = r.BranchExists
		}
		// Stash the addPlan on the model so the post-wizard handler
		// can read it without rebuilding.
		m.editResult.AddPlan = v.addPlan
		for _, r := range p.toClone {
			if _, ok := p.cloneInclude[r.Name]; !ok {
				p.cloneInclude[r.Name] = true
			}
		}
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
			for i := range p.allRepos {
				if p.allRepos[i].Name == v.name {
					p.allRepos[i].LocalPath = v.localPath
					break
				}
			}
		}
		if p.allClonesDone() {
			return p, p.finalizeCmd(m)
		}
		return p, nil

	case tickMsg:
		return p, p.tickCmd()

	case tea.KeyMsg:
		if p.creating {
			return p, nil
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
			name := p.toClone[p.cursor].Name
			p.cloneInclude[name] = !p.cloneInclude[name]
			return p, nil
		}
	}
	return p, nil
}

func (p *editSubmitPage) allClonesDone() bool {
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

func (p *editSubmitPage) tickCmd() tea.Cmd {
	if !p.creating {
		return nil
	}
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (p *editSubmitPage) startCloneCmd(m *Model) tea.Cmd {
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
			m.editResult.Skipped = append(m.editResult.Skipped, SkipReport{
				Name:   r.Name,
				Reason: "deselected before add",
			})
		}
	}
	if len(final) == 0 {
		p.buildErr = errors.New("no repos to add — uncheck or pick differently")
		return nil
	}
	p.allRepos = final
	p.creating = true
	p.startedAt = time.Now()
	p.clones = make(map[string]*cloneState, len(pending))
	p.cloneOrd = p.cloneOrd[:0]
	if len(pending) == 0 {
		return p.finalizeCmd(m)
	}
	cmds := make([]tea.Cmd, 0, 2*len(pending))
	for _, r := range pending {
		target := filepath.Join(m.editDeps.Cfg.ReposRoot, r.Name)
		cs := &cloneState{name: r.Name, cloneURL: r.CloneURL, targetDir: target}
		p.clones[r.Name] = cs
		p.cloneOrd = append(p.cloneOrd, r.Name)
		started := func(name string) tea.Cmd {
			return func() tea.Msg { return cloneStartedMsg{name: name} }
		}(r.Name)
		clone := func(name, url, dir string) tea.Cmd {
			return func() tea.Msg {
				var buf bytes.Buffer
				err := m.editDeps.Git.Clone(url, dir, &buf, &buf)
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

func (p *editSubmitPage) finalizeCmd(m *Model) tea.Cmd {
	return func() tea.Msg {
		// Filter failed clones (proceed-without-failed-repo).
		var kept []catalog.Repo
		for _, r := range p.allRepos {
			if cs, ok := p.clones[r.Name]; ok && cs.err != nil {
				m.editResult.Skipped = append(m.editResult.Skipped, SkipReport{
					Name:   r.Name,
					Reason: cs.err.Error(),
				})
				continue
			}
			kept = append(kept, r)
		}
		if len(kept) == 0 {
			return editDoneMsg{err: errors.New("every add failed — nothing to attach")}
		}
		// Re-probe BranchExists for newly-cloned repos.
		newPlanRepos := make([]workspace.PlanRepo, 0, len(kept))
		for _, r := range kept {
			src := r.LocalPath
			if src == "" {
				src = filepath.Join(m.editDeps.Cfg.ReposRoot, r.Name)
			}
			exists := p.branchExist[r.Name]
			if cs, ok := p.clones[r.Name]; ok && cs.err == nil {
				e, err := m.editDeps.Git.BranchExists(src, p.branch)
				if err != nil {
					return editDoneMsg{err: fmt.Errorf("check branch in %s after clone: %w", r.Name, err)}
				}
				exists = e
			}
			wt := filepath.Join(p.workspace, r.Name)
			newPlanRepos = append(newPlanRepos, workspace.PlanRepo{
				Name:         r.Name,
				SourcePath:   src,
				WorktreePath: wt,
				BranchExists: exists,
			})
		}
		// Update memRepos to match the kept set + the existing workspace repos.
		ws := m.selectedWorkspace
		memRepos := make([]memory.RepoEntry, 0, len(ws.State.Repos)+len(kept))
		for _, r := range ws.State.Repos {
			memRepos = append(memRepos, memory.RepoEntry{
				Name:         r.Name,
				Branch:       p.branch,
				WorktreePath: r.WorktreePath,
			})
		}
		for _, r := range kept {
			memRepos = append(memRepos, memory.RepoEntry{
				Name:          r.Name,
				Branch:        p.branch,
				WorktreePath:  filepath.Join(p.workspace, r.Name),
				DefaultBranch: r.DefaultBranch,
			})
		}
		// Carry forward the ticket-context fields from the partially-
		// built plan (filled by the caller after the wizard exits).
		addPlan := m.editResult.AddPlan
		addPlan.NewRepos = newPlanRepos
		addPlan.Memory.Repos = memRepos
		_ = context.Background // imported elsewhere; placeholder keeps import set stable
		return editDoneMsg{result: EditResult{
			AddPlan: addPlan,
			Skipped: m.editResult.Skipped,
		}}
	}
}

func (p *editSubmitPage) View(m *Model) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Review and add to workspace"))
	b.WriteString("\n\n")

	if m.selectedWorkspace != nil {
		b.WriteString(renderEditWorkspaceSummary(*m.selectedWorkspace))
		b.WriteString("\n")
	}

	if !p.built {
		if p.buildErr != nil {
			b.WriteString("  " + errStyle.Render(fmtErr(p.buildErr)) + "\n")
			return indent(b.String(), 2)
		}
		b.WriteString("  " + hintStyle.Render("building plan…") + "\n")
		return indent(b.String(), 2)
	}

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
				abbrevHome(filepath.Join(m.editDeps.Cfg.ReposRoot, r.Name)))
			if !p.focusBtn && i == p.cursor {
				cursor = cursorStyle.Render("▶")
				line = fmt.Sprintf("%s  %s %s",
					cursor,
					cursorStyle.Render(check+" "+padRight(r.Name, 32)),
					dimStyle.Render("→ "+abbrevHome(filepath.Join(m.editDeps.Cfg.ReposRoot, r.Name))))
			}
			b.WriteString("  " + line + "\n")
		}
		b.WriteString("\n")
	}

	if !p.creating {
		toClone := p.checkedClones()
		if len(toClone) > 0 {
			b.WriteString("  " + planHeaderStyle.Render(
				fmt.Sprintf("The following repos will be cloned into %s:",
					abbrevHome(m.editDeps.Cfg.ReposRoot))) + "\n")
			for _, r := range toClone {
				b.WriteString(fmt.Sprintf("      • %s\n", r.Name))
			}
			b.WriteString("\n")
		}

		b.WriteString("  " + planHeaderStyle.Render("The following will be attached:") + "\n")
		b.WriteString(fmt.Sprintf("    workspace: %s\n", abbrevHome(p.workspace)))
		b.WriteString(fmt.Sprintf("    branch:    %s\n", p.branch))
		final := p.finalSelection()
		b.WriteString(fmt.Sprintf("    new worktrees: %d\n", len(final)))
		for _, r := range final {
			mode := "create branch"
			if p.branchExist[r.Name] {
				mode = "checkout existing"
			}
			b.WriteString(fmt.Sprintf("      • %s (%s)\n", r.Name, mode))
		}
		b.WriteString("\n")
		btn := createBtnIdle.Render("[ Add to workspace ]")
		if p.focusBtn {
			btn = createBtnStyle.Render("Add to workspace")
		}
		b.WriteString("  " + btn + "\n")
	} else {
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

func (p *editSubmitPage) checkedClones() []catalog.Repo {
	out := make([]catalog.Repo, 0, len(p.toClone))
	for _, r := range p.toClone {
		if p.cloneInclude[r.Name] {
			out = append(out, r)
		}
	}
	return out
}

func (p *editSubmitPage) finalSelection() []catalog.Repo {
	out := make([]catalog.Repo, 0, len(p.allRepos))
	for _, r := range p.allRepos {
		if r.LocalPath != "" || p.cloneInclude[r.Name] {
			out = append(out, r)
		}
	}
	return out
}
