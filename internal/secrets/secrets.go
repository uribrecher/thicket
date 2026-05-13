// Package secrets retrieves credentials from a user-chosen password
// manager (1Password, Bitwarden, pass, ...) rather than asking the user to
// paste raw values. The thicket config stores only an *item reference* per
// secret; the live value is fetched on demand by shelling out to the PM's
// CLI.
//
// Why not the OS keychain? Several users (including the maintainer) keep
// their everyday credentials in a password manager and explicitly do not
// want a tool to plant tokens elsewhere. This package treats the PM as the
// canonical source of truth and the config as a pointer table.
package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// jsonUnmarshal is a tiny indirection used to keep the import list clean
// (the package needs json only for OnePassword account listing).
var jsonUnmarshal = json.Unmarshal

// ErrNotFound means the manager couldn't resolve the given reference.
var ErrNotFound = errors.New("secret not found")

// ErrNotAuthenticated means the manager's CLI is installed but no active
// session — the user needs to sign in / unlock first.
var ErrNotAuthenticated = errors.New("password manager not signed in")

// ErrCLIMissing means the manager's CLI isn't on PATH at all.
var ErrCLIMissing = errors.New("password manager CLI not found on PATH")

// Manager fetches secrets by item reference from a password manager.
type Manager interface {
	// Name identifies the manager ("1password", "bitwarden", ...).
	Name() string
	// Get resolves ref → secret value. The exact form of ref depends on
	// the manager — see Describe() for the per-manager format hint.
	Get(ctx context.Context, ref string) (string, error)
	// Check verifies the CLI is installed and the manager is unlocked.
	Check(ctx context.Context) error
	// Describe returns a one-line hint for the user explaining what an
	// item ref looks like for this manager. Shown in the init wizard.
	Describe() string
}

// Runner abstracts command execution so tests don't shell out for real.
type Runner interface {
	Run(ctx context.Context, name string, args []string, stdin io.Reader) (stdout, stderr []byte, err error)
}

// LookPathFn resolves a binary name to an absolute path. Defaults to
// exec.LookPath; tests inject a stub so they don't depend on which CLIs
// are installed on the developer's machine.
type LookPathFn func(name string) (string, error)

// DefaultRunner shells out via os/exec.
type DefaultRunner struct{}

func (DefaultRunner) Run(ctx context.Context, name string, args []string, stdin io.Reader) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // we control name & args
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// alwaysFound is the LookPath stub tests inject when they want the
// presence check to pass.
func alwaysFound(name string) (string, error) { return "/usr/local/bin/" + name, nil }

// Supported is the canonical list of manager identifiers users can pick
// from in `thicket init`. Order is the display order.
var Supported = []string{"1password", "bitwarden", "pass", "env"}

// Options carries manager-specific construction parameters.
// Unset fields fall back to each manager's defaults.
type Options struct {
	// OnePasswordAccount selects which 1Password account to use (account
	// UUID or sign-in email). Ignored by other managers.
	OnePasswordAccount string
}

// New returns a Manager by identifier. Unknown identifiers return an error.
func New(id string, opts ...Options) (Manager, error) {
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	r := DefaultRunner{}
	switch id {
	case "1password":
		return &OnePassword{Runner: r, Account: o.OnePasswordAccount}, nil
	case "bitwarden":
		return &Bitwarden{Runner: r}, nil
	case "pass":
		return &Pass{Runner: r}, nil
	case "env":
		return &EnvOnly{}, nil
	default:
		return nil, fmt.Errorf("unknown password manager %q (supported: %s)",
			id, strings.Join(Supported, ", "))
	}
}

// ----- 1Password (op CLI) -----

// OnePassword reads secrets via `op read "op://<vault>/<item>/<field>"`.
// The op CLI handles Touch ID / biometric prompts itself; this code never
// sees the raw secret in transit.
//
// Account, when non-empty, is prepended as `--account <id>` to every op
// invocation so users with multiple signed-in 1Password accounts can pick
// the one that holds the items in question.
type OnePassword struct {
	Runner   Runner
	LookPath LookPathFn // defaults to exec.LookPath
	Account  string     // account UUID or sign-in email; empty = op default
}

func (OnePassword) Name() string { return "1password" }
func (OnePassword) Describe() string {
	return "1Password: op://<vault>/<item>/<field>  (e.g. op://Private/Shortcut/credential)"
}
func (p OnePassword) lookPath() LookPathFn {
	if p.LookPath != nil {
		return p.LookPath
	}
	return exec.LookPath
}

// opArgs prepends --account <id> when an account is set; otherwise returns
// the args unchanged.
func (p OnePassword) opArgs(subArgs ...string) []string {
	if p.Account == "" {
		return subArgs
	}
	out := make([]string, 0, len(subArgs)+2)
	out = append(out, "--account", p.Account)
	out = append(out, subArgs...)
	return out
}

func (p OnePassword) Check(ctx context.Context) error {
	if _, err := p.lookPath()("op"); err != nil {
		return ErrCLIMissing
	}
	// `op vault list` exits non-zero with a recognizable stderr if not
	// signed in. We don't actually care about the output.
	_, stderr, err := p.Runner.Run(ctx, "op", p.opArgs("vault", "list"), nil)
	if err != nil {
		s := strings.ToLower(string(stderr))
		if strings.Contains(s, "not signed in") || strings.Contains(s, "sign in") ||
			strings.Contains(s, "session") {
			return ErrNotAuthenticated
		}
		return fmt.Errorf("op vault list: %w (%s)", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

func (p OnePassword) Get(ctx context.Context, ref string) (string, error) {
	if !strings.HasPrefix(ref, "op://") {
		return "", fmt.Errorf("1password ref must start with op:// — got %q", ref)
	}
	stdout, stderr, err := p.Runner.Run(ctx, "op", p.opArgs("read", ref, "--no-newline"), nil)
	if err != nil {
		s := strings.ToLower(string(stderr))
		if strings.Contains(s, "not signed in") || strings.Contains(s, "session") {
			return "", ErrNotAuthenticated
		}
		if strings.Contains(s, "isn't an item") || strings.Contains(s, "not found") {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("op read %s: %w (%s)", ref, err, strings.TrimSpace(string(stderr)))
	}
	return string(stdout), nil
}

// ----- 1Password account discovery -----

// OnePasswordAccount is one row of `op account list --format json`.
type OnePasswordAccount struct {
	URL         string `json:"url"`
	Email       string `json:"email"`
	UserUUID    string `json:"user_uuid"`
	AccountUUID string `json:"account_uuid"`
}

// ListOnePasswordAccounts enumerates every 1Password account the local op
// CLI knows about. Used by `thicket init` to let the user pick one when
// more than one account is signed in.
func ListOnePasswordAccounts(ctx context.Context) ([]OnePasswordAccount, error) {
	return listOnePasswordAccounts(ctx, DefaultRunner{}, exec.LookPath)
}

// listOnePasswordAccounts is the testable inner of ListOnePasswordAccounts.
func listOnePasswordAccounts(ctx context.Context, r Runner, lookPath LookPathFn) ([]OnePasswordAccount, error) {
	if _, err := lookPath("op"); err != nil {
		return nil, ErrCLIMissing
	}
	stdout, stderr, err := r.Run(ctx, "op", []string{"account", "list", "--format", "json"}, nil)
	if err != nil {
		return nil, fmt.Errorf("op account list: %w (%s)", err, strings.TrimSpace(string(stderr)))
	}
	var accs []OnePasswordAccount
	if err := jsonUnmarshal(stdout, &accs); err != nil {
		return nil, fmt.Errorf("decode op accounts: %w", err)
	}
	return accs, nil
}

// ----- 1Password item discovery -----

// OnePasswordItem is one row of `op item list --format json`. The full
// item structure has many more fields; we keep only what the init wizard
// needs to render an autocomplete picker.
type OnePasswordItem struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Vault struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"vault"`
	Category string `json:"category"`
}

// OnePasswordField is one row of `op item get --format json`'s `fields`.
// `Reference` is the canonical op:// URI for that exact field — we store
// it verbatim in thicket's config so future fetches don't need to
// re-resolve via item id.
type OnePasswordField struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Type      string `json:"type"`    // STRING / CONCEALED / OTP / DATE / URL / ...
	Purpose   string `json:"purpose"` // USERNAME / PASSWORD / NOTES / ""
	Reference string `json:"reference"`
}

// OnePasswordItemDetail is the subset of `op item get --format json` the
// init wizard renders for field selection.
type OnePasswordItemDetail struct {
	ID     string             `json:"id"`
	Title  string             `json:"title"`
	Fields []OnePasswordField `json:"fields"`
}

// ListItems lists every item the chosen account can see, across vaults.
// Triggers a biometric/Touch ID prompt scoped to OnePassword.Account so
// the prompt shows the correct account name.
func (p OnePassword) ListItems(ctx context.Context) ([]OnePasswordItem, error) {
	stdout, stderr, err := p.Runner.Run(ctx, "op",
		p.opArgs("item", "list", "--format", "json"), nil)
	if err != nil {
		return nil, fmt.Errorf("op item list: %w (%s)", err, strings.TrimSpace(string(stderr)))
	}
	var items []OnePasswordItem
	if err := jsonUnmarshal(stdout, &items); err != nil {
		return nil, fmt.Errorf("decode op items: %w", err)
	}
	return items, nil
}

// GetItem fetches the full structure of one item, including its fields
// and their canonical op:// references. The id may be the item UUID or
// its title.
func (p OnePassword) GetItem(ctx context.Context, id string) (*OnePasswordItemDetail, error) {
	stdout, stderr, err := p.Runner.Run(ctx, "op",
		p.opArgs("item", "get", id, "--format", "json"), nil)
	if err != nil {
		return nil, fmt.Errorf("op item get %s: %w (%s)", id, err, strings.TrimSpace(string(stderr)))
	}
	var item OnePasswordItemDetail
	if err := jsonUnmarshal(stdout, &item); err != nil {
		return nil, fmt.Errorf("decode op item: %w", err)
	}
	return &item, nil
}

// ----- Bitwarden (bw CLI) -----

// Bitwarden uses `bw get password <ref>`. Requires `BW_SESSION` env var
// (from `bw unlock`) to be set.
type Bitwarden struct {
	Runner   Runner
	LookPath LookPathFn
}

func (Bitwarden) Name() string { return "bitwarden" }
func (Bitwarden) Describe() string {
	return "Bitwarden: item id or name (e.g. \"Shortcut API Token\"). Requires `bw unlock` first."
}
func (b Bitwarden) lookPath() LookPathFn {
	if b.LookPath != nil {
		return b.LookPath
	}
	return exec.LookPath
}

func (b Bitwarden) Check(ctx context.Context) error {
	if _, err := b.lookPath()("bw"); err != nil {
		return ErrCLIMissing
	}
	stdout, _, err := b.Runner.Run(ctx, "bw", []string{"status"}, nil)
	if err != nil {
		return fmt.Errorf("bw status: %w", err)
	}
	var st struct {
		Status string `json:"status"`
	}
	if err := jsonUnmarshal(stdout, &st); err != nil {
		return fmt.Errorf("decode bw status: %w", err)
	}
	if st.Status != "unlocked" {
		return ErrNotAuthenticated
	}
	return nil
}

func (b Bitwarden) Get(ctx context.Context, ref string) (string, error) {
	stdout, stderr, err := b.Runner.Run(ctx, "bw", []string{"get", "password", ref}, nil)
	if err != nil {
		s := strings.ToLower(string(stderr))
		if strings.Contains(s, "not found") {
			return "", ErrNotFound
		}
		if strings.Contains(s, "vault is locked") || strings.Contains(s, "session") {
			return "", ErrNotAuthenticated
		}
		return "", fmt.Errorf("bw get %s: %w (%s)", ref, err, strings.TrimSpace(string(stderr)))
	}
	return strings.TrimRight(string(stdout), "\n"), nil
}

// ----- pass (unix password store) -----

// Pass uses `pass show <path>`.
type Pass struct {
	Runner   Runner
	LookPath LookPathFn
}

func (Pass) Name() string { return "pass" }
func (Pass) Describe() string {
	return "pass: store-relative path (e.g. work/shortcut-token)"
}
func (p Pass) lookPath() LookPathFn {
	if p.LookPath != nil {
		return p.LookPath
	}
	return exec.LookPath
}

func (p Pass) Check(ctx context.Context) error {
	if _, err := p.lookPath()("pass"); err != nil {
		return ErrCLIMissing
	}
	// `pass ls` exits 0 even on an empty store as long as it's initialised.
	if _, _, err := p.Runner.Run(ctx, "pass", []string{"ls"}, nil); err != nil {
		return fmt.Errorf("pass ls: %w", err)
	}
	return nil
}

func (p Pass) Get(ctx context.Context, ref string) (string, error) {
	stdout, stderr, err := p.Runner.Run(ctx, "pass", []string{"show", ref}, nil)
	if err != nil {
		s := strings.ToLower(string(stderr))
		if strings.Contains(s, "is not in the password store") {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("pass show %s: %w (%s)", ref, err, strings.TrimSpace(string(stderr)))
	}
	// pass appends a newline; secrets shouldn't include it.
	return strings.TrimRight(string(stdout), "\n"), nil
}

// ----- env-var-only -----

// EnvOnly reads secrets from environment variables. The "ref" is the env
// var name. Provided for CI and headless setups where no PM exists.
type EnvOnly struct{}

func (EnvOnly) Name() string { return "env" }
func (EnvOnly) Describe() string {
	return "env: name of the environment variable (e.g. SHORTCUT_API_TOKEN)"
}

func (EnvOnly) Check(_ context.Context) error { return nil }

func (EnvOnly) Get(_ context.Context, ref string) (string, error) {
	v := os.Getenv(ref)
	if v == "" {
		return "", ErrNotFound
	}
	return v, nil
}

// ----- caching wrapper -----

// Cached memoizes Get calls for the lifetime of one CLI invocation, so a
// single `thicket start` only prompts the user (Touch ID etc.) once per
// secret even if multiple subsystems ask for the same ref.
type Cached struct {
	Inner Manager
	cache map[string]string
}

func NewCached(m Manager) *Cached { return &Cached{Inner: m, cache: map[string]string{}} }

func (c *Cached) Name() string                    { return c.Inner.Name() }
func (c *Cached) Describe() string                { return c.Inner.Describe() }
func (c *Cached) Check(ctx context.Context) error { return c.Inner.Check(ctx) }

func (c *Cached) Get(ctx context.Context, ref string) (string, error) {
	if v, ok := c.cache[ref]; ok {
		return v, nil
	}
	v, err := c.Inner.Get(ctx, ref)
	if err != nil {
		return "", err
	}
	c.cache[ref] = v
	return v, nil
}
