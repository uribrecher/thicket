package main

import "testing"

func TestBuildInitialPrompt(t *testing.T) {
	cases := []struct {
		name, color, userPrompt, want string
	}{
		{"both empty", "", "", ""},
		{"only color", "blue", "", "/color blue"},
		{"only prompt", "", "review the plan", "review the plan"},
		{"both set", "blue", "review the plan", "/color blue\nreview the plan"},
		{"trims whitespace", "blue", "   review   ", "/color blue\nreview"},
		{"drops unknown color", "chartreuse", "review", "review"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildInitialPrompt(tc.color, tc.userPrompt)
			if got != tc.want {
				t.Errorf("buildInitialPrompt(%q, %q) = %q, want %q", tc.color, tc.userPrompt, got, tc.want)
			}
		})
	}
}
