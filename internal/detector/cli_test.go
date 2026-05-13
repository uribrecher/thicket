package detector

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type fakeCLIRunner struct {
	stdout []byte
	stderr []byte
	err    error

	gotName  string
	gotArgs  []string
	gotStdin string
}

func (f *fakeCLIRunner) Run(_ context.Context, name string, args []string, stdin io.Reader) ([]byte, []byte, error) {
	f.gotName, f.gotArgs = name, args
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		f.gotStdin = string(b)
	}
	return f.stdout, f.stderr, f.err
}

func TestClaudeCLI_parsesBareJSON(t *testing.T) {
	fr := &fakeCLIRunner{stdout: []byte(`[{"name":"alpha","confidence":0.9,"reason":"in title"}]`)}
	d := &ClaudeCLIDetector{BinaryPath: "claude", Model: "claude-haiku-4-5", Runner: fr}

	got, err := d.Detect(context.Background(), Input{
		TicketTitle: "x", TicketBody: "y",
		Repos: []CatalogRepo{{Name: "alpha"}, {Name: "beta"}},
	})
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if len(got) != 1 || got[0].Name != "alpha" || got[0].Confidence != 0.9 {
		t.Errorf("got %+v", got)
	}
	if fr.gotName != "claude" {
		t.Errorf("binary = %q", fr.gotName)
	}
	// Prompt goes on stdin now (to avoid argv leakage / length limits);
	// argv carries only the flags.
	if len(fr.gotArgs) != 3 || fr.gotArgs[0] != "-p" || fr.gotArgs[1] != "--model" ||
		fr.gotArgs[2] != "claude-haiku-4-5" {
		t.Errorf("argv = %v, want [-p --model claude-haiku-4-5]", fr.gotArgs)
	}
	if !strings.Contains(fr.gotStdin, "JSON array") {
		t.Errorf("prompt missing JSON instruction on stdin: %s", fr.gotStdin)
	}
	if !strings.Contains(fr.gotStdin, "KNOWN REPOS") {
		t.Errorf("prompt missing catalog on stdin")
	}
}

func TestClaudeCLI_recoversFromProseWrap(t *testing.T) {
	// Claude commonly wraps output in prose and/or markdown fences.
	body := "Sure! Here are the repos:\n```json\n" +
		`[{"name":"x","confidence":0.7,"reason":"matches body"}]` +
		"\n```\nLet me know if you need more."
	fr := &fakeCLIRunner{stdout: []byte(body)}
	d := &ClaudeCLIDetector{BinaryPath: "claude", Runner: fr}
	got, err := d.Detect(context.Background(), Input{
		TicketTitle: "t", TicketBody: "b", Repos: []CatalogRepo{{Name: "x"}},
	})
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if len(got) != 1 || got[0].Name != "x" {
		t.Errorf("got %+v", got)
	}
}

func TestClaudeCLI_propagatesRunnerError(t *testing.T) {
	fr := &fakeCLIRunner{err: errors.New("exit 1"), stderr: []byte("claude not authenticated")}
	d := &ClaudeCLIDetector{BinaryPath: "claude", Runner: fr}
	_, err := d.Detect(context.Background(), Input{TicketTitle: "t", TicketBody: "b",
		Repos: []CatalogRepo{{Name: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "claude") {
		t.Errorf("want wrapped error mentioning claude, got %v", err)
	}
}

func TestClaudeCLI_emptyCatalogRejected(t *testing.T) {
	d := &ClaudeCLIDetector{BinaryPath: "claude", Runner: &fakeCLIRunner{}}
	_, err := d.Detect(context.Background(), Input{TicketTitle: "t", TicketBody: "b"})
	if err == nil {
		t.Fatal("expected error on empty catalog")
	}
}

func TestExtractJSONArray_handlesStringsWithBrackets(t *testing.T) {
	// Strings containing ']' must not be treated as array close.
	in := []byte(`prose [{"reason":"contains ] bracket","name":"x","confidence":0.5}] more prose`)
	got, err := extractJSONArray(in)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"reason":"contains ] bracket","name":"x","confidence":0.5}]`
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractJSONArray_handlesEscapedQuotes(t *testing.T) {
	in := []byte(`[{"name":"foo \"bar\""}]`)
	got, err := extractJSONArray(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(in) {
		t.Errorf("got %q", got)
	}
}

func TestExtractJSONArray_noArrayErrors(t *testing.T) {
	_, err := extractJSONArray([]byte("sorry, I cannot answer that"))
	if err == nil {
		t.Fatal("expected error when no array found")
	}
}
