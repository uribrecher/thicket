// Package detector uses an LLM (Claude via Anthropic API) to suggest which
// repos a given ticket touches. The output is a list of RepoMatch entries
// the TUI pre-selects in the interactive selection step.
//
// The package exposes a small Detector interface so callers can swap in:
//   - AnthropicDetector for production (Haiku via tool-use, structured output)
//   - RuleDetector for `--only <name,...>` (no LLM, pure name resolution)
//   - a stub for tests
package detector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"text/template"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// RepoMatch is one detected repo + the model's rationale.
type RepoMatch struct {
	Name       string  `json:"name"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

// CatalogRepo is the slim projection the detector needs about each repo —
// name + a short description used in the prompt.
type CatalogRepo struct {
	Name        string
	Description string
}

// Input is everything the detector needs to make one call.
type Input struct {
	TicketTitle string
	TicketBody  string
	Repos       []CatalogRepo
}

// Detector resolves an Input into a list of RepoMatch entries.
type Detector interface {
	Detect(ctx context.Context, in Input) ([]RepoMatch, error)
}

// ----- Anthropic-backed detector -----

const toolName = "submit_repos"

// AnthropicDetector calls the Anthropic Messages API with a forced
// `submit_repos` tool-use so we get a structured JSON array back instead of
// trying to parse free-form text.
type AnthropicDetector struct {
	Client anthropic.Client
	Model  anthropic.Model
}

// NewAnthropic builds an AnthropicDetector. apiKey is required; baseURL may
// be empty (production) or pointed at an httptest server.
func NewAnthropic(apiKey, baseURL string, model anthropic.Model) *AnthropicDetector {
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	if model == "" {
		model = anthropic.ModelClaudeHaiku4_5
	}
	return &AnthropicDetector{
		Client: anthropic.NewClient(opts...),
		Model:  model,
	}
}

// Detect makes the Anthropic call and returns the parsed RepoMatch list.
// Empty result (no repos detected) is NOT an error — callers fall back to
// presenting an empty preselection.
func (a *AnthropicDetector) Detect(ctx context.Context, in Input) ([]RepoMatch, error) {
	if len(in.Repos) == 0 {
		return nil, errors.New("detector: empty repo catalog")
	}
	prompt, err := renderPrompt(in)
	if err != nil {
		return nil, err
	}

	tool := anthropic.ToolParam{
		Name:        toolName,
		Description: anthropic.String("Submit the list of repos that need code changes for this ticket."),
		InputSchema: submitReposInputSchema(),
	}
	msg, err := a.Client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     a.Model,
		MaxTokens: 1024,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
		Tools:      []anthropic.ToolUnionParam{{OfTool: &tool}},
		ToolChoice: anthropic.ToolChoiceParamOfTool(toolName),
	})
	if err != nil {
		return nil, fmt.Errorf("anthropic messages.new: %w", err)
	}
	return extractMatches(msg)
}

func extractMatches(msg *anthropic.Message) ([]RepoMatch, error) {
	for _, block := range msg.Content {
		if block.Type == "tool_use" && block.Name == toolName {
			var payload struct {
				Repos []RepoMatch `json:"repos"`
			}
			raw := []byte(block.Input)
			if len(raw) == 0 {
				continue
			}
			if err := json.Unmarshal(raw, &payload); err != nil {
				return nil, fmt.Errorf("decode submit_repos input: %w (raw: %s)", err, raw)
			}
			return payload.Repos, nil
		}
	}
	return nil, errors.New("anthropic response missing submit_repos tool_use")
}

func submitReposInputSchema() anthropic.ToolInputSchemaParam {
	return anthropic.ToolInputSchemaParam{
		Required: []string{"repos"},
		Properties: map[string]any{
			"repos": map[string]any{
				"type":        "array",
				"description": "Repos that will need code changes for this ticket.",
				"items": map[string]any{
					"type":     "object",
					"required": []string{"name", "confidence", "reason"},
					"properties": map[string]any{
						"name":       map[string]any{"type": "string"},
						"confidence": map[string]any{"type": "number", "minimum": 0, "maximum": 1},
						"reason":     map[string]any{"type": "string"},
					},
				},
			},
		},
	}
}

// ----- prompt -----

const promptTmpl = `You are routing a ticket to the GitHub repos that will need code changes.
Be conservative — prefer fewer repos. Do not include repos that are merely mentioned but not modified.

TICKET:
  Title: {{.TicketTitle}}
  Body:
{{.IndentedBody}}

KNOWN REPOS:
{{range .Repos}}  - {{.Name}}{{if .Description}}: {{.Description}}{{end}}
{{end}}

Call the submit_repos tool with the array of repos that need code changes. Confidence is 0..1.`

type promptData struct {
	TicketTitle  string
	IndentedBody string
	Repos        []CatalogRepo
}

func renderPrompt(in Input) (string, error) {
	t, err := template.New("detect").Parse(promptTmpl)
	if err != nil {
		return "", fmt.Errorf("compile prompt template: %w", err)
	}
	indented := strings.TrimSpace(in.TicketBody)
	if indented == "" {
		indented = "  (no description)"
	} else {
		var sb strings.Builder
		for _, line := range strings.Split(indented, "\n") {
			sb.WriteString("    ")
			sb.WriteString(line)
			sb.WriteString("\n")
		}
		indented = strings.TrimRight(sb.String(), "\n")
	}
	var out strings.Builder
	if err := t.Execute(&out, promptData{
		TicketTitle:  in.TicketTitle,
		IndentedBody: indented,
		Repos:        in.Repos,
	}); err != nil {
		return "", fmt.Errorf("execute prompt template: %w", err)
	}
	return out.String(), nil
}

// ----- rule detector (`--only` path) -----

// RuleDetector resolves names + aliases against a catalog without calling
// an LLM. Used when the caller passes `--only repo1,repo2`.
type RuleDetector struct {
	Catalog []CatalogRepo
	Aliases map[string]string // alias → canonical name
}

// Detect treats Input.TicketBody as a comma-separated list of names/aliases
// and returns one RepoMatch per resolved repo. Used by the CLI's --only flag.
func (r *RuleDetector) Detect(_ context.Context, in Input) ([]RepoMatch, error) {
	canonical := map[string]bool{}
	for _, c := range r.Catalog {
		canonical[c.Name] = true
	}
	var out []RepoMatch
	for _, raw := range strings.Split(in.TicketBody, ",") {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if alias, ok := r.Aliases[strings.ToLower(name)]; ok {
			name = alias
		}
		if !canonical[name] {
			return nil, fmt.Errorf("unknown repo %q (not in catalog)", name)
		}
		out = append(out, RepoMatch{Name: name, Confidence: 1.0, Reason: "manual --only"})
	}
	return out, nil
}
