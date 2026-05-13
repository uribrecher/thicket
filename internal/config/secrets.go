package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/zalando/go-keyring"
)

// Secret keys stored in the keychain under service "thicket".
const (
	SecretShortcutAPIToken = "shortcut-api-token"
	SecretAnthropicAPIKey  = "anthropic-api-key"

	keychainService = "thicket"
)

// ErrSecretNotFound is returned when no store yields a value for a key.
var ErrSecretNotFound = errors.New("secret not found")

// SecretStore reads and writes individual secret values.
type SecretStore interface {
	Get(key string) (string, error)
	Set(key, value string) error
	// Name returns a human-readable label for diagnostics ("keychain",
	// "env", "config-file"). Useful in `thicket doctor`.
	Name() string
}

// KeyringStore stores secrets in the OS keychain.
type KeyringStore struct{}

func (KeyringStore) Name() string { return "keychain" }

func (KeyringStore) Get(key string) (string, error) {
	v, err := keyring.Get(keychainService, key)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", ErrSecretNotFound
		}
		return "", fmt.Errorf("keychain get %q: %w", key, err)
	}
	return v, nil
}

func (KeyringStore) Set(key, value string) error {
	if err := keyring.Set(keychainService, key, value); err != nil {
		return fmt.Errorf("keychain set %q: %w", key, err)
	}
	return nil
}

// EnvStore is read-only — it reflects env vars set by the user/shell.
type EnvStore struct {
	// Mapping from secret key to env var name.
	EnvVars map[string]string
}

// DefaultEnvStore maps thicket's known secret keys to conventional env vars.
func DefaultEnvStore() EnvStore {
	return EnvStore{EnvVars: map[string]string{
		SecretShortcutAPIToken: "SHORTCUT_API_TOKEN",
		SecretAnthropicAPIKey:  "ANTHROPIC_API_KEY",
	}}
}

func (EnvStore) Name() string { return "env" }

func (e EnvStore) Get(key string) (string, error) {
	name, ok := e.EnvVars[key]
	if !ok {
		return "", ErrSecretNotFound
	}
	v := os.Getenv(name)
	if v == "" {
		return "", ErrSecretNotFound
	}
	return v, nil
}

// Set is unsupported on EnvStore — env vars are owned by the shell.
func (EnvStore) Set(_, _ string) error {
	return errors.New("EnvStore is read-only; set the env var in your shell instead")
}

// ConfigFileStore is a read-only view over the plain-text [secrets] table.
// Provided as a last-resort fallback for environments where the OS keychain
// is unavailable (e.g. headless Linux without libsecret). Writes are
// refused — secrets stored in the config file should be edited there
// deliberately.
type ConfigFileStore struct {
	Secrets SecretsConfig
}

func (ConfigFileStore) Name() string { return "config-file" }

func (c ConfigFileStore) Get(key string) (string, error) {
	switch key {
	case SecretShortcutAPIToken:
		if c.Secrets.ShortcutAPIToken != "" {
			return c.Secrets.ShortcutAPIToken, nil
		}
	case SecretAnthropicAPIKey:
		if c.Secrets.AnthropicAPIKey != "" {
			return c.Secrets.AnthropicAPIKey, nil
		}
	}
	return "", ErrSecretNotFound
}

func (ConfigFileStore) Set(_, _ string) error {
	return errors.New("ConfigFileStore is read-only; edit the [secrets] table by hand")
}

// ChainStore tries each store in order on Get; writes go to the first
// writable store (skipping read-only stores like EnvStore).
type ChainStore struct {
	Stores []SecretStore
}

func (ChainStore) Name() string { return "chain" }

func (c ChainStore) Get(key string) (string, error) {
	var lastErr error
	for _, s := range c.Stores {
		v, err := s.Get(key)
		if err == nil {
			return v, nil
		}
		if !errors.Is(err, ErrSecretNotFound) {
			lastErr = err
		}
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", ErrSecretNotFound
}

func (c ChainStore) Set(key, value string) error {
	for _, s := range c.Stores {
		err := s.Set(key, value)
		if err == nil {
			return nil
		}
		// Skip read-only stores silently and try the next one.
		if isReadOnly(err) {
			continue
		}
		return err
	}
	return errors.New("no writable secret store in chain")
}

func isReadOnly(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return msg == "EnvStore is read-only; set the env var in your shell instead" ||
		msg == "ConfigFileStore is read-only; edit the [secrets] table by hand"
}

// DefaultStore returns the standard secret store: keychain → env → config-file.
// The Config is only used for the read-only ConfigFileStore tail.
func DefaultStore(c *Config) SecretStore {
	stores := []SecretStore{
		KeyringStore{},
		DefaultEnvStore(),
	}
	if c != nil {
		stores = append(stores, ConfigFileStore{Secrets: c.Secrets})
	}
	return ChainStore{Stores: stores}
}
