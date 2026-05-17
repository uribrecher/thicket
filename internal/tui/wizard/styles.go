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
	// bright pink background) — matches the CursorStyle pink used
	// for selected list rows so "this is current" reads the same
	// across tabs and tables. Foreground is hex #000000 (not palette
	// color "0") because many terminal themes render ANSI black as
	// a dim gray that loses contrast on a light-pink ground,
	// especially with the bold attribute.
	// Tab styles are tab-only — don't reuse them as row styles, the
	// padding will throw off table columns (see DimStyle for the
	// row-cell alternative).
	ActiveTabStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#000000")).Background(lipgloss.Color("213")).Padding(0, 1)
	CompletedTabStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("76")).Padding(0, 1)
	PendingTabStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Padding(0, 1)
	TabSepStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	HintStyle         = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("245"))
	TitleStyle        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	SectionStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245"))
	CursorStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Bold(true)
	HighlightStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Bold(true)
	// CommittedRowStyle marks a row whose value drives downstream
	// pages — e.g. the previously-picked ticket on the Ticket page
	// after the user has been to Repos and come back. Green to match
	// the existing "completion" semantics (CompletedTabStyle,
	// SelectedTagStyle); not bold so the pink cursor still wins
	// when it lands on the same row.
	CommittedRowStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("76"))
	SelectedTagStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("76"))
	RelevanceTagStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("99"))
	DimStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	WarnStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	ErrStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	PlanHeaderStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	CreateBtnStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#000000")).Background(lipgloss.Color("213")).Padding(0, 2)
	CreateBtnIdle     = lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Padding(0, 2)
)
