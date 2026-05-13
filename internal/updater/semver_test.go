package updater

import "testing"

func TestParseRelease_accepts(t *testing.T) {
	cases := map[string]Release{
		"v0.1.0":    {0, 1, 0},
		"v0.1.1":    {0, 1, 1},
		"v1.0.0":    {1, 0, 0},
		"0.1.1":     {0, 1, 1},
		" v0.1.1":   {0, 1, 1},
		"v10.20.30": {10, 20, 30},
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			got, err := ParseRelease(in)
			if err != nil {
				t.Fatalf("parse(%q): %v", in, err)
			}
			if got != want {
				t.Errorf("parse(%q) = %v, want %v", in, got, want)
			}
		})
	}
}

func TestParseRelease_rejectsDev(t *testing.T) {
	cases := []string{
		"",
		"v0.1.0-rc1",
		"v0.1.0-13-gabcd",
		"v0.1.0-13-gabcd-dirty",
		"v0.1",
		"v0.1.x",
		"latest",
		"dev",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			if _, err := ParseRelease(in); err == nil {
				t.Fatalf("parse(%q): want error, got nil", in)
			}
		})
	}
}

func TestRelease_Compare(t *testing.T) {
	mk := func(s string) Release {
		r, err := ParseRelease(s)
		if err != nil {
			t.Fatalf("setup parse(%q): %v", s, err)
		}
		return r
	}
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.1.0", "v0.1.1", -1},
		{"v0.1.1", "v0.1.0", 1},
		{"v0.1.1", "v0.1.1", 0},
		{"v0.2.0", "v0.10.0", -1},
		{"v1.0.0", "v0.99.99", 1},
	}
	for _, tc := range cases {
		t.Run(tc.a+"_vs_"+tc.b, func(t *testing.T) {
			got := mk(tc.a).Compare(mk(tc.b))
			if got != tc.want {
				t.Errorf("compare(%s, %s) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
