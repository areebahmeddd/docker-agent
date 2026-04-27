package dialog

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/docker/docker-agent/pkg/tui/commands"
	"github.com/docker/docker-agent/pkg/tui/components/toolcommon"
	"github.com/docker/docker-agent/pkg/tui/core/layout"
	"github.com/docker/docker-agent/pkg/tui/styles"
)

// CommandExecuteMsg is sent when a command is selected
type CommandExecuteMsg struct {
	Command commands.Item
}

// commandPaletteDialog implements Dialog for the command palette.
// It uses pickerCore for the shared filter/scroll/select skeleton and only
// adds the bits that are specific to running commands.
type commandPaletteDialog struct {
	pickerCore

	categories []commands.Category
	filtered   []commands.Item
}

// Command palette dialog dimension constants
const (
	paletteWidthPercent  = 80
	paletteMinWidth      = 50
	paletteMaxWidth      = 80
	paletteHeightPercent = 70
	paletteMaxHeight     = 30

	// paletteListOverhead = title(1) + space(1) + input(1) + separator(1) +
	// space(1) + help(1) + borders/padding(2)
	paletteListOverhead = 8

	// paletteListStartY = border(1) + padding(1) + title(1) + space(1) +
	// input(1) + separator(1)
	paletteListStartY = 6
)

// commandPalettePickerLayout is the layout used by the command palette.
var commandPalettePickerLayout = pickerLayout{
	WidthPercent:    paletteWidthPercent,
	MinWidth:        paletteMinWidth,
	MaxWidth:        paletteMaxWidth,
	HeightPercent:   paletteHeightPercent,
	MaxHeight:       paletteMaxHeight,
	ListOverhead:    paletteListOverhead,
	ListStartOffset: paletteListStartY,
}

// NewCommandPaletteDialog creates a new command palette dialog
func NewCommandPaletteDialog(categories []commands.Category) Dialog {
	d := &commandPaletteDialog{
		pickerCore: newPickerCore(commandPalettePickerLayout, "Type to search commands…"),
		categories: categories,
	}
	d.textInput.CharLimit = 100
	d.filterCommands()
	return d
}

// Init initializes the command palette dialog
func (d *commandPaletteDialog) Init() tea.Cmd {
	return textinput.Blink
}

// Update handles messages for the command palette dialog
func (d *commandPaletteDialog) Update(msg tea.Msg) (layout.Model, tea.Cmd) {
	// Scrollview handles mouse scrollbar, wheel, and pgup/pgdn/home/end
	if handled, cmd := d.scrollview.Update(msg); handled {
		return d, cmd
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		cmd := d.SetSize(msg.Width, msg.Height)
		return d, cmd

	case tea.PasteMsg:
		var cmd tea.Cmd
		d.textInput, cmd = d.textInput.Update(msg)
		d.filterCommands()
		return d, cmd

	case tea.MouseClickMsg:
		// Scrollbar clicks already handled above; this handles list item clicks
		if msg.Button == tea.MouseLeft {
			if cmdIdx := d.mouseListIndex(msg.Y, d.lineToCmdIndex); cmdIdx >= 0 {
				if d.recordClick(cmdIdx) {
					d.selected = cmdIdx
					cmd := d.executeSelected()
					return d, cmd
				}
				d.selected = cmdIdx
			}
		}
		return d, nil

	case tea.KeyPressMsg:
		if cmd := HandleQuit(msg); cmd != nil {
			return d, cmd
		}

		switch {
		case key.Matches(msg, d.keyMap.Escape):
			return d, closeDialogCmd()

		case key.Matches(msg, d.keyMap.Up):
			if d.selected > 0 {
				d.selected--
				d.scrollview.EnsureLineVisible(d.findSelectedLine())
			}
			return d, nil

		case key.Matches(msg, d.keyMap.Down):
			if d.selected < len(d.filtered)-1 {
				d.selected++
				d.scrollview.EnsureLineVisible(d.findSelectedLine())
			}
			return d, nil

		case key.Matches(msg, d.keyMap.Enter):
			cmd := d.executeSelected()
			return d, cmd

		default:
			var cmd tea.Cmd
			d.textInput, cmd = d.textInput.Update(msg)
			d.filterCommands()
			return d, cmd
		}
	}

	return d, nil
}

// executeSelected executes the currently selected command and closes the dialog.
func (d *commandPaletteDialog) executeSelected() tea.Cmd {
	if d.selected < 0 || d.selected >= len(d.filtered) {
		return nil
	}
	selectedCmd := d.filtered[d.selected]
	cmds := []tea.Cmd{closeDialogCmd()}
	if selectedCmd.Execute != nil {
		cmds = append(cmds, selectedCmd.Execute(""))
	}
	return tea.Sequence(cmds...)
}

// filterCommands filters the command list based on search input
func (d *commandPaletteDialog) filterCommands() {
	query := strings.ToLower(strings.TrimSpace(d.textInput.Value()))

	d.filtered = d.filtered[:0]
	for _, cat := range d.categories {
		for _, cmd := range cat.Commands {
			if query == "" || matchesCommandQuery(cmd, query) {
				d.filtered = append(d.filtered, cmd)
			}
		}
	}

	if d.selected >= len(d.filtered) {
		d.selected = 0
	}
	d.scrollview.SetScrollOffset(0)
}

// matchesCommandQuery reports whether the given command matches the lowercase
// query string by searching label, description, category, or slash command.
func matchesCommandQuery(cmd commands.Item, query string) bool {
	return strings.Contains(strings.ToLower(cmd.Label), query) ||
		strings.Contains(strings.ToLower(cmd.Description), query) ||
		strings.Contains(strings.ToLower(cmd.Category), query) ||
		strings.Contains(strings.ToLower(cmd.SlashCommand), query)
}

// buildLines builds the visual lines for the command list and returns:
//   - lines: the rendered line strings (with category headers)
//   - lineToCmd: maps each line index to command index (-1 for headers/blanks)
//
// contentWidth controls whether items are rendered with full styling: when 0,
// raw category names and empty strings are emitted (used by tests and by
// findSelectedLine, which only needs the mapping).
func (d *commandPaletteDialog) buildLines(contentWidth int) (lines []string, lineToCmd []int) {
	gl := newGroupedList()
	var lastCategory string

	for i, cmd := range d.filtered {
		if cmd.Category != lastCategory {
			if lastCategory != "" {
				gl.AddNonItem("")
			}
			if contentWidth > 0 {
				gl.AddNonItem(styles.PaletteCategoryStyle.MarginTop(0).Render(cmd.Category))
			} else {
				gl.AddNonItem(cmd.Category)
			}
			lastCategory = cmd.Category
		}

		if contentWidth > 0 {
			gl.AddItem(d.renderCommand(cmd, i == d.selected, contentWidth))
		} else {
			gl.AddItem("")
		}
	}

	return gl.Lines(), gl.LineToItem()
}

// lineToCmdIndex returns the command index for a given visual line, or -1
// when the line is a header or blank. Used for mouse hit-testing.
func (d *commandPaletteDialog) lineToCmdIndex(line int) int {
	_, lineToCmd := d.buildLines(0)
	if line < 0 || line >= len(lineToCmd) {
		return -1
	}
	return lineToCmd[line]
}

// findSelectedLine returns the line index that corresponds to the selected command.
func (d *commandPaletteDialog) findSelectedLine() int {
	_, lineToCmd := d.buildLines(0)
	for i, cmdIdx := range lineToCmd {
		if cmdIdx == d.selected {
			return i
		}
	}
	return 0
}

// View renders the command palette dialog
func (d *commandPaletteDialog) View() string {
	dialogWidth, _, contentWidth := d.dialogSize()
	d.textInput.SetWidth(contentWidth)

	allLines, _ := d.buildLines(contentWidth)
	d.updateScrollviewPosition()
	d.scrollview.SetContent(allLines, len(allLines))

	var scrollableContent string
	if len(d.filtered) == 0 {
		scrollableContent = d.renderEmptyState("No commands found", contentWidth)
	} else {
		scrollableContent = d.scrollview.View()
	}

	content := NewContent(d.regionWidth(contentWidth)).
		AddTitle("Commands").
		AddSpace().
		AddContent(d.textInput.View()).
		AddSeparator().
		AddContent(scrollableContent).
		AddSpace().
		AddHelpKeys("↑/↓", "navigate", "enter", "execute", "esc", "close").
		Build()

	return styles.DialogStyle.Width(dialogWidth).Render(content)
}

// renderCommand renders a single command in the list
func (d *commandPaletteDialog) renderCommand(cmd commands.Item, selected bool, contentWidth int) string {
	actionStyle := styles.PaletteUnselectedActionStyle
	descStyle := styles.PaletteUnselectedDescStyle
	if selected {
		actionStyle = styles.PaletteSelectedActionStyle
		descStyle = styles.PaletteSelectedDescStyle
	}

	label := " " + cmd.Label
	labelWidth := lipgloss.Width(actionStyle.Render(label))

	content := actionStyle.Render(label)
	if cmd.Description != "" {
		separator := " • "
		separatorWidth := lipgloss.Width(separator)
		availableWidth := contentWidth - labelWidth - separatorWidth
		if availableWidth > 0 {
			truncatedDesc := toolcommon.TruncateText(cmd.Description, availableWidth)
			content += descStyle.Render(separator + truncatedDesc)
		}
	}
	return content
}
