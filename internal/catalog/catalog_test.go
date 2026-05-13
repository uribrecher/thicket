package catalog

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeFetcher returns canned responses keyed by org.
type fakeFetcher struct {
	repos map[string][]Repo
	err   error
}

func (f fakeFetcher) List(org string) ([]Repo, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.repos[org], nil
}

func TestBuild_filtersArchivedAndDedups(t *testing.T) {
	f := fakeFetcher{repos: map[string][]Repo{
		"acme": {
			{Name: "alpha", Archived: false},
			{Name: "beta", Archived: true}, // dropped
			{Name: "gamma", Archived: false},
		},
		"acme-tools": {
			{Name: "gamma", Archived: false}, // duplicate, drop second
			{Name: "delta", Archived: false},
		},
	}}
	got, err := Build([]string{"acme", "acme-tools"}, f)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var names []string
	for _, r := range got {
		names = append(names, r.Name)
	}
	want := []string{"alpha", "gamma", "delta"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", names, want)
	}
}

func TestBuild_emptyOrgList(t *testing.T) {
	_, err := Build(nil, fakeFetcher{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBuild_fetcherError(t *testing.T) {
	_, err := Build([]string{"acme"}, fakeFetcher{err: errors.New("boom")})
	if err == nil {
		t.Fatal("expected error to propagate")
	}
}

func TestBuild_emptyResultReturnsSentinel(t *testing.T) {
	// gh returned []; user probably typed the wrong org name.
	_, err := Build([]string{"acme"}, fakeFetcher{repos: map[string][]Repo{"acme": nil}})
	if !errors.Is(err, ErrEmptyCatalog) {
		t.Fatalf("want ErrEmptyCatalog, got %v", err)
	}
}

func TestBuild_allArchivedReturnsSentinel(t *testing.T) {
	f := fakeFetcher{repos: map[string][]Repo{
		"acme": {{Name: "old", Archived: true}},
	}}
	_, err := Build([]string{"acme"}, f)
	if !errors.Is(err, ErrEmptyCatalog) {
		t.Fatalf("want ErrEmptyCatalog, got %v", err)
	}
}

func TestWithLocalPaths_marksExistingClones(t *testing.T) {
	root := t.TempDir()
	// Create a fake git repo at root/alpha
	if err := makeFakeGitRepo(filepath.Join(root, "alpha")); err != nil {
		t.Fatal(err)
	}
	in := []Repo{{Name: "alpha"}, {Name: "beta"}}
	out := WithLocalPaths(in, root)
	if out[0].LocalPath == "" {
		t.Errorf("alpha should be marked as cloned")
	}
	if !out[0].Cloned() {
		t.Errorf("Cloned() should be true")
	}
	if out[1].LocalPath != "" {
		t.Errorf("beta should not be marked: %q", out[1].LocalPath)
	}
	// input not mutated
	if in[0].LocalPath != "" {
		t.Errorf("input was mutated")
	}
}

func TestSaveLoad_roundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")
	repos := []Repo{{Name: "alpha", Description: "first"}, {Name: "beta"}}
	if err := Save(path, repos); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, age, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 2 || got[0].Name != "alpha" {
		t.Errorf("got %+v", got)
	}
	if age < 0 || age > time.Minute {
		t.Errorf("unrealistic age: %v", age)
	}
}

func TestLoad_missing(t *testing.T) {
	_, _, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if !errors.Is(err, ErrNoCache) {
		t.Fatalf("want ErrNoCache, got %v", err)
	}
}

func TestParseGHJSON(t *testing.T) {
	in := []byte(`[
		{"name":"alpha","description":"d","sshUrl":"git@github.com:acme/alpha.git","isArchived":false,"defaultBranchRef":{"name":"main"}},
		{"name":"old","isArchived":true,"defaultBranchRef":{"name":"master"}}
	]`)
	out, err := parseGHJSON(in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d repos", len(out))
	}
	if out[0].Name != "alpha" || out[0].CloneURL != "git@github.com:acme/alpha.git" || out[0].DefaultBranch != "main" {
		t.Errorf("alpha mismapped: %+v", out[0])
	}
	if !out[1].Archived {
		t.Errorf("old should be archived")
	}
}

// makeFakeGitRepo creates the minimal structure that isGitRepo recognises.
func makeFakeGitRepo(path string) error {
	if err := mkdirs(path, ".git"); err != nil {
		return err
	}
	return nil
}

func mkdirs(parts ...string) error {
	return runShell("mkdir", "-p", filepath.Join(parts...))
}

func runShell(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	return cmd.Run()
}
