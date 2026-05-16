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
			{ID: 102, Name: "In Review", Type: "started"}, // NO LONGER excluded
			{ID: 103, Name: "Verifying", Type: "started"}, // still excluded by name
			{ID: 104, Name: "Completed", Type: "done"},
		},
	}}
	stories := []storyResponse{
		{ID: 1, Name: "active dev", WorkflowStateID: 101},
		{ID: 2, Name: "in review", WorkflowStateID: 102}, // surfaced as neutral
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

	// Order is not asserted — the source no longer sorts; callers
	// (cmd/thicket/start.go and the wizard) apply rank.Sort.
	gotByID := make(map[string]string, len(got))
	for _, tk := range got {
		gotByID[tk.SourceID] = tk.State
	}
	want := map[string]string{
		"sc-1": "In Development",
		"sc-2": "In Review",
		"sc-6": "Ready for Dev",
	}
	if len(gotByID) != len(want) {
		t.Fatalf("got %d tickets %+v, want %d %+v", len(gotByID), gotByID, len(want), want)
	}
	for id, wantState := range want {
		if got := gotByID[id]; got != wantState {
			t.Errorf("%s: state=%q, want %q", id, got, wantState)
		}
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

func TestListAssigned_setsUpdatedAtAndIterationDistanceDefault(t *testing.T) {
	member := memberResponse{ID: "u"}
	workflows := []workflowResponse{{
		States: []workflowStateResponse{{ID: 1, Name: "In Development", Type: "started"}},
	}}
	updated := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	stories := []storyResponse{
		{ID: 1, Name: "t", WorkflowStateID: 1, UpdatedAt: updated},
	}
	srv := listAssignedServer(t, member, workflows, stories)
	defer srv.Close()

	got, err := New("tok", srv.URL).ListAssigned(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d tickets, want 1", len(got))
	}
	if !got[0].UpdatedAt.Equal(updated) {
		t.Errorf("UpdatedAt = %v, want %v", got[0].UpdatedAt, updated)
	}
	if got[0].IterationDistance != -1 {
		t.Errorf("IterationDistance = %d, want -1 (sentinel for no iteration)",
			got[0].IterationDistance)
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
