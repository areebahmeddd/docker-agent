package dialog

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/docker/docker-agent/pkg/tools"
	"github.com/docker/docker-agent/pkg/tools/lifecycle"
)

func TestFormatToolsetStatus_Empty(t *testing.T) {
	t.Parallel()
	d := NewToolsetsDialog(nil).(*toolsetsDialog)
	out := strings.Join(d.renderLines(80, 24), "\n")
	assert.Contains(t, out, "Toolsets (0)")
	assert.Contains(t, out, "No toolsets configured")
}

func TestFormatToolsetStatus_RendersFields(t *testing.T) {
	t.Parallel()

	statuses := []tools.ToolsetStatus{
		{
			Name:        "gopls",
			Description: "lsp(gopls)",
			State:       lifecycle.StateReady,
		},
		{
			Name:         "github-mcp",
			Description:  "mcp(remote host=api.github.com)",
			State:        lifecycle.StateRestarting,
			LastError:    errors.New("connection reset"),
			RestartCount: 2,
		},
	}
	d := NewToolsetsDialog(statuses).(*toolsetsDialog)
	out := strings.Join(d.renderLines(80, 24), "\n")
	assert.Contains(t, out, "Toolsets (2)")
	assert.Contains(t, out, "gopls")
	assert.Contains(t, out, "ready")
	assert.Contains(t, out, "github-mcp")
	assert.Contains(t, out, "restarting")
	assert.Contains(t, out, "connection reset")
	assert.Contains(t, out, "restarts: 2")
}

// TestFormatToolsetStatus_TruncatesLongErrors guards against blowing out the
// dialog with multi-kilobyte error messages from upstream stack traces.
func TestFormatToolsetStatus_TruncatesLongErrors(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", 1000)
	statuses := []tools.ToolsetStatus{{
		Name:      "x",
		State:     lifecycle.StateFailed,
		LastError: errors.New(long),
	}}
	d := NewToolsetsDialog(statuses).(*toolsetsDialog)
	out := strings.Join(d.renderLines(80, 24), "\n")
	// The "…" marker must appear because the error is truncated.
	assert.Contains(t, out, "…")
}
