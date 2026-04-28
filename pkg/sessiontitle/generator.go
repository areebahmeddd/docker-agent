// Package sessiontitle provides session title generation using a one-shot LLM call.
// It is designed to be independent of pkg/runtime to avoid circular dependencies
// and the overhead of spinning up a nested runtime.
package sessiontitle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/docker/docker-agent/pkg/chat"
	"github.com/docker/docker-agent/pkg/model/provider"
	"github.com/docker/docker-agent/pkg/model/provider/options"
)

const (
	systemPrompt     = "You are a helpful AI assistant that generates concise, descriptive titles for conversations. You will be given up to 2 recent user messages and asked to create a single-line title that captures the main topic. Never use newlines or line breaks in your response."
	userPromptFormat = "Based on the following recent user messages from a conversation with an AI assistant, generate a short, descriptive title (maximum 50 characters) that captures the main topic or purpose of the conversation. Return ONLY the title text on a single line, nothing else. Do not include any newlines, explanations, or formatting.\n\nRecent user messages:\n%s\n\n"

	// titleMaxTokens is the max output token budget for title generation.
	// This is sized for visible output only (~50 chars ≈ 12-15 tokens).
	// Providers that need extra headroom for hidden reasoning tokens
	// (e.g. OpenAI reasoning models) handle the adjustment internally.
	titleMaxTokens = 20

	// titleGenerationTimeout is the maximum time to wait for title generation.
	// Title generation should be quick since we disable thinking and use low max_tokens.
	// If the API is slow or hanging (e.g., due to server-side thinking), we should timeout.
	titleGenerationTimeout = 30 * time.Second
)

// Generator generates session titles using a one-shot LLM completion.
type Generator struct {
	models []provider.Provider
}

// New creates a new title Generator with the given model provider.
// The first argument is treated as the primary model; any additional models are
// treated as fallbacks (tried in order) if earlier models fail.
// Nil providers are silently ignored.
func New(model provider.Provider, fallbackModels ...provider.Provider) *Generator {
	models := slices.DeleteFunc(
		append([]provider.Provider{model}, fallbackModels...),
		func(p provider.Provider) bool { return p == nil },
	)
	return &Generator{models: models}
}

// Generate produces a title for a session based on the provided user messages.
// It performs a one-shot LLM call directly via the provider's CreateChatCompletionStream,
// avoiding the overhead of spinning up a nested runtime.
// Returns an empty string if generation fails or no messages are provided.
func (g *Generator) Generate(ctx context.Context, sessionID string, userMessages []string) (string, error) {
	if g == nil || len(g.models) == 0 || len(userMessages) == 0 {
		return "", nil
	}

	// Apply timeout to prevent hanging on slow or unresponsive models.
	ctx, cancel := context.WithTimeout(ctx, titleGenerationTimeout)
	defer cancel()

	slog.Debug("Generating title for session", "session_id", sessionID, "message_count", len(userMessages))

	messages := buildPrompt(userMessages)

	var lastErr error
	for idx, baseModel := range g.models {
		if err := ctx.Err(); err != nil {
			return "", err
		}

		title, err := generateOnce(ctx, baseModel, messages)
		if err != nil {
			lastErr = err
			slog.Error("Title generation attempt failed",
				"session_id", sessionID,
				"model", baseModel.ID(),
				"attempt", idx+1,
				"error", err)
			continue
		}
		if title == "" {
			// Empty/invalid title output - treat as a failure and try fallbacks.
			lastErr = fmt.Errorf("empty title output from model %q", baseModel.ID())
			slog.Debug("Generated empty title, trying next model",
				"session_id", sessionID,
				"model", baseModel.ID(),
				"attempt", idx+1)
			continue
		}

		slog.Debug("Generated session title", "session_id", sessionID, "title", title, "model", baseModel.ID())
		return title, nil
	}

	if lastErr != nil {
		return "", fmt.Errorf("generating title failed: %w", lastErr)
	}
	return "", nil
}

// generateOnce performs a single one-shot title generation call against baseModel
// and returns the sanitized title (which may be empty if the model produced no
// usable output).
func generateOnce(ctx context.Context, baseModel provider.Provider, messages []chat.Message) (string, error) {
	// Clone the model with title-generation-specific options so each attempt
	// gets a consistent, low-token one-shot call.
	titleModel := provider.CloneWithOptions(
		ctx,
		baseModel,
		options.WithStructuredOutput(nil),
		options.WithMaxTokens(titleMaxTokens),
		options.WithNoThinking(),
		options.WithGeneratingTitle(),
	)

	stream, err := titleModel.CreateChatCompletionStream(ctx, messages, nil)
	if err != nil {
		return "", err
	}
	defer stream.Close()

	var title strings.Builder
	for {
		response, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		if len(response.Choices) > 0 {
			title.WriteString(response.Choices[0].Delta.Content)
		}
	}

	return sanitizeTitle(title.String()), nil
}

// buildPrompt formats the user messages into the system+user message pair sent
// to the model.
func buildPrompt(userMessages []string) []chat.Message {
	var formatted strings.Builder
	for i, msg := range userMessages {
		fmt.Fprintf(&formatted, "%d. %s\n", i+1, msg)
	}
	return []chat.Message{
		{Role: chat.MessageRoleSystem, Content: systemPrompt},
		{Role: chat.MessageRoleUser, Content: fmt.Sprintf(userPromptFormat, formatted.String())},
	}
}

// sanitizeTitle returns the first non-empty trimmed line of title, with any
// stray carriage returns removed. This guarantees a single-line title safe for
// TUI rendering.
func sanitizeTitle(title string) string {
	for line := range strings.SplitSeq(title, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return strings.ReplaceAll(line, "\r", "")
		}
	}
	return ""
}
