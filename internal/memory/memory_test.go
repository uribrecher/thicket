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
		URL:          "https://app.shortcut.com/acme/story/12345",
		State:        "In Development",
		Owner:        "uri",
		Body:         "We need to fix the grouping pipeline.\n\n- bullet one\n- bullet two\n",
		Branch:       "uri/sc-12345-fix-inventory-grouping",
		WorkspaceDir: "/Users/uri/tasks/sc-12345-fix-inventory-grouping",
		Repos: []RepoEntry{
			{Name: "acme-scan-state-manager", Branch: "uri/sc-12345-fix-inventory-grouping",
				WorktreePath:  "/Users/uri/tasks/sc-12345-fix-inventory-grouping/acme-scan-state-manager",
				DefaultBranch: "main"},
			{Name: "acme-discovery", Branch: "uri/sc-12345-fix-inventory-grouping",
				WorktreePath:  "/Users/uri/tasks/sc-12345-fix-inventory-grouping/acme-discovery",
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

func TestRegenPreservingStatusLog_preservesTail(t *testing.T) {
	in := Input{
		TicketID: "sc-1", Title: "T", Body: "b",
		Branch: "x", WorkspaceDir: "/tmp/x",
		Repos: []RepoEntry{
			{Name: "alpha", Branch: "x", WorktreePath: "/tmp/x/alpha", DefaultBranch: "main"},
		},
		CreatedAt: time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC),
	}
	existing := []byte(`# Ticket sc-1 — Old title

stale top half — should be replaced

## Status log

<!-- comment -->

### 2026-05-13 14:00
- Investigated grouping pipeline
- Found root cause in inventories_utils.py
`)
	got, preserved, err := RegenPreservingStatusLog(in, existing)
	if err != nil {
		t.Fatalf("regen: %v", err)
	}
	if !preserved {
		t.Fatalf("preserved=false, want true")
	}
	gotStr := string(got)
	// New header from the fresh render.
	if !strings.Contains(gotStr, "Ticket sc-1 — T") {
		t.Errorf("fresh header missing:\n%s", gotStr)
	}
	if strings.Contains(gotStr, "Old title") {
		t.Errorf("stale title leaked into refreshed file:\n%s", gotStr)
	}
	// New repo from the input made it in.
	if !strings.Contains(gotStr, "| alpha |") {
		t.Errorf("new repo row missing:\n%s", gotStr)
	}
	// Existing status-log entries preserved.
	if !strings.Contains(gotStr, "### 2026-05-13 14:00") {
		t.Errorf("status-log heading lost:\n%s", gotStr)
	}
	if !strings.Contains(gotStr, "Investigated grouping pipeline") {
		t.Errorf("status-log entry body lost:\n%s", gotStr)
	}
	// Exactly one "## Status log" heading — splice didn't duplicate it.
	if c := strings.Count(gotStr, "## Status log"); c != 1 {
		t.Errorf("status-log heading count = %d, want 1\n%s", c, gotStr)
	}
}

func TestRegenPreservingStatusLog_emptyExistingFallsBackToRender(t *testing.T) {
	in := Input{TicketID: "sc-1", Title: "T", Body: "b", Branch: "x",
		WorkspaceDir: "/tmp/x", CreatedAt: time.Now()}
	got, preserved, err := RegenPreservingStatusLog(in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if preserved {
		t.Errorf("preserved=true for empty existing")
	}
	if !strings.Contains(string(got), "## Status log") {
		t.Errorf("fallback should be a full render:\n%s", got)
	}
}

func TestRegenPreservingStatusLog_missingMarkerFallsBack(t *testing.T) {
	in := Input{TicketID: "sc-1", Title: "T", Body: "b", Branch: "x",
		WorkspaceDir: "/tmp/x", CreatedAt: time.Now()}
	existing := []byte("# Ticket sc-1 — Old title\n\nuser deleted everything below\n")
	got, preserved, err := RegenPreservingStatusLog(in, existing)
	if err != nil {
		t.Fatal(err)
	}
	if preserved {
		t.Errorf("preserved=true when marker is absent")
	}
	if !strings.Contains(string(got), "## Status log") {
		t.Errorf("fresh render expected when fallback triggered:\n%s", got)
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

func TestExtractURL_roundTrip(t *testing.T) {
	dir := t.TempDir()
	in := Input{
		TicketID: "sc-42", Title: "T", Body: "body",
		URL: "https://app.shortcut.com/acme/story/42",
		Branch: "b", WorkspaceDir: dir,
		CreatedAt: time.Now(),
	}
	body, err := Render(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), body, 0o644); err != nil {
		t.Fatal(err)
	}
	got := ExtractURL(dir)
	if got != in.URL {
		t.Errorf("ExtractURL = %q, want %q", got, in.URL)
	}
}

func TestExtractURL_missingFileReturnsEmpty(t *testing.T) {
	if got := ExtractURL(t.TempDir()); got != "" {
		t.Errorf("ExtractURL on dir without %s = %q, want \"\"", FileName, got)
	}
}

func TestExtractURL_noURLLineReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("# nothing useful\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ExtractURL(dir); got != "" {
		t.Errorf("ExtractURL on file without URL line = %q, want \"\"", got)
	}
}
