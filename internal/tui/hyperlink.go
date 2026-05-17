package tui

// Hyperlink wraps label in an OSC 8 hyperlink pointing at url so that
// terminals which support the sequence (iTerm2, WezTerm, kitty, Ghostty,
// modern Terminal.app, VS Code's integrated terminal, GNOME Terminal,
// Windows Terminal) render label as ⌘-clickable / Ctrl-clickable text
// that opens url. Terminals that don't recognize OSC 8 strip the
// escape and show label unchanged, so this is safe to emit
// unconditionally.
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
func Hyperlink(url, label string) string {
	if url == "" {
		return label
	}
	// OSC 8 ; <params> ; <uri> ST  ...label...  OSC 8 ; ; ST
	// ST is the string terminator; we use ESC \ ("\x1b\\") which is
	// the canonical 7-bit form and the one every terminal that speaks
	// OSC 8 accepts.
	return "\x1b]8;;" + url + "\x1b\\" + label + "\x1b]8;;\x1b\\"
}
