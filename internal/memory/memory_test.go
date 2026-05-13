package memory

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite golden files from current output")

func TestRender_golden(t *testing.T) {
	in := Input{
		TicketID:     "sc-12345",
		Title:        "Fix inventory grouping for AWS native rules",
		URL:          "https://app.shortcut.com/sentra/story/12345",
		State:        "In Development",
		Owner:        "uri",
		Body:         "We need to fix the grouping pipeline.\n\n- bullet one\n- bullet two\n",
		Branch:       "uri/sc-12345-fix-inventory-grouping",
		WorkspaceDir: "/Users/uri/tasks/sc-12345-fix-inventory-grouping",
		Repos: []RepoEntry{
			{Name: "sentra-scan-state-manager", Branch: "uri/sc-12345-fix-inventory-grouping",
				WorktreePath:  "/Users/uri/tasks/sc-12345-fix-inventory-grouping/sentra-scan-state-manager",
				DefaultBranch: "main"},
			{Name: "sentra-discovery", Branch: "uri/sc-12345-fix-inventory-grouping",
				WorktreePath:  "/Users/uri/tasks/sc-12345-fix-inventory-grouping/sentra-discovery",
				DefaultBranch: "main"},
		},
		CreatedAt: time.Date(2026, 5, 13, 12, 30, 0, 0, time.UTC),
	}
	got, err := Render(in)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	goldenPath := filepath.Join("testdata", "claude_local.golden.md")
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (re-run with -update-golden to create it): %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("rendered output mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRender_emptyBodyFallback(t *testing.T) {
	out, err := Render(Input{TicketID: "sc-1", Title: "Tiny ticket", Branch: "b",
		WorkspaceDir: "/tmp/x", CreatedAt: time.Now()})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(string(out), "ticket has no description") {
		t.Errorf("expected placeholder for empty body, got:\n%s", out)
	}
}

func TestRender_omitsOptionalFields(t *testing.T) {
	out, err := Render(Input{TicketID: "sc-1", Title: "T", Body: "b",
		Branch: "x", WorkspaceDir: "/tmp/x", CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "**State:**") {
		t.Errorf("State row should be omitted when empty\n%s", s)
	}
	if strings.Contains(s, "**Owner:**") {
		t.Errorf("Owner row should be omitted when empty\n%s", s)
	}
}
