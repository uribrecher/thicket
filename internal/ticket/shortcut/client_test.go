package shortcut

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/uribrecher/thicket/internal/ticket"
)

func TestParse_acceptsAllFormats(t *testing.T) {
	cases := map[string]int{
		"12345":    12345,
		"sc-12345": 12345,
		"SC-12345": 12345,
		"sc_12345": 12345,
		"https://app.shortcut.com/acme/story/12345":            12345,
		"https://app.shortcut.com/acme/story/12345/some-title": 12345,
	}
	s := New("tok", "")
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			id, err := s.Parse(in)
			if err != nil {
				t.Fatalf("parse(%q): %v", in, err)
			}
			scID, ok := id.(ID)
			if !ok || int(scID) != want {
				t.Fatalf("parse(%q) = %v, want %d", in, id, want)
			}
		})
	}
}

func TestParse_rejectsJunk(t *testing.T) {
	s := New("tok", "")
	cases := []string{"", "  ", "abc", "notaurl", "https://example.com/foo/123"}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			_, err := s.Parse(in)
			var unp ticket.ErrUnparseable
			if !errors.As(err, &unp) {
				t.Fatalf("parse(%q): want ErrUnparseable, got %v", in, err)
			}
		})
	}
}

func TestID_String(t *testing.T) {
	got := ID(42).String()
	if got != "sc-42" {
		t.Errorf("got %q, want %q", got, "sc-42")
	}
}

func TestFetch_happyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/stories/777" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Shortcut-Token"); got != "my-token" {
			t.Errorf("token header: got %q, want %q", got, "my-token")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"id": 777,
			"name": "Fix inventory grouping",
			"description": "Long story.",
			"app_url": "https://app.shortcut.com/acme/story/777",
			"formatted_vcs_branch_name": "uri/sc-777-fix-inventory-grouping",
			"workflow_state_id": 500001
		}`)
	}))
	defer srv.Close()

	s := New("my-token", srv.URL)
	tk, err := s.Fetch(ID(777))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if tk.SourceID != "sc-777" {
		t.Errorf("SourceID = %q", tk.SourceID)
	}
	if tk.Title != "Fix inventory grouping" {
		t.Errorf("Title = %q", tk.Title)
	}
	if tk.URL != "https://app.shortcut.com/acme/story/777" {
		t.Errorf("URL = %q", tk.URL)
	}
	if branch := s.BranchName(tk); branch != "uri/sc-777-fix-inventory-grouping" {
		t.Errorf("BranchName = %q", branch)
	}
	if got := tk.Extra["workflow_state_id"]; got != "500001" {
		t.Errorf("workflow_state_id extra = %q", got)
	}
}

func TestFetch_surfacesLabelsAndResolvesRequester(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/stories/123":
			fmt.Fprint(w, `{
				"id": 123,
				"name": "x",
				"requested_by_id": "user-uuid-1",
				"labels": [{"name": "infra"}, {"name": "p1"}, {"name": "tech-debt"}]
			}`)
		case "/api/v3/members/user-uuid-1":
			fmt.Fprint(w, `{"profile": {"name": "Alice Example", "mention_name": "alice"}}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	tk, err := New("tok", srv.URL).Fetch(ID(123))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if tk.Requester != "Alice Example" {
		t.Errorf("Requester = %q, want %q", tk.Requester, "Alice Example")
	}
	wantLabels := []string{"infra", "p1", "tech-debt"}
	if fmt.Sprint(tk.Labels) != fmt.Sprint(wantLabels) {
		t.Errorf("Labels = %v, want %v", tk.Labels, wantLabels)
	}
}

// Member lookup failures must not abort Fetch — the rest of the ticket
// is still valuable, so Requester just stays empty.
func TestFetch_requesterLookupFailureIsTolerated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/stories/123":
			fmt.Fprint(w, `{"id": 123, "name": "x", "requested_by_id": "user-uuid-1"}`)
		case "/api/v3/members/user-uuid-1":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	tk, err := New("tok", srv.URL).Fetch(ID(123))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if tk.Requester != "" {
		t.Errorf("Requester = %q, want empty", tk.Requester)
	}
}

func TestFetch_notFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	_, err := New("tok", srv.URL).Fetch(ID(1))
	if err == nil || !contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

func TestFetch_unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := New("bad", srv.URL).Fetch(ID(1))
	if err == nil || !contains(err.Error(), "401") {
		t.Fatalf("expected 401 error, got %v", err)
	}
}

func TestFetch_wrongIDType(t *testing.T) {
	s := New("tok", "")
	type otherID struct{ ticket.ID }
	_, err := s.Fetch(nil)
	if err == nil {
		t.Fatal("expected error for nil id")
	}
	_, err = s.Fetch(otherID{})
	if err == nil {
		t.Fatal("expected error for foreign id type")
	}
}

func TestBranchName_emptyExtra(t *testing.T) {
	s := New("tok", "")
	if got := s.BranchName(ticket.Ticket{}); got != "" {
		t.Errorf("want empty, got %q", got)
	}
}

// ----- ListAssigned -----

// listAssignedServer wires up an httptest.Server that handles the three
// endpoints ListAssigned hits. Each handler returns canned JSON; the
// search handler asserts the request body matches what the caller is
// expected to send.
func listAssignedServer(t *testing.T, member memberResponse,
	workflows []workflowResponse, stories []storyResponse) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Shortcut-Token"); got != "tok" {
			t.Errorf("token header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v3/member" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(member)
		case r.URL.Path == "/api/v3/workflows" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(workflows)
		case r.URL.Path == "/api/v3/stories/search" && r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			var sb searchBody
			if err := json.Unmarshal(body, &sb); err != nil {
				t.Errorf("decode search body: %v", err)
			}
			if len(sb.OwnerIDs) != 1 || sb.OwnerIDs[0] != member.ID {
				t.Errorf("owner_ids = %v, want [%s]", sb.OwnerIDs, member.ID)
			}
			if sb.Archived {
				t.Errorf("Archived = true, want false")
			}
			_ = json.NewEncoder(w).Encode(stories)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestListAssigned_filtersDoneArchivedAndExcludedStates(t *testing.T) {
	member := memberResponse{ID: "user-abc"}
	workflows := []workflowResponse{{
		States: []workflowStateResponse{
			{ID: 100, Name: "Ready for Dev", Type: "unstarted"},
			{ID: 101, Name: "In Development", Type: "started"},
			{ID: 102, Name: "In Review", Type: "started"},
			{ID: 103, Name: "Verifying", Type: "started"},
			{ID: 104, Name: "Completed", Type: "done"},
		},
	}}
	stories := []storyResponse{
		{ID: 1, Name: "active dev", WorkflowStateID: 101},
		{ID: 2, Name: "in review", WorkflowStateID: 102}, // excluded by name
		{ID: 3, Name: "verifying", WorkflowStateID: 103}, // excluded by name
		{ID: 4, Name: "done", WorkflowStateID: 104},      // excluded by type
		{ID: 5, Name: "archived", WorkflowStateID: 101, Archived: true},
		{ID: 6, Name: "ready", WorkflowStateID: 100},
		{ID: 7, Name: "unknown state", WorkflowStateID: 9999}, // not in workflow
	}
	srv := listAssignedServer(t, member, workflows, stories)
	defer srv.Close()

	got, err := New("tok", srv.URL).ListAssigned(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d tickets, want 2 (active dev + ready)", len(got))
	}
	if got[0].SourceID != "sc-1" || got[0].State != "In Development" {
		t.Errorf("first ticket = %+v", got[0])
	}
	if got[1].SourceID != "sc-6" || got[1].State != "Ready for Dev" {
		t.Errorf("second ticket = %+v", got[1])
	}
}

func TestListAssigned_unauthorizedSurfacesClearError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := New("bad", srv.URL).ListAssigned(context.Background())
	if err == nil || !contains(err.Error(), "401") {
		t.Fatalf("want 401 error, got %v", err)
	}
}

func TestListAssigned_sortsByUpdatedAtDescending(t *testing.T) {
	member := memberResponse{ID: "u"}
	workflows := []workflowResponse{{
		States: []workflowStateResponse{{ID: 1, Name: "Dev", Type: "started"}},
	}}
	t0 := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	stories := []storyResponse{
		{ID: 1, Name: "oldest", WorkflowStateID: 1, UpdatedAt: t0},
		{ID: 2, Name: "newest", WorkflowStateID: 1, UpdatedAt: t0.Add(48 * time.Hour)},
		{ID: 3, Name: "middle", WorkflowStateID: 1, UpdatedAt: t0.Add(24 * time.Hour)},
	}
	srv := listAssignedServer(t, member, workflows, stories)
	defer srv.Close()

	got, err := New("tok", srv.URL).ListAssigned(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []string{"sc-2", "sc-3", "sc-1"}
	if len(got) != len(want) {
		t.Fatalf("got %d tickets, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].SourceID != w {
			t.Errorf("position %d: got %s, want %s", i, got[i].SourceID, w)
		}
	}
}

func TestListAssigned_tieredSortByStateThenUpdatedAt(t *testing.T) {
	// Three stories per tier — mixed UpdatedAt so we can confirm
	// the secondary key still works within a tier, and that tier
	// boundaries override UpdatedAt across tiers.
	member := memberResponse{ID: "u"}
	workflows := []workflowResponse{{
		States: []workflowStateResponse{
			{ID: 10, Name: "In Development", Type: "started"},         // tier 2
			{ID: 20, Name: "Doing Something Custom", Type: "started"}, // tier 1 (unknown name)
			{ID: 30, Name: "Paused", Type: "started"},                 // tier 0
		},
	}}
	t0 := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	stories := []storyResponse{
		// Paused (tier 0) — most recently touched of all, but
		// should still sort to the bottom.
		{ID: 1, Name: "paused-newest", WorkflowStateID: 30, UpdatedAt: t0.Add(100 * time.Hour)},
		// In Development (tier 2) — should land at the top even
		// though older than the paused one.
		{ID: 2, Name: "dev-older", WorkflowStateID: 10, UpdatedAt: t0.Add(10 * time.Hour)},
		// Custom state (tier 1) — middle.
		{ID: 3, Name: "custom-mid", WorkflowStateID: 20, UpdatedAt: t0.Add(50 * time.Hour)},
		// Another In Development — newer than dev-older, so
		// should sort first overall.
		{ID: 4, Name: "dev-newest", WorkflowStateID: 10, UpdatedAt: t0.Add(20 * time.Hour)},
	}
	srv := listAssignedServer(t, member, workflows, stories)
	defer srv.Close()

	got, err := New("tok", srv.URL).ListAssigned(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// Expected order:
	//   tier 2: dev-newest (newer UpdatedAt within tier), dev-older
	//   tier 1: custom-mid
	//   tier 0: paused-newest
	want := []string{"sc-4", "sc-2", "sc-3", "sc-1"}
	if len(got) != len(want) {
		t.Fatalf("got %d tickets, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].SourceID != w {
			t.Errorf("position %d: got %s (%s), want %s", i, got[i].SourceID, got[i].State, w)
		}
	}
}

func TestStateRank(t *testing.T) {
	cases := map[string]int{
		"In Development":        2,
		"in development":        2, // case-insensitive
		"  Backlog  ":           2, // trimmed
		"Ready for Development": 2,
		"Waiting for R&D":       2,
		"In Code Review":        0,
		"Waiting for CS":        0,
		"Paused":                0,
		"Custom Workflow State": 1, // unknown → neutral
		"":                      1,
	}
	for in, want := range cases {
		if got := stateRank(in); got != want {
			t.Errorf("stateRank(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestListAssigned_emptyStoriesNotAnError(t *testing.T) {
	srv := listAssignedServer(t,
		memberResponse{ID: "u"},
		[]workflowResponse{{States: []workflowStateResponse{{ID: 1, Name: "Dev", Type: "started"}}}},
		[]storyResponse{},
	)
	defer srv.Close()
	got, err := New("tok", srv.URL).ListAssigned(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d, want 0", len(got))
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
