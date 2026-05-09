package root

import (
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
