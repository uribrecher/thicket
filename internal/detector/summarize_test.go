package detector

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestParseSummaryLines(t *testing.T) {
	cases := map[string]struct {
		raw  string
		want []string
	}{
		"plain three lines": {
			raw:  "first line\nsecond line\nthird line\n",
			want: []string{"first line", "second line", "third line"},
		},
		"dashed bullets are stripped": {
			raw:  "- alpha\n- beta\n- gamma",
			want: []string{"alpha", "beta", "gamma"},
		},
		"asterisk bullets are stripped": {
			raw:  "* one\n* two\n* three",
			want: []string{"one", "two", "three"},
		},
		"numbered bullets are stripped": {
			raw:  "1. foo\n2) bar\n3. baz",
			want: []string{"foo", "bar", "baz"},
		},
		"unicode bullets are stripped": {
			raw:  "• alpha\n• beta\n• gamma",
			want: []string{"alpha", "beta", "gamma"},
		},
		"caps at 3 even when more provided": {
			raw:  "a\nb\nc\nd\ne",
			want: []string{"a", "b", "c"},
		},
		"blank lines are skipped": {
			raw:  "\nalpha\n\n\nbeta\n\ngamma",
			want: []string{"alpha", "beta", "gamma"},
		},
		"empty input gives empty result": {
			raw:  "",
			want: []string{},
		},
		"only whitespace gives empty result": {
			raw:  "   \n\t\n  ",
			want: []string{},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := parseSummaryLines(tc.raw)
			// reflect.DeepEqual treats nil and zero-len slice as
			// different; normalize so the empty-case assertions still
			// pass when parseSummaryLines pre-allocates the result.
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestClaudeCLISummarizer_parsesPlainTextOutput(t *testing.T) {
	fr := &fakeCLIRunner{
		stdout: []byte("- Zero-size SMB files are silently dropped\n- Track them in inventory as SKIPPED\n- Affects assets coverage\n"),
	}
	s := &ClaudeCLISummarizer{BinaryPath: "claude", Model: "claude-haiku-4-5", Runner: fr}

	got, err := s.Summarize(context.Background(),
		"Track zero-size file share assets in inventory (mark as SKIPPED)",
		"## Background\nZero-size files are silently dropped during create_assets_v2 parsing...")
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d lines, want 3", len(got))
	}
	if !strings.Contains(got[0], "Zero-size") {
		t.Errorf("first line lost content: %q", got[0])
	}
	if strings.HasPrefix(got[0], "-") || strings.HasPrefix(got[1], "-") {
		t.Errorf("bullet prefix not stripped: %v", got)
	}
	if fr.gotName != "claude" {
		t.Errorf("binary = %q", fr.gotName)
	}
	if len(fr.gotArgs) < 1 || fr.gotArgs[0] != "-p" {
		t.Errorf("argv missing -p: %v", fr.gotArgs)
	}
	if !strings.Contains(fr.gotStdin, "Zero-size") {
		t.Errorf("body not forwarded to claude prompt: %q", fr.gotStdin)
	}
}

func TestClaudeCLISummarizer_rejectsEmptyTicket(t *testing.T) {
	fr := &fakeCLIRunner{}
	s := &ClaudeCLISummarizer{BinaryPath: "claude", Runner: fr}
	if _, err := s.Summarize(context.Background(), "", ""); err == nil {
		t.Fatal("want error for empty title+body, got nil")
	}
	if fr.gotName != "" {
		t.Errorf("runner should not be called; got name %q", fr.gotName)
	}
}

func TestClaudeCLISummarizer_emptyOutputIsError(t *testing.T) {
	fr := &fakeCLIRunner{stdout: []byte("   \n\n   ")}
	s := &ClaudeCLISummarizer{BinaryPath: "claude", Runner: fr}
	if _, err := s.Summarize(context.Background(), "title", "body"); err == nil {
		t.Fatal("want error for whitespace-only output, got nil")
	}
}
