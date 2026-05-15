package term

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseHexColor(t *testing.T) {
	cases := map[string]struct {
		in       string
		r, g, b  int
		wantOK   bool
		wantNorm string // SanitizeHexColor expected output
	}{
		"hash":         {"#FF5733", 0xff, 0x57, 0x33, true, "#FF5733"},
		"no hash":      {"FF5733", 0xff, 0x57, 0x33, true, "#FF5733"},
		"lowercase":    {"#ff5733", 0xff, 0x57, 0x33, true, "#FF5733"},
		"mixed case":   {"#Ff57Aa", 0xff, 0x57, 0xaa, true, "#FF57AA"},
		"with spaces":  {"  #FF5733  ", 0xff, 0x57, 0x33, true, "#FF5733"},
		"too short":    {"#FFF", 0, 0, 0, false, ""},
		"too long":     {"#FF5733AA", 0, 0, 0, false, ""},
		"rgb fn":       {"rgb(255,87,51)", 0, 0, 0, false, ""},
		"named color":  {"red", 0, 0, 0, false, ""},
		"empty string": {"", 0, 0, 0, false, ""},
		"non-hex char": {"#FFXY33", 0, 0, 0, false, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r, g, b, ok := ParseHexColor(tc.in)
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok {
				if r != tc.r || g != tc.g || b != tc.b {
					t.Errorf("rgb = (%d,%d,%d), want (%d,%d,%d)", r, g, b, tc.r, tc.g, tc.b)
				}
			}
			if got := SanitizeHexColor(tc.in); got != tc.wantNorm {
				t.Errorf("SanitizeHexColor = %q, want %q", got, tc.wantNorm)
			}
		})
	}
}

func TestWriteTabTitle(t *testing.T) {
	var b bytes.Buffer
	WriteTabTitle(&b, "🐛 picker fix")
	got := b.String()
	want := "\x1b]0;🐛 picker fix\x07"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWriteTabTitle_emptyIsNoop(t *testing.T) {
	var b bytes.Buffer
	WriteTabTitle(&b, "")
	if b.Len() != 0 {
		t.Errorf("expected no output, got %q", b.String())
	}
}

func TestWriteBadge_base64Encodes(t *testing.T) {
	var b bytes.Buffer
	WriteBadge(&b, "🐛 picker fix")
	got := b.String()
	// Frame: ESC ] 1337 ; SetBadgeFormat = <b64> ESC \
	if !strings.HasPrefix(got, "\x1b]1337;SetBadgeFormat=") {
		t.Errorf("missing frame prefix: %q", got)
	}
	if !strings.HasSuffix(got, "\x1b\\") {
		t.Errorf("missing ST terminator: %q", got)
	}
	// The base64 of "🐛 picker fix" should be embedded literally.
	if !strings.Contains(got, "8J+QmyBwaWNrZXIgZml4") {
		t.Errorf("expected base64 body, got %q", got)
	}
}

func TestWriteTabColor_emitsThreeComponents(t *testing.T) {
	var b bytes.Buffer
	WriteTabColor(&b, "#FF5733")
	got := b.String()
	for _, want := range []string{
		"\x1b]6;1;bg;red;brightness;255\x07",
		"\x1b]6;1;bg;green;brightness;87\x07",
		"\x1b]6;1;bg;blue;brightness;51\x07",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing component %q in output %q", want, got)
		}
	}
}

func TestWriteTabColor_invalidIsNoop(t *testing.T) {
	var b bytes.Buffer
	WriteTabColor(&b, "not-a-color")
	if b.Len() != 0 {
		t.Errorf("expected silent no-op, got %q", b.String())
	}
}
