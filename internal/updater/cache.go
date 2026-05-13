package updater

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/adrg/xdg"
)

// cacheState is the on-disk record of the last update check. It
// answers two questions: (1) is the cached "latest" still fresh enough
// to trust without re-fetching? and (2) did the user already say no to
// this particular version? — so we don't re-prompt them every
// invocation within the same 24h window.
type cacheState struct {
	CheckedAt       time.Time `json:"checked_at"`
	LatestVersion   string    `json:"latest_version"`
	DeclinedVersion string    `json:"declined_version,omitempty"`
}

const cacheTTL = 24 * time.Hour

func cachePath() (string, error) {
	dir := filepath.Join(xdg.ConfigHome, "thicket")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, ".update-check.json"), nil
}

func loadCache() (cacheState, error) {
	p, err := cachePath()
	if err != nil {
		return cacheState{}, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cacheState{}, nil
		}
		return cacheState{}, err
	}
	var st cacheState
	if err := json.Unmarshal(b, &st); err != nil {
		// Corrupt cache → treat as missing, will be overwritten on
		// the next successful check.
		return cacheState{}, nil
	}
	return st, nil
}

func saveCache(st cacheState) error {
	p, err := cachePath()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

func (s cacheState) fresh() bool {
	return !s.CheckedAt.IsZero() &&
		time.Since(s.CheckedAt) < cacheTTL &&
		s.LatestVersion != ""
}
