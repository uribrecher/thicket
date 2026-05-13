package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestKeyringStore_setGet(t *testing.T) {
	keyring.MockInit()
	s := KeyringStore{}
	if err := s.Set("foo", "bar"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := s.Get("foo")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "bar" {
		t.Errorf("got %q, want %q", got, "bar")
	}
}

func TestKeyringStore_missingKey(t *testing.T) {
	keyring.MockInit()
	_, err := KeyringStore{}.Get("never-set")
	if !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("want ErrSecretNotFound, got %v", err)
	}
}

func TestEnvStore_getsValueFromEnv(t *testing.T) {
	t.Setenv("THICKET_TEST_TOKEN", "value-from-env")
	s := EnvStore{EnvVars: map[string]string{"k": "THICKET_TEST_TOKEN"}}
	got, err := s.Get("k")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "value-from-env" {
		t.Errorf("got %q", got)
	}
	if err := s.Set("k", "x"); err == nil {
		t.Error("expected Set on EnvStore to fail")
	}
}

func TestEnvStore_unmappedKey(t *testing.T) {
	_, err := EnvStore{EnvVars: nil}.Get("missing")
	if !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("want ErrSecretNotFound, got %v", err)
	}
}

func TestConfigFileStore_returnsFromSecretsTable(t *testing.T) {
	s := ConfigFileStore{Secrets: SecretsConfig{ShortcutAPIToken: "abc"}}
	got, err := s.Get(SecretShortcutAPIToken)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "abc" {
		t.Errorf("got %q", got)
	}
	if _, err := s.Get(SecretAnthropicAPIKey); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("want ErrSecretNotFound for missing key, got %v", err)
	}
}

func TestChainStore_fallsThroughInOrder(t *testing.T) {
	keyring.MockInit()
	// keychain empty, env has it
	t.Setenv("MY_TOKEN", "from-env")
	chain := ChainStore{Stores: []SecretStore{
		KeyringStore{},
		EnvStore{EnvVars: map[string]string{"my-key": "MY_TOKEN"}},
		ConfigFileStore{Secrets: SecretsConfig{AnthropicAPIKey: "ignored"}},
	}}
	got, err := chain.Get("my-key")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "from-env" {
		t.Errorf("got %q, want %q", got, "from-env")
	}
}

func TestChainStore_setWritesToFirstWritable(t *testing.T) {
	keyring.MockInit()
	chain := ChainStore{Stores: []SecretStore{
		EnvStore{EnvVars: map[string]string{}},
		KeyringStore{},
		ConfigFileStore{},
	}}
	if err := chain.Set("k", "v"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := KeyringStore{}.Get("k")
	if err != nil {
		t.Fatalf("keychain get after chain set: %v", err)
	}
	if got != "v" {
		t.Errorf("got %q, want %q", got, "v")
	}
}

func TestChainStore_allMissingYieldsNotFound(t *testing.T) {
	keyring.MockInit()
	chain := ChainStore{Stores: []SecretStore{KeyringStore{}, EnvStore{EnvVars: map[string]string{}}}}
	_, err := chain.Get("nope")
	if !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("want ErrSecretNotFound, got %v", err)
	}
}

func TestDefaultStore_chainsKeyringEnvAndConfig(t *testing.T) {
	keyring.MockInit()
	cfg := &Config{Secrets: SecretsConfig{AnthropicAPIKey: "from-file"}}
	s := DefaultStore(cfg)
	chain, ok := s.(ChainStore)
	if !ok {
		t.Fatalf("expected ChainStore, got %T", s)
	}
	if len(chain.Stores) != 3 {
		t.Fatalf("expected 3 stores, got %d", len(chain.Stores))
	}
	names := []string{}
	for _, st := range chain.Stores {
		names = append(names, st.Name())
	}
	got := strings.Join(names, ",")
	if got != "keychain,env,config-file" {
		t.Errorf("store order: got %q, want %q", got, "keychain,env,config-file")
	}
}
