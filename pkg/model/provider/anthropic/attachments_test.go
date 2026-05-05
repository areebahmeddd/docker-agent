package anthropic

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/chat"
)

func TestConvertDocumentAnthropic_StrategyTXT(t *testing.T) {
	doc := chat.Document{
		Name:     "spec.md",
		MimeType: "text/markdown",
		Source:   chat.DocumentSource{InlineText: "## Specification"},
	}

	blocks, err := convertDocument(context.Background(), doc, "")
	require.NoError(t, err)
	require.Len(t, blocks, 1)
	require.NotNil(t, blocks[0].OfText)
	assert.Contains(t, blocks[0].OfText.Text, "spec.md")
	assert.Contains(t, blocks[0].OfText.Text, "text/markdown")
	assert.Contains(t, blocks[0].OfText.Text, "## Specification")
}

func TestConvertDocumentAnthropic_StrategyTXT_Envelope(t *testing.T) {
	doc := chat.Document{
		Name:     "notes.txt",
		MimeType: "text/plain",
		Source:   chat.DocumentSource{InlineText: "some notes"},
	}

	blocks, err := convertDocument(context.Background(), doc, "")
	require.NoError(t, err)
	require.Len(t, blocks, 1)
	require.NotNil(t, blocks[0].OfText)
	text := blocks[0].OfText.Text
	assert.True(t, strings.HasPrefix(text, "<document"), "should be wrapped in envelope")
	assert.Contains(t, text, `name="notes.txt"`)
}

func TestConvertDocumentAnthropic_Drop_NoContent(t *testing.T) {
	doc := chat.Document{
		Name:     "empty.txt",
		MimeType: "text/plain",
		Source:   chat.DocumentSource{},
	}

	blocks, err := convertDocument(context.Background(), doc, "")
	require.NoError(t, err)
	assert.Nil(t, blocks, "should be dropped when no inline content")
}

func TestConvertDocumentAnthropic_Drop_UnsupportedMIME(t *testing.T) {
	// image/* with text-only model (modelID="") should be dropped
	doc := chat.Document{
		Name:     "photo.jpg",
		MimeType: "image/jpeg",
		Source:   chat.DocumentSource{InlineData: []byte{0xFF, 0xD8}},
	}

	blocks, err := convertDocument(context.Background(), doc, "")
	require.NoError(t, err)
	assert.Nil(t, blocks, "image should be dropped for text-only model")
}
