package wizard

import (
	"fmt"
	"os"
	"strings"

	"github.com/uribrecher/thicket/internal/detector"
	"github.com/uribrecher/thicket/internal/ticket"
)

// Helpers shared by every wizard's pages. Lives in the root `wizard`
// package so the start/edit/config sub-packages all see the same
// implementations and table-formatting / path-rendering / summary
// rendering stays consistent across flows.

// indent prepends `n` spaces to every non-empty line. Used by page
// bodies so they sit flush under the tab bar at a consistent inset.
// Empty lines stay empty (no trailing whitespace).
func indent(s string, n int) string {
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

// fmtErr renders an error for inline display in a page body.
func fmtErr(err error) string {
	return fmt.Sprintf("error: %s", err.Error())
}

// padRight pads `s` with spaces on the right so the displayed width
// equals `n` runes. Strings already at or above `n` are returned
// unchanged.
func padRight(s string, n int) string {
	r := []rune(s)
	if len(r) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(r))
}

// truncate clips `s` to at most `n` runes, replacing the last
// character with `…` so the result reads as deliberately truncated.
// Returns the empty string when n < 1.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n < 1 {
		return ""
	}
	return string(r[:n-1]) + "…"
}

// abbrevHome collapses an absolute path under $HOME to a leading `~`.
// Paths outside $HOME are returned unchanged.
func abbrevHome(path string) string {
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

// firstNonEmptyLines returns up to `n` trimmed non-empty lines from
// `s`. Used as the fallback when an LLM summary isn't available.
func firstNonEmptyLines(s string, n int) []string {
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

// renderTicketSummary draws a short header for the picked ticket:
// "<id> — <title>" plus an up-to-SummaryLines summary, requester, and
// the first 3 labels. `summary` is the LLM-generated summary when
// available; nil falls back to the first non-empty lines of the
// description so the panel always shows context.
//
// Returns "" when there is no ticket to summarize so callers can skip
// the surrounding padding.
func renderTicketSummary(tk ticket.Ticket, summary []string) string {
	if tk.SourceID == "" && tk.Title == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString(warnStyle.Render(fmt.Sprintf("%s — %s", tk.SourceID, tk.Title)))
	b.WriteString("\n")
	lines := summary
	if len(lines) == 0 {
		lines = firstNonEmptyLines(tk.Body, detector.SummaryLines)
	}
	if len(lines) > detector.SummaryLines {
		lines = lines[:detector.SummaryLines]
	}
	for _, line := range lines {
		b.WriteString("  " + line + "\n")
	}
	if tk.Requester != "" {
		b.WriteString("  " + hintStyle.Render("requester: "+tk.Requester) + "\n")
	}
	if len(tk.Labels) > 0 {
		shown := tk.Labels
		if len(shown) > 3 {
			shown = shown[:3]
		}
		b.WriteString("  " + hintStyle.Render("labels: "+strings.Join(shown, ", ")) + "\n")
	}
	return b.String()
}
