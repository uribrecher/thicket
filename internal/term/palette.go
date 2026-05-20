package term

import "strings"

// paletteEntry pairs a canonical name with its hex color.
// Name is what we persist in the manifest and pass to /color <name>.
// Hex is what we feed to WriteTabColor for iTerm2 tab tinting.
// Keeping them together means TUI rendering, LLM prompts, manifest validation,
// and iTerm2 tinting all agree on the same set of values.
type paletteEntry struct {
	Name string
	Hex  string
}

// palette is the fixed set the LLM is allowed to suggest from and the user is
// allowed to pick from. Order is stable across calls and determines display
// order in the TUI picker.
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
// Called at the persistence boundary (writeState) and when parsing LLM output,
// so callers never need to compare strings inline.
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
// name is not in the palette. Used by the iTerm2 tinting path.
func PaletteHex(name string) string {
	name = SanitizePaletteName(name)
	for _, p := range palette {
		if p.Name == name {
			return p.Hex
		}
	}
	return ""
}

// PaletteNames returns all palette names in their canonical order.
// Callers MUST NOT mutate the slice.
func PaletteNames() []string {
	out := make([]string, len(palette))
	for i, p := range palette {
		out[i] = p.Name
	}
	return out
}
