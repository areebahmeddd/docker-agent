package root

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/paths"
	"github.com/docker/docker-agent/pkg/runregistry"
)

func TestResolveTarget_NoLiveRun(t *testing.T) {
	paths.SetDataDir(t.TempDir())
	t.Cleanup(func() { paths.SetDataDir("") })

	_, err := resolveTarget("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no live docker-agent run")
}

func TestResolveTarget_LatestWhenEmpty(t *testing.T) {
	paths.SetDataDir(t.TempDir())
	t.Cleanup(func() { paths.SetDataDir("") })

	cleanup, err := runregistry.Write(runregistry.Record{
		PID: os.Getpid(), Addr: "http://1", SessionID: "s1", StartedAt: time.Now(),
	})
	require.NoError(t, err)
	defer cleanup()

	rec, err := resolveTarget("")
	require.NoError(t, err)
	assert.Equal(t, "s1", rec.SessionID)
}

func TestResolveTarget_ByPID(t *testing.T) {
	paths.SetDataDir(t.TempDir())
	t.Cleanup(func() { paths.SetDataDir("") })

	cleanup, err := runregistry.Write(runregistry.Record{
		PID: os.Getpid(), Addr: "http://x", SessionID: "matched", StartedAt: time.Now(),
	})
	require.NoError(t, err)
	defer cleanup()

	rec, err := resolveTarget(strconv.Itoa(os.Getpid()))
	require.NoError(t, err)
	assert.Equal(t, "matched", rec.SessionID)
}

func TestResolveTarget_NonNumericTo(t *testing.T) {
	_, err := resolveTarget("not-a-pid")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be a pid")
}
