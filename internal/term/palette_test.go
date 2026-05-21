package term

import "testing"

func TestSanitizePaletteName_canonicalizes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"blue", "blue"},
		{"BLUE", "blue"},
		{"  Blue  ", "blue"},
		{"purple", "purple"},
		{"not-a-color", ""},
		{"", ""},
		{"#FF5733", ""}, // hex inputs no longer accepted as palette names
	}
	for _, tc := range cases {
		if got := SanitizePaletteName(tc.in); got != tc.want {
			t.Errorf("SanitizePaletteName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPaletteHex_returnsRepresentativeHex(t *testing.T) {
	if PaletteHex("blue") == "" {
		t.Error("PaletteHex(blue) is empty")
	}
	if got := PaletteHex("unknown"); got != "" {
		t.Errorf("PaletteHex(unknown) = %q, want empty", got)
	}
}

func TestPaletteNames_returnsStableOrder(t *testing.T) {
	names := PaletteNames()
	if len(names) < 4 {
		t.Fatalf("palette too small: %v", names)
	}
	again := PaletteNames()
	for i := range names {
		if names[i] != again[i] {
			t.Errorf("order mismatch at %d: %q vs %q", i, names[i], again[i])
		}
	}
	if SanitizePaletteName(names[0]) != names[0] {
		t.Errorf("names[0]=%q does not canonicalize to itself", names[0])
	}
}
