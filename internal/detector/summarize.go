package detector

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

// SummaryLines is the target number of lines the summarizer asks the
// model for. Wizard rendering also clamps to this — kept in one place
// so the prompt and the renderer agree.
const SummaryLines = 3

// Summarizer condenses a ticket title+body into a few short lines —
// the wizard renders these as the ticket's "3-line summary" in place
// of the dumb "first 3 non-empty lines of the description" fallback.
type Summarizer interface {
	Summarize(ctx context.Context, title, body string) ([]string, error)
}

// summarizePrompt is the prompt body both backends share.
const summarizePrompt = `Summarize this ticket in EXACTLY 3 short bullet lines, one per line.

Rules:
- Each line ≤ 100 characters, plain text, no markdown, no fenced code.
- No leading dash, asterisk, or numbering — just the text.
- Focus on: what's broken or missing, what change is requested, who's affected (if mentioned).
- If the body is too thin to summarize, output 3 lines that restate the title in different framings.

TICKET TITLE:
%s

TICKET BODY:
%s`

// parseSummaryLines pulls up to SummaryLines clean lines from raw model
// output. Strips common bullet prefixes ("-", "*", "1.", "•") and
// empty/whitespace lines.
func parseSummaryLines(raw string) []string {
	out := make([]string, 0, SummaryLines)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "-*•·")
		// Strip "1. " / "1) " style numbering.
		if len(line) > 1 && line[0] >= '0' && line[0] <= '9' {
			for i := 1; i < len(line) && i < 4; i++ {
				if line[i] == '.' || line[i] == ')' {
					line = strings.TrimSpace(line[i+1:])
					break
				}
			}
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
		if len(out) == SummaryLines {
			break
		}
	}
	return out
}

// ----- Anthropic-backed summarizer -----

// AnthropicSummarizer hits the Anthropic Messages API for a plain-text
// summary. Tool-use would force structured output, but for 3 lines of
// prose that's overkill — we trim and split client-side instead.
type AnthropicSummarizer struct {
	Client anthropic.Client
	Model  anthropic.Model
}

// NewAnthropicSummarizer reuses NewAnthropic's option setup so a single
// API key / base URL configures both detector and summarizer.
func NewAnthropicSummarizer(apiKey, baseURL string, model anthropic.Model) *AnthropicSummarizer {
	d := NewAnthropic(apiKey, baseURL, model)
	return &AnthropicSummarizer{Client: d.Client, Model: d.Model}
}

func (a *AnthropicSummarizer) Summarize(ctx context.Context, title, body string) ([]string, error) {
	if strings.TrimSpace(title) == "" && strings.TrimSpace(body) == "" {
		return nil, errors.New("summarize: empty title and body")
	}
	prompt := fmt.Sprintf(summarizePrompt, title, body)
	msg, err := a.Client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     a.Model,
		MaxTokens: 512,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("anthropic messages.new (summarize): %w", err)
	}
	var sb strings.Builder
	for _, block := range msg.Content {
		if block.Type == "text" {
			sb.WriteString(block.Text)
			sb.WriteString("\n")
		}
	}
	lines := parseSummaryLines(sb.String())
	if len(lines) == 0 {
		return nil, errors.New("summarize: no usable lines in model response")
	}
	return lines, nil
}

// ----- Claude CLI-backed summarizer -----

// ClaudeCLISummarizer shells out to the local `claude` binary, mirroring
// ClaudeCLIDetector's pattern so Claude-Enterprise users get summaries
// without a separate Anthropic API key.
type ClaudeCLISummarizer struct {
	BinaryPath string
	Model      string
	Runner     CLIRunner
}

// NewClaudeCLISummarizer builds a CLI-backed summarizer.
func NewClaudeCLISummarizer(binary, model string) *ClaudeCLISummarizer {
	if binary == "" {
		binary = "claude"
	}
	return &ClaudeCLISummarizer{BinaryPath: binary, Model: model, Runner: DefaultCLIRunner{}}
}

func (d *ClaudeCLISummarizer) Summarize(ctx context.Context, title, body string) ([]string, error) {
	if strings.TrimSpace(title) == "" && strings.TrimSpace(body) == "" {
		return nil, errors.New("summarize: empty title and body")
	}
	prompt := fmt.Sprintf(summarizePrompt, title, body)
	prompt += "\n\nReturn ONLY the 3 lines. No prose around them, no fenced code."

	args := []string{"-p"}
	if d.Model != "" {
		args = append(args, "--model", d.Model)
	}
	stdout, stderr, err := d.Runner.Run(ctx, d.BinaryPath, args, strings.NewReader(prompt))
	if err != nil {
		return nil, fmt.Errorf("claude -p (summarize): %w (%s)", err, strings.TrimSpace(string(stderr)))
	}
	lines := parseSummaryLines(string(bytes.TrimSpace(stdout)))
	if len(lines) == 0 {
		return nil, fmt.Errorf("summarize: no usable lines in claude output (raw: %s)",
			strings.TrimSpace(string(stdout)))
	}
	return lines, nil
}
