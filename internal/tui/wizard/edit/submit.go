package edit

import (
	"github.com/uribrecher/thicket/internal/tui/wizard"

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

// submitPage is the third page of the edit wizard: build an
// AddPlan from m.Additions + m.SelectedWorkspace, show the user what
// will be cloned and what worktrees will be attached, then run the
// clones in-page. workspace.Add itself runs AFTER the wizard exits
// (in cmd/thicket/edit.go), mirroring start's post-wizard
// workspace.Create.
type submitPage struct {
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
	clones    map[string]*wizard.CloneState
	cloneOrd  []string
	startedAt time.Time
}

func newSubmitPage() *submitPage {
	return &submitPage{
		cloneInclude: make(map[string]bool),
	}
}

func (p *submitPage) Title() string { return "Submit" }

func (p *submitPage) Hints() string {
	if p.creating {
		return ""
	}
	if len(p.toClone) > 0 {
		return "↑/↓ cursor · space toggles clone · enter adds"
	}
	return "enter adds"
}

func (p *submitPage) Complete() bool { return p.built && p.buildErr == nil }

func (p *submitPage) Locked() bool { return p.creating }

func (p *submitPage) InitCmd(m *wizard.Model) tea.Cmd {
	key := ""
	if m.SelectedWorkspace != nil {
		key = m.SelectedWorkspace.Slug
	}
	key += fmt.Sprintf("|%d", len(m.Additions))
	if p.builtForID != key {
		p.cloneInclude = make(map[string]bool)
		p.builtForID = key
	}
	p.built = false
	p.buildErr = nil
	return buildPlanCmd(m)
}

func buildPlanCmd(m *wizard.Model) tea.Cmd {
	return func() tea.Msg {
		ws := m.SelectedWorkspace
		if ws == nil {
			return wizard.EditPlanBuiltMsg{Err: errors.New("no workspace selected")}
		}
		branch := ws.State.Branch
		// Probe BranchExists for additions that are already cloned;
		// repos that still need cloning get assumed-create.
		exist := make(map[string]bool, len(m.Additions))
		for _, r := range m.Additions {
			if r.LocalPath == "" {
				continue
			}
			ok, err := m.EditDeps.Git.BranchExists(r.LocalPath, branch)
			if err != nil {
				return wizard.EditPlanBuiltMsg{Err: fmt.Errorf("check branch in %s: %w", r.Name, err)}
			}
			exist[r.Name] = ok
		}
		var toClone []catalog.Repo
		for _, r := range m.Additions {
			if r.LocalPath == "" {
				toClone = append(toClone, r)
			}
		}
		// Build the AddPlan eagerly so the preview reflects final
		// state. Sources for uncloned repos point at the expected
		// post-clone target so the preview reads sensibly; the real
		// LocalPath lands after clone.
		newPlanRepos := make([]workspace.PlanRepo, 0, len(m.Additions))
		for _, r := range m.Additions {
			src := r.LocalPath
			if src == "" {
				src = filepath.Join(m.EditDeps.Cfg.ReposRoot, r.Name)
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
		memRepos := make([]memory.RepoEntry, 0, len(ws.State.Repos)+len(m.Additions))
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
		for _, r := range m.Additions {
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
		return wizard.EditPlanBuiltMsg{AddPlan: addPlan, ToClone: toClone, Branch: branch}
	}
}

func (p *submitPage) Update(m *wizard.Model, msg tea.Msg) (wizard.Page, tea.Cmd) {
	switch v := msg.(type) {
	case wizard.EditPlanBuiltMsg:
		if v.Err != nil {
			p.buildErr = v.Err
			return p, nil
		}
		p.built = true
		p.branch = v.Branch
		p.workspace = v.AddPlan.WorkspaceDir
		p.allRepos = append(p.allRepos[:0], m.Additions...)
		p.toClone = v.ToClone
		p.branchExist = make(map[string]bool, len(v.AddPlan.NewRepos))
		for _, r := range v.AddPlan.NewRepos {
			p.branchExist[r.Name] = r.BranchExists
		}
		// Stash the addPlan on the model so the post-wizard handler
		// can read it without rebuilding.
		m.EditResult.AddPlan = v.AddPlan
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

	case wizard.CloneStartedMsg:
		if cs, ok := p.clones[v.Name]; ok {
			cs.Started = time.Now()
		}
		return p, p.tickCmd()

	case wizard.CloneDoneMsg:
		cs, ok := p.clones[v.Name]
		if !ok {
			return p, nil
		}
		cs.Done = true
		cs.Err = v.Err
		if v.Err == nil {
			for i := range p.allRepos {
				if p.allRepos[i].Name == v.Name {
					p.allRepos[i].LocalPath = v.LocalPath
					break
				}
			}
		}
		if p.allClonesDone() {
			return p, p.finalizeCmd(m)
		}
		return p, nil

	case wizard.TickMsg:
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

func (p *submitPage) allClonesDone() bool {
	if len(p.clones) == 0 {
		return false
	}
	for _, cs := range p.clones {
		if !cs.Done {
			return false
		}
	}
	return true
}

func (p *submitPage) tickCmd() tea.Cmd {
	if !p.creating {
		return nil
	}
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return wizard.TickMsg(t) })
}

func (p *submitPage) startCloneCmd(m *wizard.Model) tea.Cmd {
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
			m.EditResult.Skipped = append(m.EditResult.Skipped, wizard.SkipReport{
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
	p.clones = make(map[string]*wizard.CloneState, len(pending))
	p.cloneOrd = p.cloneOrd[:0]
	if len(pending) == 0 {
		return p.finalizeCmd(m)
	}
	cmds := make([]tea.Cmd, 0, 2*len(pending))
	for _, r := range pending {
		target := filepath.Join(m.EditDeps.Cfg.ReposRoot, r.Name)
		cs := &wizard.CloneState{Name: r.Name, CloneURL: r.CloneURL, TargetDir: target}
		p.clones[r.Name] = cs
		p.cloneOrd = append(p.cloneOrd, r.Name)
		started := func(name string) tea.Cmd {
			return func() tea.Msg { return wizard.CloneStartedMsg{Name: name} }
		}(r.Name)
		clone := func(name, url, dir string) tea.Cmd {
			return func() tea.Msg {
				var buf bytes.Buffer
				err := m.EditDeps.Git.Clone(url, dir, &buf, &buf)
				out := strings.TrimSpace(buf.String())
				if err != nil {
					return wizard.CloneDoneMsg{Name: name, Err: fmt.Errorf("%s: %w (git output: %s)", name, err, wizard.Truncate(out, 200))}
				}
				return wizard.CloneDoneMsg{Name: name, LocalPath: dir}
			}
		}(r.Name, r.CloneURL, target)
		cmds = append(cmds, started, clone)
	}
	cmds = append(cmds, p.tickCmd())
	return tea.Batch(cmds...)
}

func (p *submitPage) finalizeCmd(m *wizard.Model) tea.Cmd {
	return func() tea.Msg {
		// Filter failed clones (proceed-without-failed-repo).
		var kept []catalog.Repo
		for _, r := range p.allRepos {
			if cs, ok := p.clones[r.Name]; ok && cs.Err != nil {
				m.EditResult.Skipped = append(m.EditResult.Skipped, wizard.SkipReport{
					Name:   r.Name,
					Reason: cs.Err.Error(),
				})
				continue
			}
			kept = append(kept, r)
		}
		if len(kept) == 0 {
			return wizard.EditDoneMsg{Err: errors.New("every add failed — nothing to attach")}
		}
		// Re-probe BranchExists for newly-cloned repos.
		newPlanRepos := make([]workspace.PlanRepo, 0, len(kept))
		for _, r := range kept {
			src := r.LocalPath
			if src == "" {
				src = filepath.Join(m.EditDeps.Cfg.ReposRoot, r.Name)
			}
			exists := p.branchExist[r.Name]
			if cs, ok := p.clones[r.Name]; ok && cs.Err == nil {
				e, err := m.EditDeps.Git.BranchExists(src, p.branch)
				if err != nil {
					return wizard.EditDoneMsg{Err: fmt.Errorf("check branch in %s after clone: %w", r.Name, err)}
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
		ws := m.SelectedWorkspace
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
		addPlan := m.EditResult.AddPlan
		addPlan.NewRepos = newPlanRepos
		addPlan.Memory.Repos = memRepos
		_ = context.Background // imported elsewhere; placeholder keeps import set stable
		return wizard.EditDoneMsg{Result: wizard.EditResult{
			AddPlan: addPlan,
			Skipped: m.EditResult.Skipped,
		}}
	}
}

func (p *submitPage) View(m *wizard.Model) string {
	var b strings.Builder
	b.WriteString(wizard.TitleStyle.Render("Review and add to workspace"))
	b.WriteString("\n\n")

	if m.SelectedWorkspace != nil {
		b.WriteString(renderWorkspaceSummary(*m.SelectedWorkspace))
		b.WriteString("\n")
	}

	if !p.built {
		if p.buildErr != nil {
			b.WriteString("  " + wizard.ErrStyle.Render(wizard.FmtErr(p.buildErr)) + "\n")
			return wizard.Indent(b.String(), 2)
		}
		b.WriteString("  " + wizard.HintStyle.Render("building plan…") + "\n")
		return wizard.Indent(b.String(), 2)
	}

	if len(p.toClone) > 0 && !p.creating {
		b.WriteString("  " + wizard.SectionStyle.Render("Missing clones") + "\n")
		for i, r := range p.toClone {
			check := "[ ]"
			if p.cloneInclude[r.Name] {
				check = "[x]"
			}
			cursor := " "
			line := fmt.Sprintf("%s  %s %-32s → %s",
				cursor, check, r.Name,
				wizard.AbbrevHome(filepath.Join(m.EditDeps.Cfg.ReposRoot, r.Name)))
			if !p.focusBtn && i == p.cursor {
				cursor = wizard.CursorStyle.Render("▶")
				line = fmt.Sprintf("%s  %s %s",
					cursor,
					wizard.CursorStyle.Render(check+" "+wizard.PadRight(r.Name, 32)),
					wizard.DimStyle.Render("→ "+wizard.AbbrevHome(filepath.Join(m.EditDeps.Cfg.ReposRoot, r.Name))))
			}
			b.WriteString("  " + line + "\n")
		}
		b.WriteString("\n")
	}

	if !p.creating {
		toClone := p.checkedClones()
		if len(toClone) > 0 {
			b.WriteString("  " + wizard.PlanHeaderStyle.Render(
				fmt.Sprintf("The following repos will be cloned into %s:",
					wizard.AbbrevHome(m.EditDeps.Cfg.ReposRoot))) + "\n")
			for _, r := range toClone {
				b.WriteString(fmt.Sprintf("      • %s\n", r.Name))
			}
			b.WriteString("\n")
		}

		b.WriteString("  " + wizard.PlanHeaderStyle.Render("The following will be attached:") + "\n")
		b.WriteString(fmt.Sprintf("    workspace: %s\n", wizard.AbbrevHome(p.workspace)))
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
		btn := wizard.CreateBtnIdle.Render("[ Add to workspace ]")
		if p.focusBtn {
			btn = wizard.CreateBtnStyle.Render("Add to workspace")
		}
		b.WriteString("  " + btn + "\n")
	} else {
		b.WriteString("  " + wizard.SectionStyle.Render("Cloning…") + "\n")
		for _, name := range p.cloneOrd {
			cs := p.clones[name]
			switch {
			case cs.Done && cs.Err == nil:
				b.WriteString("    " + wizard.SelectedTagStyle.Render("✓") +
					fmt.Sprintf(" cloned %s → %s\n", name, wizard.AbbrevHome(cs.TargetDir)))
			case cs.Done && cs.Err != nil:
				b.WriteString("    " + wizard.ErrStyle.Render("✗") +
					fmt.Sprintf(" clone failed for %s: %s — skipping\n", name, cs.Err.Error()))
			default:
				elapsed := 0
				if !cs.Started.IsZero() {
					elapsed = int(time.Since(cs.Started).Seconds())
				}
				b.WriteString("    " +
					fmt.Sprintf("cloning %s → %s… %ds\n", name, wizard.AbbrevHome(cs.TargetDir), elapsed))
			}
		}
	}
	return wizard.Indent(b.String(), 2)
}

func (p *submitPage) checkedClones() []catalog.Repo {
	out := make([]catalog.Repo, 0, len(p.toClone))
	for _, r := range p.toClone {
		if p.cloneInclude[r.Name] {
			out = append(out, r)
		}
	}
	return out
}

func (p *submitPage) finalSelection() []catalog.Repo {
	out := make([]catalog.Repo, 0, len(p.allRepos))
	for _, r := range p.allRepos {
		if r.LocalPath != "" || p.cloneInclude[r.Name] {
			out = append(out, r)
		}
	}
	return out
}
