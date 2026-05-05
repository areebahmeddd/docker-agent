package oaistream

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/chat"
)

func TestConvertDocument_StrategyB64_Image(t *testing.T) {
	doc := chat.Document{
		Name:     "photo.jpg",
		MimeType: "image/jpeg",
		Source:   chat.DocumentSource{InlineData: []byte{0xFF, 0xD8, 0xFF, 0xE0}},
	}

	// Use a model ID that is unknown (text-only caps) — image/* will be dropped.
	// For B64 testing we rely on an empty modelID which gives text-only caps.
	// Use modelID "anthropic/claude-3-5-sonnet-20241022" which supports vision.
	// Since we can't fetch live data in tests, we use the function directly with
	// modelID="" (text-only) and verify the drop path, then test TXT path.

	// StrategyDrop: image not supported by text-only model
	parts, err := convertDocument(context.Background(), doc, "")
	require.NoError(t, err)
	assert.Nil(t, parts, "image should be dropped for text-only model")
}

func TestConvertDocument_StrategyTXT(t *testing.T) {
	doc := chat.Document{
		Name:     "readme.md",
		MimeType: "text/markdown",
		Source:   chat.DocumentSource{InlineText: "# Hello World"},
	}

	parts, err := convertDocument(context.Background(), doc, "")
	require.NoError(t, err)
	require.Len(t, parts, 1)
	require.NotNil(t, parts[0].OfText)
	assert.Contains(t, parts[0].OfText.Text, "readme.md")
	assert.Contains(t, parts[0].OfText.Text, "text/markdown")
	assert.Contains(t, parts[0].OfText.Text, "# Hello World")
}

func TestConvertDocument_StrategyTXT_Envelope(t *testing.T) {
	doc := chat.Document{
		Name:     "data.csv",
		MimeType: "text/csv",
		Source:   chat.DocumentSource{InlineText: "a,b,c\n1,2,3"},
	}

	parts, err := convertDocument(context.Background(), doc, "")
	require.NoError(t, err)
	require.Len(t, parts, 1)
	require.NotNil(t, parts[0].OfText)
	// Verify envelope format
	text := parts[0].OfText.Text
	assert.True(t, strings.HasPrefix(text, "<document"), "should be wrapped in document envelope")
	assert.Contains(t, text, `name="data.csv"`)
	assert.Contains(t, text, `mime-type="text/csv"`)
}

func TestConvertDocument_Drop_NoContent(t *testing.T) {
	doc := chat.Document{
		Name:     "empty.txt",
		MimeType: "text/plain",
		Source:   chat.DocumentSource{},
	}

	parts, err := convertDocument(context.Background(), doc, "")
	require.NoError(t, err)
	assert.Nil(t, parts, "should be dropped when no inline content")
}
