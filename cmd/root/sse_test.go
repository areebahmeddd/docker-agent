package root

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadEventStream_DecodesDataLines(t *testing.T) {
	body := strings.NewReader("data: {\"a\":1}\n\ndata: {\"a\":2}\n\n: ignored comment\n")

	var got []string
	err := readEventStream(t.Context(), body, func(p json.RawMessage) error {
		got = append(got, string(p))
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{`{"a":1}`, `{"a":2}`}, got)
}

func TestReadEventStream_StopsOnHandlerError(t *testing.T) {
	body := strings.NewReader("data: {\"a\":1}\n\ndata: {\"a\":2}\n\n")
	want := assert.AnError

	count := 0
	err := readEventStream(t.Context(), body, func(p json.RawMessage) error {
		count++
		return want
	})
	require.ErrorIs(t, err, want)
	assert.Equal(t, 1, count)
}

func TestOpenEventStream_PropagatesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("nope"))
	}))
	defer srv.Close()

	_, err := openEventStream(t.Context(), srv.URL, "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestOpenEventStream_StreamsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"hello\":\"world\"}\n\n"))
	}))
	defer srv.Close()

	body, err := openEventStream(t.Context(), srv.URL, "s1")
	require.NoError(t, err)
	defer body.Close()

	var got []string
	err = readEventStream(t.Context(), body, func(p json.RawMessage) error {
		got = append(got, string(p))
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{`{"hello":"world"}`}, got)
}

// TestOpenEventStream_CapsErrorBody guards against a misbehaving server
// pushing an unbounded error body into client memory.
func TestOpenEventStream_CapsErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(strings.Repeat("x", 10*maxErrorBodyBytes)))
	}))
	defer srv.Close()

	_, err := openEventStream(t.Context(), srv.URL, "s1")
	require.Error(t, err)
	// The error message embeds at most maxErrorBodyBytes of the body.
	assert.LessOrEqual(t, len(err.Error()), maxErrorBodyBytes+256)
}

// TestReadEventStream_ReturnsCtxErr verifies the helper surfaces ctx
// cancellation so callers can distinguish it from a clean stream end.
func TestReadEventStream_ReturnsCtxErr(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	body := strings.NewReader("data: {}\n\ndata: {}\n\n")

	calls := 0
	err := readEventStream(ctx, body, func(json.RawMessage) error {
		calls++
		cancel()
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, calls)
}
