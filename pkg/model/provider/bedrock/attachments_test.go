package bedrock

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/chat"
)

func TestConvertDocumentBedrock_StrategyTXT(t *testing.T) {
	doc := chat.Document{
		Name:     "notes.md",
		MimeType: "text/markdown",
		Source:   chat.DocumentSource{InlineText: "## Notes"},
	}

	blocks, err := convertDocument(context.Background(), doc, "")
	require.NoError(t, err)
	require.Len(t, blocks, 1)
	textBlock, ok := blocks[0].(*types.ContentBlockMemberText)
	require.True(t, ok, "expected text block for TXT strategy")
	assert.Contains(t, textBlock.Value, "notes.md")
	assert.Contains(t, textBlock.Value, "text/markdown")
	assert.Contains(t, textBlock.Value, "## Notes")
}

func TestConvertDocumentBedrock_StrategyTXT_Envelope(t *testing.T) {
	doc := chat.Document{
		Name:     "data.csv",
		MimeType: "text/csv",
		Source:   chat.DocumentSource{InlineText: "a,b"},
	}

	blocks, err := convertDocument(context.Background(), doc, "")
	require.NoError(t, err)
	require.Len(t, blocks, 1)
	textBlock, ok := blocks[0].(*types.ContentBlockMemberText)
	require.True(t, ok, "expected text block")
	assert.True(t, strings.HasPrefix(textBlock.Value, "<document"), "should be wrapped in envelope")
	assert.Contains(t, textBlock.Value, `name="data.csv"`)
}

func TestConvertDocumentBedrock_Drop_NoContent(t *testing.T) {
	doc := chat.Document{
		Name:     "empty.txt",
		MimeType: "text/plain",
		Source:   chat.DocumentSource{},
	}

	blocks, err := convertDocument(context.Background(), doc, "")
	require.NoError(t, err)
	assert.Nil(t, blocks, "should be nil when no inline content")
}

func TestConvertDocumentBedrock_Drop_UnsupportedMIME(t *testing.T) {
	doc := chat.Document{
		Name:     "photo.jpg",
		MimeType: "image/jpeg",
		Source:   chat.DocumentSource{InlineData: []byte{0xFF, 0xD8}},
	}

	blocks, err := convertDocument(context.Background(), doc, "")
	require.NoError(t, err)
	assert.Nil(t, blocks, "image should be dropped for text-only model")
}
