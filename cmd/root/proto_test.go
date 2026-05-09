package root

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/runtime"
)

// recorder mimics enough of the docker-agent control plane to verify the
// proto dispatcher routes requests to the right HTTP endpoints.
type recorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *recorder) record(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, path)
}

func TestProtoDispatch_RoutesRequestsToHTTPClient(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r.Method + " " + r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/steer"),
			strings.HasSuffix(r.URL.Path, "/followup"),
			strings.HasSuffix(r.URL.Path, "/resume"):
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		default:
			_, _ = w.Write([]byte(`{"id":"s1","title":"T","messages":[]}`))
		}
	}))
	defer srv.Close()

	client, err := runtime.NewClient(srv.URL)
	require.NoError(t, err)

	out := &bytes.Buffer{}
	w := newProtoWriter(out)
	ctx := t.Context()

	cases := []struct {
		req  protoRequest
		want string
	}{
		{protoRequest{Type: "send", Message: "hi"}, "POST /api/sessions/s1/steer"},
		{protoRequest{Type: "followup", Message: "later"}, "POST /api/sessions/s1/followup"},
		{protoRequest{Type: "resume", Decision: "approve"}, "POST /api/sessions/s1/resume"},
		{protoRequest{Type: "interrupt"}, "POST /api/sessions/s1/resume"},
		{protoRequest{Type: "transcript"}, "GET /api/sessions/s1"},
	}
	for _, c := range cases {
		require.NoError(t, dispatchProto(ctx, client, "s1", c.req, w))
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, want := range []string{
		"POST /api/sessions/s1/steer",
		"POST /api/sessions/s1/followup",
		"POST /api/sessions/s1/resume",
		"POST /api/sessions/s1/resume",
		"GET /api/sessions/s1",
	} {
		assert.Contains(t, rec.calls, want)
	}
}

func TestProtoDispatch_UnknownType(t *testing.T) {
	out := &bytes.Buffer{}
	w := newProtoWriter(out)

	err := dispatchProto(t.Context(), nil, "s1", protoRequest{Type: "nope"}, w)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown request type")
}
