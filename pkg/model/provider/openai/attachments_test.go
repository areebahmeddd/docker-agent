package openai

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/chat"
)

func TestConvertDocumentResponseInput_StrategyTXT(t *testing.T) {
	doc := chat.Document{
		Name:     "spec.md",
		MimeType: "text/markdown",
		Source:   chat.DocumentSource{InlineText: "## API Spec"},
	}

	parts, err := convertDocumentToResponseInput(context.Background(), doc, "")
	require.NoError(t, err)
	require.Len(t, parts, 1)
	require.NotNil(t, parts[0].OfInputText)
	text := parts[0].OfInputText.Text
	assert.Contains(t, text, "spec.md")
	assert.Contains(t, text, "text/markdown")
	assert.Contains(t, text, "## API Spec")
}

func TestConvertDocumentResponseInput_StrategyTXT_Envelope(t *testing.T) {
	doc := chat.Document{
		Name:     "data.csv",
		MimeType: "text/csv",
		Source:   chat.DocumentSource{InlineText: "x,y"},
	}

	parts, err := convertDocumentToResponseInput(context.Background(), doc, "")
	require.NoError(t, err)
	require.Len(t, parts, 1)
	require.NotNil(t, parts[0].OfInputText)
	text := parts[0].OfInputText.Text
	assert.True(t, strings.HasPrefix(text, "<document"), "should be wrapped in envelope")
	assert.Contains(t, text, `name="data.csv"`)
}

func TestConvertDocumentResponseInput_Drop_NoContent(t *testing.T) {
	doc := chat.Document{
		Name:     "empty.md",
		MimeType: "text/markdown",
		Source:   chat.DocumentSource{},
	}

	parts, err := convertDocumentToResponseInput(context.Background(), doc, "")
	require.NoError(t, err)
	assert.Nil(t, parts, "should be nil when no inline content")
}

func TestConvertDocumentResponseInput_Drop_UnsupportedMIME(t *testing.T) {
	doc := chat.Document{
		Name:     "photo.jpg",
		MimeType: "image/jpeg",
		Source:   chat.DocumentSource{InlineData: []byte{0xFF, 0xD8}},
	}

	parts, err := convertDocumentToResponseInput(context.Background(), doc, "")
	require.NoError(t, err)
	assert.Nil(t, parts, "image should be dropped for text-only model")
}
