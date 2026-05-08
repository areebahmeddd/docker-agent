package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/api"
	"github.com/docker/docker-agent/pkg/config"
	"github.com/docker/docker-agent/pkg/session"
)

func TestAttachedServer_SteerReachesAttachedRuntime(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	store := session.NewInMemorySessionStore()
	sess := session.New()
	require.NoError(t, store.AddSession(ctx, sess))

	sm := NewSessionManager(ctx, config.Sources{}, store, 0, &config.RuntimeConfig{})
	fake := &fakeRuntime{}
	sm.AttachRuntime(sess.ID, fake, sess)

	srv := NewWithManager(sm)

	ln, err := Listen(ctx, "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = srv.Serve(ctx, ln) }()

	addr := "http://" + ln.Addr().String()
	resp := httpDoTCP(t, ctx, http.MethodPost, addr+"/api/sessions/"+sess.ID+"/steer",
		api.SteerSessionRequest{Messages: []api.Message{{Content: "hello"}}})
	assert.Contains(t, string(resp), "queued")
}

func httpDoTCP(t *testing.T, ctx context.Context, method, url string, payload any) []byte {
	t.Helper()

	buf, err := json.Marshal(payload)
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(buf))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	out, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Less(t, resp.StatusCode, 400, string(out))
	return out
}
