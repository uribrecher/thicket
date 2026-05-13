package detector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

func TestRenderPrompt_includesTitleBodyAndRepos(t *testing.T) {
	got, err := renderPrompt(Input{
		TicketTitle: "Fix inventory grouping",
		TicketBody:  "Line one\nLine two",
		Repos: []CatalogRepo{
			{Name: "alpha", Description: "first repo"},
			{Name: "beta"},
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, sub := range []string{"Fix inventory grouping", "Line one", "Line two",
		"alpha: first repo", "beta", "JSON array"} {
		if !strings.Contains(got, sub) {
			t.Errorf("missing %q in prompt:\n%s", sub, got)
		}
	}
}

func TestRenderPrompt_handlesEmptyBody(t *testing.T) {
	got, err := renderPrompt(Input{TicketTitle: "T", TicketBody: "",
		Repos: []CatalogRepo{{Name: "a"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "(no description)") {
		t.Errorf("empty-body placeholder missing:\n%s", got)
	}
}

func TestAnthropicDetector_parsesToolUse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sanity-check the request shape
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode req: %v", err)
		}
		tools, ok := body["tools"].([]any)
		if !ok || len(tools) != 1 {
			t.Errorf("expected exactly one tool, got %+v", body["tools"])
		}
		// Return a canned tool_use response.
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"id": "msg_1",
			"type": "message",
			"role": "assistant",
			"model": "claude-haiku-4-5",
			"stop_reason": "tool_use",
			"content": [
				{
					"type": "tool_use",
					"id": "tu_1",
					"name": "submit_repos",
					"input": {"repos": [
						{"name":"alpha","confidence":0.9,"reason":"title mentions alpha"},
						{"name":"gamma","confidence":0.6,"reason":"description hints at grouping"}
					]}
				}
			],
			"usage": {"input_tokens": 1, "output_tokens": 1}
		}`)
	}))
	defer srv.Close()

	d := NewAnthropic("test-key", srv.URL, anthropic.ModelClaudeHaiku4_5)
	got, err := d.Detect(context.Background(), Input{
		TicketTitle: "x", TicketBody: "y",
		Repos: []CatalogRepo{{Name: "alpha"}, {Name: "gamma"}, {Name: "delta"}},
	})
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "gamma" {
		t.Errorf("got %+v", got)
	}
	if got[0].Confidence != 0.9 {
		t.Errorf("confidence parse: %v", got[0].Confidence)
	}
}

func TestAnthropicDetector_emptyCatalogRejected(t *testing.T) {
	d := NewAnthropic("k", "http://localhost:0", "")
	_, err := d.Detect(context.Background(), Input{TicketTitle: "x", TicketBody: "y", Repos: nil})
	if err == nil {
		t.Fatal("expected error on empty catalog")
	}
}

func TestAnthropicDetector_missingToolUse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"id": "msg_1","type":"message","role":"assistant","model":"claude-haiku-4-5",
			"stop_reason":"end_turn",
			"content":[{"type":"text","text":"sorry, no idea"}],
			"usage":{"input_tokens":1,"output_tokens":1}
		}`)
	}))
	defer srv.Close()
	d := NewAnthropic("k", srv.URL, anthropic.ModelClaudeHaiku4_5)
	_, err := d.Detect(context.Background(), Input{TicketTitle: "x", TicketBody: "y",
		Repos: []CatalogRepo{{Name: "alpha"}}})
	if err == nil || !strings.Contains(err.Error(), "submit_repos") {
		t.Fatalf("want missing-tool-use error, got %v", err)
	}
}

func TestRuleDetector_resolvesNamesAndAliases(t *testing.T) {
	d := &RuleDetector{
		Catalog: []CatalogRepo{{Name: "sentra-scan-state-manager"}, {Name: "sentra-discovery"}},
		Aliases: map[string]string{"ssm": "sentra-scan-state-manager"},
	}
	got, err := d.Detect(context.Background(), Input{
		TicketBody: "ssm, sentra-discovery",
	})
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if len(got) != 2 || got[0].Name != "sentra-scan-state-manager" || got[1].Name != "sentra-discovery" {
		t.Errorf("got %+v", got)
	}
	for _, m := range got {
		if m.Confidence != 1.0 || m.Reason != "manual --only" {
			t.Errorf("bad metadata: %+v", m)
		}
	}
}

func TestRuleDetector_unknownRepoErrors(t *testing.T) {
	d := &RuleDetector{Catalog: []CatalogRepo{{Name: "alpha"}}}
	_, err := d.Detect(context.Background(), Input{TicketBody: "ghost"})
	if err == nil || !strings.Contains(err.Error(), "unknown repo") {
		t.Fatalf("want unknown-repo error, got %v", err)
	}
}
