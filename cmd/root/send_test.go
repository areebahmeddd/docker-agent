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

// These smoke tests exercise the send command's reliance on
// runregistry.Find. The richer behaviour of Find itself (pid, addr, session
// id, ambiguity) lives in the runregistry package tests.

func TestSendUsesRunregistryFind_NoLiveRun(t *testing.T) {
	paths.SetDataDir(t.TempDir())
	t.Cleanup(func() { paths.SetDataDir("") })

	_, err := runregistry.Find("")
	require.ErrorIs(t, err, runregistry.ErrNoRun)
}

func TestSendUsesRunregistryFind_LatestWhenEmpty(t *testing.T) {
	paths.SetDataDir(t.TempDir())
	t.Cleanup(func() { paths.SetDataDir("") })

	cleanup, err := runregistry.Write(runregistry.Record{
		PID: os.Getpid(), Addr: "http://1", SessionID: "s1", StartedAt: time.Now(),
	})
	require.NoError(t, err)
	defer cleanup()

	rec, err := runregistry.Find("")
	require.NoError(t, err)
	assert.Equal(t, "s1", rec.SessionID)
}

func TestSendUsesRunregistryFind_ByPID(t *testing.T) {
	paths.SetDataDir(t.TempDir())
	t.Cleanup(func() { paths.SetDataDir("") })

	cleanup, err := runregistry.Write(runregistry.Record{
		PID: os.Getpid(), Addr: "http://x", SessionID: "matched", StartedAt: time.Now(),
	})
	require.NoError(t, err)
	defer cleanup()

	rec, err := runregistry.Find(strconv.Itoa(os.Getpid()))
	require.NoError(t, err)
	assert.Equal(t, "matched", rec.SessionID)
}
