package chatserver

import "github.com/openai/openai-go/v3"

// This file declares the OpenAI-compatible request/response types used by
// /v1/chat/completions and /v1/models. We hand-roll most of them instead of
// borrowing from github.com/openai/openai-go/v3 because the SDK's response
// structs are deserialised through its internal `apijson` package and don't
// have `omitempty` JSON tags; marshalling them with stdlib `encoding/json`
// produces noisy responses full of empty audio/tool_call/refusal
// placeholders. `openai.Model` round-trips cleanly with stdlib json, so
// /v1/models reuses it.

// --- Request --------------------------------------------------------------

// ChatCompletionRequest is the body of a /v1/chat/completions call. We
// only declare the fields we act on; any extras are silently ignored.
type ChatCompletionRequest struct {
	Model    string                  `json:"model"`
	Messages []ChatCompletionMessage `json:"messages"`
	Stream   bool                    `json:"stream,omitempty"`
}

// ChatCompletionMessage is a single message in the conversation. Multi-modal
// content (image parts, audio, etc.) is not supported and falls back to the
// `Content` string.
type ChatCompletionMessage struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	Name       string `json:"name,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// --- Non-streaming response -----------------------------------------------

type ChatCompletionResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []ChatCompletionChoice `json:"choices"`
	Usage   *ChatCompletionUsage   `json:"usage,omitempty"`
}

type ChatCompletionChoice struct {
	Index        int                   `json:"index"`
	Message      ChatCompletionMessage `json:"message"`
	FinishReason string                `json:"finish_reason"`
}

// ChatCompletionUsage reports approximate token counts. Best-effort: when
// the underlying provider doesn't report usage we omit the field entirely.
type ChatCompletionUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// --- Streaming response ---------------------------------------------------

// ChatCompletionStreamResponse is one SSE chunk emitted when the client
// requests stream: true.
type ChatCompletionStreamResponse struct {
	ID      string                       `json:"id"`
	Object  string                       `json:"object"`
	Created int64                        `json:"created"`
	Model   string                       `json:"model"`
	Choices []ChatCompletionStreamChoice `json:"choices"`
}

type ChatCompletionStreamChoice struct {
	Index        int                       `json:"index"`
	Delta        ChatCompletionStreamDelta `json:"delta"`
	FinishReason string                    `json:"finish_reason,omitempty"`
}

type ChatCompletionStreamDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

// --- Models endpoint ------------------------------------------------------

// ModelsResponse is the body returned by /v1/models. Each agent in the team
// is exposed as one entry.
type ModelsResponse struct {
	Object string         `json:"object"`
	Data   []openai.Model `json:"data"`
}

// --- Errors ---------------------------------------------------------------

// ErrorResponse is the OpenAI-style error envelope returned on 4xx/5xx.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}
