package detector

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/anthropics/anthropic-sdk-go"
)

// NicknameMaxChars is the upper bound on a workspace nickname's
// length, in runes. Matches the textinput.CharLimit on the wizard's
// Plan page so the prompt, the parser, and the UI agree.
const NicknameMaxChars = 20

// NicknameSuggester proposes a short, human-friendly label for a
// ticket. The wizard's Plan page seeds its editable nickname input
// from this call; the user can accept the suggestion or type their
// own. Spaces and emoji are allowed; uniqueness is NOT required.
type NicknameSuggester interface {
	Suggest(ctx context.Context, title, body string) (string, error)
}

// nicknamePrompt is the prompt body both backends share. The 20-char
// rule is also enforced client-side by parseNickname as a safety net.
const nicknamePrompt = `Suggest a short, memorable nickname for this ticket so a developer can quickly recognize it in a list of workspaces.

Rules:
- Maximum 20 characters total. Count carefully — emoji often count as one rune visually but multiple bytes; stay safely under the limit.
- Use spaces freely; this is a human label, not an identifier.
- Emoji are encouraged when they tightly convey the concept (🐛 bug, ⚡ performance, 🔒 security, 📝 docs, 🎨 UI, 🧪 test, 🔧 refactor, etc.).
- Make it memorable and specific — not a generic restatement of the ticket id.
- Output ONLY the nickname itself on a single line. No quotes, no preamble, no explanation, no markdown.

TICKET TITLE:
%s

TICKET BODY:
%s`

// parseNickname cleans raw model output: takes the first non-empty
// line, strips surrounding whitespace and matched quotes, and
// truncates to NicknameMaxChars runes as a safety net.
func parseNickname(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = stripWrappingQuotes(line)
		if utf8.RuneCountInString(line) > NicknameMaxChars {
			// Truncate at NicknameMaxChars runes (not bytes — emoji
			// must survive intact).
			cut := 0
			for i := range line {
				if cut == NicknameMaxChars {
					line = line[:i]
					break
				}
				cut++
			}
		}
		return line
	}
	return ""
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

// ----- Anthropic-backed nickname suggester -----

// AnthropicNicknameSuggester hits the Anthropic Messages API. Mirrors
// AnthropicSummarizer.
type AnthropicNicknameSuggester struct {
	Client anthropic.Client
	Model  anthropic.Model
}

func NewAnthropicNicknameSuggester(apiKey, baseURL string, model anthropic.Model) *AnthropicNicknameSuggester {
	d := NewAnthropic(apiKey, baseURL, model)
	return &AnthropicNicknameSuggester{Client: d.Client, Model: d.Model}
}

func (a *AnthropicNicknameSuggester) Suggest(ctx context.Context, title, body string) (string, error) {
	if strings.TrimSpace(title) == "" && strings.TrimSpace(body) == "" {
		return "", errors.New("nickname: empty title and body")
	}
	prompt := fmt.Sprintf(nicknamePrompt, title, body)
	msg, err := a.Client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     a.Model,
		MaxTokens: 64,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})
	if err != nil {
		return "", fmt.Errorf("anthropic messages.new (nickname): %w", err)
	}
	var sb strings.Builder
	for _, block := range msg.Content {
		if block.Type == "text" {
			sb.WriteString(block.Text)
			sb.WriteString("\n")
		}
	}
	nn := parseNickname(sb.String())
	if nn == "" {
		return "", errors.New("nickname: no usable line in model response")
	}
	return nn, nil
}

// ----- Claude CLI-backed nickname suggester -----

// ClaudeCLINicknameSuggester shells out to the local `claude` binary.
// Mirrors ClaudeCLISummarizer.
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

func (d *ClaudeCLINicknameSuggester) Suggest(ctx context.Context, title, body string) (string, error) {
	if strings.TrimSpace(title) == "" && strings.TrimSpace(body) == "" {
		return "", errors.New("nickname: empty title and body")
	}
	prompt := fmt.Sprintf(nicknamePrompt, title, body)
	prompt += "\n\nReturn ONLY the nickname on one line. No prose around it."

	args := []string{"-p"}
	if d.Model != "" {
		args = append(args, "--model", d.Model)
	}
	stdout, stderr, err := d.Runner.Run(ctx, d.BinaryPath, args, strings.NewReader(prompt))
	if err != nil {
		return "", fmt.Errorf("claude -p (nickname): %w (%s)", err, strings.TrimSpace(string(stderr)))
	}
	nn := parseNickname(string(bytes.TrimSpace(stdout)))
	if nn == "" {
		return "", fmt.Errorf("nickname: no usable line in claude output (raw: %s)",
			strings.TrimSpace(string(stdout)))
	}
	return nn, nil
}
