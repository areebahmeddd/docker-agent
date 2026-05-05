package httpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHeaders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		opts       []Opt
		wantHeader string
		wantValue  string
	}{
		{
			name:       "WithModel sets X-Cagent-Model",
			opts:       []Opt{WithModel("gpt-4o")},
			wantHeader: "X-Cagent-Model",
			wantValue:  "gpt-4o",
		},
		{
			name:       "WithModelName sets X-Cagent-Model-Name",
			opts:       []Opt{WithModelName("my-fast-model")},
			wantHeader: "X-Cagent-Model-Name",
			wantValue:  "my-fast-model",
		},
		{
			name:       "WithModelName skips header when empty",
			opts:       []Opt{WithModelName("")},
			wantHeader: "X-Cagent-Model-Name",
			wantValue:  "",
		},
		{
			name:       "WithProvider sets X-Cagent-Provider",
			opts:       []Opt{WithProvider("openai")},
			wantHeader: "X-Cagent-Provider",
			wantValue:  "openai",
		},
		{
			name:       "compression is disabled to support SSE streaming",
			wantHeader: "Accept-Encoding",
			wantValue:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			headers := doRequest(t, tt.opts...)

			if tt.wantValue != "" {
				assert.Equal(t, tt.wantValue, headers.Get(tt.wantHeader))
			} else {
				assert.Empty(t, headers.Get(tt.wantHeader))
			}
		})
	}
}

// doRequest creates an HTTP client with the given options, sends a GET request
// to a test server, and returns the headers the server received.
func doRequest(t *testing.T, opts ...Opt) http.Header {
	t.Helper()
	return doRequestWithCtx(t, t.Context(), opts...)
}

// doRequestWithCtx is like doRequest but uses the supplied context for
// the outbound request, so callers can exercise context-derived header
// injection (e.g. session ID propagation).
func doRequestWithCtx(t *testing.T, ctx context.Context, opts ...Opt) http.Header {
	t.Helper()

	var capturedHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header
	}))
	defer srv.Close()

	client := NewHTTPClient(ctx, opts...)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, http.NoBody)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	return capturedHeaders
}

func TestSessionIDHeader_GatewayBoundOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		ctxSessionID   string
		opts           []Opt
		wantHeaderSent bool
	}{
		{
			name:           "session ID present, gateway-bound (X-Cagent-Forward set) → header sent",
			ctxSessionID:   "sess-abc",
			opts:           []Opt{WithProxiedBaseURL("https://gateway.example/v1")},
			wantHeaderSent: true,
		},
		{
			name:           "session ID present, no X-Cagent-Forward → header skipped",
			ctxSessionID:   "sess-abc",
			opts:           nil,
			wantHeaderSent: false,
		},
		{
			name:           "no session ID on context, gateway-bound → header skipped",
			ctxSessionID:   "",
			opts:           []Opt{WithProxiedBaseURL("https://gateway.example/v1")},
			wantHeaderSent: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			if tt.ctxSessionID != "" {
				ctx = ContextWithSessionID(ctx, tt.ctxSessionID)
			}
			headers := doRequestWithCtx(t, ctx, tt.opts...)

			if tt.wantHeaderSent {
				assert.Equal(t, tt.ctxSessionID, headers.Get("X-Cagent-Session-Id"))
			} else {
				assert.Empty(t, headers.Get("X-Cagent-Session-Id"))
			}
		})
	}
}

func TestContextWithSessionID_RoundTrip(t *testing.T) {
	t.Parallel()

	assert.Empty(t, SessionIDFromContext(t.Context()), "empty context yields empty session ID")
	ctx := ContextWithSessionID(t.Context(), "sess-xyz")
	assert.Equal(t, "sess-xyz", SessionIDFromContext(ctx))
}
