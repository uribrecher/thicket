package wizard

import (
	"context"

	"github.com/uribrecher/thicket/internal/config"
)

// ConfigDeps wires the config wizard to the rest of thicket. Same
// one-way dependency pattern as Deps / EditDeps — the wizard never
// imports cmd/thicket.
type ConfigDeps struct {
	Ctx context.Context

	// Cfg is the *working* config the wizard mutates as the user
	// answers each page. On first run callers seed it with
	// config.Default(); on re-run they seed it with the loaded config
	// so existing values pre-fill each field.
	Cfg *config.Config

	// FirstRun controls whether the Welcome page is included.
	FirstRun bool
}

// ConfigResult is what config.Run hands back. The actual file write
// happens post-wizard in cmd/thicket/config.go — same separation as
// workspace.Create after start.Run and workspace.Add after edit.Run.
type ConfigResult struct {
	// Cfg is the populated config the caller should validate + save.
	// nil when the user cancelled.
	Cfg *config.Config

	// Confirmed is true only when the user reached the Submit page
	// and hit Confirm. If false (e.g. Esc), the caller should not
	// save.
	Confirmed bool
}
