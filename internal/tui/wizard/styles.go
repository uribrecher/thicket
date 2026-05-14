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
	// Active tab: high-contrast filled cell (true-black text on
	// bright pink background) — matches the cursorStyle pink used
	// for selected list rows so "this is current" reads the same
	// across tabs and tables. Foreground is hex #000000 (not palette
	// color "0") because many terminal themes render ANSI black as
	// a dim gray that loses contrast on a light-pink ground,
	// especially with the bold attribute.
	// Tab styles are tab-only — don't reuse them as row styles, the
	// padding will throw off table columns (see dimStyle for the
	// row-cell alternative).
	activeTabStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#000000")).Background(lipgloss.Color("213")).Padding(0, 1)
	completedTabStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("76")).Padding(0, 1)
	pendingTabStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Padding(0, 1)
	tabSepStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	hintStyle         = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("245"))
	titleStyle        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	sectionStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245"))
	cursorStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Bold(true)
	highlightStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Bold(true)
	selectedTagStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("76"))
	relevanceTagStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("99"))
	dimStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	warnStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	errStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	planHeaderStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	createBtnStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#000000")).Background(lipgloss.Color("213")).Padding(0, 2)
	createBtnIdle     = lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Padding(0, 2)
)
