package runregistry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/paths"
)

func TestWriteAndList_RoundTrip(t *testing.T) {
	withTempDataDir(t)

	rec := Record{
		PID:       os.Getpid(),
		Addr:      "http://127.0.0.1:1234",
		SessionID: "sess-1",
		Agent:     "root",
		StartedAt: time.Now(),
	}
	cleanup, err := Write(rec)
	require.NoError(t, err)

	records, err := List()
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, rec.SessionID, records[0].SessionID)
	assert.Equal(t, rec.Addr, records[0].Addr)

	cleanup()
	cleanup() // safe to call twice

	records, err = List()
	require.NoError(t, err)
	assert.Empty(t, records)
}

func TestList_DropsStaleRecords(t *testing.T) {
	withTempDataDir(t)

	writeRecord(t, "999999.json", Record{
		PID: 999999, Addr: "x", SessionID: "y",
		StartedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	})

	records, err := List()
	require.NoError(t, err)
	assert.Empty(t, records)

	_, err = os.Stat(filepath.Join(Dir(), "999999.json"))
	assert.True(t, os.IsNotExist(err))
}

func TestLatest_PicksMostRecent(t *testing.T) {
	withTempDataDir(t)

	pid := os.Getpid()
	writeRecord(t, "1.json", Record{PID: pid, Addr: "http://a", SessionID: "old", StartedAt: time.Now().Add(-time.Hour)})
	writeRecord(t, "2.json", Record{PID: pid, Addr: "http://b", SessionID: "new", StartedAt: time.Now()})

	rec, ok, err := Latest()
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "new", rec.SessionID)
}

func withTempDataDir(t *testing.T) {
	t.Helper()
	paths.SetDataDir(t.TempDir())
	t.Cleanup(func() { paths.SetDataDir("") })
}

func writeRecord(t *testing.T, name string, rec Record) {
	t.Helper()
	require.NoError(t, os.MkdirAll(Dir(), 0o755))
	buf, err := json.Marshal(rec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(Dir(), name), buf, 0o600))
}
