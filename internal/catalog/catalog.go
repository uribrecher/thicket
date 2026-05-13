// Package catalog enumerates a user's known GitHub repos and caches the
// listing locally so subsequent thicket runs don't pay the API roundtrip.
//
// The catalog is the universe of repos the LLM detector picks from and the
// TUI lets the user toggle. Local-clone status (whether the user already
// has the repo on disk under repos_root) is annotated onto each entry so
// later steps can decide whether to clone on demand.
package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/adrg/xdg"
)

// Repo is one entry in the catalog.
type Repo struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	DefaultBranch string `json:"default_branch"`
	CloneURL      string `json:"clone_url"`
	Archived      bool   `json:"archived"`
	// LocalPath is set by WithLocalPaths to the absolute path of a local
	// clone, if one exists at repos_root/<name>. Empty otherwise.
	LocalPath string `json:"local_path,omitempty"`
}

// Cloned reports whether the repo has a local clone.
func (r Repo) Cloned() bool { return r.LocalPath != "" }

// Fetcher returns the raw list of repos for one org. Implementations exist
// for the `gh` CLI (production) and an in-memory stub (tests).
type Fetcher interface {
	List(org string) ([]Repo, error)
}

// DefaultCacheTTL is how long Load considers a cache file fresh.
const DefaultCacheTTL = 7 * 24 * time.Hour

// ErrEmptyCatalog is returned by Build when every configured org came
// back with zero non-archived repos. Cached empty results are usually a
// symptom of a typo in `github_orgs` (e.g. "sentrasec" vs "sentraio")
// or missing `gh` access to the org — refusing to return them lets
// callers prompt the user to re-check config instead of silently
// caching an empty list.
var ErrEmptyCatalog = errors.New("no repos visible — check github_orgs and `gh auth status`")

// Build collects repos from all orgs via the fetcher and filters archived.
// Order: orgs in the order given; within each org, the order from the
// fetcher (typically alphabetical from `gh`).
func Build(orgs []string, f Fetcher) ([]Repo, error) {
	if len(orgs) == 0 {
		return nil, errors.New("no GitHub orgs configured")
	}
	if f == nil {
		return nil, errors.New("nil fetcher")
	}
	seen := map[string]bool{}
	var out []Repo
	for _, org := range orgs {
		rs, err := f.List(org)
		if err != nil {
			return nil, fmt.Errorf("list %q: %w", org, err)
		}
		for _, r := range rs {
			if r.Archived {
				continue
			}
			if seen[r.Name] {
				continue // a repo in two orgs — keep the first.
			}
			seen[r.Name] = true
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w (queried %v)", ErrEmptyCatalog, orgs)
	}
	return out, nil
}

// WithLocalPaths returns repos with LocalPath populated for any clone that
// already exists at reposRoot/<name>/.git. Input slice is not modified.
func WithLocalPaths(repos []Repo, reposRoot string) []Repo {
	out := make([]Repo, len(repos))
	for i, r := range repos {
		out[i] = r
		path := filepath.Join(reposRoot, r.Name)
		if isGitRepo(path) {
			out[i].LocalPath = path
		}
	}
	return out
}

func isGitRepo(path string) bool {
	st, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil && (st.IsDir() || st.Mode().IsRegular())
}

// ----- cache -----

type cacheFile struct {
	BuiltAt time.Time `json:"built_at"`
	Repos   []Repo    `json:"repos"`
}

// Path returns the cache file location. It ensures the parent dir exists.
func Path() (string, error) {
	dir := filepath.Join(xdg.CacheHome, "thicket")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create cache dir: %w", err)
	}
	return filepath.Join(dir, "catalog.json"), nil
}

// Load returns the cached repos and how stale they are. If the cache is
// missing it returns (nil, 0, ErrNoCache) so callers can decide to build
// fresh.
var ErrNoCache = errors.New("no catalog cache")

func Load(path string) ([]Repo, time.Duration, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, 0, ErrNoCache
		}
		return nil, 0, fmt.Errorf("read cache: %w", err)
	}
	var cf cacheFile
	if err := json.Unmarshal(b, &cf); err != nil {
		return nil, 0, fmt.Errorf("parse cache: %w", err)
	}
	return cf.Repos, time.Since(cf.BuiltAt), nil
}

// Save writes the cache atomically.
func Save(path string, repos []Repo) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}
	b, err := json.MarshalIndent(cacheFile{BuiltAt: time.Now(), Repos: repos}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode cache: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("write cache: %w", err)
	}
	return os.Rename(tmp, path)
}

// ----- gh fetcher -----

// GHFetcher shells out to the `gh` CLI to list an org's repos. It expects
// `gh` to be authenticated already.
type GHFetcher struct {
	// GHPath overrides the `gh` binary path (tests inject a shim).
	GHPath string
	// Limit caps the listing (gh defaults to 30 otherwise).
	Limit int
}

func (g GHFetcher) List(org string) ([]Repo, error) {
	bin := g.GHPath
	if bin == "" {
		bin = "gh"
	}
	limit := g.Limit
	if limit <= 0 {
		limit = 1000
	}
	cmd := exec.Command(bin, //nolint:gosec // we control the args
		"repo", "list", org,
		"--limit", fmt.Sprintf("%d", limit),
		"--json", "name,description,defaultBranchRef,sshUrl,isArchived",
	)
	// Capture stderr explicitly — exec.ExitError.Stderr is only set
	// when stdout is captured separately (i.e. when cmd.Stderr is set).
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh repo list %s: %w (stderr: %s)", org, err,
			strings.TrimSpace(stderr.String()))
	}
	return parseGHJSON(stdout)
}

type ghRepoJSON struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	SSHURL           string `json:"sshUrl"`
	IsArchived       bool   `json:"isArchived"`
	DefaultBranchRef struct {
		Name string `json:"name"`
	} `json:"defaultBranchRef"`
}

func parseGHJSON(b []byte) ([]Repo, error) {
	var raw []ghRepoJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parse gh output: %w", err)
	}
	out := make([]Repo, 0, len(raw))
	for _, r := range raw {
		out = append(out, Repo{
			Name:          r.Name,
			Description:   r.Description,
			DefaultBranch: r.DefaultBranchRef.Name,
			CloneURL:      r.SSHURL,
			Archived:      r.IsArchived,
		})
	}
	return out, nil
}
