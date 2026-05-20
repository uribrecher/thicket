package detector

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/uribrecher/thicket/internal/term"
	"github.com/uribrecher/thicket/internal/workspace"
)

// NicknameSuggestion is what NicknameSuggester returns: a short human
// label plus an optional tab color hint. Either field can be empty —
// the caller is responsible for treating empties as "no suggestion"
// rather than failing the call.
type NicknameSuggestion struct {
	// Nickname is pre-sanitized via workspace.SanitizeNickname.
	Nickname string
	// Color is the canonical palette name (e.g. "blue") sanitized
	// via term.SanitizePaletteName. Empty when the model didn't
	// emit a value that matches the palette.
	Color string
}

// NicknameSuggester proposes a short, human-friendly label and a tab
// color for a ticket. The wizard's Plan page seeds its editable
// nickname input from this call; the launcher uses the color to tint
// the iTerm2 tab background so multiple concurrent workspaces are
// visually distinguishable.
//
// existingColors is the list of palette names already in use by
// other workspaces. The prompt asks the model to avoid names already
// taken so the new tab contrasts visibly in the tab strip. Pass
// nil or empty when there are no other workspaces.
type NicknameSuggester interface {
	Suggest(ctx context.Context, title, body string, existingColors []string) (NicknameSuggestion, error)
}

// nicknamePromptTemplate is the prompt body both backends share.
// Three %s slots in order: existing-colors block, ticket title,
// ticket body. Asks for two lines: nickname, then a palette name.
// Client-side parsing is tolerant of order, prefix, and case — see
// parseSuggestion.
const nicknamePromptTemplate = `Suggest a workspace nickname AND a tab color for this ticket. The developer is juggling many concurrent workspaces and needs to recognize THIS one INSTANTLY in a list of tabs.

NICKNAME RULES:
- Maximum 25 characters. Use ACRONYMS and shorthand to fit — e.g. "MR" for Munich Re, "WD" for Workday, "SP" for SharePoint, "GD" for GoogleDrive.
- MINE the title and body for distinctive nouns and use them in the nickname:
  * Customer / company / org names (Wix, Munich Re, Workday, Rivian, Sentra, Anthropic, etc.) — these are gold.
  * Hosting-service names (SharePoint, GoogleDrive, S3, FileShare, Snowflake, Databricks, Confluence, Jira, GitHub, Azure Blob, BigQuery, MongoDB, Postgres, etc.).
  * File-format or domain keywords when central (CAD, PDF, DICOM, parquet, etc.).
- One emoji prefix is encouraged when it tightly conveys the WORK TYPE:
  🐛 bug · ⚡ perf · 🔒 security · 📝 docs · 🎨 UI · 🧪 test · 🔧 refactor · 🚀 deploy · 📊 data · 🔍 investigate · ✨ feature
- BAD (too generic — never do this): "fix the bug", "add feature", "investigate issue", "update code", "ticket fix".
- GOOD: "🐛 Wix S3 dedup", "🔍 MR Snowflake enum", "⚡ WD GDrive scan", "🧪 SP file probe", "📝 Rivian CAD docs", "🔧 Sentra retry loop".

COLOR RULES — pick exactly one name from this fixed list:
  red, orange, yellow, green, cyan, blue, purple, pink

- PRIMARY inspiration — match a famous brand when one is mentioned in the ticket:
  * AWS / S3 → orange
  * Google Drive / GCP → blue or green
  * Microsoft / SharePoint / OneDrive / Azure → blue
  * Snowflake → cyan
  * Databricks → red
  * Atlassian / Jira / Confluence → blue
  * GitHub → purple
  * MongoDB → green
  * Slack → purple or pink
- FALLBACK inspiration — when no brand fits, pick from the WORK TYPE:
  * Bug / fix → red
  * Feature → green or blue
  * Refactor / chore → blue
  * Investigation / spike → purple
  * Performance → cyan
  * Security / auth → red or orange
  * Docs → green or yellow
- DIFFERENTIATION (CRITICAL): %s

Output format — EXACTLY two lines, in this order, no preamble or trailing prose:
<nickname>
<color-name>

TICKET TITLE:
%s

TICKET BODY:
%s`

// renderExistingColorsClause builds the differentiation rule
// referenced by the prompt's "%s" slot. Empty input → encourages
// novelty; non-empty → asks the model to pick something different
// from the already-used palette names.
func renderExistingColorsClause(existingColors []string) string {
	if len(existingColors) == 0 {
		return "no other thicket workspaces are currently colored — feel free to pick anything from the list."
	}
	// Cap to the most-recent 8 so the prompt stays bounded.
	if len(existingColors) > 8 {
		existingColors = existingColors[:8]
	}
	return fmt.Sprintf("other workspace tabs are already using these colors — pick something different so the user can tell them apart at a glance: %s.",
		strings.Join(existingColors, ", "))
}

// parseSuggestion pulls a nickname + hex color from raw model output.
// Tolerant of:
//   - leading blank / chatty lines (skipped)
//   - the order being color-first instead of nickname-first
//   - the color line being prefixed (`color:`, `Tab color:`, etc.)
//   - the nickname being on its own line with no prefix
//
// Both fields are sanitized via the canonical workspace + term
// helpers before being returned.
func parseSuggestion(raw string) NicknameSuggestion {
	var s NicknameSuggestion
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = stripWrappingQuotes(line)
		// Strip a "key:" or "key -" prefix (common chatty model
		// formats). Take whatever's after the first colon.
		if idx := strings.IndexAny(line, ":="); idx > 0 && idx < 32 {
			candidate := strings.TrimSpace(line[idx+1:])
			if candidate != "" {
				line = candidate
			} else {
				// Empty value after the separator means this is an
				// introductory label line (e.g. "Here you go:",
				// "Sure! Here's a nickname:"). The real content is
				// on the next line — skip this entirely so it
				// doesn't become the nickname by default.
				continue
			}
		}
		// Try color first — if it canonicalizes to a palette name,
		// it's the color line regardless of where it appeared.
		if c := term.SanitizePaletteName(line); c != "" && s.Color == "" {
			s.Color = c
			continue
		}
		// Otherwise treat as nickname (first non-color line wins).
		if s.Nickname == "" {
			s.Nickname = workspace.SanitizeNickname(line)
		}
		if s.Nickname != "" && s.Color != "" {
			break
		}
	}
	return s
}

func stripWrappingQuotes(s string) string {
	if len(s) < 2 {
		return s
	}
	first, last := s[0], s[len(s)-1]
	if (first == '"' && last == '"') || (first == '\'' && last == '\'') ||
		(first == '`' && last == '`') {
		return strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

// ----- Anthropic-backed suggester -----

type AnthropicNicknameSuggester struct {
	Client anthropic.Client
	Model  anthropic.Model
}

func NewAnthropicNicknameSuggester(apiKey, baseURL string, model anthropic.Model) *AnthropicNicknameSuggester {
	d := NewAnthropic(apiKey, baseURL, model)
	return &AnthropicNicknameSuggester{Client: d.Client, Model: d.Model}
}

func (a *AnthropicNicknameSuggester) Suggest(ctx context.Context, title, body string, existingColors []string) (NicknameSuggestion, error) {
	if strings.TrimSpace(title) == "" && strings.TrimSpace(body) == "" {
		return NicknameSuggestion{}, errors.New("nickname: empty title and body")
	}
	prompt := fmt.Sprintf(nicknamePromptTemplate, renderExistingColorsClause(existingColors), title, body)
	msg, err := a.Client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     a.Model,
		MaxTokens: 80,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})
	if err != nil {
		return NicknameSuggestion{}, fmt.Errorf("anthropic messages.new (nickname): %w", err)
	}
	var sb strings.Builder
	for _, block := range msg.Content {
		if block.Type == "text" {
			sb.WriteString(block.Text)
			sb.WriteString("\n")
		}
	}
	out := parseSuggestion(sb.String())
	if out.Nickname == "" {
		return NicknameSuggestion{}, errors.New("nickname: no usable line in model response")
	}
	return out, nil
}

// ----- Claude CLI-backed suggester -----

type ClaudeCLINicknameSuggester struct {
	BinaryPath string
	Model      string
	Runner     CLIRunner
}

func NewClaudeCLINicknameSuggester(binary, model string) *ClaudeCLINicknameSuggester {
	if binary == "" {
		binary = "claude"
	}
	return &ClaudeCLINicknameSuggester{BinaryPath: binary, Model: model, Runner: DefaultCLIRunner{}}
}

func (d *ClaudeCLINicknameSuggester) Suggest(ctx context.Context, title, body string, existingColors []string) (NicknameSuggestion, error) {
	if strings.TrimSpace(title) == "" && strings.TrimSpace(body) == "" {
		return NicknameSuggestion{}, errors.New("nickname: empty title and body")
	}
	prompt := fmt.Sprintf(nicknamePromptTemplate, renderExistingColorsClause(existingColors), title, body)
	prompt += "\n\nReturn ONLY the two lines (nickname then color-name). No prose around them."

	args := []string{"-p"}
	if d.Model != "" {
		args = append(args, "--model", d.Model)
	}
	stdout, stderr, err := d.Runner.Run(ctx, d.BinaryPath, args, strings.NewReader(prompt))
	if err != nil {
		return NicknameSuggestion{}, fmt.Errorf("claude -p (nickname): %w (%s)", err, strings.TrimSpace(string(stderr)))
	}
	out := parseSuggestion(string(bytes.TrimSpace(stdout)))
	if out.Nickname == "" {
		return NicknameSuggestion{}, fmt.Errorf("nickname: no usable line in claude output (raw: %s)",
			strings.TrimSpace(string(stdout)))
	}
	return out, nil
}
