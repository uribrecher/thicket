package wizard

import "github.com/charmbracelet/lipgloss"

// Color palette is shared with the rest of the thicket UI:
//   - 214 (yellow/orange) is the project's accent — matches the `plan:`
//     header in start.go and the "catalog:" label.
//   - 76 (green) signals completion.
//   - 241/245 (gray) is for dim / footer text.
//   - 99 (magenta) and 213 (bright magenta) come from the existing
//     picker.go and are reused here so colors don't drift across the app.
var (
	activeTabStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	completedTabStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("76"))
	pendingTabStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	tabSepStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	underlineStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	hintStyle         = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("245"))
	titleStyle        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	sectionStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245"))
	cursorStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Bold(true)
	highlightStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Bold(true)
	selectedTagStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("76"))
	llmTagStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("99"))
	dimStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	warnStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	errStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	planHeaderStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	createBtnStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("214")).Padding(0, 2)
	createBtnIdle     = lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Padding(0, 2)
)
