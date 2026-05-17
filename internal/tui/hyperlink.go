package tui

import (
	"io"
	"os"

	"golang.org/x/term"
)

// Hyperlink wraps label in an OSC 8 hyperlink pointing at url so that
// terminals which support the sequence (iTerm2, WezTerm, kitty, Ghostty,
// modern Terminal.app, VS Code's integrated terminal, GNOME Terminal,
// Windows Terminal) render label as ⌘-clickable / Ctrl-clickable text
// that opens url. Terminals that don't recognize OSC 8 strip the
// escape and show label unchanged.
//
// Width math is the caller's responsibility — wrap *after* truncation
// and padding, because go-runewidth treats the OSC escape bytes as
// printable runes and would otherwise mis-size the column. Trailing
// padding spaces inside the hyperlink are fine; terminals just extend
// the clickable region across them, which matches the surrounding
// table cell.
//
// An empty url returns label unchanged — convenient when callers
// don't always have a URL handy (e.g. legacy workspace manifests
// written before the URL field was persisted).
//
// Hyperlink refuses to emit when url carries a C0 control character
// (0x00–0x1f) or DEL (0x7f). The OSC sequence is delimited by ESC \
// (ST), so an unsanitized url containing ESC could terminate the
// hyperlink early and let a hand-edited CLAUDE.local.md or a
// misbehaving ticket provider inject arbitrary escape sequences into
// the terminal. Returning label unchanged on detection is the
// fail-closed choice — the user just sees plain text.
//
// Callers writing directly to stdout (`thicket list`, the `rm`
// preview, `thicket start`'s confirmation prints) should use
// HyperlinkForWriter instead so piped/redirected output sees plain
// text. Pickers run under bubbletea, which requires a TTY, so they
// can call Hyperlink directly.
func Hyperlink(url, label string) string {
	if url == "" {
		return label
	}
	if hasControlByte(url) {
		return label
	}
	// OSC 8 ; <params> ; <uri> ST  ...label...  OSC 8 ; ; ST
	// ST is the string terminator; we use ESC \ ("\x1b\\") which is
	// the canonical 7-bit form and the one every terminal that
	// speaks OSC 8 accepts.
	return "\x1b]8;;" + url + "\x1b\\" + label + "\x1b]8;;\x1b\\"
}

// HyperlinkForWriter is the TTY-aware variant of Hyperlink: if w is
// not a terminal (a pipe, a tee target, a redirected file) it returns
// label unchanged so scripts parsing `thicket list` / `thicket start`
// output don't see raw OSC 8 bytes. The detection is best-effort:
// only `*os.File`s expose an Fd we can test, so any other io.Writer
// is treated as non-TTY (the safe default).
func HyperlinkForWriter(w io.Writer, url, label string) string {
	if !writerIsTTY(w) {
		return label
	}
	return Hyperlink(url, label)
}

func writerIsTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

func hasControlByte(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == 0x7f {
			return true
		}
	}
	return false
}
