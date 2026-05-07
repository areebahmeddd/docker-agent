package builtins

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/docker/docker-agent/pkg/hooks"
)

// HTTPPost is the registered name of the http_post builtin.
const HTTPPost = "http_post"

// httpPost POSTs args[1] to args[0] with Content-Type: application/json.
// Empty URL is a no-op; network errors and non-2xx responses are
// logged and swallowed so the dispatch verdict stays nil. The hook
// executor already wraps ctx with [Hook.GetTimeout].
func httpPost(ctx context.Context, _ *hooks.Input, args []string) (*hooks.Output, error) {
	if len(args) == 0 || args[0] == "" {
		return nil, nil
	}
	url := args[0]
	var body string
	if len(args) >= 2 {
		body = args[1]
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("http_post: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.WarnContext(ctx, "http_post: request failed", "url", url, "error", err)
		return nil, nil
	}
	defer resp.Body.Close()
	// Drain so the connection can be reused by keep-alive.
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		slog.WarnContext(ctx, "http_post: non-success response", "url", url, "status", resp.StatusCode)
	}
	return nil, nil
}
