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

// unloadTimeout caps each per-model Unload call so a stalled engine
// cannot stall agent switching.
const unloadTimeout = 10 * time.Second

// unload iterates the [hooks.Input.FromAgentModels] snapshot the
// runtime captured at dispatch time and POSTs `{"model": "<id>"}` to
// the resolved unload endpoint of each DMR model. Errors are logged
// but never propagated — agent switching must never block on a slow
// or unreachable engine.
func unload(ctx context.Context, in *hooks.Input, _ []string) (*hooks.Output, error) {
	if in == nil || in.FromAgent == "" || in.FromAgent == in.ToAgent {
		return nil, nil
	}
	for _, m := range in.FromAgentModels {
		if m.Provider != "dmr" {
			continue
		}
		if err := unloadOne(ctx, m); err != nil {
			slog.WarnContext(ctx, "unload: failed",
				"agent", in.FromAgent, "model", m.Model, "error", err)
		}
	}
	return nil, nil
}

// unloadOne resolves the unload URL for m and POSTs the model id to
// it, bounded by [unloadTimeout]. A model with no resolvable endpoint
// (no base_url and no unload_api) is a silent no-op so the hook stays
// harmless on test / in-process providers.
func unloadOne(parent context.Context, m hooks.ModelEndpoint) error {
	endpoint, err := unloadURL(m)
	if err != nil || endpoint == "" {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, unloadTimeout)
	defer cancel()

	body, _ := json.Marshal(map[string]string{"model": m.Model})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building unload request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	slog.DebugContext(ctx, "Unloading model", "url", endpoint, "model", m.Model)

	resp, err := http.DefaultClient.Do(req)
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

// unloadURL picks the unload endpoint for one model, in this order:
//
//  1. unload_api is an absolute URL — used verbatim (lets users point
//     at a different host than the model's base_url);
//  2. unload_api is set but relative — rebased onto base_url's
//     scheme + host (the model's path is dropped);
//  3. unload_api is unset — the default `_unload` URL is derived from
//     base_url by replacing its trailing `/v1` segment.
//
// Returns ("", nil) when neither base_url nor unload_api is set, so
// the caller can skip without erroring.
func unloadURL(m hooks.ModelEndpoint) (string, error) {
	if strings.HasPrefix(m.UnloadAPI, "http://") || strings.HasPrefix(m.UnloadAPI, "https://") {
		return m.UnloadAPI, nil
	}
	if m.BaseURL == "" && m.UnloadAPI == "" {
		return "", nil
	}
	u, err := url.Parse(m.BaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("base_url %q is not absolute; cannot resolve unload endpoint", m.BaseURL)
	}
	switch {
	case m.UnloadAPI == "":
		u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), "/v1") + "/_unload"
	case strings.HasPrefix(m.UnloadAPI, "/"):
		u.Path = m.UnloadAPI
	default:
		u.Path = "/" + m.UnloadAPI
	}
	return u.String(), nil
}
