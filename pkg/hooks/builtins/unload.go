package builtins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/docker/docker-agent/pkg/hooks"
)

// Unload is the registered name of the on_agent_switch builtin that
// asks the previous agent's local inference engines (today: Docker
// Model Runner) to release the resources they hold.
//
// Wire it into a config with:
//
//	hooks:
//	  on_agent_switch:
//	    - type: builtin
//	      command: unload
//
// The hook is pure: it depends only on the [hooks.Input.FromAgentModels]
// snapshot the runtime ships on every on_agent_switch dispatch, plus
// net/http. It carries no runtime-side coupling and silently skips any
// model whose endpoint isn't reachable as plain HTTP (e.g. cloud
// providers that don't expose [hooks.ModelEndpoint.BaseURL]).
const Unload = "unload"

// unloadProviderDMR is the only provider type the builtin currently
// knows how to unload against. Other providers ship through a no-op so
// wiring this hook on a heterogeneous chain is harmless.
const unloadProviderDMR = "dmr"

// unloadTimeout caps each Unload call so a stalled engine cannot stall
// agent switching. Each model gets its own deadline so a slow first
// model can't starve the rest.
const unloadTimeout = 10 * time.Second

// unload iterates the [hooks.Input.FromAgentModels] snapshot the
// runtime captured at dispatch time and POSTs `{"model": "<id>"}` to
// the resolved unload endpoint of each one we know how to unload.
// Errors are logged but never propagated — agent switching must never
// block on a slow or unreachable engine.
func unload(ctx context.Context, in *hooks.Input, _ []string) (*hooks.Output, error) {
	if in == nil || in.FromAgent == "" || in.FromAgent == in.ToAgent {
		return nil, nil
	}
	for _, m := range in.FromAgentModels {
		if m.Provider != unloadProviderDMR {
			continue
		}
		endpoint, err := resolveUnloadURL(m.BaseURL, m.UnloadAPI)
		if err != nil {
			slog.WarnContext(ctx, "unload: resolving endpoint failed",
				"agent", in.FromAgent, "model", m.Model, "error", err)
			continue
		}
		if endpoint == "" {
			continue
		}
		callCtx, cancel := context.WithTimeout(ctx, unloadTimeout)
		if err := postUnloadModel(callCtx, http.DefaultClient, endpoint, m.Model); err != nil {
			slog.WarnContext(ctx, "unload: provider unload failed",
				"agent", in.FromAgent, "model", m.Model, "error", err)
		}
		cancel()
	}
	return nil, nil
}

// resolveUnloadURL picks the unload endpoint for one model:
// the configured `unload_api` override (rebased against baseURL when
// it isn't already absolute) wins, otherwise [defaultUnloadURL]
// derives one from baseURL by replacing the trailing /v1 segment.
// Returns ("", nil) when no endpoint can be determined so the caller
// can skip without erroring.
func resolveUnloadURL(baseURL, override string) (string, error) {
	if override != "" {
		return rebaseURL(baseURL, override)
	}
	if baseURL == "" {
		return "", nil
	}
	return defaultUnloadURL(baseURL), nil
}

// defaultUnloadURL derives the `_unload` endpoint URL from the OpenAI
// base URL by replacing the trailing `/v1` segment, mirroring how the
// DMR client derives `_configure`:
//
//	http://host:port/engines/v1/             → http://host:port/engines/_unload
//	http://host:port/engines/llama.cpp/v1/   → http://host:port/engines/llama.cpp/_unload
//	http://_/exp/vDD4.40/engines/v1          → http://_/exp/vDD4.40/engines/_unload
func defaultUnloadURL(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return strings.TrimSuffix(strings.TrimSuffix(baseURL, "/"), "/v1") + "/_unload"
	}
	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), "/v1") + "/_unload"
	return u.String()
}

// rebaseURL returns path verbatim if it is already an absolute URL,
// otherwise attaches it to baseURL's scheme + host (dropping any path
// baseURL may carry). This lets users point base_url at e.g.
// http://localhost:12434/engines/v1 and override unload_api with
// /engines/_unload without the version prefix bleeding through.
func rebaseURL(baseURL, path string) (string, error) {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path, nil
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("base_url %q is not absolute; cannot resolve %q", baseURL, path)
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return u.Scheme + "://" + u.Host + path, nil
}

// postUnloadModel issues `POST <endpoint>` with body `{"model": "<modelID>"}`.
func postUnloadModel(ctx context.Context, client *http.Client, endpoint, modelID string) error {
	body, _ := json.Marshal(map[string]string{"model": modelID})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building unload request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	slog.DebugContext(ctx, "Unloading model", "url", endpoint, "model", modelID)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("calling unload endpoint %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		return fmt.Errorf("unload endpoint returned %d: %s",
			resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	// Drain the success-path body so the underlying transport can reuse
	// the connection (Go's http.Client only re-pools a connection whose
	// body has been read to EOF and closed).
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}
