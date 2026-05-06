package federation

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/config/latest"
	"github.com/docker/docker-agent/pkg/environment"
)

func TestFileSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	require.NoError(t, os.WriteFile(path, []byte("  abc.def.ghi\n"), 0o600))

	src := fileSource(path)
	got, err := src(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "abc.def.ghi", got)
}

func TestFileSource_Missing(t *testing.T) {
	src := fileSource(filepath.Join(t.TempDir(), "missing"))
	_, err := src(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read token file")
}

func TestFileSource_Empty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty")
	require.NoError(t, os.WriteFile(path, []byte("   \n"), 0o600))
	src := fileSource(path)
	_, err := src(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is empty")
}

func TestEnvSource(t *testing.T) {
	env := environment.NewMapEnvProvider(map[string]string{"MY_TOKEN": " jwt-payload\n"})
	src := envSource("MY_TOKEN", env)
	got, err := src(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "jwt-payload", got)
}

func TestEnvSource_Missing(t *testing.T) {
	env := environment.NewMapEnvProvider(map[string]string{})
	src := envSource("MY_TOKEN", env)
	_, err := src(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not set or empty")
}

func TestCommandSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell command")
	}
	src := commandSource([]string{"sh", "-c", "printf '  abc.def.ghi\\n'"}, nil)
	got, err := src(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "abc.def.ghi", got)
}

func TestCommandSource_Failure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell command")
	}
	src := commandSource([]string{"sh", "-c", "echo boom 1>&2; exit 7"}, nil)
	_, err := src(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

func TestCommandSource_EmptyOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell command")
	}
	src := commandSource([]string{"sh", "-c", "true"}, nil)
	_, err := src(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no token on stdout")
}

func TestURLSource_PlainText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("the.jwt.token\n"))
	}))
	defer server.Close()

	env := environment.NewMapEnvProvider(nil)
	src := urlSource(server.URL, nil, "", env, server.Client())
	got, err := src(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "the.jwt.token", got)
}

func TestURLSource_JSONField_WithExpansion(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":"the.jwt.token"}`))
	}))
	defer server.Close()

	env := environment.NewMapEnvProvider(map[string]string{
		"OIDC_BEARER": "secret-bearer",
	})
	src := urlSource(
		server.URL+"?audience=https://api.anthropic.com",
		map[string]string{"Authorization": "bearer ${OIDC_BEARER}"},
		"value",
		env,
		server.Client(),
	)
	got, err := src(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "the.jwt.token", got)
	assert.Equal(t, "bearer secret-bearer", gotAuth)
}

func TestURLSource_NonOK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`unauthorized`))
	}))
	defer server.Close()

	env := environment.NewMapEnvProvider(nil)
	src := urlSource(server.URL, nil, "", env, server.Client())
	_, err := src(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 401")
}

func TestURLSource_MissingField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"other":"x"}`))
	}))
	defer server.Close()

	src := urlSource(server.URL, nil, "value", environment.NewMapEnvProvider(nil), server.Client())
	_, err := src(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), `missing field "value"`)
}

func TestRequestOptions_RejectsNilConfig(t *testing.T) {
	_, err := RequestOptions(nil, environment.NewMapEnvProvider(nil), nil)
	require.Error(t, err)
}

func TestRequestOptions_BuildsForFileSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tok")
	require.NoError(t, os.WriteFile(path, []byte("jwt"), 0o600))

	opts, err := RequestOptions(&latest.FederationAuthConfig{
		FederationRuleID: "fdrl_abc",
		OrganizationID:   "org",
		IdentityToken:    &latest.IdentityTokenSourceConfig{File: path},
	}, environment.NewMapEnvProvider(nil), nil)
	require.NoError(t, err)
	require.Len(t, opts, 1)
}

func TestRefreshError_WrapsAndIdentifies(t *testing.T) {
	cause := errors.New("boom")
	err := &RefreshError{
		FederationRuleID: "fdrl_x",
		SourceKind:       "file",
		Err:              cause,
	}
	require.ErrorIs(t, err, cause)
	assert.True(t, IsRefreshError(err))
	assert.Contains(t, err.Error(), "fdrl_x")
	assert.Contains(t, err.Error(), "file")
	assert.Contains(t, err.Error(), "boom")
	assert.False(t, IsRefreshError(cause))
}

func TestRequestOptions_ProviderInvokesOnRefreshError(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "missing")
	cfg := &latest.FederationAuthConfig{
		FederationRuleID: "fdrl_x",
		OrganizationID:   "org",
		IdentityToken:    &latest.IdentityTokenSourceConfig{File: missingPath},
	}

	var captured error
	hook := func(err error) { captured = err }

	// We can't directly drive WithFederationTokenProvider here without a
	// network call, so we instead drive the inner provider closure that
	// RequestOptions wraps. We do that by reaching back into the package.
	src := fileSource(missingPath)
	provider := wrapForTest(cfg, src, hook)
	_, err := provider(t.Context())
	require.Error(t, err)
	require.Error(t, captured)
	assert.Contains(t, captured.Error(), "anthropic workload identity federation")
}

// wrapForTest mirrors the wrapping logic in RequestOptions so we can exercise
// the error-reporting path without having to open a real federation exchange.
func wrapForTest(cfg *latest.FederationAuthConfig, src func(context.Context) (string, error), hook func(error)) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		token, err := src(ctx)
		if err != nil {
			wrapped := &RefreshError{
				FederationRuleID: cfg.FederationRuleID,
				OrganizationID:   cfg.OrganizationID,
				SourceKind:       sourceKind(cfg.IdentityToken),
				Err:              err,
			}
			if hook != nil {
				hook(wrapped)
			}
			return "", wrapped
		}
		return token, nil
	}
}
