package detector

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseNickname(t *testing.T) {
	cases := map[string]struct {
		raw  string
		want string
	}{
		"plain":                  {"flaky tests", "flaky tests"},
		"trailing newline":       {"flaky tests\n", "flaky tests"},
		"with emoji":             {"🐛 flaky tests", "🐛 flaky tests"},
		"double-quoted":          {`"flaky tests"`, "flaky tests"},
		"single-quoted":          {"'flaky tests'", "flaky tests"},
		"backtick-wrapped":       {"`flaky tests`", "flaky tests"},
		"first line wins": {"flaky tests\nwith more prose", "flaky tests"}, // chatty trailing lines are dropped; preamble removal is the prompt's job
		"second line ignored":    {"flaky tests\nextra prose\nmore", "flaky tests"},
		"leading blank lines":    {"\n\n  \nflaky tests\n", "flaky tests"},
		"empty input":            {"", ""},
		"whitespace only":        {"   \n\t\n  ", ""},
		"too long ascii":         {"a very very long nickname that is way past twenty", "a very very long nic"}, // 20 chars
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := parseNickname(tc.raw)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
			if utf8.RuneCountInString(got) > NicknameMaxChars {
				t.Errorf("%q exceeds %d runes", got, NicknameMaxChars)
			}
		})
	}
}

func TestParseNickname_truncatesAtRuneBoundary(t *testing.T) {
	// 21 runes, each emoji is multibyte — truncating at a byte
	// boundary would split an emoji and corrupt the output.
	raw := strings.Repeat("🐛", 21)
	got := parseNickname(raw)
	if utf8.RuneCountInString(got) != NicknameMaxChars {
		t.Errorf("rune count = %d, want %d", utf8.RuneCountInString(got), NicknameMaxChars)
	}
	if !utf8.ValidString(got) {
		t.Errorf("truncation produced invalid UTF-8: %q", got)
	}
}

func TestClaudeCLINicknameSuggester_returnsCleanedOutput(t *testing.T) {
	fr := &fakeCLIRunner{stdout: []byte("🐛 picker fix\n")}
	s := &ClaudeCLINicknameSuggester{BinaryPath: "claude", Model: "claude-haiku-4-5", Runner: fr}

	got, err := s.Suggest(context.Background(),
		"Fix flaky ticket picker", "## Repro\nThe Shortcut picker drops focus...")
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if got != "🐛 picker fix" {
		t.Errorf("got %q, want %q", got, "🐛 picker fix")
	}
	if fr.gotName != "claude" {
		t.Errorf("binary = %q", fr.gotName)
	}
	if len(fr.gotArgs) < 1 || fr.gotArgs[0] != "-p" {
		t.Errorf("argv missing -p: %v", fr.gotArgs)
	}
	if !strings.Contains(fr.gotStdin, "Fix flaky ticket picker") {
		t.Errorf("title not forwarded to claude prompt: %q", fr.gotStdin)
	}
}

func TestClaudeCLINicknameSuggester_rejectsEmptyTicket(t *testing.T) {
	fr := &fakeCLIRunner{}
	s := &ClaudeCLINicknameSuggester{BinaryPath: "claude", Runner: fr}
	if _, err := s.Suggest(context.Background(), "", ""); err == nil {
		t.Fatal("want error for empty title+body, got nil")
	}
	if fr.gotName != "" {
		t.Errorf("runner should not be called; got name %q", fr.gotName)
	}
}

func TestClaudeCLINicknameSuggester_emptyOutputIsError(t *testing.T) {
	fr := &fakeCLIRunner{stdout: []byte("   \n\n   ")}
	s := &ClaudeCLINicknameSuggester{BinaryPath: "claude", Runner: fr}
	if _, err := s.Suggest(context.Background(), "title", "body"); err == nil {
		t.Fatal("want error for whitespace-only output, got nil")
	}
}
