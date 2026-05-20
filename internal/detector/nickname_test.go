package detector

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/uribrecher/thicket/internal/workspace"
)

func TestParseSuggestion(t *testing.T) {
	cases := []struct {
		name         string
		raw          string
		wantNickname string
		wantColor    string
	}{
		{
			name:         "nickname then color",
			raw:          "🐛 Wix S3 dedup\nblue",
			wantNickname: "🐛 Wix S3 dedup",
			wantColor:    "blue",
		},
		{
			name:         "color then nickname",
			raw:          "blue\n🐛 Wix S3 dedup",
			wantNickname: "🐛 Wix S3 dedup",
			wantColor:    "blue",
		},
		{
			name:         "color prefix",
			raw:          "🐛 Wix S3 dedup\ncolor: blue",
			wantNickname: "🐛 Wix S3 dedup",
			wantColor:    "blue",
		},
		{
			name:         "no color line",
			raw:          "🐛 Wix S3 dedup",
			wantNickname: "🐛 Wix S3 dedup",
			wantColor:    "",
		},
		{
			name:         "leading prose then content",
			raw:          "Here you go:\n🐛 Wix S3 dedup\nblue",
			wantNickname: "🐛 Wix S3 dedup",
			wantColor:    "blue",
		},
		{
			name:         "generic prose intro skipped",
			raw:          "Output:\n🐛 Wix S3 dedup\nblue",
			wantNickname: "🐛 Wix S3 dedup",
			wantColor:    "blue",
		},
		{
			name:         "unknown color name drops to empty",
			raw:          "🐛 Wix S3 dedup\nchartreuse",
			wantNickname: "🐛 Wix S3 dedup",
			wantColor:    "",
		},
		{
			name:         "uppercase color",
			raw:          "🐛 Wix S3 dedup\nBLUE",
			wantNickname: "🐛 Wix S3 dedup",
			wantColor:    "blue",
		},
		{
			name:         "wrapping quotes stripped",
			raw:          "\"picker fix\"\nblue",
			wantNickname: "picker fix",
			wantColor:    "blue",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseSuggestion(tc.raw)
			if got.Nickname != tc.wantNickname {
				t.Errorf("Nickname = %q, want %q", got.Nickname, tc.wantNickname)
			}
			if got.Color != tc.wantColor {
				t.Errorf("Color = %q, want %q", got.Color, tc.wantColor)
			}
			if utf8.RuneCountInString(got.Nickname) > workspace.NicknameMaxChars {
				t.Errorf("Nickname %q exceeds %d runes", got.Nickname, workspace.NicknameMaxChars)
			}
		})
	}
}

func TestClaudeCLINicknameSuggester_parsesBothFields(t *testing.T) {
	fr := &fakeCLIRunner{stdout: []byte("🐛 picker fix\nblue\n")}
	s := &ClaudeCLINicknameSuggester{BinaryPath: "claude", Model: "claude-haiku-4-5", Runner: fr}

	got, err := s.Suggest(context.Background(),
		"Fix flaky ticket picker", "## Repro\nThe Shortcut picker drops focus...", nil)
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if got.Nickname != "🐛 picker fix" {
		t.Errorf("Nickname = %q, want %q", got.Nickname, "🐛 picker fix")
	}
	if got.Color != "blue" {
		t.Errorf("Color = %q, want %q", got.Color, "blue")
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

func TestClaudeCLINicknameSuggester_nicknameOnlyOK(t *testing.T) {
	// Model omits the color line — we still take the nickname.
	fr := &fakeCLIRunner{stdout: []byte("🐛 picker fix\n")}
	s := &ClaudeCLINicknameSuggester{BinaryPath: "claude", Runner: fr}
	got, err := s.Suggest(context.Background(), "title", "body", nil)
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if got.Nickname != "🐛 picker fix" {
		t.Errorf("Nickname = %q", got.Nickname)
	}
	if got.Color != "" {
		t.Errorf("Color = %q, want empty", got.Color)
	}
}

func TestClaudeCLINicknameSuggester_rejectsEmptyTicket(t *testing.T) {
	fr := &fakeCLIRunner{}
	s := &ClaudeCLINicknameSuggester{BinaryPath: "claude", Runner: fr}
	if _, err := s.Suggest(context.Background(), "", "", nil); err == nil {
		t.Fatal("want error for empty title+body, got nil")
	}
	if fr.gotName != "" {
		t.Errorf("runner should not be called; got name %q", fr.gotName)
	}
}

func TestRenderExistingColorsClause(t *testing.T) {
	t.Run("empty list", func(t *testing.T) {
		got := renderExistingColorsClause(nil)
		if !strings.Contains(got, "no other") {
			t.Errorf("empty case should encourage novelty, got %q", got)
		}
	})
	t.Run("with colors", func(t *testing.T) {
		got := renderExistingColorsClause([]string{"orange", "blue", "green"})
		for _, want := range []string{"orange", "blue", "green", "pick something different"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in %q", want, got)
			}
		}
	})
	t.Run("caps at 8", func(t *testing.T) {
		// 12 palette names → only the first 8 should land in the
		// rendered clause so the prompt stays bounded.
		got := renderExistingColorsClause([]string{
			"red", "orange", "yellow", "green",
			"cyan", "blue", "purple", "pink",
			"red", "orange", "yellow", "green", // these four should be dropped
		})
		if !strings.Contains(got, "pink") {
			t.Errorf("8th entry missing from clause: %q", got)
		}
		if strings.Count(got, "red") != 1 || strings.Count(got, "orange") != 1 {
			t.Errorf("over-cap duplicates leaked into clause: %q", got)
		}
	})
}

func TestClaudeCLINicknameSuggester_emptyOutputIsError(t *testing.T) {
	fr := &fakeCLIRunner{stdout: []byte("   \n\n   ")}
	s := &ClaudeCLINicknameSuggester{BinaryPath: "claude", Runner: fr}
	if _, err := s.Suggest(context.Background(), "title", "body", nil); err == nil {
		t.Fatal("want error for whitespace-only output, got nil")
	}
}
