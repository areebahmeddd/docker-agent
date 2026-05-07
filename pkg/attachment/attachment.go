// Package attachment provides MIME-aware routing for document attachments.
//
// It defines how a chat.Document should be sent to a model: either dropped
// (unsupported), wrapped in a plain-text envelope (StrategyTXT), or encoded
// as inline base64 data (StrategyB64).
package attachment

import (
	"fmt"
	"strings"

	"github.com/docker/docker-agent/pkg/attachment/modelcaps"
	"github.com/docker/docker-agent/pkg/chat"
)

// Strategy describes how an attachment should be handled before sending to the
// provider.
type Strategy int

const (
	// StrategyDrop means the attachment is not supported by the model or has no
	// inline content, and should be silently skipped (with a log warning).
	StrategyDrop Strategy = iota

	// StrategyTXT means the attachment should be wrapped in a TXTEnvelope and
	// sent as plain text.  Used for text/* MIME types whose content is already
	// in Source.InlineText.
	StrategyTXT

	// StrategyB64 means the attachment content (Source.InlineData) should be
	// base64-encoded and sent as a native provider image/document block.
	StrategyB64
)

// Decide returns the routing Strategy for a document given the current model's
// capabilities.
//
// Algorithm:
//  1. If the model does not support the document's MIME type → (Drop, reason).
//  2. If Source.InlineData is non-empty → (B64, "").
//  3. If Source.InlineText is non-empty → (TXT, "").
//  4. Otherwise → (Drop, "no inline content").
func Decide(doc chat.Document, mc modelcaps.ModelCapabilities) (Strategy, string) {
	if !mc.Supports(doc.MimeType) {
		return StrategyDrop, fmt.Sprintf("model does not support MIME type %q", doc.MimeType)
	}
	if len(doc.Source.InlineData) > 0 {
		return StrategyB64, ""
	}
	if doc.Source.InlineText != "" {
		return StrategyTXT, ""
	}
	return StrategyDrop, "no inline content"
}

// TXTEnvelope wraps a text document body in an XML-like tag that models can
// parse as a named attachment.
//
//	<document name="report.md" mime-type="text/markdown">…body…</document>
//
// The body is sanitised to prevent content from breaking out of the envelope:
// any occurrence of "</document>" in the body is replaced with "&lt;/document&gt;".
func TXTEnvelope(name, mimeType, body string) string {
	safe := strings.ReplaceAll(body, "</document>", "&lt;/document&gt;")
	return fmt.Sprintf("<document name=%q mime-type=%q>%s</document>", name, mimeType, safe)
}
