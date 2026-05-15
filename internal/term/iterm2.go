// Package term writes terminal-specific OSC escape sequences so a
// long-running workspace session is visually distinguishable in the
// terminal's tab strip. The only terminal currently supported is
// iTerm2; callers MUST gate Write* calls behind IsITerm2 themselves.
// The helpers do not re-check, so unit tests can verify the exact
// emitted bytes without env-mocking.
//
// Why iTerm2 only: tab color and per-tab badge are non-standard
// extensions, and iTerm2's documented escape codes are stable and
// widely deployed on macOS. Other terminals either ignore the
// escapes (harmless on most modern emulators) or render gibberish
// (older ones, raw pipes) — hence the caller-side gate.
package term

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// IsITerm2 reports whether the current process is running inside
// iTerm2. Checks `$LC_TERMINAL` (set by iTerm2 and survives SSH
// hops) first, then `$TERM_PROGRAM` (the more common local marker).
func IsITerm2() bool {
	if os.Getenv("LC_TERMINAL") == "iTerm2" {
		return true
	}
	return os.Getenv("TERM_PROGRAM") == "iTerm.app"
}

// WriteTabTitle sets the current tab's title via the standard OSC 0
// escape. iTerm2, plus most other terminals, honor this. Silent
// no-op on a nil writer or empty title.
func WriteTabTitle(w io.Writer, title string) {
	if w == nil || title == "" {
		return
	}
	fmt.Fprintf(w, "\x1b]0;%s\x07", title)
}

// WriteBadge sets the iTerm2 tab badge — a small label drawn on top
// of the terminal contents, useful for spotting the right tab among
// many. Uses the documented iTerm2 proprietary escape
// `OSC 1337 ; SetBadgeFormat = <base64> ST`. The badge text supports
// iTerm2's variable expansion; we base64-encode the literal so any
// special characters in the nickname (emoji, $, %) round-trip cleanly.
// Silent no-op on empty input.
func WriteBadge(w io.Writer, text string) {
	if w == nil || text == "" {
		return
	}
	enc := base64.StdEncoding.EncodeToString([]byte(text))
	fmt.Fprintf(w, "\x1b]1337;SetBadgeFormat=%s\x1b\\", enc)
}

// WriteTabColor sets the current tab's background color in iTerm2
// via three component escapes (red / green / blue). Accepts hex in
// `#RRGGBB` or `RRGGBB` form; invalid input is a silent no-op so
// callers can pass through unsanitized values without guarding.
func WriteTabColor(w io.Writer, hex string) {
	if w == nil {
		return
	}
	r, g, b, ok := ParseHexColor(hex)
	if !ok {
		return
	}
	// Component-by-component escape, the form documented for iTerm2
	// since at least 3.0. `1` = set, `bg` = which slot, `<comp>` +
	// `brightness` + value.
	fmt.Fprintf(w, "\x1b]6;1;bg;red;brightness;%d\x07", r)
	fmt.Fprintf(w, "\x1b]6;1;bg;green;brightness;%d\x07", g)
	fmt.Fprintf(w, "\x1b]6;1;bg;blue;brightness;%d\x07", b)
}

// ParseHexColor accepts `#RRGGBB` or `RRGGBB` (case-insensitive) and
// returns the three component bytes. Anything else (rgb()-style
// notation, color names, short `#RGB`, garbage) returns ok=false so
// the caller can fall back to leaving the tab uncolored.
func ParseHexColor(hex string) (r, g, b int, ok bool) {
	s := strings.TrimSpace(hex)
	s = strings.TrimPrefix(s, "#")
	if len(s) != 6 {
		return 0, 0, 0, false
	}
	rr, err := strconv.ParseUint(s[0:2], 16, 8)
	if err != nil {
		return 0, 0, 0, false
	}
	gg, err := strconv.ParseUint(s[2:4], 16, 8)
	if err != nil {
		return 0, 0, 0, false
	}
	bb, err := strconv.ParseUint(s[4:6], 16, 8)
	if err != nil {
		return 0, 0, 0, false
	}
	return int(rr), int(gg), int(bb), true
}

// SanitizeHexColor normalizes a color string to `#RRGGBB` uppercase,
// or returns "" if the input doesn't parse as a 6-digit hex color.
// Used at the persistence boundary in workspace.writeState so what
// lands in state.json is always canonical.
func SanitizeHexColor(hex string) string {
	r, g, b, ok := ParseHexColor(hex)
	if !ok {
		return ""
	}
	return fmt.Sprintf("#%02X%02X%02X", r, g, b)
}
