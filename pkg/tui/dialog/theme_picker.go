package dialog

import (
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/docker/docker-agent/pkg/tui/components/toolcommon"
	"github.com/docker/docker-agent/pkg/tui/core"
	"github.com/docker/docker-agent/pkg/tui/core/layout"
	"github.com/docker/docker-agent/pkg/tui/messages"
	"github.com/docker/docker-agent/pkg/tui/styles"
)

// ThemeChoice represents a selectable theme option
type ThemeChoice struct {
	Ref       string // Theme reference ("default" for built-in default)
	Name      string // Display name
	IsCurrent bool   // Currently active theme
	IsDefault bool   // Built-in default theme ("default")
	IsBuiltin bool   // Built-in theme shipped with docker agent
}

// themePickerDialog is a dialog for selecting a theme.
type themePickerDialog struct {
	pickerCore

	themes   []ThemeChoice
	filtered []ThemeChoice

	// Original theme for restoration on cancel
	originalThemeRef string

	// Avoid re-applying the same preview repeatedly (e.g., during filtering)
	lastPreviewRef string

	// Cached grouped list rebuilt on every render so mouse hit-testing
	// and findSelectedLine share the layout used by View().
	cached *groupedList
}

// customThemesSeparatorLabel labels the separator above the custom themes group.
const customThemesSeparatorLabel = "Custom themes"

// NewThemePickerDialog creates a new theme picker dialog.
// originalThemeRef is the currently active theme ref (for restoration on cancel).
func NewThemePickerDialog(themes []ThemeChoice, originalThemeRef string) Dialog {
	d := &themePickerDialog{
		pickerCore:       newPickerCore(themePickerLayout, "Type to search themes…"),
		originalThemeRef: originalThemeRef,
	}
	d.textInput.CharLimit = 100

	// Sort themes: built-in first, then custom. Within each section: current
	// first, then default, then alphabetically.
	sortedThemes := make([]ThemeChoice, len(themes))
	copy(sortedThemes, themes)
	slices.SortFunc(sortedThemes, func(a, b ThemeChoice) int {
		return comparePickerSortKeys(themeSortKeys(a), themeSortKeys(b))
	})
	d.themes = sortedThemes
	d.filterThemes()

	// Find current theme and select it (if multiple are marked current, pick first)
	for i, t := range d.filtered {
		if t.IsCurrent {
			d.selected = i
			d.scrollview.EnsureLineVisible(d.findSelectedLine())
			break
		}
	}

	// Initialize preview tracking to current selection (theme is already
	// applied when dialog opens).
	if d.selected >= 0 && d.selected < len(d.filtered) {
		d.lastPreviewRef = d.filtered[d.selected].Ref
	}

	return d
}

// themeSortKeys derives the sort key tuple from a ThemeChoice.
func themeSortKeys(t ThemeChoice) pickerSortKeys {
	section := 1
	if t.IsBuiltin {
		section = 0
	}
	return pickerSortKeys{
		Section:   section,
		IsCurrent: t.IsCurrent,
		IsDefault: t.IsDefault,
		Name:      t.Name,
		Tiebreak:  t.Ref,
	}
}

// themePickerLayout is the layout used by the theme picker.
var themePickerLayout = pickerLayout{
	WidthPercent:    pickerWidthPercent,
	MinWidth:        pickerMinWidth,
	MaxWidth:        pickerMaxWidth,
	HeightPercent:   pickerHeightPercent,
	MaxHeight:       pickerMaxHeight,
	ListOverhead:    pickerListVerticalOverhead,
	ListStartOffset: pickerListStartOffset,
}

func (d *themePickerDialog) Init() tea.Cmd {
	return textinput.Blink
}

func (d *themePickerDialog) Update(msg tea.Msg) (layout.Model, tea.Cmd) {
	// Scrollview handles mouse scrollbar, wheel, and pgup/pgdn/home/end
	if handled, cmd := d.scrollview.Update(msg); handled {
		return d, cmd
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		cmd := d.SetSize(msg.Width, msg.Height)
		return d, cmd

	case messages.ThemeChangedMsg:
		d.textInput.SetStyles(styles.DialogInputStyle)
		return d, nil

	case tea.PasteMsg:
		var cmd tea.Cmd
		d.textInput, cmd = d.textInput.Update(msg)
		if d.filterThemes() {
			d.scrollview.EnsureLineVisible(d.findSelectedLine())
			return d, tea.Batch(cmd, d.emitPreview())
		}
		return d, cmd

	case tea.MouseClickMsg:
		// Scrollbar clicks handled above; this handles list item clicks
		if msg.Button == tea.MouseLeft {
			if themeIdx := d.mouseListIndex(msg.Y, d.lineToThemeIndex); themeIdx >= 0 {
				if d.recordClick(themeIdx) {
					d.selected = themeIdx
					cmd := d.handleSelection()
					return d, cmd
				}
				oldSelected := d.selected
				d.selected = themeIdx
				if d.selected != oldSelected {
					cmd := d.emitPreview()
					return d, cmd
				}
			}
		}
		return d, nil

	case tea.KeyPressMsg:
		if cmd := HandleQuit(msg); cmd != nil {
			return d, cmd
		}

		switch {
		case key.Matches(msg, d.keyMap.Escape):
			return d, tea.Sequence(
				closeDialogCmd(),
				core.CmdHandler(messages.ThemeCancelPreviewMsg{OriginalRef: d.originalThemeRef}),
			)

		case key.Matches(msg, d.keyMap.Up):
			if d.selected > 0 {
				d.selected--
				d.scrollview.EnsureLineVisible(d.findSelectedLine())
				cmd := d.emitPreview()
				return d, cmd
			}
			return d, nil

		case key.Matches(msg, d.keyMap.Down):
			if d.selected < len(d.filtered)-1 {
				d.selected++
				d.scrollview.EnsureLineVisible(d.findSelectedLine())
				cmd := d.emitPreview()
				return d, cmd
			}
			return d, nil

		case key.Matches(msg, d.keyMap.Enter):
			cmd := d.handleSelection()
			return d, cmd

		default:
			var cmd tea.Cmd
			d.textInput, cmd = d.textInput.Update(msg)
			if d.filterThemes() {
				d.scrollview.EnsureLineVisible(d.findSelectedLine())
				return d, tea.Batch(cmd, d.emitPreview())
			}
			return d, cmd
		}
	}

	return d, nil
}

func (d *themePickerDialog) handleSelection() tea.Cmd {
	if d.selected >= 0 && d.selected < len(d.filtered) {
		selected := d.filtered[d.selected]
		return tea.Sequence(
			closeDialogCmd(),
			core.CmdHandler(messages.ChangeThemeMsg{ThemeRef: selected.Ref}),
		)
	}
	return nil
}

// emitPreview requests a theme preview via an app-level message.
func (d *themePickerDialog) emitPreview() tea.Cmd {
	if d.selected >= 0 && d.selected < len(d.filtered) {
		selected := d.filtered[d.selected]
		// Skip if we're already previewing this exact selection.
		if selected.Ref == d.lastPreviewRef {
			return nil
		}
		d.lastPreviewRef = selected.Ref
		return core.CmdHandler(messages.ThemePreviewMsg{
			ThemeRef:    selected.Ref,
			OriginalRef: d.originalThemeRef,
		})
	}
	return nil
}

// buildList rebuilds the cached grouped list of themes including separators.
func (d *themePickerDialog) buildList(contentWidth int) *groupedList {
	gl := newGroupedList()

	hasBuiltinThemes := false
	for _, t := range d.filtered {
		if t.IsBuiltin {
			hasBuiltinThemes = true
			break
		}
	}

	customSeparatorShown := false
	for i, theme := range d.filtered {
		if !theme.IsBuiltin && !customSeparatorShown {
			if hasBuiltinThemes {
				gl.AddNonItem(RenderGroupSeparator(customThemesSeparatorLabel, contentWidth))
			}
			customSeparatorShown = true
		}
		gl.AddItem(d.renderTheme(theme, i == d.selected, contentWidth))
	}
	d.cached = gl
	return gl
}

// lineToThemeIndex returns the theme index for a rendered line, or -1 for
// separators. Used by mouse hit-testing.
func (d *themePickerDialog) lineToThemeIndex(line int) int {
	if d.cached == nil {
		d.buildList(0)
	}
	return d.cached.ItemForLine(line)
}

// findSelectedLine returns the line index for the currently selected theme.
func (d *themePickerDialog) findSelectedLine() int {
	if d.cached == nil {
		d.buildList(0)
	}
	return d.cached.LineForItem(d.selected)
}

func (d *themePickerDialog) View() string {
	dialogWidth, _, contentWidth := d.dialogSize()
	d.textInput.SetWidth(contentWidth)

	gl := d.buildList(contentWidth)
	d.updateScrollviewPosition()
	d.scrollview.SetContent(gl.Lines(), len(gl.Lines()))

	var scrollableContent string
	if len(d.filtered) == 0 {
		scrollableContent = d.renderEmptyState("No themes found", contentWidth)
	} else {
		scrollableContent = d.scrollview.View()
	}

	content := NewContent(d.regionWidth(contentWidth)).
		AddTitle("Select Theme").
		AddSpace().
		AddContent(d.textInput.View()).
		AddSeparator().
		AddContent(scrollableContent).
		AddSpace().
		AddHelpKeys("↑/↓", "navigate", "enter", "select", "esc", "cancel").
		Build()

	return styles.DialogStyle.Width(dialogWidth).Render(content)
}

func (d *themePickerDialog) renderTheme(theme ThemeChoice, selected bool, maxWidth int) string {
	nameStyle, descStyle := styles.PaletteUnselectedActionStyle, styles.PaletteUnselectedDescStyle
	defaultBadgeStyle := styles.BadgeDefaultStyle
	currentBadgeStyle := styles.BadgeCurrentStyle
	if selected {
		nameStyle, descStyle = styles.PaletteSelectedActionStyle, styles.PaletteSelectedDescStyle
		defaultBadgeStyle = defaultBadgeStyle.Background(styles.MobyBlue)
		currentBadgeStyle = currentBadgeStyle.Background(styles.MobyBlue)
	}

	displayName := theme.Name

	// For custom themes, show filename for identification. Built-in themes
	// don't need a description.
	var desc string
	if !theme.IsBuiltin {
		desc = strings.TrimPrefix(theme.Ref, styles.UserThemePrefix)
	}

	// Calculate badge widths - show all applicable badges
	var badgeWidth int
	if theme.IsCurrent {
		badgeWidth += lipgloss.Width(" (current)")
	}
	if theme.IsDefault {
		badgeWidth += lipgloss.Width(" (default)")
	}

	separatorWidth := 0
	if desc != "" {
		separatorWidth = lipgloss.Width(" • ")
	}

	maxNameWidth := maxWidth - badgeWidth
	if desc != "" {
		minDescWidth := min(10, lipgloss.Width(desc))
		maxNameWidth = maxWidth - badgeWidth - separatorWidth - minDescWidth
	}

	if lipgloss.Width(displayName) > maxNameWidth {
		displayName = toolcommon.TruncateText(displayName, maxNameWidth)
	}

	// Build the name with colored badges in order: name (current) (default).
	nameParts := []string{nameStyle.Render(displayName)}
	if theme.IsCurrent {
		nameParts = append(nameParts, currentBadgeStyle.Render(" (current)"))
	}
	if theme.IsDefault {
		nameParts = append(nameParts, defaultBadgeStyle.Render(" (default)"))
	}
	name := strings.Join(nameParts, "")

	if desc != "" {
		nameWidth := lipgloss.Width(name)
		remainingWidth := maxWidth - nameWidth - separatorWidth
		if remainingWidth > 0 {
			truncatedDesc := toolcommon.TruncateText(desc, remainingWidth)
			return name + descStyle.Render(" • "+truncatedDesc)
		}
	}

	return name
}

func (d *themePickerDialog) filterThemes() (selectionChanged bool) {
	query := strings.ToLower(strings.TrimSpace(d.textInput.Value()))

	// Remember current selection so filtering doesn't cause surprising jumps.
	prevRef := ""
	if d.selected >= 0 && d.selected < len(d.filtered) {
		prevRef = d.filtered[d.selected].Ref
	}

	d.filtered = d.filtered[:0]
	for _, theme := range d.themes {
		if query == "" {
			d.filtered = append(d.filtered, theme)
			continue
		}
		searchText := strings.ToLower(theme.Name + " " + theme.Ref)
		if strings.Contains(searchText, query) {
			d.filtered = append(d.filtered, theme)
		}
	}

	// Restore selection if possible; otherwise fall back to first item.
	d.selected = 0
	if prevRef != "" {
		for i, t := range d.filtered {
			if t.Ref == prevRef {
				d.selected = i
				break
			}
		}
	}

	d.scrollview.SetScrollOffset(0)
	d.cached = nil // layout will be rebuilt on next render

	newRef := ""
	if d.selected >= 0 && d.selected < len(d.filtered) {
		newRef = d.filtered[d.selected].Ref
	}
	return newRef != prevRef
}
