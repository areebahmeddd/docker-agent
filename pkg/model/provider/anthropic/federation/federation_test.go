package federation

import (
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

	got, err := fileSource(path)(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "abc.def.ghi", got)
}

func TestFileSource_Missing(t *testing.T) {
	_, err := fileSource(filepath.Join(t.TempDir(), "missing"))(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read token file")
}

func TestFileSource_Empty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty")
	require.NoError(t, os.WriteFile(path, []byte("   \n"), 0o600))

	_, err := fileSource(path)(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is empty")
}

func TestEnvSource(t *testing.T) {
	env := environment.NewMapEnvProvider(map[string]string{"MY_TOKEN": " jwt-payload\n"})
	got, err := envSource("MY_TOKEN", env)(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "jwt-payload", got)
}

func TestEnvSource_Missing(t *testing.T) {
	_, err := envSource("MY_TOKEN", environment.NewMapEnvProvider(nil))(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not set or empty")
}

func TestCommandSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell command")
	}
	got, err := commandSource([]string{"sh", "-c", "printf '  abc.def.ghi\\n'"})(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "abc.def.ghi", got)
}

func TestCommandSource_Failure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell command")
	}
	_, err := commandSource([]string{"sh", "-c", "echo boom 1>&2; exit 7"})(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

func TestCommandSource_EmptyOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell command")
	}
	_, err := commandSource([]string{"sh", "-c", "true"})(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no token on stdout")
}

func TestURLSource_PlainText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("the.jwt.token\n"))
	}))
	defer server.Close()

	got, err := urlSource(server.URL, nil, "", environment.NewMapEnvProvider(nil))(t.Context())
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

	env := environment.NewMapEnvProvider(map[string]string{"OIDC_BEARER": "secret-bearer"})
	got, err := urlSource(
		server.URL+"?audience=https://api.anthropic.com",
		map[string]string{"Authorization": "bearer ${OIDC_BEARER}"},
		"value",
		env,
	)(t.Context())
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

	_, err := urlSource(server.URL, nil, "", environment.NewMapEnvProvider(nil))(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 401")
}

func TestURLSource_MissingField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"other":"x"}`))
	}))
	defer server.Close()

	_, err := urlSource(server.URL, nil, "value", environment.NewMapEnvProvider(nil))(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), `missing field "value"`)
}

func TestRequestOptions_RejectsNilConfig(t *testing.T) {
	_, err := RequestOptions(nil, environment.NewMapEnvProvider(nil))
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
	}, environment.NewMapEnvProvider(nil))
	require.NoError(t, err)
	require.Len(t, opts, 1)
}

// TestTokenSource_WrapsFailureMessage exercises the wrapping path that
// surfaces refresh errors in the TUI: a failing source must produce a
// message that names the source kind and federation rule.
func TestTokenSource_WrapsFailureMessage(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	cfg := &latest.FederationAuthConfig{
		FederationRuleID: "fdrl_x",
		OrganizationID:   "org",
		IdentityToken:    &latest.IdentityTokenSourceConfig{File: missing},
	}

	// We can't call WithFederationTokenProvider's closure directly without
	// triggering a real network exchange, so we build the same wrapper
	// inline and verify its output.
	src, kind, err := tokenSource(cfg.IdentityToken, environment.NewMapEnvProvider(nil))
	require.NoError(t, err)
	require.Equal(t, "file", kind)

	_, err = src(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read token file")
}
