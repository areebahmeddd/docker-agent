package anthropic

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/attachment/modelcaps"
	"github.com/docker/docker-agent/pkg/chat"
	"github.com/docker/docker-agent/pkg/modelsdev"
)

// minJPEG is a minimal JPEG magic-byte header for use in tests.
var minJPEG = []byte{0xFF, 0xD8, 0xFF, 0xE0}

// minPDF is a minimal PDF magic-byte header for use in tests.
var minPDF = []byte{0x25, 0x50, 0x44, 0x46, 0x2D} // %PDF-

// TestConvertDocumentAnthropic_StrategyB64_Image verifies that an image document
// with InlineData and a vision-capable model produces a native ImageBlockParam.
func TestConvertDocumentAnthropic_StrategyB64_Image(t *testing.T) {
	doc := chat.Document{
		Name:     "photo.jpg",
		MimeType: "image/jpeg",
		Source:   chat.DocumentSource{InlineData: minJPEG},
	}

	visionCaps := modelcaps.CapsWith(true, true)
	blocks, err := convertDocumentWithCaps(t.Context(), doc, visionCaps)
	require.NoError(t, err)
	require.Len(t, blocks, 1, "expected exactly one block")
	require.NotNil(t, blocks[0].OfImage, "expected image block")
	assert.Nil(t, blocks[0].OfText, "expected no text block for image")
}

// TestConvertDocumentAnthropic_StrategyB64_PDF verifies that a PDF document
// produces a native BetaRequestDocumentBlock when the model supports PDFs.
func TestConvertDocumentAnthropic_StrategyB64_PDF(t *testing.T) {
	doc := chat.Document{
		Name:     "spec.pdf",
		MimeType: "application/pdf",
		Source:   chat.DocumentSource{InlineData: minPDF},
	}

	pdfCaps := modelcaps.CapsWith(true, true)
	blocks, err := convertDocumentWithCaps(t.Context(), doc, pdfCaps)
	require.NoError(t, err)
	require.Len(t, blocks, 1, "expected exactly one block")
	require.NotNil(t, blocks[0].OfDocument, "expected document block for PDF")
	assert.Nil(t, blocks[0].OfText, "expected no text block for PDF")
}

// TestConvertDocumentAnthropic_QualifiedIDRequired is the regression test for
// the bug where callers passed a bare model name to convertDocument instead of
// a "provider/model" qualified identifier, causing modelcaps to miss the model
// and silently drop all image/PDF attachments.
//
// It calls convertDocumentFromStore directly with an injected fake store so
// the test exercises the full modelID → LoadFromStore → caps → blocks path
// without hitting the network.
func TestConvertDocumentAnthropic_QualifiedIDRequired(t *testing.T) {
	store := modelsdev.NewDatabaseStore(&modelsdev.Database{
		Providers: map[string]modelsdev.Provider{
			"anthropic": {
				Models: map[string]modelsdev.Model{
					"claude-sonnet-4-6": {
						Modalities: modelsdev.Modalities{
							Input: []string{"text", "image", "pdf"},
						},
					},
				},
			},
		},
	})

	doc := chat.Document{
		Name:     "photo.jpg",
		MimeType: "image/jpeg",
		Source:   chat.DocumentSource{InlineData: minJPEG},
	}

	// Bare model name (the original bug): LoadFromStore misses the model,
	// capabilities fall back to text-only, image must be dropped.
	blocksBare, err := convertDocumentFromStore(t.Context(), doc, "claude-sonnet-4-6", store)
	require.NoError(t, err)
	assert.Nil(t, blocksBare, "bare model name must not resolve caps: image should be dropped")

	// Qualified ID (the fix): LoadFromStore finds the model, vision caps
	// are set, image must be preserved as a native image block.
	blocksQualified, err := convertDocumentFromStore(t.Context(), doc, "anthropic/claude-sonnet-4-6", store)
	require.NoError(t, err)
	require.Len(t, blocksQualified, 1, "qualified ID must resolve caps: image should be present")
	assert.NotNil(t, blocksQualified[0].OfImage, "expected native image block for qualified model ID")
}

func TestConvertDocumentAnthropic_StrategyTXT(t *testing.T) {
	doc := chat.Document{
		Name:     "spec.md",
		MimeType: "text/markdown",
		Source:   chat.DocumentSource{InlineText: "## Specification"},
	}

	blocks, err := convertDocument(t.Context(), doc, "")
	require.NoError(t, err)
	require.Len(t, blocks, 1)
	require.NotNil(t, blocks[0].OfText)
	assert.Contains(t, blocks[0].OfText.Text, "spec-md")
	assert.Contains(t, blocks[0].OfText.Text, "text-markdown")
	assert.Contains(t, blocks[0].OfText.Text, "## Specification")
}

func TestConvertDocumentAnthropic_StrategyTXT_Envelope(t *testing.T) {
	doc := chat.Document{
		Name:     "notes.txt",
		MimeType: "text/plain",
		Source:   chat.DocumentSource{InlineText: "some notes"},
	}

	blocks, err := convertDocument(t.Context(), doc, "")
	require.NoError(t, err)
	require.Len(t, blocks, 1)
	require.NotNil(t, blocks[0].OfText)
	text := blocks[0].OfText.Text
	assert.True(t, strings.HasPrefix(text, "<document"), "should be wrapped in envelope")
}

func TestConvertDocumentAnthropic_Drop_NoContent(t *testing.T) {
	doc := chat.Document{
		Name:     "empty.txt",
		MimeType: "text/plain",
		Source:   chat.DocumentSource{},
	}

	blocks, err := convertDocument(t.Context(), doc, "")
	require.NoError(t, err)
	assert.Nil(t, blocks, "should be dropped when no inline content")
}

func TestConvertDocumentAnthropic_Drop_UnsupportedMIME(t *testing.T) {
	doc := chat.Document{
		Name:     "photo.jpg",
		MimeType: "image/jpeg",
		Source:   chat.DocumentSource{InlineData: minJPEG},
	}

	textOnlyCaps := modelcaps.CapsWith(false, false)
	blocks, err := convertDocumentWithCaps(t.Context(), doc, textOnlyCaps)
	require.NoError(t, err)
	assert.Nil(t, blocks, "image should be dropped for text-only model")
}
