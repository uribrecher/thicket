// Package shortcut implements the ticket.Source interface against the
// Shortcut REST API (https://developer.shortcut.com/api/rest/v3).
//
// Wired endpoints:
//   - GET  /api/v3/stories/{public-id}
//   - GET  /api/v3/member        (current authenticated user)
//   - GET  /api/v3/workflows     (state id → name/type lookup)
//   - POST /api/v3/stories/search
//
// Resolution of member mention names is deferred (would require a
// per-member lookup); Ticket.Owner is empty for now.
package shortcut

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/uribrecher/thicket/internal/ticket"
)

const (
	defaultBaseURL = "https://api.app.shortcut.com"
	sourceName     = "shortcut"
)

// Source is the Shortcut implementation of ticket.Source.
type Source struct {
	token string
	base  string
	http  *http.Client
}

// New creates a Shortcut Source with sensible defaults. The token is sent
// as the Shortcut-Token header. baseURL may be empty (uses production); the
// httptest tests inject a test server URL here.
func New(token, baseURL string) *Source {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Source{
		token: token,
		base:  strings.TrimRight(baseURL, "/"),
		http:  &http.Client{Timeout: 15 * time.Second},
	}
}

func (s *Source) Name() string { return sourceName }

// ID is a Shortcut story public ID.
type ID int

func (i ID) String() string { return fmt.Sprintf("sc-%d", int(i)) }

// urlRegexp matches the path segment of a Shortcut story URL.
//
//	https://app.shortcut.com/<workspace>/story/12345/some-slug
//	https://app.shortcut.com/<workspace>/story/12345
var urlRegexp = regexp.MustCompile(`/story/(\d+)(?:/|$)`)

// numRegexp matches a bare or prefixed numeric id.
var numRegexp = regexp.MustCompile(`^(?i)(?:sc[-_]?)?(\d+)$`)

// Parse accepts:
//
//	12345               → ID(12345)
//	sc-12345            → ID(12345)
//	SC_12345            → ID(12345)
//	https://app.shortcut.com/.../story/12345/...   → ID(12345)
func (s *Source) Parse(raw string) (ticket.ID, error) {
	in := strings.TrimSpace(raw)
	if in == "" {
		return nil, ticket.ErrUnparseable{Input: raw, Source: sourceName}
	}

	// URL form
	if u, err := url.Parse(in); err == nil && u.Scheme != "" && u.Host != "" {
		if m := urlRegexp.FindStringSubmatch(u.Path); m != nil {
			n, atoiErr := strconv.Atoi(m[1])
			if atoiErr != nil {
				return nil, ticket.ErrUnparseable{Input: raw, Source: sourceName}
			}
			return ID(n), nil
		}
		// URL but doesn't look like a Shortcut story URL.
		return nil, ticket.ErrUnparseable{Input: raw, Source: sourceName}
	}

	// id / sc-id form
	if m := numRegexp.FindStringSubmatch(in); m != nil {
		n, atoiErr := strconv.Atoi(m[1])
		if atoiErr != nil {
			return nil, ticket.ErrUnparseable{Input: raw, Source: sourceName}
		}
		return ID(n), nil
	}

	return nil, ticket.ErrUnparseable{Input: raw, Source: sourceName}
}

func (s *Source) BranchName(t ticket.Ticket) string {
	if t.Extra == nil {
		return ""
	}
	return t.Extra["formatted_vcs_branch_name"]
}

// storyResponse is the subset of the Shortcut story payload thicket consumes.
type storyResponse struct {
	ID                     int      `json:"id"`
	Name                   string   `json:"name"`
	Description            string   `json:"description"`
	AppURL                 string   `json:"app_url"`
	FormattedVCSBranchName string   `json:"formatted_vcs_branch_name"`
	WorkflowStateID        int      `json:"workflow_state_id"`
	OwnerIDs               []string `json:"owner_ids"`
	Archived               bool     `json:"archived"`
}

// doRequest is the shared HTTP helper. method ∈ {GET, POST}; body may be nil.
// `out` may be nil to discard the response.
func (s *Source) doRequest(ctx context.Context, method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.base+path, reqBody)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Shortcut-Token", s.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("shortcut request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized {
		return errors.New("shortcut: 401 unauthorized — verify your Shortcut token reference (run `thicket doctor` to re-test the fetch from your password manager)")
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("shortcut: not found (404) for %s", path)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("shortcut: HTTP %d for %s", resp.StatusCode, path)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode shortcut response: %w", err)
	}
	return nil
}

// Fetch fetches the story by ID and projects it into a ticket.Ticket.
func (s *Source) Fetch(id ticket.ID) (ticket.Ticket, error) {
	scID, ok := id.(ID)
	if !ok {
		return ticket.Ticket{}, fmt.Errorf("shortcut.Fetch: id has wrong type %T", id)
	}
	var sr storyResponse
	if err := s.doRequest(context.Background(), http.MethodGet,
		fmt.Sprintf("/api/v3/stories/%d", int(scID)), nil, &sr); err != nil {
		return ticket.Ticket{}, err
	}
	return s.toTicket(sr, ""), nil
}

func (s *Source) toTicket(sr storyResponse, stateName string) ticket.Ticket {
	return ticket.Ticket{
		SourceID: ID(sr.ID).String(),
		Title:    sr.Name,
		Body:     sr.Description,
		URL:      sr.AppURL,
		State:    stateName,
		Extra: map[string]string{
			"formatted_vcs_branch_name": sr.FormattedVCSBranchName,
			"workflow_state_id":         strconv.Itoa(sr.WorkflowStateID),
		},
	}
}

// ----- ListAssigned -----

type memberResponse struct {
	ID string `json:"id"`
}

type workflowResponse struct {
	States []workflowStateResponse `json:"states"`
}

type workflowStateResponse struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"` // unstarted | started | done | backlog
}

type searchBody struct {
	OwnerIDs []string `json:"owner_ids"`
	Archived bool     `json:"archived"`
}

// excludedStateNames is the case-insensitive list of state names the
// ListAssigned picker hides by default — typical "out of the dev's
// hands" stages. Custom workflow naming can override this in a future
// config field; for now it's hardcoded to the common Shortcut defaults.
var excludedStateNames = map[string]bool{
	"in review":              true,
	"ready for verification": true,
	"verifying":              true,
	"in verification":        true,
	"awaiting verification":  true,
	"qa":                     true,
	"ready for deploy":       true,
}

// ListAssigned returns the authenticated user's currently-active
// assigned tickets — excluding archived stories, anything in a
// workflow state of type "done", and a handful of "out of dev hands"
// state names (In Review, Verifying, etc.).
func (s *Source) ListAssigned(ctx context.Context) ([]ticket.Ticket, error) {
	var me memberResponse
	if err := s.doRequest(ctx, http.MethodGet, "/api/v3/member", nil, &me); err != nil {
		return nil, fmt.Errorf("fetch current shortcut member: %w", err)
	}

	var workflows []workflowResponse
	if err := s.doRequest(ctx, http.MethodGet, "/api/v3/workflows", nil, &workflows); err != nil {
		return nil, fmt.Errorf("fetch workflows: %w", err)
	}
	type stateInfo struct{ Name, Type string }
	stateByID := make(map[int]stateInfo)
	for _, w := range workflows {
		for _, st := range w.States {
			stateByID[st.ID] = stateInfo{st.Name, st.Type}
		}
	}

	var stories []storyResponse
	if err := s.doRequest(ctx, http.MethodPost, "/api/v3/stories/search",
		searchBody{OwnerIDs: []string{me.ID}, Archived: false}, &stories); err != nil {
		return nil, fmt.Errorf("search stories: %w", err)
	}

	out := make([]ticket.Ticket, 0, len(stories))
	for _, sr := range stories {
		if sr.Archived {
			continue
		}
		st, ok := stateByID[sr.WorkflowStateID]
		if !ok || st.Type == "done" {
			continue
		}
		if excludedStateNames[strings.ToLower(st.Name)] {
			continue
		}
		out = append(out, s.toTicket(sr, st.Name))
	}
	return out, nil
}
