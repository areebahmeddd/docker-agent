package httpclient

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"runtime"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/docker/docker-agent/pkg/remote"
	"github.com/docker/docker-agent/pkg/version"
)

type HTTPOptions struct {
	Header http.Header
	Query  url.Values
}

type Opt func(*HTTPOptions)

func NewHTTPClient(ctx context.Context, opts ...Opt) *http.Client {
	httpOptions := HTTPOptions{
		Header: make(http.Header),
	}

	for _, opt := range opts {
		opt(&httpOptions)
	}

	// Enforce a consistent User-Agent header
	httpOptions.Header.Set("User-Agent", fmt.Sprintf("Cagent/%s (%s; %s)", version.Version, runtime.GOOS, runtime.GOARCH))

	// Disable automatic gzip: Go's default transport transparently compresses
	// and decompresses responses, which is incompatible with SSE streaming.
	// See https://github.com/docker/docker-agent/issues/1956
	rt := newTransport(ctx)

	return &http.Client{
		Transport: &userAgentTransport{
			httpOptions: httpOptions,
			rt:          rt,
		},
	}
}

func WithHeader(key, value string) Opt {
	return func(o *HTTPOptions) {
		o.Header.Set(key, value)
	}
}

func WithHeaders(headers map[string]string) Opt {
	return func(o *HTTPOptions) {
		for k, v := range headers {
			o.Header.Add(k, v)
		}
	}
}

func WithProxiedBaseURL(value string) Opt {
	return func(o *HTTPOptions) {
		o.Header.Set("X-Cagent-Forward", value)

		// Enforce consistent headers (Anthropic client sets similar header already)
		o.Header.Set("X-Cagent-Lang", "go")
		o.Header.Set("X-Cagent-OS", runtime.GOOS)
		o.Header.Set("X-Cagent-Arch", runtime.GOARCH)
		o.Header.Set("X-Cagent-Runtime", "cagent")
		o.Header.Set("X-Cagent-Runtime-Version", version.Version)
	}
}

func WithProvider(provider string) Opt {
	return func(o *HTTPOptions) {
		o.Header.Set("X-Cagent-Provider", provider)
	}
}

func WithModel(model string) Opt {
	return func(o *HTTPOptions) {
		o.Header.Set("X-Cagent-Model", model)
	}
}

func WithModelName(name string) Opt {
	return func(o *HTTPOptions) {
		if name != "" {
			o.Header.Set("X-Cagent-Model-Name", name)
		}
	}
}

func WithQuery(query url.Values) Opt {
	return func(o *HTTPOptions) {
		o.Query = query
	}
}

// newTransport returns an HTTP transport with automatic gzip compression
// disabled and using Docker Desktop proxy if available.
//
// When OpenTelemetry is enabled (i.e. OTEL_EXPORTER_OTLP_ENDPOINT is set,
// matching the gating in initOTelSDK), the transport is wrapped with
// otelhttp so each outbound request emits a CLIENT span and the W3C
// traceparent header is injected. When OTel is disabled, the bare
// transport is returned so we don't allocate per-request spans nor send
// a traceparent header to upstream LLM providers.
func newTransport(ctx context.Context) http.RoundTripper {
	// Get the base transport with Desktop proxy support from remote package
	rt := remote.NewTransport(ctx)

	// Disable compression for SSE streaming compatibility
	// Handle both direct *http.Transport and the fallback transport wrapper
	switch t := rt.(type) {
	case *http.Transport:
		t.DisableCompression = true
	case interface{ DisableCompression() }:
		t.DisableCompression()
	}

	return WrapWithOTel(rt)
}

// WrapWithOTel returns rt wrapped with otelhttp when OpenTelemetry is
// enabled (OTEL_EXPORTER_OTLP_ENDPOINT set, matching the gating in
// cmd/root/otel.go), or rt unchanged otherwise. Exposed so callers that
// build their own transports outside of NewHTTPClient can opt into the
// same env-gated instrumentation without duplicating the gating logic.
func WrapWithOTel(rt http.RoundTripper) http.RoundTripper {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return rt
	}
	return otelhttp.NewTransport(rt)
}

type userAgentTransport struct {
	httpOptions HTTPOptions
	rt          http.RoundTripper
}

func (u *userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r2 := req.Clone(req.Context())
	maps.Copy(r2.Header, u.httpOptions.Header)

	if u.httpOptions.Query != nil {
		q := r2.URL.Query()
		for k, vs := range u.httpOptions.Query {
			for _, v := range vs {
				q.Add(k, v)
			}
		}
		r2.URL.RawQuery = q.Encode()
	}

	return u.rt.RoundTrip(r2)
}
