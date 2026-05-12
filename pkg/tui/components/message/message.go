package message

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/docker/docker-agent/pkg/tui/components/markdown"
	"github.com/docker/docker-agent/pkg/tui/components/spinner"
	"github.com/docker/docker-agent/pkg/tui/core/layout"
	"github.com/docker/docker-agent/pkg/tui/styles"
	"github.com/docker/docker-agent/pkg/tui/types"
)

// Model represents a view that can render a message
type Model interface {
	layout.Model
	layout.Sizeable
	SetMessage(msg *types.Message)
	SetSelected(selected bool)
	SetHovered(hovered bool)
}

// messageModel implements Model
type messageModel struct {
	message  *types.Message
	previous *types.Message

	width    int
	height   int
	focused  bool
	selected bool
	hovered  bool
	spinner  spinner.Spinner

	// renderCache memoizes the output of Render(width) keyed by the inputs
	// that affect its output. During streaming, View() and Height() are called
	// in pairs for each new chunk, and the chat list also re-renders for hover
	// tracking and scroll updates; without this cache each call would re-parse
	// the entire accumulated markdown from scratch.
	renderCache renderCache

	// mdRenderer is reused across renders of an assistant message so that
	// streamed-in chunks only re-render the trailing block instead of the whole
	// accumulated markdown each time.
	mdRenderer *markdown.IncrementalRenderer
}

// renderCache stores the most recent Render result keyed by the inputs that
// can change its output. The key is small enough (a string and a few flags)
// that comparing it is much cheaper than rendering markdown.
type renderCache struct {
	valid     bool
	content   string
	msgType   types.MessageType
	width     int
	selected  bool
	hovered   bool
	sameAgent bool
	result    string
}

// New creates a new message view
func New(msg, previous *types.Message) *messageModel {
	return &messageModel{
		message:  msg,
		previous: previous,
		width:    80, // Default width
		height:   1,  // Will be calculated
		focused:  false,
		spinner:  spinner.New(spinner.ModeBoth, styles.SpinnerDotsAccentStyle),
	}
}

// Bubble Tea Model methods

// Init initializes the message view
func (mv *messageModel) Init() tea.Cmd {
	if mv.message.Type == types.MessageTypeSpinner || mv.message.Type == types.MessageTypeLoading {
		return mv.spinner.Init()
	}
	return nil
}

func (mv *messageModel) SetMessage(msg *types.Message) {
	// If the new content is not an extension of the previous one (different
	// message, or the message was edited), drop the IncrementalRenderer's
	// cached prefix so its memory is released immediately rather than on the
	// next render. The renderer detects mismatches on its own and falls back
	// to a full render either way, so this is purely an optimization.
	if mv.mdRenderer != nil && mv.message != nil && msg != nil && !strings.HasPrefix(msg.Content, mv.message.Content) {
		mv.mdRenderer.Reset()
	}
	mv.message = msg
	mv.renderCache.valid = false
}

func (mv *messageModel) SetSelected(selected bool) {
	if mv.selected != selected {
		mv.selected = selected
		mv.renderCache.valid = false
	}
}

func (mv *messageModel) SetHovered(hovered bool) {
	if mv.hovered != hovered {
		mv.hovered = hovered
		mv.renderCache.valid = false
	}
}

// Update handles messages and updates the message view state
func (mv *messageModel) Update(msg tea.Msg) (layout.Model, tea.Cmd) {
	if mv.message.Type == types.MessageTypeSpinner || mv.message.Type == types.MessageTypeLoading {
		s, cmd := mv.spinner.Update(msg)
		mv.spinner = s.(spinner.Spinner)
		return mv, cmd
	}
	return mv, nil
}

// View renders the message view
func (mv *messageModel) View() string {
	return mv.Render(mv.width)
}

// Render renders the message view content. Results are memoized so repeated
// calls with the same inputs (very common during streaming, hover tracking,
// and from Height()) skip the expensive markdown parse.
func (mv *messageModel) Render(width int) string {
	msg := mv.message

	// Spinner-driven types (MessageTypeSpinner, MessageTypeLoading, and an empty
	// MessageTypeAssistant placeholder) animate on every tick, so the result is
	// not cacheable. Everything else is a pure function of the inputs tracked in
	// renderCache below.
	cacheable := !mv.isSpinnerDriven()
	if cacheable {
		c := &mv.renderCache
		if c.valid &&
			c.width == width &&
			c.msgType == msg.Type &&
			c.selected == mv.selected &&
			c.hovered == mv.hovered &&
			c.content == msg.Content &&
			c.sameAgent == mv.sameAgentAsPrevious(msg) {
			return c.result
		}
	}

	result := mv.render(width)

	if cacheable {
		mv.renderCache = renderCache{
			valid:     true,
			content:   msg.Content,
			msgType:   msg.Type,
			width:     width,
			selected:  mv.selected,
			hovered:   mv.hovered,
			sameAgent: mv.sameAgentAsPrevious(msg),
			result:    result,
		}
	}
	return result
}

// isSpinnerDriven reports whether the rendered output animates on every tick
// and therefore cannot be cached across renders.
func (mv *messageModel) isSpinnerDriven() bool {
	switch mv.message.Type {
	case types.MessageTypeSpinner, types.MessageTypeLoading:
		return true
	case types.MessageTypeAssistant:
		return mv.message.Content == ""
	}
	return false
}

// render is the uncached rendering core. Render() wraps it with memoization.
func (mv *messageModel) render(width int) string {
	msg := mv.message
	switch msg.Type {
	case types.MessageTypeSpinner:
		return mv.spinner.View()
	case types.MessageTypeUser:
		// Choose style based on selection state
		messageStyle := styles.UserMessageStyle
		if mv.selected && msg.SessionPosition != nil {
			messageStyle = styles.SelectedUserMessageStyle
		}

		if msg.SessionPosition == nil {
			return messageStyle.Width(width).Render(msg.Content)
		}

		// For editable messages, place the pencil icon in the top padding row
		innerWidth := width - messageStyle.GetHorizontalFrameSize()
		content := strings.TrimRight(msg.Content, "\n\r\t ")
		if content == "" {
			content = msg.Content
		}

		// Create the edit icon for the top row
		editIcon := styles.MutedStyle.Render(types.UserMessageEditLabel)
		iconWidth := ansi.StringWidth(types.UserMessageEditLabel)

		// Create a top row with the icon pushed to the right edge
		// This row replaces the top padding and becomes part of the content
		topPadding := max(innerWidth-iconWidth, 0)
		topRow := strings.Repeat(" ", topPadding) + editIcon

		// Combine: icon row + content (icon row acts as the top padding)
		contentWithIcon := topRow + "\n" + content

		// Use a modified style with no top padding (our icon row replaces it)
		noTopPaddingStyle := messageStyle.PaddingTop(0)
		return noTopPaddingStyle.Width(width).Render(contentWithIcon)
	case types.MessageTypeAssistant:
		if msg.Content == "" {
			return mv.spinner.View()
		}

		messageStyle := styles.AssistantMessageStyle
		if mv.selected {
			messageStyle = styles.SelectedMessageStyle
		}

		innerRenderWidth := width - messageStyle.GetHorizontalFrameSize()
		rendered, err := mv.renderAssistantMarkdown(msg.Content, innerRenderWidth)
		if err != nil {
			rendered = msg.Content
		}

		var prefix string
		if !mv.sameAgentAsPrevious(msg) {
			prefix = mv.senderPrefix(msg.Sender)
		}

		// Always reserve a top row to avoid layout shifts when the copy icon
		// appears on hover. When not hovered, the row is filled with spaces
		// (invisible). AssistantMessageStyle has PaddingTop=0, so this extra
		// row acts as a stable spacer.
		innerWidth := width - messageStyle.GetHorizontalFrameSize()
		topRow := strings.Repeat(" ", innerWidth)
		if mv.hovered || mv.selected {
			copyIcon := styles.MutedStyle.Render(types.AssistantMessageCopyLabel)
			iconWidth := ansi.StringWidth(types.AssistantMessageCopyLabel)
			padding := max(innerWidth-iconWidth, 0)
			topRow = strings.Repeat(" ", padding) + copyIcon
		}
		return prefix + messageStyle.Width(width).Render(topRow+"\n"+rendered)
	case types.MessageTypeShellOutput:
		if rendered, err := markdown.NewRenderer(width).Render(fmt.Sprintf("```console\n%s\n```", msg.Content)); err == nil {
			return rendered
		}
		return msg.Content
	case types.MessageTypeCancelled:
		return styles.WarningStyle.Render("⚠ stream cancelled ⚠")
	case types.MessageTypeWelcome:
		messageStyle := styles.WelcomeMessageStyle
		// Convert explicit newlines to markdown hard line breaks (two trailing spaces)
		// This preserves line breaks from YAML multiline syntax (|) while still
		// allowing markdown formatting like **bold** and *italic*
		content := preserveLineBreaks(msg.Content)
		rendered, err := markdown.NewRenderer(width - messageStyle.GetHorizontalFrameSize()).Render(content)
		if err != nil {
			rendered = msg.Content
		}
		return messageStyle.Width(width - 1).Render(strings.TrimRight(rendered, "\n\r\t "))
	case types.MessageTypeError:
		return styles.ErrorMessageStyle.Width(width - 1).Render(msg.Content)
	case types.MessageTypeLoading:
		// Show spinner with the loading description, truncated to fit width
		spinnerView := mv.spinner.View()
		spinnerWidth := ansi.StringWidth(spinnerView) + 1 // +1 for space separator
		maxDescWidth := width - spinnerWidth
		description := msg.Content
		if maxDescWidth > 0 && ansi.StringWidth(description) > maxDescWidth {
			description = ansi.Truncate(description, maxDescWidth, "…")
		}
		return spinnerView + " " + styles.MutedStyle.Render(description)
	default:
		return msg.Content
	}
}

// renderAssistantMarkdown renders streamed assistant content using a per-message
// IncrementalRenderer. The renderer remembers the last rendered stable prefix
// so each new chunk only re-parses the trailing region. The first render at a
// given width is equivalent to a fresh full render.
func (mv *messageModel) renderAssistantMarkdown(content string, width int) (string, error) {
	if mv.mdRenderer == nil {
		mv.mdRenderer = markdown.NewIncrementalRenderer(width)
	} else {
		mv.mdRenderer.SetWidth(width)
	}
	return mv.mdRenderer.Render(content)
}

func (mv *messageModel) senderPrefix(sender string) string {
	if sender == "" {
		return ""
	}
	return styles.AgentBadgeStyleFor(sender).MarginLeft(2).Render(sender) + "\n\n"
}

// sameAgentAsPrevious returns true if the previous message was from the same agent
func (mv *messageModel) sameAgentAsPrevious(msg *types.Message) bool {
	if mv.previous == nil || mv.previous.Sender != msg.Sender {
		return false
	}
	switch mv.previous.Type {
	case types.MessageTypeAssistant,
		types.MessageTypeAssistantReasoningBlock,
		types.MessageTypeToolCall,
		types.MessageTypeToolResult:
		return true
	default:
		return false
	}
}

// Height calculates the height needed for this message view. Render() is
// memoized, so calling it from here does not duplicate work when View() is
// invoked for the same inputs.
func (mv *messageModel) Height(width int) int {
	content := mv.Render(width)
	return strings.Count(content, "\n") + 1
}

// Message returns the underlying message
func (mv *messageModel) Message() *types.Message {
	return mv.message
}

// Layout.Sizeable methods

// StopAnimation stops the spinner animation and unregisters from the animation coordinator.
// This must be called when the view is removed from the UI to avoid leaked animation subscriptions.
func (mv *messageModel) StopAnimation() {
	if mv.message.Type == types.MessageTypeSpinner || mv.message.Type == types.MessageTypeLoading {
		mv.spinner.Stop()
	}
}

// SetSize sets the dimensions of the message view
func (mv *messageModel) SetSize(width, height int) tea.Cmd {
	if mv.width != width {
		mv.renderCache.valid = false
	}
	mv.width = width
	mv.height = height
	return nil
}

// GetSize returns the current dimensions
func (mv *messageModel) GetSize() (width, height int) {
	return mv.width, mv.height
}

// preserveLineBreaks preserves leading indentation by converting leading spaces
// to non-breaking spaces (U+00A0) which won't be stripped by markdown parsers.
// Line breaks are handled by glamour.WithPreservedNewLines().
func preserveLineBreaks(s string) string {
	if !strings.Contains(s, "\n") {
		return preserveIndentation(s)
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = preserveIndentation(line)
	}
	return strings.Join(lines, "\n")
}

// preserveIndentation converts leading spaces in a line to non-breaking spaces (U+00A0).
// This prevents markdown parsers from stripping leading whitespace while maintaining
// the same visual appearance in terminal output.
func preserveIndentation(line string) string {
	if line == "" || line[0] != ' ' {
		return line
	}
	leadingSpaces := 0
	for _, c := range line {
		if c == ' ' {
			leadingSpaces++
		} else {
			break
		}
	}
	if leadingSpaces == 0 {
		return line
	}
	return strings.Repeat("\u00A0", leadingSpaces) + line[leadingSpaces:]
}
