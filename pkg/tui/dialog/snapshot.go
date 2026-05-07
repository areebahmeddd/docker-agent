package dialog

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/docker/docker-agent/pkg/app"
	"github.com/docker/docker-agent/pkg/tui/core"
	"github.com/docker/docker-agent/pkg/tui/core/layout"
	"github.com/docker/docker-agent/pkg/tui/messages"
	"github.com/docker/docker-agent/pkg/tui/styles"
)

// Layout constants for the snapshots dialog.
const (
	snapshotsDialogWidthPercent  = 60
	snapshotsDialogMinWidth      = 40
	snapshotsDialogMaxWidth      = 70
	snapshotsDialogHeightPercent = 70
	snapshotsDialogMaxHeight     = 30
)

// snapshotsKeyMap defines the navigation keys for the snapshots dialog.
type snapshotsKeyMap struct {
	Up      key.Binding
	Down    key.Binding
	Top     key.Binding
	Bottom  key.Binding
	Restore key.Binding
	Escape  key.Binding
}

func defaultSnapshotsKeyMap() snapshotsKeyMap {
	return snapshotsKeyMap{
		Up:      key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:    key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Top:     key.NewBinding(key.WithKeys("home", "g"), key.WithHelp("g", "first")),
		Bottom:  key.NewBinding(key.WithKeys("end", "G"), key.WithHelp("G", "last")),
		Restore: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "restore")),
		Escape:  key.NewBinding(key.WithKeys("esc", "q"), key.WithHelp("esc", "close")),
	}
}

// snapshotsDialog lists every captured snapshot and lets the user reset the
// workspace to the state of any of them (or to the original pre-agent state).
type snapshotsDialog struct {
	BaseDialog

	snapshots []app.SnapshotInfo
	keyMap    snapshotsKeyMap
	selected  int // 0 = <original>; 1..len(snapshots) = snapshot N
}

// NewSnapshotsDialog creates a snapshots dialog showing every captured
// checkpoint. Pass the snapshots in chronological order (oldest first).
func NewSnapshotsDialog(snapshots []app.SnapshotInfo) Dialog {
	return &snapshotsDialog{
		snapshots: snapshots,
		keyMap:    defaultSnapshotsKeyMap(),
	}
}

func (d *snapshotsDialog) Init() tea.Cmd { return nil }

func (d *snapshotsDialog) Update(msg tea.Msg) (layout.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		cmd := d.SetSize(msg.Width, msg.Height)
		return d, cmd

	case tea.KeyPressMsg:
		if cmd := HandleQuit(msg); cmd != nil {
			return d, cmd
		}
		switch {
		case key.Matches(msg, d.keyMap.Escape):
			return d, core.CmdHandler(CloseDialogMsg{})
		case key.Matches(msg, d.keyMap.Up):
			if d.selected > 0 {
				d.selected--
			}
			return d, nil
		case key.Matches(msg, d.keyMap.Down):
			if d.selected < d.maxIndex() {
				d.selected++
			}
			return d, nil
		case key.Matches(msg, d.keyMap.Top):
			d.selected = 0
			return d, nil
		case key.Matches(msg, d.keyMap.Bottom):
			d.selected = d.maxIndex()
			return d, nil
		case key.Matches(msg, d.keyMap.Restore):
			if len(d.snapshots) == 0 {
				return d, nil
			}
			return d, tea.Sequence(
				core.CmdHandler(CloseDialogMsg{}),
				core.CmdHandler(messages.ResetSnapshotMsg{Keep: d.selected}),
			)
		}
	}
	return d, nil
}

// maxIndex is the index of the last selectable item. With N snapshots there
// are N+1 selectable items (<original> + each snapshot).
func (d *snapshotsDialog) maxIndex() int {
	return len(d.snapshots)
}

func (d *snapshotsDialog) Position() (row, col int) {
	dialogWidth, maxHeight := d.dialogSize()
	return CenterPosition(d.Width(), d.Height(), dialogWidth, maxHeight)
}

func (d *snapshotsDialog) dialogSize() (dialogWidth, dialogHeight int) {
	dialogWidth = d.ComputeDialogWidth(snapshotsDialogWidthPercent, snapshotsDialogMinWidth, snapshotsDialogMaxWidth)
	dialogHeight = min(d.Height()*snapshotsDialogHeightPercent/100, snapshotsDialogMaxHeight)
	return dialogWidth, dialogHeight
}

func (d *snapshotsDialog) View() string {
	dialogWidth, _ := d.dialogSize()
	contentWidth := d.ContentWidth(dialogWidth, 2)

	builder := NewContent(contentWidth).
		AddTitle("Snapshots").
		AddSeparator().
		AddSpace()

	if len(d.snapshots) == 0 {
		empty := styles.DialogContentStyle.Italic(true).
			Foreground(styles.TextMuted).
			Width(contentWidth).
			Align(lipgloss.Center).
			Render("No snapshots taken yet.")
		content := builder.
			AddContent(empty).
			AddSpace().
			AddHelpKeys("esc", "close").
			Build()
		return styles.DialogStyle.Width(dialogWidth).Render(content)
	}

	count := fmt.Sprintf("%d snapshot", len(d.snapshots))
	if len(d.snapshots) != 1 {
		count += "s"
	}
	count += " captured"

	content := builder.
		AddContent(styles.DialogOptionsStyle.Width(contentWidth).Render(count)).
		AddSpace().
		AddContent(d.renderList(contentWidth)).
		AddSpace().
		AddHelpKeys("↑/↓", "navigate", "r", "restore", "esc", "close").
		Build()

	return styles.DialogStyle.Width(dialogWidth).Render(content)
}

// renderList renders every selectable item (<original> + each snapshot).
func (d *snapshotsDialog) renderList(contentWidth int) string {
	rows := make([]string, 0, len(d.snapshots)+1)
	rows = append(rows, d.renderRow("<original>", "restore the initial state", d.selected == 0, contentWidth))
	for i, snap := range d.snapshots {
		filesLabel := fmt.Sprintf("%d file", snap.Files)
		if snap.Files != 1 {
			filesLabel += "s"
		}
		label := fmt.Sprintf("Snapshot %d", i+1)
		rows = append(rows, d.renderRow(label, filesLabel, i+1 == d.selected, contentWidth))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// renderRow renders a single item line with name on the left and description
// on the right, highlighting the row when selected.
func (d *snapshotsDialog) renderRow(name, desc string, selected bool, width int) string {
	nameStyle, descStyle := styles.PaletteUnselectedActionStyle, styles.PaletteUnselectedDescStyle
	if selected {
		nameStyle, descStyle = styles.PaletteSelectedActionStyle, styles.PaletteSelectedDescStyle
	}

	left := nameStyle.Render(" " + name + " ")
	right := descStyle.Render(" " + desc + " ")
	gap := max(1, width-lipgloss.Width(left)-lipgloss.Width(right))
	return left + descStyle.Render(strings.Repeat(" ", gap)) + right
}
