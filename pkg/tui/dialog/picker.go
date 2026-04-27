package dialog

import (
	"cmp"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/docker/docker-agent/pkg/tui/components/scrollview"
	"github.com/docker/docker-agent/pkg/tui/core"
	"github.com/docker/docker-agent/pkg/tui/styles"
)

// pickerKeyMap defines the standard navigation key bindings used by every
// list-with-filter picker dialog (command palette, theme picker, model
// picker, file picker, working-directory picker, …).
type pickerKeyMap struct {
	Up     key.Binding
	Down   key.Binding
	Enter  key.Binding
	Escape key.Binding
}

// defaultPickerKeyMap returns the standard picker key bindings.
func defaultPickerKeyMap() pickerKeyMap {
	return pickerKeyMap{
		Up: key.NewBinding(
			key.WithKeys("up", "ctrl+k"),
			key.WithHelp("↑/ctrl+k", "up"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "ctrl+j"),
			key.WithHelp("↓/ctrl+j", "down"),
		),
		Enter: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "execute"),
		),
		Escape: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "close"),
		),
	}
}

// pickerLayout describes the dimensions and chrome offsets of a picker
// dialog. Concrete dialogs vary in how much chrome they have above/below
// the scrollable list area; pickerLayout captures everything needed for
// sizing, mouse hit-testing, and scrollview placement.
type pickerLayout struct {
	WidthPercent  int // percentage of screen width to fill
	MinWidth      int // minimum dialog width in cells
	MaxWidth      int // maximum dialog width in cells
	HeightPercent int // percentage of screen height to fill
	MaxHeight     int // maximum dialog height in cells

	// ListOverhead is the total number of rows of chrome (header + footer)
	// outside the scrollable list area, including dialog borders/padding.
	ListOverhead int

	// ListStartOffset is the Y offset from the top of the dialog to the
	// first row of the scrollable list area. Used for mouse hit-testing.
	ListStartOffset int
}

// pickerHorizontalChrome is the standard horizontal chrome of styles.DialogStyle
// (border 1 + padding 2 on each side = 6 cells).
const pickerHorizontalChrome = 6

// pickerContentStartX is the X offset from the dialog's left edge to the
// first column of content (border + horizontal padding).
const pickerContentStartX = 3

// pickerCore bundles the state and behaviour shared by every list-with-filter
// dialog. Concrete dialogs embed it and add their own item type, filtering,
// and rendering.
//
// pickerCore handles:
//   - filter text input
//   - vertical scrollview with double-click detection
//   - dialog sizing and centred positioning
type pickerCore struct {
	BaseDialog

	textInput  textinput.Model
	scrollview *scrollview.Model
	keyMap     pickerKeyMap

	selected int

	// Double-click detection
	lastClickTime  time.Time
	lastClickIndex int

	layout pickerLayout
}

// newPickerCore returns a pickerCore initialised with a focused, blank text
// input, a scroll-bar-reserving scrollview, and the default key map.
func newPickerCore(layout pickerLayout, placeholder string) pickerCore {
	ti := textinput.New()
	ti.SetStyles(styles.DialogInputStyle)
	ti.Placeholder = placeholder
	ti.Focus()
	ti.CharLimit = 256
	ti.SetWidth(50)

	return pickerCore{
		textInput:      ti,
		scrollview:     scrollview.New(scrollview.WithReserveScrollbarSpace(true)),
		keyMap:         defaultPickerKeyMap(),
		lastClickIndex: -1,
		layout:         layout,
	}
}

// dialogSize returns the dialog dimensions and the inner content width.
// Content width subtracts horizontal chrome and reserved scrollbar columns.
func (p *pickerCore) dialogSize() (dialogWidth, maxHeight, contentWidth int) {
	l := p.layout
	dialogWidth = max(min(p.Width()*l.WidthPercent/100, l.MaxWidth), l.MinWidth)
	maxHeight = min(p.Height()*l.HeightPercent/100, l.MaxHeight)
	contentWidth = dialogWidth - pickerHorizontalChrome - p.scrollview.ReservedCols()
	return dialogWidth, maxHeight, contentWidth
}

// regionWidth returns the scrollview region width (content + reserved cols).
func (p *pickerCore) regionWidth(contentWidth int) int {
	return contentWidth + p.scrollview.ReservedCols()
}

// Position returns the centred (row, col) of the dialog on screen.
func (p *pickerCore) Position() (row, col int) {
	dialogWidth, maxHeight, _ := p.dialogSize()
	return CenterPosition(p.Width(), p.Height(), dialogWidth, maxHeight)
}

// SetSize updates dialog dimensions and reconfigures the scrollview region.
func (p *pickerCore) SetSize(width, height int) tea.Cmd {
	cmd := p.BaseDialog.SetSize(width, height)
	_, maxHeight, contentWidth := p.dialogSize()
	visLines := max(1, maxHeight-p.layout.ListOverhead)
	p.scrollview.SetSize(p.regionWidth(contentWidth), visLines)
	return cmd
}

// updateScrollviewPosition repositions the scrollview for accurate mouse
// hit-testing. Call from View() once content is built.
func (p *pickerCore) updateScrollviewPosition() {
	dialogRow, dialogCol := p.Position()
	p.scrollview.SetPosition(dialogCol+pickerContentStartX, dialogRow+p.layout.ListStartOffset)
}

// mouseListIndex maps a mouse Y coordinate to an item index, or returns -1
// when the click is outside the list or on a non-item line.
//
// lineToItem maps a rendered line index (which may include separators or
// headers) to an item index, returning -1 for non-item lines. Pass nil if
// the rendered list has one line per item (no separators/headers).
func (p *pickerCore) mouseListIndex(y int, lineToItem func(line int) int) int {
	dialogRow, _ := p.Position()
	listStartY := dialogRow + p.layout.ListStartOffset
	visLines := p.scrollview.VisibleHeight()
	if y < listStartY || y >= listStartY+visLines {
		return -1
	}
	actualLine := p.scrollview.ScrollOffset() + (y - listStartY)
	if lineToItem == nil {
		return actualLine
	}
	return lineToItem(actualLine)
}

// recordClick stores the current click and reports whether it constitutes a
// double-click on the same item. idx must be a valid item index (>= 0).
func (p *pickerCore) recordClick(idx int) (doubleClick bool) {
	now := time.Now()
	if idx == p.lastClickIndex && now.Sub(p.lastClickTime) < styles.DoubleClickThreshold {
		p.lastClickTime = time.Time{}
		p.lastClickIndex = -1
		return true
	}
	p.lastClickTime = now
	p.lastClickIndex = idx
	return false
}

// renderEmptyState returns the scrollview view filled with a centred italic
// placeholder. It pads the content to occupy the whole visible region so
// the dialog doesn't shrink.
func (p *pickerCore) renderEmptyState(message string, contentWidth int) string {
	return p.renderPaddedState(styles.DialogContentStyle.
		Italic(true).Align(lipgloss.Center).Width(contentWidth).
		Render(message))
}

// renderErrorState returns the scrollview view filled with a centred error
// message, padded to the full visible region.
func (p *pickerCore) renderErrorState(message string, contentWidth int) string {
	return p.renderPaddedState(styles.ErrorStyle.
		Align(lipgloss.Center).Width(contentWidth).
		Render(message))
}

// renderPaddedState fills the scrollview with a single rendered line plus
// blank padding above and below to keep the dialog at a stable height.
func (p *pickerCore) renderPaddedState(rendered string) string {
	visLines := p.scrollview.VisibleHeight()
	lines := []string{"", rendered}
	for len(lines) < visLines {
		lines = append(lines, "")
	}
	return p.scrollview.ViewWithLines(lines)
}

// closeDialogCmd is shorthand for sending a CloseDialogMsg.
func closeDialogCmd() tea.Cmd { return core.CmdHandler(CloseDialogMsg{}) }

// pickerSortKeys captures the comparison keys for ordering a picker item.
// Items with smaller Section appear first; within each section, items with
// IsCurrent=true appear first, then IsDefault=true, then alphabetically by
// Name (case-insensitive), then by Tiebreak.
type pickerSortKeys struct {
	Section   int
	IsCurrent bool
	IsDefault bool
	Name      string
	Tiebreak  string
}

// comparePickerSortKeys compares two pickerSortKeys; suitable for slices.SortFunc.
func comparePickerSortKeys(a, b pickerSortKeys) int {
	if a.Section != b.Section {
		return cmp.Compare(a.Section, b.Section)
	}
	if a.IsCurrent != b.IsCurrent {
		if a.IsCurrent {
			return -1
		}
		return 1
	}
	if a.IsDefault != b.IsDefault {
		if a.IsDefault {
			return -1
		}
		return 1
	}
	if al, bl := strings.ToLower(a.Name), strings.ToLower(b.Name); al != bl {
		return cmp.Compare(al, bl)
	}
	return cmp.Compare(a.Tiebreak, b.Tiebreak)
}

// groupedList builds a list of rendered lines mixed with non-item lines
// (separators, headers) and tracks the mapping between line indices and
// item indices so callers can do mouse hit-testing and selection scrolling
// without re-deriving the layout.
//
// Usage:
//
//	gl := newGroupedList()
//	for i, item := range filtered {
//		if needsSeparatorBefore(item) {
//			gl.AddNonItem(renderSeparator(item))
//		}
//		gl.AddItem(renderItem(item, i == selected))
//	}
//	lines := gl.Lines()
//	idx  := gl.ItemForLine(actualLine)   // for mouse hit-testing
//	line := gl.LineForItem(selected)     // for EnsureLineVisible
type groupedList struct {
	lines      []string
	lineToItem []int // -1 for non-item lines, item index otherwise
	itemToLine []int // line index for each item, in order of insertion
}

// newGroupedList returns an empty grouped list.
func newGroupedList() *groupedList { return &groupedList{} }

// AddNonItem appends a non-selectable line (header, separator, …).
func (g *groupedList) AddNonItem(line string) {
	g.lines = append(g.lines, line)
	g.lineToItem = append(g.lineToItem, -1)
}

// AddItem appends a selectable item line.
func (g *groupedList) AddItem(line string) {
	itemIdx := len(g.itemToLine)
	g.itemToLine = append(g.itemToLine, len(g.lines))
	g.lines = append(g.lines, line)
	g.lineToItem = append(g.lineToItem, itemIdx)
}

// Lines returns the full ordered list of rendered lines.
func (g *groupedList) Lines() []string { return g.lines }

// LineToItem returns the full line→item slice (-1 for non-item lines).
func (g *groupedList) LineToItem() []int { return g.lineToItem }

// ItemForLine returns the item index at the given rendered line, or -1
// when the line is a separator/header or out of range.
func (g *groupedList) ItemForLine(line int) int {
	if line < 0 || line >= len(g.lineToItem) {
		return -1
	}
	return g.lineToItem[line]
}

// LineForItem returns the rendered line index for the given item index, or 0
// when the item is out of range.
func (g *groupedList) LineForItem(item int) int {
	if item < 0 || item >= len(g.itemToLine) {
		return 0
	}
	return g.itemToLine[item]
}
