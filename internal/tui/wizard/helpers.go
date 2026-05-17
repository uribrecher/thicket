package wizard

import (
	"fmt"
	"os"
	"strings"

	"github.com/uribrecher/thicket/internal/detector"
	"github.com/uribrecher/thicket/internal/ticket"
	"github.com/uribrecher/thicket/internal/tui"
)

// Helpers shared by every wizard's pages. Lives in the root `wizard`
// package so the start/edit/config sub-packages all see the same
// implementations and table-formatting / path-rendering / summary
// rendering stays consistent across flows.

// Indent prepends `n` spaces to every non-empty line. Used by page
// bodies so they sit flush under the tab bar at a consistent inset.
// Empty lines stay empty (no trailing whitespace).
func Indent(s string, n int) string {
	if s == "" {
		return s
	}
	pad := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if ln == "" {
			continue
		}
		lines[i] = pad + ln
	}
	return strings.Join(lines, "\n")
}

// FmtErr renders an error for inline display in a page body.
func FmtErr(err error) string {
	return fmt.Sprintf("error: %s", err.Error())
}

// PadRight right-pads `s` with spaces so the result occupies at least
// `n` visible terminal cells. Strings whose visible width already
// equals or exceeds `n` are returned unchanged — pair with Truncate
// when callers need a hard upper bound. Delegates to tui.PadRight so
// wizard pickers stay aligned when rows carry emoji or other wide
// runes — a rune-count pad under-fills the column by one cell per
// wide rune and shifts neighbouring columns right (the bug that
// misaligned `thicket edit`'s workspace picker once nicknames
// carried emoji).
func PadRight(s string, n int) string {
	return tui.PadRight(s, n)
}

// Truncate caps `s` at `n` visible terminal cells, appending `…` when
// truncation actually happens. Returns the empty string when `n < 1`.
// Delegates to tui.Truncate so the width budget matches PadRight —
// using a rune-count truncate would leave wide-rune strings
// overflowing the column PadRight just sized.
func Truncate(s string, n int) string {
	return tui.Truncate(s, n)
}

// AbbrevHome collapses an absolute path under $HOME to a leading `~`.
// Paths outside $HOME are returned unchanged.
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

// FirstNonEmptyLines returns up to `n` trimmed non-empty lines from
// `s`. Used as the fallback when an LLM summary isn't available.
func FirstNonEmptyLines(s string, n int) []string {
	var out []string
	for _, raw := range strings.Split(s, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		out = append(out, line)
		if len(out) == n {
			break
		}
	}
	return out
}

// RenderTicketSummary draws a short header for the picked ticket:
// "<id> — <title>" plus an up-to-SummaryLines summary, requester, and
// the first 3 labels. `summary` is the LLM-generated summary when
// available; nil falls back to the first non-empty lines of the
// description so the panel always shows context.
//
// Returns "" when there is no ticket to summarize so callers can skip
// the surrounding padding.
func RenderTicketSummary(tk ticket.Ticket, summary []string) string {
	if tk.SourceID == "" && tk.Title == "" {
		return ""
	}
	var b strings.Builder
	// Hyperlink wraps the styled "<id> — <title>" header so ⌘-click
	// on the visible label opens the ticket URL in terminals that
	// speak OSC 8; others see the label unchanged.
	b.WriteString(tui.Hyperlink(tk.URL,
		WarnStyle.Render(fmt.Sprintf("%s — %s", tk.SourceID, tk.Title))))
	b.WriteString("\n")
	lines := summary
	if len(lines) == 0 {
		lines = FirstNonEmptyLines(tk.Body, detector.SummaryLines)
	}
	if len(lines) > detector.SummaryLines {
		lines = lines[:detector.SummaryLines]
	}
	for _, line := range lines {
		b.WriteString("  " + line + "\n")
	}
	if tk.Requester != "" {
		b.WriteString("  " + HintStyle.Render("requester: "+tk.Requester) + "\n")
	}
	if len(tk.Labels) > 0 {
		shown := tk.Labels
		if len(shown) > 3 {
			shown = shown[:3]
		}
		b.WriteString("  " + HintStyle.Render("labels: "+strings.Join(shown, ", ")) + "\n")
	}
	return b.String()
}
