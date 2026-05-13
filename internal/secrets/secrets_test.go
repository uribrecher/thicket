package secrets

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// fakeRunner returns a canned response and records calls.
type fakeRunner struct {
	stdout []byte
	stderr []byte
	err    error

	calls []call
}
type call struct {
	name string
	args []string
}

func (f *fakeRunner) Run(_ context.Context, name string, args []string, _ io.Reader) ([]byte, []byte, error) {
	f.calls = append(f.calls, call{name, args})
	return f.stdout, f.stderr, f.err
}

func TestSupported_includesAllManagers(t *testing.T) {
	for _, id := range Supported {
		if _, err := New(id); err != nil {
			t.Errorf("New(%q): %v", id, err)
		}
	}
}

func TestNew_unknownIDErrors(t *testing.T) {
	_, err := New("rot13")
	if err == nil {
		t.Fatal("expected error for unknown manager")
	}
}

func TestOnePassword_Get_passesOpReadArgs(t *testing.T) {
	fr := &fakeRunner{stdout: []byte("the-secret")}
	p := OnePassword{Runner: fr}
	v, err := p.Get(context.Background(), "op://Personal/Shortcut/credential")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v != "the-secret" {
		t.Errorf("got %q", v)
	}
	if len(fr.calls) != 1 {
		t.Fatalf("calls = %d", len(fr.calls))
	}
	c := fr.calls[0]
	if c.name != "op" || len(c.args) != 3 || c.args[0] != "read" ||
		c.args[1] != "op://Personal/Shortcut/credential" || c.args[2] != "--no-newline" {
		t.Errorf("args = %v", c.args)
	}
}

func TestOnePassword_Get_prependsAccountFlag(t *testing.T) {
	fr := &fakeRunner{stdout: []byte("x")}
	p := OnePassword{Runner: fr, Account: "uri.brecher@gmail.com"}
	if _, err := p.Get(context.Background(), "op://x/y/z"); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(fr.calls[0].args, " ")
	want := "--account uri.brecher@gmail.com read op://x/y/z --no-newline"
	if got != want {
		t.Errorf("args = %q, want %q", got, want)
	}
}

func TestOnePassword_Check_prependsAccountFlag(t *testing.T) {
	fr := &fakeRunner{}
	p := OnePassword{Runner: fr, LookPath: alwaysFound, Account: "576UUGKY"}
	if err := p.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(fr.calls[0].args, " ")
	want := "--account 576UUGKY vault list"
	if got != want {
		t.Errorf("args = %q, want %q", got, want)
	}
}

func TestListOnePasswordAccounts_parsesOpJSON(t *testing.T) {
	fr := &fakeRunner{stdout: []byte(`[
		{"url":"my.1password.com","email":"a@x.com","user_uuid":"U1","account_uuid":"A1"},
		{"url":"sentra.1password.com","email":"b@y.com","user_uuid":"U2","account_uuid":"A2"}
	]`)}
	accs, err := listOnePasswordAccounts(context.Background(), fr, alwaysFound)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(accs) != 2 {
		t.Fatalf("got %d", len(accs))
	}
	if accs[0].Email != "a@x.com" || accs[0].AccountUUID != "A1" {
		t.Errorf("first row mismatched: %+v", accs[0])
	}
	if accs[1].URL != "sentra.1password.com" {
		t.Errorf("second row mismatched: %+v", accs[1])
	}
	got := strings.Join(fr.calls[0].args, " ")
	if got != "account list --format json" {
		t.Errorf("args = %q", got)
	}
}

func TestListOnePasswordAccounts_cliMissing(t *testing.T) {
	notFound := func(string) (string, error) { return "", errors.New("not found") }
	_, err := listOnePasswordAccounts(context.Background(), &fakeRunner{}, notFound)
	if !errors.Is(err, ErrCLIMissing) {
		t.Errorf("want ErrCLIMissing, got %v", err)
	}
}

func TestNew_withOptions_threadsOnePasswordAccount(t *testing.T) {
	m, err := New("1password", Options{OnePasswordAccount: "uuid-123"})
	if err != nil {
		t.Fatal(err)
	}
	op, ok := m.(*OnePassword)
	if !ok {
		t.Fatalf("expected *OnePassword, got %T", m)
	}
	if op.Account != "uuid-123" {
		t.Errorf("Account = %q", op.Account)
	}
}

func TestOnePassword_Get_rejectsNonOpRef(t *testing.T) {
	p := OnePassword{Runner: &fakeRunner{}}
	_, err := p.Get(context.Background(), "shortcut-token")
	if err == nil {
		t.Fatal("expected error for non-op:// ref")
	}
}

func TestOnePassword_Get_mapsNotSignedIn(t *testing.T) {
	fr := &fakeRunner{stderr: []byte("[ERROR] you are not signed in"), err: errors.New("exit 1")}
	_, err := OnePassword{Runner: fr}.Get(context.Background(), "op://x/y/z")
	if !errors.Is(err, ErrNotAuthenticated) {
		t.Errorf("want ErrNotAuthenticated, got %v", err)
	}
}

func TestOnePassword_Get_mapsNotFound(t *testing.T) {
	fr := &fakeRunner{stderr: []byte(`"foo" isn't an item`), err: errors.New("exit 1")}
	_, err := OnePassword{Runner: fr}.Get(context.Background(), "op://x/y/z")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestBitwarden_Check_unlocked(t *testing.T) {
	fr := &fakeRunner{stdout: []byte(`{"status":"unlocked","userEmail":"u@e.com"}`)}
	if err := (Bitwarden{Runner: fr, LookPath: alwaysFound}).Check(context.Background()); err != nil {
		t.Errorf("check: %v", err)
	}
}

func TestBitwarden_Check_locked(t *testing.T) {
	fr := &fakeRunner{stdout: []byte(`{"status":"locked"}`)}
	err := Bitwarden{Runner: fr, LookPath: alwaysFound}.Check(context.Background())
	if !errors.Is(err, ErrNotAuthenticated) {
		t.Errorf("want ErrNotAuthenticated, got %v", err)
	}
}

// Make sure whitespace/pretty-printed JSON parses correctly — `bw status`
// formatting varies across versions and the previous substring-matching
// implementation tripped on the pretty-printed form.
func TestBitwarden_Check_prettyPrintedJSON(t *testing.T) {
	fr := &fakeRunner{stdout: []byte("{\n  \"status\": \"unlocked\",\n  \"userEmail\": \"u@e.com\"\n}")}
	if err := (Bitwarden{Runner: fr, LookPath: alwaysFound}).Check(context.Background()); err != nil {
		t.Errorf("pretty-printed check: %v", err)
	}
}

func TestCheck_returnsCLIMissingWhenBinaryAbsent(t *testing.T) {
	notFound := func(string) (string, error) { return "", errors.New("not found") }
	cases := []Manager{
		OnePassword{Runner: &fakeRunner{}, LookPath: notFound},
		Bitwarden{Runner: &fakeRunner{}, LookPath: notFound},
		Pass{Runner: &fakeRunner{}, LookPath: notFound},
	}
	for _, m := range cases {
		if err := m.Check(context.Background()); !errors.Is(err, ErrCLIMissing) {
			t.Errorf("%s: want ErrCLIMissing, got %v", m.Name(), err)
		}
	}
}

func TestBitwarden_Get_returnsValue(t *testing.T) {
	fr := &fakeRunner{stdout: []byte("secret-value\n")}
	v, err := Bitwarden{Runner: fr}.Get(context.Background(), "Shortcut API Token")
	if err != nil {
		t.Fatal(err)
	}
	if v != "secret-value" {
		t.Errorf("got %q", v)
	}
	c := fr.calls[0]
	if c.args[0] != "get" || c.args[1] != "password" || c.args[2] != "Shortcut API Token" {
		t.Errorf("args = %v", c.args)
	}
}

func TestPass_Get_trimsTrailingNewline(t *testing.T) {
	fr := &fakeRunner{stdout: []byte("pass-secret\n")}
	v, err := Pass{Runner: fr}.Get(context.Background(), "work/shortcut")
	if err != nil {
		t.Fatal(err)
	}
	if v != "pass-secret" {
		t.Errorf("got %q", v)
	}
}

func TestPass_Get_mapsNotFound(t *testing.T) {
	fr := &fakeRunner{stderr: []byte("Error: foo is not in the password store."), err: errors.New("exit 1")}
	_, err := Pass{Runner: fr}.Get(context.Background(), "foo")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestEnvOnly_GetReadsEnv(t *testing.T) {
	t.Setenv("THICKET_TEST_REF", "yay")
	v, err := EnvOnly{}.Get(context.Background(), "THICKET_TEST_REF")
	if err != nil || v != "yay" {
		t.Errorf("got %q, err=%v", v, err)
	}
	_, err = EnvOnly{}.Get(context.Background(), "DOES_NOT_EXIST_THICKET")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestCached_dedupsRepeatedRefs(t *testing.T) {
	fr := &fakeRunner{stdout: []byte("v")}
	c := NewCached(OnePassword{Runner: fr})
	for i := 0; i < 3; i++ {
		got, err := c.Get(context.Background(), "op://x/y/z")
		if err != nil || got != "v" {
			t.Fatalf("iter %d: got %q err=%v", i, got, err)
		}
	}
	if len(fr.calls) != 1 {
		t.Errorf("underlying runner called %d times, want 1", len(fr.calls))
	}
}
