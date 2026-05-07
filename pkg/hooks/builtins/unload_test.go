package builtins

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/hooks"
)

// TestUnloadBuiltin_Registered guarantees the public name is findable
// on a registry built by [Register], so YAML hook entries that name it
// actually resolve.
func TestUnloadBuiltin_Registered(t *testing.T) {
	t.Parallel()

	r := hooks.NewRegistry()
	require.NoError(t, Register(r))

	fn, ok := r.LookupBuiltin(Unload)
	require.True(t, ok, "%q must be registered on the hook registry", Unload)
	require.NotNil(t, fn)
}

// TestUnload_PostsToDefaultEndpoint exercises the happy path against a
// real httptest server: the builtin must derive the `_unload` URL from
// the model's BaseURL and POST `{"model": "<id>"}`.
func TestUnload_PostsToDefaultEndpoint(t *testing.T) {
	t.Parallel()

	var (
		gotPath string
		gotBody map[string]string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	in := &hooks.Input{
		FromAgent: "from",
		ToAgent:   "to",
		FromAgentModels: []hooks.ModelEndpoint{{
			Provider: "dmr",
			Model:    "ai/qwen3",
			BaseURL:  server.URL + "/engines/llama.cpp/v1",
		}},
	}
	out, err := unload(t.Context(), in, nil)
	require.NoError(t, err)
	assert.Nil(t, out, "unload is observational; output must be nil")
	assert.Equal(t, "/engines/llama.cpp/_unload", gotPath)
	assert.Equal(t, map[string]string{"model": "ai/qwen3"}, gotBody)
}

// TestUnload_HonoursOverrideUnloadAPI documents that an explicit
// `unload_api` on the model takes precedence over the default
// derivation, and is rebased onto the BaseURL's host when relative.
func TestUnload_HonoursOverrideUnloadAPI(t *testing.T) {
	t.Parallel()

	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	in := &hooks.Input{
		FromAgent: "from",
		ToAgent:   "to",
		FromAgentModels: []hooks.ModelEndpoint{{
			Provider:  "dmr",
			Model:     "ai/qwen3",
			BaseURL:   server.URL + "/engines/v1",
			UnloadAPI: "/custom/unload",
		}},
	}
	_, err := unload(t.Context(), in, nil)
	require.NoError(t, err)
	assert.Equal(t, "/custom/unload", gotPath)
}

// TestUnload_SkipsNonDMRProviders pins the cross-provider safety
// property: wiring `unload` on a heterogeneous chain is harmless
// because non-DMR endpoints are silently skipped.
func TestUnload_SkipsNonDMRProviders(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	in := &hooks.Input{
		FromAgent: "from",
		ToAgent:   "to",
		FromAgentModels: []hooks.ModelEndpoint{
			{Provider: "openai", Model: "gpt-4", BaseURL: server.URL},
			{Provider: "anthropic", Model: "claude", BaseURL: server.URL},
		},
	}
	_, err := unload(t.Context(), in, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(0), calls.Load(), "no HTTP call must reach a non-DMR endpoint")
}

// TestUnload_NoopWhenFromEqualsTo documents the no-self-unload guard:
// transferring back into the same agent must not unload the model the
// next turn is about to use.
func TestUnload_NoopWhenFromEqualsTo(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	in := &hooks.Input{
		FromAgent: "same",
		ToAgent:   "same",
		FromAgentModels: []hooks.ModelEndpoint{{
			Provider: "dmr",
			Model:    "ai/qwen3",
			BaseURL:  server.URL + "/engines/v1",
		}},
	}
	_, err := unload(t.Context(), in, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(0), calls.Load())
}

// TestUnload_NoopWhenFromAgentEmpty documents the cheap path: the very
// first switch into the team's default agent has no previous agent and
// must not fire any HTTP call.
func TestUnload_NoopWhenFromAgentEmpty(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	in := &hooks.Input{
		ToAgent: "to",
		FromAgentModels: []hooks.ModelEndpoint{{
			Provider: "dmr",
			Model:    "ai/qwen3",
			BaseURL:  server.URL + "/engines/v1",
		}},
	}
	_, err := unload(t.Context(), in, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(0), calls.Load())
}

// TestUnload_SwallowsServerErrors verifies the best-effort contract:
// a 5xx from the engine must NOT propagate back as a hook error,
// because agent switching has to keep moving even when the unload
// endpoint is down.
func TestUnload_SwallowsServerErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer server.Close()

	in := &hooks.Input{
		FromAgent: "from",
		ToAgent:   "to",
		FromAgentModels: []hooks.ModelEndpoint{{
			Provider: "dmr",
			Model:    "ai/qwen3",
			BaseURL:  server.URL + "/engines/v1",
		}},
	}
	out, err := unload(t.Context(), in, nil)
	require.NoError(t, err)
	assert.Nil(t, out)
}

// TestUnload_NoopWhenNoEndpoint documents that a DMR model without a
// BaseURL or unload_api is silently skipped (rather than erroring) so
// the hook stays harmless when wired against an in-process / test
// provider that hasn't resolved an HTTP endpoint.
func TestUnload_NoopWhenNoEndpoint(t *testing.T) {
	t.Parallel()

	in := &hooks.Input{
		FromAgent: "from",
		ToAgent:   "to",
		FromAgentModels: []hooks.ModelEndpoint{{
			Provider: "dmr",
			Model:    "ai/qwen3",
		}},
	}
	out, err := unload(t.Context(), in, nil)
	require.NoError(t, err)
	assert.Nil(t, out)
}

func TestRebaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		baseURL     string
		path        string
		want        string
		errContains string // empty ⇒ expect success
	}{
		{
			name:    "absolute https URL is returned verbatim",
			baseURL: "http://anything",
			path:    "https://api.example.com/unload",
			want:    "https://api.example.com/unload",
		},
		{
			name:    "rooted path drops base path",
			baseURL: "http://localhost:12434/engines/v1",
			path:    "/engines/_unload",
			want:    "http://localhost:12434/engines/_unload",
		},
		{
			name:    "relative path is rooted",
			baseURL: "http://localhost:12434/engines/v1",
			path:    "engines/_unload",
			want:    "http://localhost:12434/engines/_unload",
		},
		{
			name:        "empty base URL with relative path errors",
			path:        "/engines/_unload",
			errContains: "is not absolute",
		},
		{
			name:        "base URL without scheme errors",
			baseURL:     "localhost:12434/engines/v1",
			path:        "/engines/_unload",
			errContains: "is not absolute",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := rebaseURL(tt.baseURL, tt.path)
			if tt.errContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDefaultUnloadURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		baseURL  string
		expected string
	}{
		{
			name:     "standard engines path",
			baseURL:  "http://127.0.0.1:12434/engines/v1/",
			expected: "http://127.0.0.1:12434/engines/_unload",
		},
		{
			name:     "no trailing slash",
			baseURL:  "http://127.0.0.1:12434/engines/v1",
			expected: "http://127.0.0.1:12434/engines/_unload",
		},
		{
			name:     "Docker Desktop experimental prefix",
			baseURL:  "http://_/exp/vDD4.40/engines/v1",
			expected: "http://_/exp/vDD4.40/engines/_unload",
		},
		{
			name:     "backend-scoped path",
			baseURL:  "http://127.0.0.1:12434/engines/llama.cpp/v1/",
			expected: "http://127.0.0.1:12434/engines/llama.cpp/_unload",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, defaultUnloadURL(tt.baseURL))
		})
	}
}
