package tui

import (
	"testing"

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
		"short":   {"hello", 10, "hello"},
		"exact":   {"hello", 5, "hello"},
		"trim":    {"helloworld", 6, "hello…"},
		"zero":    {"hi", 0, ""},
		"unicode": {"αβγδε", 4, "αβγ…"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := Truncate(tc.in, tc.n); got != tc.out {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.out)
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
