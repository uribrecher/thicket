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
	// Color is pre-normalized to `#RRGGBB` uppercase via
	// term.SanitizeHexColor. Empty when the model didn't emit a
	// parseable color.
	Color string
}

// NicknameSuggester proposes a short, human-friendly label and a tab
// color for a ticket. The wizard's Plan page seeds its editable
// nickname input from this call; the launcher uses the color to tint
// the iTerm2 tab background so multiple concurrent workspaces are
// visually distinguishable.
//
// existingColors is the list of `#RRGGBB` colors already in use by
// other workspaces. The prompt asks the model to avoid colors close
// to these so the new tab contrasts visibly in the tab strip. Pass
// nil or empty when there are no other workspaces.
type NicknameSuggester interface {
	Suggest(ctx context.Context, title, body string, existingColors []string) (NicknameSuggestion, error)
}

// nicknamePromptTemplate is the prompt body both backends share.
// Three %s slots in order: existing-colors block, ticket title,
// ticket body. Asks for two lines: nickname, then `#RRGGBB`.
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

COLOR RULES (output one hex code, #RRGGBB):
- PRIMARY inspiration — match a famous brand color when one is mentioned in the ticket:
  * AWS / S3 → orange (~#FF9900)
  * Google Drive / GCP → blue or green (~#4285F4 / ~#0F9D58)
  * Microsoft / SharePoint / OneDrive / Azure → Microsoft blue (~#0078D4)
  * Snowflake → cyan (~#29B5E8)
  * Databricks → orange-red (~#FF3621)
  * Atlassian / Jira / Confluence → Atlassian blue (~#0052CC)
  * GitHub → near-black (~#24292E) — but skip pure black; use #3A3F44
  * MongoDB → green (~#13AA52)
  * Slack → purple-pink (~#4A154B / ~#E01E5A)
  * Notion / Linear → skip if the brand color is white/very dark; pick a work-type color instead
- FALLBACK inspiration — when no brand fits, pick from the WORK TYPE:
  * Bug / fix → reds and dark oranges (#C8232C, #B22222, #DC143C, #E55934)
  * Feature → greens, vibrant blues, purples (#2E7D32, #1565C0, #6A1B9A, #00838F) — VARY these between tickets
  * Refactor / chore → muted blues or browns (#3A6EA5, #8B6F4E, #6D4C41)
  * Investigation / spike → purples (#7E57C2, #5E35B1, #4527A0)
  * Performance → cyans / teals (#00ACC1, #26A69A, #00838F)
  * Security / auth → deep red or amber (#B71C1C, #F9A825, #C62828)
  * Docs → soft greens or muted teals (#558B2F, #00897B)
- DIFFERENTIATION (CRITICAL): %s
- AVOID: pure black, pure white, washed-out pastels (R+G+B above ~700 is too light), pure neutral gray (R≈G≈B), and the exact RED-ORANGE #FF5733 family (overused).

Output format — EXACTLY two lines, in this order, no preamble or trailing prose:
<nickname>
<#RRGGBB>

TICKET TITLE:
%s

TICKET BODY:
%s`

// renderExistingColorsClause builds the differentiation rule
// referenced by the prompt's "%s" slot. Empty input → encourages
// novelty; non-empty → asks the model to pick something visibly
// distinct from each listed color.
func renderExistingColorsClause(existingColors []string) string {
	if len(existingColors) == 0 {
		return "no other thicket workspaces are currently colored — feel free to set the visual baseline."
	}
	// Cap to the most-recent 8 so the prompt stays bounded.
	if len(existingColors) > 8 {
		existingColors = existingColors[:8]
	}
	return fmt.Sprintf("other workspace tabs are already using these colors — pick something with a clearly different hue or brightness so the user can tell them apart at a glance: %s.",
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
				// Empty value after the separator. If the prefix
				// mentions "nickname" or "color" (e.g. "Sure!
				// Here's a nickname:"), the real content is on
				// the next line — skip this entirely so it
				// doesn't become the nickname by default.
				lower := strings.ToLower(line[:idx])
				if strings.Contains(lower, "nickname") || strings.Contains(lower, "color") {
					continue
				}
			}
		}
		// Try color first — if it parses as hex, it's the color
		// line regardless of where it appeared.
		if c := term.SanitizeHexColor(line); c != "" && s.Color == "" {
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
	prompt += "\n\nReturn ONLY the two lines (nickname then #RRGGBB). No prose around them."

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
