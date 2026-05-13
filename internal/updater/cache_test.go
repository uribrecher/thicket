package updater

import (
	"testing"
	"time"
)

func TestCacheState_fresh(t *testing.T) {
	cases := []struct {
		name string
		s    cacheState
		want bool
	}{
		{"zero", cacheState{}, false},
		{"empty-version", cacheState{CheckedAt: time.Now()}, false},
		{"just-now", cacheState{CheckedAt: time.Now(), LatestVersion: "v0.1.0"}, true},
		{"23h-old", cacheState{CheckedAt: time.Now().Add(-23 * time.Hour), LatestVersion: "v0.1.0"}, true},
		{"25h-old", cacheState{CheckedAt: time.Now().Add(-25 * time.Hour), LatestVersion: "v0.1.0"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.fresh(); got != tc.want {
				t.Errorf("fresh() = %v, want %v", got, tc.want)
			}
		})
	}
}
