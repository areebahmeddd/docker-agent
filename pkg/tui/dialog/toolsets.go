package dialog

import (
	"fmt"
	"strings"

	"github.com/docker/docker-agent/pkg/tools"
	"github.com/docker/docker-agent/pkg/tools/lifecycle"
	"github.com/docker/docker-agent/pkg/tui/styles"
)

// toolsetsDialog displays the lifecycle state of every toolset attached
// to the current agent. It is a read-only snapshot at the time the
// dialog was opened — the data is not subscribed.
type toolsetsDialog struct {
	readOnlyScrollDialog

	statuses []tools.ToolsetStatus
}

// NewToolsetsDialog opens a dialog showing each toolset's name, state,
// description, restart count and most recent error.
func NewToolsetsDialog(statuses []tools.ToolsetStatus) Dialog {
	d := &toolsetsDialog{statuses: statuses}
	d.readOnlyScrollDialog = newReadOnlyScrollDialog(
		readOnlyScrollDialogSize{widthPercent: 70, minWidth: 60, maxWidth: 100, heightPercent: 80, heightMax: 40},
		d.renderLines,
	)
	return d
}

func (d *toolsetsDialog) renderLines(contentWidth, _ int) []string {
	title := fmt.Sprintf("Toolsets (%d)", len(d.statuses))
	lines := []string{
		RenderTitle(title, contentWidth, styles.DialogTitleStyle),
		RenderSeparator(contentWidth),
		"",
	}

	if len(d.statuses) == 0 {
		lines = append(lines, styles.MutedStyle.Render("No toolsets configured."), "")
		return lines
	}

	for i := range d.statuses {
		s := &d.statuses[i]
		lines = append(lines, formatToolsetStatus(s, contentWidth)...)
		// Blank line between entries.
		lines = append(lines, "")
	}
	return lines
}

// formatToolsetStatus renders one toolset entry as a small block:
//
//	NAME [state]
//	  description
//	  last_error: ...   (only when set)
//	  restarts: N       (only when > 0)
func formatToolsetStatus(s *tools.ToolsetStatus, _ int) []string {
	headline := fmt.Sprintf("%s %s", s.Name, formatStateBadge(s.State))
	out := []string{styles.BoldStyle.Render(headline)}

	if s.Description != "" && s.Description != s.Name {
		out = append(out, "  "+styles.MutedStyle.Render(s.Description))
	}

	if s.RestartCount > 0 {
		out = append(out, "  "+styles.MutedStyle.Render(fmt.Sprintf("restarts: %d", s.RestartCount)))
	}

	if s.LastError != nil {
		// Truncate very long error messages so they don't blow out the
		// dialog width. The dialog is scroll-capable, so a one-line
		// summary is enough.
		msg := s.LastError.Error()
		const maxLen = 240
		if len(msg) > maxLen {
			msg = msg[:maxLen] + "…"
		}
		out = append(out, "  "+styles.ErrorStyle.Render("last_error: "+strings.ReplaceAll(msg, "\n", " ")))
	}

	return out
}

// formatStateBadge returns a short bracketed label for the lifecycle state,
// styled to draw the eye to non-Ready states.
func formatStateBadge(s lifecycle.State) string {
	label := "[" + s.String() + "]"
	switch s {
	case lifecycle.StateReady:
		return styles.SuccessStyle.Render(label)
	case lifecycle.StateDegraded, lifecycle.StateRestarting, lifecycle.StateStarting:
		return styles.WarningStyle.Render(label)
	case lifecycle.StateFailed:
		return styles.ErrorStyle.Render(label)
	case lifecycle.StateStopped:
		return styles.MutedStyle.Render(label)
	default:
		return label
	}
}
