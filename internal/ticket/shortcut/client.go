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
	"sort"
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
	ID                     int             `json:"id"`
	Name                   string          `json:"name"`
	Description            string          `json:"description"`
	AppURL                 string          `json:"app_url"`
	FormattedVCSBranchName string          `json:"formatted_vcs_branch_name"`
	WorkflowStateID        int             `json:"workflow_state_id"`
	OwnerIDs               []string        `json:"owner_ids"`
	RequestedByID          string          `json:"requested_by_id"`
	Labels                 []labelResponse `json:"labels"`
	Archived               bool            `json:"archived"`
	UpdatedAt              time.Time       `json:"updated_at"`
	IterationID            *int            `json:"iteration_id"` // nil when not assigned
}

// iterationResponse is the slice of the Shortcut iteration payload
// the ranker needs to compute distance from "current".
type iterationResponse struct {
	ID        int       `json:"id"`
	Status    string    `json:"status"` // unstarted | started | done
	StartDate time.Time `json:"start_date"`
	EndDate   time.Time `json:"end_date"`
}

// labelResponse is the slice of the Shortcut label payload we surface.
type labelResponse struct {
	Name string `json:"name"`
}

// memberProfileResponse is the subset of GET /api/v3/members/{id} that we
// use to render a requester display name.
type memberProfileResponse struct {
	Profile struct {
		Name        string `json:"name"`
		MentionName string `json:"mention_name"`
	} `json:"profile"`
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
	ctx := context.Background()
	var sr storyResponse
	if err := s.doRequest(ctx, http.MethodGet,
		fmt.Sprintf("/api/v3/stories/%d", int(scID)), nil, &sr); err != nil {
		return ticket.Ticket{}, err
	}
	tk := s.toTicket(sr, "")
	// Best-effort requester name resolution — a failed lookup just
	// leaves Requester empty so the ticket summary skips that line.
	// We don't want a flaky members endpoint to abort `thicket start`.
	if sr.RequestedByID != "" {
		if name := s.fetchMemberName(ctx, sr.RequestedByID); name != "" {
			tk.Requester = name
		}
	}
	return tk, nil
}

// fetchMemberName resolves a member UUID to a human-readable display
// name (full name, falling back to mention handle). Returns "" on any
// error — callers should treat that as "unresolved".
func (s *Source) fetchMemberName(ctx context.Context, id string) string {
	var m memberProfileResponse
	if err := s.doRequest(ctx, http.MethodGet,
		fmt.Sprintf("/api/v3/members/%s", id), nil, &m); err != nil {
		return ""
	}
	if m.Profile.Name != "" {
		return m.Profile.Name
	}
	return m.Profile.MentionName
}

func (s *Source) toTicket(sr storyResponse, stateName string) ticket.Ticket {
	var labels []string
	for _, l := range sr.Labels {
		if l.Name != "" {
			labels = append(labels, l.Name)
		}
	}
	return ticket.Ticket{
		SourceID:          ID(sr.ID).String(),
		Title:             sr.Name,
		Body:              sr.Description,
		URL:               sr.AppURL,
		State:             stateName,
		Labels:            labels,
		UpdatedAt:         sr.UpdatedAt,
		IterationDistance: -1, // overwritten in ListAssigned when iteration data is available
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
	"ready for verification": true,
	"verifying":              true,
	"in verification":        true,
	"awaiting verification":  true,
	"qa":                     true,
	"ready for deploy":       true,
}

// buildIterationTimeline returns:
//
//   - distance: iteration ID → step from the current iteration.
//     0 = current, 1 = previous, etc.
//   - future:   set of iteration IDs later in the timeline than the
//     current one. Stories in these iterations are filtered out of
//     the picker.
//
// "Current" is the latest-StartDate iteration with status="started".
// Ties broken by EndDate asc, then ID asc.
//
// If no started iteration exists, returns empty maps — the caller
// then treats every IterationID as the sentinel (factor 0) and
// nothing is filtered.
func buildIterationTimeline(iters []iterationResponse) (distance map[int]int, future map[int]bool) {
	distance = make(map[int]int, len(iters))
	future = make(map[int]bool)
	if len(iters) == 0 {
		return distance, future
	}

	// Stable order: StartDate asc, EndDate asc, ID asc.
	ordered := make([]iterationResponse, len(iters))
	copy(ordered, iters)
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if !a.StartDate.Equal(b.StartDate) {
			return a.StartDate.Before(b.StartDate)
		}
		if !a.EndDate.Equal(b.EndDate) {
			return a.EndDate.Before(b.EndDate)
		}
		return a.ID < b.ID
	})

	// Find the latest-indexed "started" iteration — that's "current".
	currentIdx := -1
	for i, it := range ordered {
		if it.Status == "started" {
			currentIdx = i
		}
	}
	if currentIdx == -1 {
		return distance, future
	}

	for i, it := range ordered {
		switch {
		case i > currentIdx:
			future[it.ID] = true
		case i == currentIdx:
			distance[it.ID] = 0
		default:
			distance[it.ID] = currentIdx - i
		}
	}
	return distance, future
}

// ListAssigned returns the authenticated user's currently-active
// assigned tickets — excluding archived stories, anything in a
// workflow state of type "done", a handful of "out of dev hands"
// state names (Verifying, etc.), and any story in a future iteration.
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

	// Best-effort iteration fetch: if it fails, we proceed with an
	// empty timeline. Every story then gets IterationDistance=-1
	// (factor 0 at the ranker) and nothing is filtered as "future".
	// This keeps the picker functional even if Shortcut briefly 5xx's
	// or the auth token loses iteration scope. Error is silently
	// swallowed because this file doesn't take a logger today.
	var iterations []iterationResponse
	if err := s.doRequest(ctx, http.MethodGet, "/api/v3/iterations", nil, &iterations); err != nil {
		iterations = nil
	}
	distanceByIter, futureIter := buildIterationTimeline(iterations)

	var stories []storyResponse
	if err := s.doRequest(ctx, http.MethodPost, "/api/v3/stories/search",
		searchBody{OwnerIDs: []string{me.ID}, Archived: false}, &stories); err != nil {
		return nil, fmt.Errorf("search stories: %w", err)
	}

	// Filter: archived, done-by-type, excluded-by-name, future-iter.
	// We keep the resolved state name alongside the story so the
	// output loop doesn't re-map workflow IDs.
	type filtered struct {
		sr    storyResponse
		state string
	}
	kept := make([]filtered, 0, len(stories))
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
		if sr.IterationID != nil && futureIter[*sr.IterationID] {
			continue // future iteration — out of scope for the picker
		}
		kept = append(kept, filtered{sr, st.Name})
	}

	// No ordering here — the cross-source ranker
	// (internal/ticket/rank) imposes the picker order in the caller.
	// We hand stories back in whatever order Shortcut returned them;
	// rank.Sort is stable, so identical-score tickets preserve that
	// order.
	out := make([]ticket.Ticket, 0, len(kept))
	for _, k := range kept {
		tk := s.toTicket(k.sr, k.state)
		if k.sr.IterationID != nil {
			if d, ok := distanceByIter[*k.sr.IterationID]; ok {
				tk.IterationDistance = d
			}
			// If the iteration isn't in our timeline (e.g. archived
			// after the workflow fetch), tk.IterationDistance stays
			// at the toTicket default of -1.
		}
		out = append(out, tk)
	}
	return out, nil
}
