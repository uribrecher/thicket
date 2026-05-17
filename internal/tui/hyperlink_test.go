package tui

import (
	"bytes"
	"strings"
	"testing"
)

func TestHyperlinkWrapsLabel(t *testing.T) {
	got := Hyperlink("https://example.com/sc-1", "sc-1")
	want := "\x1b]8;;https://example.com/sc-1\x1b\\sc-1\x1b]8;;\x1b\\"
	if got != want {
		t.Fatalf("Hyperlink = %q, want %q", got, want)
	}
}

func TestHyperlinkEmptyURLPassesLabelThrough(t *testing.T) {
	got := Hyperlink("", "sc-1")
	if got != "sc-1" {
		t.Fatalf("Hyperlink(\"\", \"sc-1\") = %q, want %q", got, "sc-1")
	}
}

func TestHyperlinkRefusesControlBytes(t *testing.T) {
	// Anything containing a C0 control character (e.g. an embedded
	// ESC that could terminate the OSC sequence early and let a
	// crafted URL inject extra escapes) must fall back to plain
	// text. Guards against a hand-edited CLAUDE.local.md or a
	// misbehaving ticket provider smuggling escapes into the
	// terminal via our hyperlinks.
	cases := []string{
		"https://example.com/\x1b]0;owned\x07",
		"https://example.com/with\x00null",
		"https://example.com/with\nnewline",
		"https://example.com/with\x07bell",
	}
	for _, url := range cases {
		got := Hyperlink(url, "label")
		if got != "label" {
			t.Errorf("Hyperlink(%q) = %q, want plain label", url, got)
		}
		if strings.Contains(got, "\x1b]8") {
			t.Errorf("Hyperlink(%q) emitted OSC bytes despite control char", url)
		}
	}
}

func TestHyperlinkForWriter_nonTTYReturnsPlainLabel(t *testing.T) {
	// A bytes.Buffer is not a *os.File, so writerIsTTY rejects it
	// — the test stands in for `thicket list | tee log.txt` where
	// stdout is a pipe.
	var buf bytes.Buffer
	got := HyperlinkForWriter(&buf, "https://example.com/sc-1", "sc-1")
	if got != "sc-1" {
		t.Fatalf("HyperlinkForWriter(non-TTY) = %q, want plain label", got)
	}
}
