package tui

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"

	"github.com/uribrecher/thicket/internal/catalog"
	"github.com/uribrecher/thicket/internal/detector"
)

func TestAutoSelector_takesPicksAsIs(t *testing.T) {
	a := AutoSelector{AutoClone: true}
	got, err := a.SelectRepos(
		[]catalog.Repo{{Name: "alpha"}, {Name: "beta"}},
		[]detector.RepoMatch{{Name: "beta", Confidence: 0.9}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "beta" {
		t.Errorf("got %v, want [beta]", got)
	}
	yes, err := a.ConfirmClone("x", "/tmp")
	if err != nil || !yes {
		t.Errorf("want auto-yes; got %v err=%v", yes, err)
	}
	yes, _ = AutoSelector{}.ConfirmClone("x", "/tmp")
	if yes {
		t.Errorf("zero-value AutoSelector should say no")
	}
}

func TestRemoveFromSlice(t *testing.T) {
	cases := map[string]struct {
		in   []string
		drop string
		want []string
	}{
		"middle":  {[]string{"a", "b", "c"}, "b", []string{"a", "c"}},
		"first":   {[]string{"a", "b"}, "a", []string{"b"}},
		"last":    {[]string{"a", "b"}, "b", []string{"a"}},
		"missing": {[]string{"a", "b"}, "z", []string{"a", "b"}},
		"empty":   {nil, "x", nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := removeFromSlice(tc.in, tc.drop)
			if !sliceEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestTruncate(t *testing.T) {
	cases := map[string]struct {
		in  string
		n   int
		out string
	}{
		"short":          {"hello", 10, "hello"},
		"exact":          {"hello", 5, "hello"},
		"trim":           {"helloworld", 6, "hello…"},
		"zero":           {"hi", 0, ""},
		"unicode 1-wide": {"αβγδε", 4, "αβγ…"},
		// Wide-cell handling: emoji are 2 cells visually but 1
		// rune. A pre-runewidth truncate would keep the emoji and
		// still report width 6 when the visible width is 7.
		// 🐛 + " " + "fix" = 2+1+3 = 6 cells, exact.
		"emoji within budget": {"🐛 fix", 6, "🐛 fix"},
		// 🐛 + " " + "pick" + "…" = 2+1+4+1 = 8 cells; runewidth
		// keeps the ellipsis inside the budget.
		"emoji needs trimming": {"🐛 picker fix", 8, "🐛 pick…"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := Truncate(tc.in, tc.n); got != tc.out {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.out)
			}
		})
	}
}

// TestPadRight covers the alignment fix: emoji are 2 visible cells
// but only 1 rune, so a rune-count pad under-fills the column by
// the emoji's extra cell. PadRight now uses visible-cell width so
// columns line up regardless of which rows contain emoji.
func TestPadRight(t *testing.T) {
	cases := map[string]struct {
		in     string
		n      int
		wantW  int    // visible width after padding
		wantHi string // suffix the result must end with (asserts the pad chars)
	}{
		"ascii pads to 10":     {"abc", 10, 10, "       "},
		"ascii already exact":  {"abcdef", 6, 6, ""},
		"emoji pads correctly": {"🐛 fix", 10, 10, "    "},
		// 🐛 + " fix" = 6 cells; budget 5 → already too wide, no
		// pad chars added (width stays at 6).
		"emoji over budget no-op": {"🐛 fix", 5, 6, ""},
		"empty input":             {"", 5, 5, "     "},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := PadRight(tc.in, tc.n)
			if w := runewidth.StringWidth(got); w != tc.wantW {
				t.Errorf("visible width = %d, want %d (result=%q)", w, tc.wantW, got)
			}
			if !strings.HasSuffix(got, tc.wantHi) {
				t.Errorf("expected pad suffix %q in %q", tc.wantHi, got)
			}
		})
	}
}

// HuhSelector itself is not unit-tested — it drives a terminal UI. It's
// validated end-to-end via `thicket start` in manual smoke tests.
//
// Confirm that the type satisfies the Selector interface at compile time:
var (
	_ Selector = HuhSelector{}
	_ Selector = AutoSelector{}
)
