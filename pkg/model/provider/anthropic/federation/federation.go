// Package federation builds the Anthropic Workload Identity Federation
// pieces (identity-token providers and SDK request options) from a typed
// [latest.AuthConfig].
//
// The package is deliberately Anthropic-specific for now. If another
// provider gains a federation flow we'll factor out the source-of-token
// helpers; until then keeping the Anthropic dependency local avoids a
// cross-cutting auth abstraction.
package federation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/docker/docker-agent/pkg/config/latest"
	"github.com/docker/docker-agent/pkg/environment"
)

// RequestOptions returns the anthropic-sdk-go RequestOption(s) that
// authenticate the client using OIDC Workload Identity Federation, plus a
// ready-to-call IdentityTokenFunc whose errors are wrapped with
// [RefreshError] so callers can recognise refresh failures specifically.
//
// The returned options should be passed to anthropic.NewClient instead of
// (not in addition to) option.WithAPIKey.
//
// If onRefreshError is non-nil, it is invoked synchronously every time the
// token source returns an error. This is the hook the runtime uses to
// surface refresh errors in the TUI.
func RequestOptions(
	cfg *latest.FederationAuthConfig,
	env environment.Provider,
	onRefreshError func(error),
) ([]option.RequestOption, error) {
	if cfg == nil {
		return nil, errors.New("federation: nil config")
	}
	src, err := tokenSource(cfg.IdentityToken, env)
	if err != nil {
		return nil, err
	}

	provider := func(ctx context.Context) (string, error) {
		token, err := src(ctx)
		if err != nil {
			wrapped := &RefreshError{
				FederationRuleID: cfg.FederationRuleID,
				OrganizationID:   cfg.OrganizationID,
				SourceKind:       sourceKind(cfg.IdentityToken),
				Err:              err,
			}
			slog.Error("Anthropic federation: failed to obtain identity token",
				"federation_rule_id", cfg.FederationRuleID,
				"source", wrapped.SourceKind,
				"error", err)
			if onRefreshError != nil {
				onRefreshError(wrapped)
			}
			return "", wrapped
		}
		return token, nil
	}

	return []option.RequestOption{
		option.WithFederationTokenProvider(provider, option.FederationOptions{
			FederationRuleID: cfg.FederationRuleID,
			OrganizationID:   cfg.OrganizationID,
			ServiceAccountID: cfg.ServiceAccountID,
		}),
	}, nil
}

// RefreshError wraps any failure to obtain a fresh identity token from the
// configured source. Both the SDK error path and the TUI-facing event hook
// receive this type, so we can render a clear, source-aware message.
type RefreshError struct {
	FederationRuleID string
	OrganizationID   string
	SourceKind       string
	Err              error
}

func (e *RefreshError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("anthropic workload identity federation: failed to refresh identity token from %s source (federation_rule=%s): %v",
		e.SourceKind, e.FederationRuleID, e.Err)
}

func (e *RefreshError) Unwrap() error { return e.Err }

// IsRefreshError reports whether err is, or wraps, a [RefreshError].
func IsRefreshError(err error) bool {
	var re *RefreshError
	return errors.As(err, &re)
}

// sourceKind names the kind of token source for logging and error messages.
func sourceKind(s *latest.IdentityTokenSourceConfig) string {
	switch {
	case s == nil:
		return "unknown"
	case s.File != "":
		return "file"
	case s.Env != "":
		return "env"
	case len(s.Command) > 0:
		return "command"
	case s.URL != "":
		return "url"
	default:
		return "unknown"
	}
}

// tokenSource turns the typed config into an option.IdentityTokenFunc.
// Validation should already have ensured exactly one of the four kinds is
// set, but we re-check defensively.
func tokenSource(s *latest.IdentityTokenSourceConfig, env environment.Provider) (option.IdentityTokenFunc, error) {
	if s == nil {
		return nil, errors.New("federation: identity_token is required")
	}
	switch {
	case s.File != "":
		return fileSource(s.File), nil
	case s.Env != "":
		return envSource(s.Env, env), nil
	case len(s.Command) > 0:
		return commandSource(s.Command, env), nil
	case s.URL != "":
		return urlSource(s.URL, s.Headers, s.ResponseField, env, http.DefaultClient), nil
	}
	return nil, errors.New("federation: identity_token has no source configured")
}

// fileSource reads the token from path on every invocation. We delegate to
// a tiny inline reader rather than option.IdentityTokenFile so that error
// messages mention the path and so that test injection of fs.FS would be
// straightforward later.
func fileSource(path string) option.IdentityTokenFunc {
	return func(_ context.Context) (string, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read token file %q: %w", path, err)
		}
		token := strings.TrimSpace(string(data))
		if token == "" {
			return "", fmt.Errorf("token file %q is empty", path)
		}
		return token, nil
	}
}

// envSource reads the token from the named environment variable through the
// runtime [environment.Provider], so docker-agent's secret-provider chain
// (run secrets, credential helpers, keychain, ...) all work out of the box.
func envSource(name string, env environment.Provider) option.IdentityTokenFunc {
	return func(ctx context.Context) (string, error) {
		v, _ := env.Get(ctx, name)
		v = strings.TrimSpace(v)
		if v == "" {
			return "", fmt.Errorf("environment variable %q is not set or empty", name)
		}
		return v, nil
	}
}

// commandSource executes argv on every invocation and returns trimmed
// stdout. Stderr is logged at warn level. The command is re-resolved by
// [exec.LookPath] each call so that PATH changes inside long-running
// processes are picked up.
func commandSource(argv []string, _ environment.Provider) option.IdentityTokenFunc {
	return func(ctx context.Context) (string, error) {
		if len(argv) == 0 {
			return "", errors.New("identity_token.command is empty")
		}
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // user-provided per config; intentional
		var stdout, stderr strings.Builder
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				return "", fmt.Errorf("command %q failed: %w: %s", argv[0], err, msg)
			}
			return "", fmt.Errorf("command %q failed: %w", argv[0], err)
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			slog.Warn("identity_token.command produced stderr", "command", argv[0], "stderr", msg)
		}
		token := strings.TrimSpace(stdout.String())
		if token == "" {
			return "", fmt.Errorf("command %q produced no token on stdout", argv[0])
		}
		return token, nil
	}
}

// urlSource fetches a JWT from an HTTP(S) endpoint. The URL and header
// values support ${VAR} expansion against env so callers can plug in
// dynamic values like ACTIONS_ID_TOKEN_REQUEST_TOKEN without putting them
// in the YAML.
//
// When responseField is non-empty, the response body is parsed as JSON and
// the named top-level field is used as the token (e.g. GitHub Actions
// returns {"value": "<jwt>"}). Otherwise, the trimmed response body is
// used verbatim (e.g. GCP / Azure metadata servers return the raw JWT).
func urlSource(rawURL string, headers map[string]string, responseField string, env environment.Provider, httpClient *http.Client) option.IdentityTokenFunc {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return func(ctx context.Context) (string, error) {
		expandedURL, err := environment.Expand(ctx, rawURL, env)
		if err != nil {
			return "", fmt.Errorf("expand identity_token.url: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, expandedURL, http.NoBody)
		if err != nil {
			return "", fmt.Errorf("build request for %q: %w", expandedURL, err)
		}
		for k, v := range headers {
			expanded, err := environment.Expand(ctx, v, env)
			if err != nil {
				return "", fmt.Errorf("expand identity_token.headers[%q]: %w", k, err)
			}
			req.Header.Set(k, expanded)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("fetch %q: %w", expandedURL, err)
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", fmt.Errorf("read response from %q: %w", expandedURL, err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			snippet := strings.TrimSpace(string(body))
			if len(snippet) > 256 {
				snippet = snippet[:256] + "…"
			}
			return "", fmt.Errorf("fetch %q: status %d: %s", expandedURL, resp.StatusCode, snippet)
		}

		if responseField == "" {
			token := strings.TrimSpace(string(body))
			if token == "" {
				return "", fmt.Errorf("fetch %q: empty response body", expandedURL)
			}
			return token, nil
		}

		var parsed map[string]any
		if err := json.Unmarshal(body, &parsed); err != nil {
			return "", fmt.Errorf("parse JSON response from %q: %w", expandedURL, err)
		}
		raw, ok := parsed[responseField]
		if !ok {
			return "", fmt.Errorf("fetch %q: response is missing field %q", expandedURL, responseField)
		}
		token, ok := raw.(string)
		if !ok {
			return "", fmt.Errorf("fetch %q: field %q is not a string", expandedURL, responseField)
		}
		token = strings.TrimSpace(token)
		if token == "" {
			return "", fmt.Errorf("fetch %q: field %q is empty", expandedURL, responseField)
		}
		return token, nil
	}
}
