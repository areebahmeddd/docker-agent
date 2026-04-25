package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/docker/docker-agent/pkg/config/latest"
	"github.com/docker/docker-agent/pkg/upstream"
)

type remoteMCPClient struct {
	sessionClient

	url           string
	transportType string
	headers       map[string]string
	tokenStore    OAuthTokenStore
	managed       bool
	oauthConfig   *latest.RemoteOAuthConfig
}

func newRemoteClient(url, transportType string, headers map[string]string, tokenStore OAuthTokenStore, oauthConfig *latest.RemoteOAuthConfig) *remoteMCPClient {
	slog.Debug("Creating remote MCP client", "url", url, "transport", transportType, "headers", headers)

	if tokenStore == nil {
		tokenStore = NewInMemoryTokenStore()
	}

	return &remoteMCPClient{
		url:           url,
		transportType: transportType,
		headers:       headers,
		tokenStore:    tokenStore,
		oauthConfig:   oauthConfig,
	}
}

func (c *remoteMCPClient) Initialize(ctx context.Context, _ *gomcp.InitializeRequest) (*gomcp.InitializeResult, error) {
	// Create HTTP client with OAuth support. We keep a reference to the
	// oauthTransport so we can recognise the deferred-OAuth case (the
	// transport returned an AuthorizationRequiredError because the request
	// context disallowed prompts) and re-emit a clean
	// AuthorizationRequiredError that callers can detect with errors.As.
	httpClient, oauthT := c.createHTTPClient()

	var transport gomcp.Transport

	switch c.transportType {
	case "sse":
		transport = &gomcp.SSEClientTransport{
			Endpoint:   c.url,
			HTTPClient: httpClient,
		}
	case "streamable", "streamable-http":
		transport = &gomcp.StreamableClientTransport{
			Endpoint:             c.url,
			HTTPClient:           httpClient,
			DisableStandaloneSSE: true,
		}
	default:
		return nil, fmt.Errorf("unsupported transport type: %s", c.transportType)
	}

	// Create an MCP client with elicitation support
	impl := &gomcp.Implementation{
		Name:    "docker agent",
		Version: "1.0.0",
	}

	toolChanged, promptChanged := c.notificationHandlers()

	opts := &gomcp.ClientOptions{
		ElicitationHandler:       c.handleElicitationRequest,
		ToolListChangedHandler:   toolChanged,
		PromptListChangedHandler: promptChanged,
	}

	client := gomcp.NewClient(impl, opts)

	// Connect to the MCP server
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, enrichConnectError(err, oauthT)
	}

	c.setSession(session)

	slog.Debug("Remote MCP client connected successfully")
	return session.InitializeResult(), nil
}

// enrichConnectError wraps the error returned by client.Connect so callers
// can distinguish the deferred-OAuth case from a real failure.
//
// The MCP SDK uses fmt.Errorf("%w: %v", …) when it surfaces transport errors,
// which means the original error is included as text only — not in the unwrap
// chain — so we can't rely on errors.As against the SDK-wrapped error.
// Instead we read the deferred-auth flag back off the transport and re-emit
// a clean AuthorizationRequiredError.
//
// Pre: err != nil and t != nil; only called from the Connect failure path.
func enrichConnectError(err error, t *oauthTransport) error {
	if t.authorizationRequired() {
		return &AuthorizationRequiredError{URL: t.baseURL}
	}
	return fmt.Errorf("failed to connect to MCP server: %w", err)
}

// SetManagedOAuth sets whether OAuth should be handled in managed mode.
// In managed mode, the client handles the OAuth flow instead of the server.
func (c *remoteMCPClient) SetManagedOAuth(managed bool) {
	c.mu.Lock()
	c.managed = managed
	c.mu.Unlock()
}

// createHTTPClient creates an HTTP client with custom headers and OAuth support.
// Header values may contain ${headers.NAME} placeholders that are resolved
// at request time from upstream headers stored in the request context.
//
// The oauthTransport is returned alongside the client so callers can inspect
// the transport's state (e.g. whether OAuth was deferred) when Connect()
// returns and we need to surface the actual cause of the failure.
func (c *remoteMCPClient) createHTTPClient() (*http.Client, *oauthTransport) {
	base := c.headerTransport()

	// Then wrap with OAuth support
	oauthT := &oauthTransport{
		base:        base,
		client:      c,
		tokenStore:  c.tokenStore,
		baseURL:     c.url,
		managed:     c.managed,
		oauthConfig: c.oauthConfig,
	}

	return &http.Client{Transport: oauthT}, oauthT
}

func (c *remoteMCPClient) headerTransport() http.RoundTripper {
	if len(c.headers) > 0 {
		return upstream.NewHeaderTransport(http.DefaultTransport, c.headers)
	}
	return http.DefaultTransport
}
