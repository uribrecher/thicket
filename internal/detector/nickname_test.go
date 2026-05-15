package detector

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/uribrecher/thicket/internal/workspace"
)

func TestParseSuggestion(t *testing.T) {
	cases := map[string]struct {
		raw          string
		wantNickname string
		wantColor    string
	}{
		"two lines, expected order": {
			raw:          "🐛 picker fix\n#FF5733",
			wantNickname: "🐛 picker fix",
			wantColor:    "#FF5733",
		},
		"color line prefixed": {
			raw:          "🐛 picker fix\ncolor: #ff5733",
			wantNickname: "🐛 picker fix",
			wantColor:    "#FF5733",
		},
		"color first, nickname second": {
			raw:          "#FF5733\n🐛 picker fix",
			wantNickname: "🐛 picker fix",
			wantColor:    "#FF5733",
		},
		"nickname only, no color": {
			raw:          "🐛 picker fix",
			wantNickname: "🐛 picker fix",
			wantColor:    "",
		},
		"lowercase hex normalized": {
			raw:          "picker fix\n#ff5733",
			wantNickname: "picker fix",
			wantColor:    "#FF5733",
		},
		"quoted nickname": {
			raw:          `"picker fix"` + "\n#ff5733",
			wantNickname: "picker fix",
			wantColor:    "#FF5733",
		},
		"chatty preamble skipped": {
			// "Here's a nickname:" ends in a bare colon AND the
			// prefix mentions "nickname" → parser skips it and
			// the next line lands as the real nickname.
			raw:          "Sure! Here's a nickname:\n🐛 picker fix\n#ff5733",
			wantNickname: "🐛 picker fix",
			wantColor:    "#FF5733",
		},
		"chatty color preamble skipped": {
			// Same trick on a "color:" line.
			raw:          "🐛 picker fix\nAnd the color:\n#ff5733",
			wantNickname: "🐛 picker fix",
			wantColor:    "#FF5733",
		},
		"empty input": {
			raw:          "",
			wantNickname: "",
			wantColor:    "",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
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
	fr := &fakeCLIRunner{stdout: []byte("🐛 picker fix\n#FF5733\n")}
	s := &ClaudeCLINicknameSuggester{BinaryPath: "claude", Model: "claude-haiku-4-5", Runner: fr}

	got, err := s.Suggest(context.Background(),
		"Fix flaky ticket picker", "## Repro\nThe Shortcut picker drops focus...", nil)
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if got.Nickname != "🐛 picker fix" {
		t.Errorf("Nickname = %q, want %q", got.Nickname, "🐛 picker fix")
	}
	if got.Color != "#FF5733" {
		t.Errorf("Color = %q, want %q", got.Color, "#FF5733")
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
		got := renderExistingColorsClause([]string{"#FF9900", "#0078D4", "#13AA52"})
		for _, want := range []string{"#FF9900", "#0078D4", "#13AA52", "different hue or brightness"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in %q", want, got)
			}
		}
	})
	t.Run("caps at 8", func(t *testing.T) {
		// 12 fake colors → only the first 8 should land in the
		// rendered clause so the prompt stays bounded.
		in := []string{
			"#111111", "#222222", "#333333", "#444444",
			"#555555", "#666666", "#777777", "#888888",
			"#999999", "#AAAAAA", "#BBBBBB", "#CCCCCC",
		}
		got := renderExistingColorsClause(in)
		if strings.Contains(got, "#999999") || strings.Contains(got, "#AAAAAA") {
			t.Errorf("over-cap colors leaked into prompt: %q", got)
		}
		if !strings.Contains(got, "#888888") {
			t.Errorf("8th color missing: %q", got)
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
