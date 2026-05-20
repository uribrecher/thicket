package term

import "strings"

// paletteEntry pairs a canonical lowercase name with its representative hex color.
type paletteEntry struct {
	Name string
	Hex  string
}

// palette is the authoritative ordered list of Claude colors.
// Order is stable across calls and determines display order in the TUI picker.
var palette = []paletteEntry{
	{"red", "#C8232C"},
	{"orange", "#FF9900"},
	{"yellow", "#F9A825"},
	{"green", "#2E7D32"},
	{"cyan", "#00ACC1"},
	{"blue", "#1565C0"},
	{"purple", "#6A1B9A"},
	{"pink", "#E91E63"},
}

// SanitizePaletteName trims space and lowercases s, then returns the canonical
// palette name if s matches one, or "" if it does not.
func SanitizePaletteName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, p := range palette {
		if p.Name == s {
			return p.Name
		}
	}
	return ""
}

// PaletteHex returns the hex value for the named palette entry, or "" if the
// name is not in the palette.
func PaletteHex(name string) string {
	name = SanitizePaletteName(name)
	if name == "" {
		return ""
	}
	for _, p := range palette {
		if p.Name == name {
			return p.Hex
		}
	}
	return ""
}

// PaletteNames returns all palette names in their canonical order.
func PaletteNames() []string {
	out := make([]string, len(palette))
	for i, p := range palette {
		out[i] = p.Name
	}
	return out
}
