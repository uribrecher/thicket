package shortcut

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

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

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
