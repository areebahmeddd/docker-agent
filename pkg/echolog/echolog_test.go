package echolog

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRedactedRequestLogger_DropsQueryString verifies that secrets passed
// through query parameters never reach slog. Echo's default RequestLogger
// emits v.URI (which includes "?token=..."); RedactedRequestLogger must
// log v.URIPath instead.
func TestRedactedRequestLogger_DropsQueryString(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	e := echo.New()
	e.Use(RedactedRequestLogger())
	e.GET("/api/things", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/api/things?token=swordfish&user=alice",
		http.NoBody,
	)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	logged := buf.String()
	assert.Contains(t, logged, "/api/things", "the path itself should be logged")
	assert.NotContains(t, logged, "swordfish", "query parameter values must not leak into logs")
	assert.NotContains(t, logged, "token=", "query parameter names+values must not leak")
	// Sanity: the path key should be present, not "uri".
	assert.Contains(t, logged, "path=", "expected path= attribute, got: %s", logged)
}
