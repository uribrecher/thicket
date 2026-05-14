// Package ticket defines the Source interface that any ticket provider
// (Shortcut, Linear, Jira, GitHub Issues...) must implement, plus a small
// Ticket data type that everything downstream consumes.
package ticket

import (
	"context"
	"fmt"
)

// Lister is the optional capability a Source exposes when it can
// enumerate the tickets currently assigned to the authenticated user
// in an "active" state (e.g. not closed, not in review). When a Source
// also implements Lister, `thicket start` (with no id arg) opens a
// fuzzy-search picker over the results.
type Lister interface {
	ListAssigned(ctx context.Context) ([]Ticket, error)
}

// ID is an opaque identifier for one ticket, canonical to its source.
// Implementations stringify to the form humans paste back into the CLI
// (e.g. "sc-12345").
type ID interface {
	String() string
}

// Source fetches tickets and answers a few questions about them.
type Source interface {
	// Name is the source's short identifier ("shortcut", "linear", ...).
	Name() string

	// Parse extracts a ticket ID from a raw user input — id, prefixed id,
	// or full URL. Returns ErrUnparseable if the input doesn't look like a
	// ticket of this source.
	Parse(idOrURL string) (ID, error)

	// Fetch returns the full ticket. Wraps the underlying provider error.
	Fetch(id ID) (Ticket, error)

	// BranchName returns the branch name the source itself suggests for a
	// ticket, if any. Empty string means "no opinion — caller must derive
	// one from title + id."
	BranchName(t Ticket) string
}

// Ticket is the source-agnostic projection of one issue/story/whatever.
type Ticket struct {
	SourceID  string            // e.g. "sc-12345"
	Title     string            // single-line title
	Body      string            // markdown description
	URL       string            // canonical web URL
	State     string            // workflow state name; "" if not resolved
	Owner     string            // mention name / handle; "" if not resolved
	Requester string            // display name of whoever filed the ticket; "" if not resolved
	Labels    []string          // ticket labels in source order; nil if none
	Extra     map[string]string // source-specific extras
}

// ErrUnparseable indicates the raw input could not be parsed as a ticket
// reference for this source. Callers should fall through to the next
// candidate source (if any) before failing.
type ErrUnparseable struct {
	Input  string
	Source string
}

func (e ErrUnparseable) Error() string {
	return fmt.Sprintf("not a %s ticket id: %q", e.Source, e.Input)
}
