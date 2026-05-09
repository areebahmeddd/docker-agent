package root

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// maxErrorBodyBytes caps how much of an error response body we read into
// memory. SSE error replies should be tiny; this just defends the client
// against a misbehaving server that streams an unbounded error payload.
const maxErrorBodyBytes = 4 * 1024

// openEventStream connects to the SSE event stream of a session running on
// addr and returns the response body. Callers are responsible for closing
// the body. The body produces standard text/event-stream output with one
// JSON payload per "data:" line.
func openEventStream(ctx context.Context, addr, sessionID string) (io.ReadCloser, error) {
	url := addr + "/api/sessions/" + sessionID + "/events"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", url, err)
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("server returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return resp.Body, nil
}

// readEventStream reads SSE "data:" lines from r and invokes onEvent with
// each raw JSON payload. The function returns when ctx is cancelled (with
// ctx.Err), the stream ends (nil), or onEvent returns an error.
//
// Payloads are passed through as json.RawMessage so callers can either
// forward the bytes verbatim or re-decode them into a typed value without
// paying a redundant unmarshal/marshal round-trip.
func readEventStream(ctx context.Context, r io.Reader, onEvent func(json.RawMessage) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		after, ok := bytes.CutPrefix(scanner.Bytes(), []byte("data: "))
		if !ok {
			continue
		}
		// Copy because the scanner reuses its underlying buffer.
		payload := append(json.RawMessage(nil), after...)
		if err := onEvent(payload); err != nil {
			return err
		}
	}
	return scanner.Err()
}
