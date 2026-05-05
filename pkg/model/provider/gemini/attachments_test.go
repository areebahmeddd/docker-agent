package gemini

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/chat"
)

func TestConvertDocumentGemini_StrategyTXT(t *testing.T) {
	doc := chat.Document{
		Name:     "readme.md",
		MimeType: "text/markdown",
		Source:   chat.DocumentSource{InlineText: "# Read Me"},
	}

	part, err := convertDocument(context.Background(), doc, "")
	require.NoError(t, err)
	require.NotNil(t, part)
	// The TXT path returns a text part; verify content via the string representation
	assert.Contains(t, part.Text, "readme.md")
	assert.Contains(t, part.Text, "text/markdown")
	assert.Contains(t, part.Text, "# Read Me")
}

func TestConvertDocumentGemini_StrategyTXT_Envelope(t *testing.T) {
	doc := chat.Document{
		Name:     "data.csv",
		MimeType: "text/csv",
		Source:   chat.DocumentSource{InlineText: "col1,col2"},
	}

	part, err := convertDocument(context.Background(), doc, "")
	require.NoError(t, err)
	require.NotNil(t, part)
	assert.True(t, strings.HasPrefix(part.Text, "<document"), "should be wrapped in envelope")
	assert.Contains(t, part.Text, `name="data.csv"`)
}

func TestConvertDocumentGemini_Drop_NoContent(t *testing.T) {
	doc := chat.Document{
		Name:     "empty.txt",
		MimeType: "text/plain",
		Source:   chat.DocumentSource{},
	}

	part, err := convertDocument(context.Background(), doc, "")
	require.NoError(t, err)
	assert.Nil(t, part, "should be nil when no inline content")
}

func TestConvertDocumentGemini_Drop_UnsupportedMIME(t *testing.T) {
	doc := chat.Document{
		Name:     "photo.jpg",
		MimeType: "image/jpeg",
		Source:   chat.DocumentSource{InlineData: []byte{0xFF, 0xD8}},
	}

	// text-only model (empty modelID) → drop
	part, err := convertDocument(context.Background(), doc, "")
	require.NoError(t, err)
	assert.Nil(t, part, "image should be dropped for text-only model")
}
