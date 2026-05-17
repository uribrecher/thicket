package tui

import "testing"

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
