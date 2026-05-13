// Package shortcut implements the ticket.Source interface against the
// Shortcut REST API (https://developer.shortcut.com/api/rest/v3).
//
// Only the read paths needed by thicket are wired:
//   - GET /api/v3/stories/{public-id}
//
// Resolution of workflow state names and member mention names is deferred
// (those require extra calls and a cache); for now Ticket.State and
// Ticket.Owner are left empty when running against the live API.
package shortcut

import (
	"encoding/json"
	"errors"
	"fmt"
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
			n, _ := strconv.Atoi(m[1])
			return ID(n), nil
		}
		// URL but doesn't look like a Shortcut story URL.
		return nil, ticket.ErrUnparseable{Input: raw, Source: sourceName}
	}

	// id / sc-id form
	if m := numRegexp.FindStringSubmatch(in); m != nil {
		n, _ := strconv.Atoi(m[1])
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
}

// Fetch fetches the story by ID and projects it into a ticket.Ticket.
func (s *Source) Fetch(id ticket.ID) (ticket.Ticket, error) {
	scID, ok := id.(ID)
	if !ok {
		return ticket.Ticket{}, fmt.Errorf("shortcut.Fetch: id has wrong type %T", id)
	}
	u := fmt.Sprintf("%s/api/v3/stories/%d", s.base, int(scID))
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return ticket.Ticket{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Shortcut-Token", s.token)
	req.Header.Set("Accept", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		return ticket.Ticket{}, fmt.Errorf("shortcut request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return ticket.Ticket{}, fmt.Errorf("shortcut story %s not found", scID)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return ticket.Ticket{}, errors.New("shortcut: 401 unauthorized — verify your Shortcut token reference (run `thicket doctor` to re-test the fetch from your password manager)")
	}
	if resp.StatusCode/100 != 2 {
		return ticket.Ticket{}, fmt.Errorf("shortcut: HTTP %d for %s", resp.StatusCode, u)
	}

	var sr storyResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return ticket.Ticket{}, fmt.Errorf("decode shortcut response: %w", err)
	}
	return ticket.Ticket{
		SourceID: ID(sr.ID).String(),
		Title:    sr.Name,
		Body:     sr.Description,
		URL:      sr.AppURL,
		// State + Owner are intentionally empty until member/workflow
		// resolution is wired in a follow-up.
		Extra: map[string]string{
			"formatted_vcs_branch_name": sr.FormattedVCSBranchName,
			"workflow_state_id":         strconv.Itoa(sr.WorkflowStateID),
		},
	}, nil
}
