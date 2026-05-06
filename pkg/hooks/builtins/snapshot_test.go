package builtins_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/hooks"
	"github.com/docker/docker-agent/pkg/hooks/builtins"
	"github.com/docker/docker-agent/pkg/paths"
)

func TestSnapshotBuiltinUndoSurvivesStreamEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	paths.SetDataDir(t.TempDir())
	t.Cleanup(func() { paths.SetDataDir("") })

	r := hooks.NewRegistry()
	state, err := builtins.Register(r)
	require.NoError(t, err)
	fn, ok := r.LookupBuiltin(builtins.Snapshot)
	require.True(t, ok)

	dir := snapshotBuiltinRepo(t)
	_, err = fn(t.Context(), &hooks.Input{
		SessionID:     "s",
		Cwd:           dir,
		HookEventName: hooks.EventTurnStart,
	}, nil)
	require.NoError(t, err)

	changedPath := filepath.Join(dir, "changed.txt")
	require.NoError(t, os.WriteFile(changedPath, []byte("changed"), 0o644))

	_, err = fn(t.Context(), &hooks.Input{
		SessionID:     "s",
		Cwd:           dir,
		HookEventName: hooks.EventTurnEnd,
		Reason:        "continue",
	}, nil)
	require.NoError(t, err)

	_, err = fn(t.Context(), &hooks.Input{
		SessionID:     "s",
		Cwd:           dir,
		HookEventName: hooks.EventTurnStart,
	}, nil)
	require.NoError(t, err)

	_, err = fn(t.Context(), &hooks.Input{
		SessionID:     "s",
		Cwd:           dir,
		HookEventName: hooks.EventTurnEnd,
		Reason:        "normal",
	}, nil)
	require.NoError(t, err)

	_, err = fn(t.Context(), &hooks.Input{
		SessionID:     "s",
		Cwd:           dir,
		HookEventName: hooks.EventSessionEnd,
		Reason:        "stream_ended",
	}, nil)
	require.NoError(t, err)
	state.ClearSession("s")

	entries, err := os.ReadDir(filepath.Join(paths.GetDataDir(), "snapshot"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.DirExists(t, filepath.Join(paths.GetDataDir(), "snapshot", entries[0].Name()))

	files, restored, err := state.UndoLastSnapshot(t.Context(), "s", dir)
	require.NoError(t, err)
	assert.True(t, restored)
	assert.Equal(t, 1, files)
	assert.NoFileExists(t, changedPath)
}

func snapshotBuiltinRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGitForSnapshotBuiltin(t, dir, "init")
	runGitForSnapshotBuiltin(t, dir, "config", "user.email", "test@example.com")
	runGitForSnapshotBuiltin(t, dir, "config", "user.name", "Test User")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base"), 0o644))
	runGitForSnapshotBuiltin(t, dir, "add", ".")
	runGitForSnapshotBuiltin(t, dir, "commit", "-m", "init")
	return dir
}

func runGitForSnapshotBuiltin(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}
