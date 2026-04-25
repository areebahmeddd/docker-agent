// Package chatserver implements an OpenAI-compatible HTTP server that exposes
// docker-agent agents through the /v1/chat/completions and /v1/models
// endpoints.
//
// The goal is to let any tool that already speaks OpenAI's chat protocol
// (e.g. Open WebUI, custom shell scripts using the openai SDK) drive a
// docker-agent agent without needing to know about docker-agent's own
// protocol.
//
// On types: we deliberately don't reuse the request/response structs from
// github.com/openai/openai-go/v3. The SDK is built around its internal
// `apijson` encoder; with stdlib `encoding/json` those types serialize
// every field and produce noisy responses. `apijson` lives under
// `internal/`, so we can't borrow it. `openai.Model` is the one type that
// round-trips cleanly with stdlib json, so we reuse it for /v1/models.
package chatserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/openai/openai-go/v3"

	"github.com/docker/docker-agent/pkg/config"
	"github.com/docker/docker-agent/pkg/runtime"
	"github.com/docker/docker-agent/pkg/session"
	"github.com/docker/docker-agent/pkg/team"
	"github.com/docker/docker-agent/pkg/teamloader"
)

// Run starts an OpenAI-compatible HTTP server on the given listener and
// blocks until ctx is cancelled or the server fails. The team is loaded
// once from agentFilename and shared across requests; every chat completion
// request gets a fresh session.
//
// If agentName is empty, every agent in the team is exposed and the team's
// default agent is used when the client doesn't pin one.
func Run(ctx context.Context, agentFilename, agentName string, runConfig *config.RuntimeConfig, ln net.Listener) error {
	slog.Debug("Starting chat completions server", "agent", agentFilename, "addr", ln.Addr())

	t, err := loadTeam(ctx, agentFilename, runConfig)
	if err != nil {
		return err
	}
	defer func() {
		if err := t.StopToolSets(ctx); err != nil {
			slog.Error("Failed to stop tool sets", "error", err)
		}
	}()

	policy, err := newAgentPolicy(t, agentName)
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Handler:           newRouter(&server{team: t, policy: policy}),
		ReadHeaderTimeout: 30 * time.Second,
	}
	return serve(ctx, httpServer, ln)
}

// loadTeam resolves and loads the team referenced by agentFilename.
func loadTeam(ctx context.Context, agentFilename string, runConfig *config.RuntimeConfig) (*team.Team, error) {
	src, err := config.Resolve(agentFilename, nil)
	if err != nil {
		return nil, err
	}
	t, err := teamloader.Load(ctx, src, runConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to load agents: %w", err)
	}
	return t, nil
}

// serve runs httpServer on ln until ctx is cancelled, then triggers a
// graceful shutdown.
func serve(ctx context.Context, httpServer *http.Server, ln net.Listener) error {
	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.Serve(ln) }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// server is concurrent-safe: every request creates its own session and
// runtime, so the only shared state is the team (whose toolsets are
// independently safe to call).
type server struct {
	team   *team.Team
	policy agentPolicy
}

func newRouter(s *server) http.Handler {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	e.Use(middleware.RequestLogger())
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins: []string{"*"},
		AllowMethods: []string{http.MethodGet, http.MethodPost, http.MethodOptions},
		AllowHeaders: []string{"Authorization", "Content-Type", "Accept"},
		MaxAge:       86400,
	}))

	e.GET("/v1/models", s.handleModels)
	e.POST("/v1/chat/completions", s.handleChatCompletions)
	return e
}

func (s *server) handleModels(c echo.Context) error {
	data := make([]openai.Model, 0, len(s.policy.exposed))
	for _, name := range s.policy.exposed {
		data = append(data, openai.Model{ID: name, OwnedBy: "docker-agent"})
	}
	return c.JSON(http.StatusOK, ModelsResponse{Object: "list", Data: data})
}

func (s *server) handleChatCompletions(c echo.Context) error {
	var req ChatCompletionRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return writeError(c, http.StatusBadRequest, err.Error())
	}
	if len(req.Messages) == 0 {
		return writeError(c, http.StatusBadRequest, "at least one message is required")
	}

	sess := buildSession(req.Messages)
	if sess == nil {
		return writeError(c, http.StatusBadRequest, "no user message provided")
	}

	agentName := s.policy.pick(req.Model)
	rt, err := runtime.New(s.team, runtime.WithCurrentAgent(agentName))
	if err != nil {
		return writeError(c, http.StatusInternalServerError, fmt.Sprintf("failed to create runtime: %v", err))
	}

	// Echo back the requested model verbatim when set, so clients matching
	// on the model field stay happy. Otherwise expose the actual agent.
	model := agentName
	if req.Model != "" {
		model = req.Model
	}

	if req.Stream {
		return s.streamChatCompletion(c, rt, sess, model)
	}
	return s.chatCompletion(c, rt, sess, model)
}

// chatCompletion runs the agent to completion and replies with one
// non-streaming OpenAI ChatCompletion object.
func (s *server) chatCompletion(c echo.Context, rt runtime.Runtime, sess *session.Session, model string) error {
	if err := runAgentLoop(c.Request().Context(), rt, sess, nil); err != nil {
		return writeError(c, http.StatusInternalServerError, fmt.Sprintf("agent execution failed: %v", err))
	}

	return c.JSON(http.StatusOK, ChatCompletionResponse{
		ID:      newChatID(),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []ChatCompletionChoice{{
			Index: 0,
			Message: ChatCompletionMessage{
				Role:    "assistant",
				Content: sess.GetLastAssistantMessageContent(),
			},
			FinishReason: "stop",
		}},
		Usage: sessionUsage(sess),
	})
}

// streamChatCompletion runs the agent and streams its response back to the
// client as Server-Sent Events in OpenAI's chat.completion.chunk format.
func (s *server) streamChatCompletion(c echo.Context, rt runtime.Runtime, sess *session.Session, model string) error {
	stream := newSSEStream(c.Response(), newChatID(), model)

	// Initial "role: assistant" delta so clients can start rendering.
	stream.send(ChatCompletionStreamDelta{Role: "assistant"}, "")

	if err := runAgentLoop(c.Request().Context(), rt, sess, func(content string) {
		if content != "" {
			stream.send(ChatCompletionStreamDelta{Content: content}, "")
		}
	}); err != nil {
		// Surface the error as a final content chunk so the client sees it.
		stream.send(ChatCompletionStreamDelta{Content: fmt.Sprintf("\n\n[error: %v]", err)}, "")
	}

	stream.send(ChatCompletionStreamDelta{}, "stop")
	stream.done()
	return nil
}

// sseStream writes OpenAI-style chat.completion.chunk events to a response.
// It centralises SSE bookkeeping (headers, JSON encoding, flushing,
// terminator) so the handler can focus on what to emit.
type sseStream struct {
	w       http.ResponseWriter
	id      string
	model   string
	created int64
}

func newSSEStream(w http.ResponseWriter, id, model string) *sseStream {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	return &sseStream{w: w, id: id, model: model, created: time.Now().Unix()}
}

func (s *sseStream) send(delta ChatCompletionStreamDelta, finishReason string) {
	chunk := ChatCompletionStreamResponse{
		ID:      s.id,
		Object:  "chat.completion.chunk",
		Created: s.created,
		Model:   s.model,
		Choices: []ChatCompletionStreamChoice{{
			Index:        0,
			Delta:        delta,
			FinishReason: finishReason,
		}},
	}
	data, err := json.Marshal(chunk)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(s.w, "data: %s\n\n", data)
	if f, ok := s.w.(http.Flusher); ok {
		f.Flush()
	}
}

// done writes the OpenAI sentinel terminator that ends the stream.
func (s *sseStream) done() {
	_, _ = fmt.Fprint(s.w, "data: [DONE]\n\n")
	if f, ok := s.w.(http.Flusher); ok {
		f.Flush()
	}
}

// newChatID returns a fresh OpenAI-style chat completion id.
func newChatID() string { return "chatcmpl-" + uuid.NewString() }

// writeError writes an OpenAI-style error envelope.
func writeError(c echo.Context, status int, message string) error {
	return c.JSON(status, ErrorResponse{Error: ErrorDetail{
		Message: message,
		Type:    errTypeFor(status),
	}})
}

func errTypeFor(status int) string {
	if status >= 500 {
		return "internal_error"
	}
	return "invalid_request_error"
}
