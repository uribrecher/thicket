// Package config loads, validates, and persists thicket's user configuration.
//
// The configuration lives at $XDG_CONFIG_HOME/thicket/config.toml (typically
// ~/.config/thicket/config.toml on Linux and macOS). Secrets are NOT stored
// in this file by default — they belong in the OS keychain (see secrets.go),
// with env vars and an explicit [secrets] table as fallbacks.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/adrg/xdg"
)

// ErrNoConfig is returned by Load when the config file does not exist.
var ErrNoConfig = errors.New("config file not found")

// Config mirrors the TOML on disk. Keep it flat where reasonable; nested
// tables only when grouping is meaningful to the user.
type Config struct {
	ReposRoot     string `toml:"repos_root"`
	WorkspaceRoot string `toml:"workspace_root"`
	DefaultBranch string `toml:"default_branch"`
	ClaudeModel   string `toml:"claude_model"`
	ClaudeBinary  string `toml:"claude_binary"`

	TicketSource string   `toml:"ticket_source"`
	GithubOrgs   []string `toml:"github_orgs"`

	Shortcut    ShortcutConfig `toml:"shortcut"`
	RepoAliases []RepoAlias    `toml:"repo_alias"`

	// Secrets is a plain-text fallback ONLY. The keychain is preferred and
	// env vars are a secondary fallback. If non-empty, Load emits a warning.
	Secrets SecretsConfig `toml:"secrets,omitempty"`
}

type ShortcutConfig struct {
	WorkspaceSlug string `toml:"workspace_slug"`
}

type RepoAlias struct {
	Name    string   `toml:"name"`
	Aliases []string `toml:"aliases"`
}

type SecretsConfig struct {
	ShortcutAPIToken string `toml:"shortcut_api_token,omitempty"`
	AnthropicAPIKey  string `toml:"anthropic_api_key,omitempty"`
}

// Default returns a Config pre-filled with the defaults the init wizard
// presents to the user. Callers are still expected to override repos_root,
// workspace_root, and github_orgs before persisting.
func Default() Config {
	return Config{
		ReposRoot:     "~/code",
		WorkspaceRoot: "~/tasks",
		DefaultBranch: "main",
		ClaudeModel:   "claude-haiku-4-5",
		ClaudeBinary:  "claude",
		TicketSource:  "shortcut",
		GithubOrgs:    nil,
		Shortcut:      ShortcutConfig{},
	}
}

// Path returns the canonical config-file path (it may not exist yet).
func Path() (string, error) {
	dir := filepath.Join(xdg.ConfigHome, "thicket")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create config dir: %w", err)
	}
	return filepath.Join(dir, "config.toml"), nil
}

// Load reads the config from path, expands ~ in path fields, and validates
// the result. Returns ErrNoConfig if the file is missing — callers can react
// (e.g. point the user at `thicket init`).
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNoConfig
		}
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if _, err := toml.Decode(string(b), &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := c.expandPaths(); err != nil {
		return nil, fmt.Errorf("expand paths: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Save writes the config to path with 0600 perms, atomically.
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open tmp config: %w", err)
	}
	if err := toml.NewEncoder(f).Encode(c); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("encode config: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close tmp config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename tmp config: %w", err)
	}
	return nil
}

// Validate enforces required fields and shapes that other packages rely on.
func (c *Config) Validate() error {
	var problems []string
	if c.ReposRoot == "" {
		problems = append(problems, "repos_root is required")
	}
	if c.WorkspaceRoot == "" {
		problems = append(problems, "workspace_root is required")
	}
	if c.TicketSource == "" {
		problems = append(problems, "ticket_source is required (e.g. \"shortcut\")")
	}
	if len(c.GithubOrgs) == 0 {
		problems = append(problems, "github_orgs must list at least one GitHub org")
	}
	if c.ClaudeModel == "" {
		problems = append(problems, "claude_model is required")
	}
	if len(problems) > 0 {
		return fmt.Errorf("invalid config:\n  - %s\nrun `thicket init` to set these up",
			strings.Join(problems, "\n  - "))
	}
	return nil
}

func (c *Config) expandPaths() error {
	var err error
	c.ReposRoot, err = expand(c.ReposRoot)
	if err != nil {
		return err
	}
	c.WorkspaceRoot, err = expand(c.WorkspaceRoot)
	return err
}

func expand(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve $HOME: %w", err)
		}
		if p == "~" {
			return home, nil
		}
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}
