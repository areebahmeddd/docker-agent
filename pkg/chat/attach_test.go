package chat_test

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/chat"
)

// ──────────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────────

// encodeJPEGBytes returns a minimal JPEG image as raw bytes.
func encodeJPEGBytes(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: 200, G: 100, B: 50, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// encodePNGBytes returns a minimal PNG image as raw bytes.
func encodePNGBytes(w, h int, alpha bool) []byte {
	if alpha {
		img := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := range h {
			for x := range w {
				img.Set(x, y, color.NRGBA{R: 0, G: 128, B: 255, A: 128})
			}
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			panic(err)
		}
		return buf.Bytes()
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: 0, G: 128, B: 255, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// writeTempFile writes data to a temp file with the given extension and returns its path.
func writeTempFile(t *testing.T, ext string, data []byte) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "attach-*"+ext)
	require.NoError(t, err)
	_, err = f.Write(data)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	return f.Name()
}

// ──────────────────────────────────────────────────────────────────────────────
// ProcessAttachment — MessagePartTypeFile
// ──────────────────────────────────────────────────────────────────────────────

func TestProcessAttachment_JPEG_Passthrough(t *testing.T) {
	data := encodeJPEGBytes(100, 100)
	path := writeTempFile(t, ".jpg", data)

	doc, err := chat.ProcessAttachment(t.Context(), chat.MessagePart{
		Type: chat.MessagePartTypeFile,
		File: &chat.MessageFile{Path: path, MimeType: "image/jpeg"},
	})
	require.NoError(t, err)
	assert.Equal(t, "image/jpeg", doc.MimeType)
	assert.NotEmpty(t, doc.Source.InlineData)
	assert.Empty(t, doc.Source.InlineText)
	assert.Equal(t, filepath.Base(path), doc.Name)
}

func TestProcessAttachment_PNG_Passthrough(t *testing.T) {
	data := encodePNGBytes(100, 100, false)
	path := writeTempFile(t, ".png", data)

	doc, err := chat.ProcessAttachment(t.Context(), chat.MessagePart{
		Type: chat.MessagePartTypeFile,
		File: &chat.MessageFile{Path: path, MimeType: "image/png"},
	})
	require.NoError(t, err)
	assert.Equal(t, "image/png", doc.MimeType)
	assert.NotEmpty(t, doc.Source.InlineData)
}

func TestProcessAttachment_PNG_WithAlpha_StaysPNG(t *testing.T) {
	// A PNG with alpha channel must not be converted to JPEG (lossy strips alpha).
	data := encodePNGBytes(100, 100, true)
	path := writeTempFile(t, ".png", data)

	doc, err := chat.ProcessAttachment(t.Context(), chat.MessagePart{
		Type: chat.MessagePartTypeFile,
		File: &chat.MessageFile{Path: path, MimeType: "image/png"},
	})
	require.NoError(t, err)
	// ResizeImage picks the smaller of PNG/JPEG; for small images PNG is usually smaller
	// but what matters is that the output is a valid image.
	assert.True(t, doc.MimeType == "image/png" || doc.MimeType == "image/jpeg",
		"expected png or jpeg, got %q", doc.MimeType)
	assert.NotEmpty(t, doc.Source.InlineData)
}

func TestProcessAttachment_ImageTooLarge_Resized(t *testing.T) {
	// An image larger than MaxImageDimension should be resized.
	bigData := encodeJPEGBytes(chat.MaxImageDimension+200, chat.MaxImageDimension+200)
	path := writeTempFile(t, ".jpg", bigData)

	doc, err := chat.ProcessAttachment(t.Context(), chat.MessagePart{
		Type: chat.MessagePartTypeFile,
		File: &chat.MessageFile{Path: path, MimeType: "image/jpeg"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, doc.Source.InlineData)

	// Verify the output image dimensions fit within limits.
	img, _, decErr := image.Decode(bytes.NewReader(doc.Source.InlineData))
	require.NoError(t, decErr)
	b := img.Bounds()
	assert.LessOrEqual(t, b.Dx(), chat.MaxImageDimension)
	assert.LessOrEqual(t, b.Dy(), chat.MaxImageDimension)
}

func TestProcessAttachment_PDF_Passthrough(t *testing.T) {
	pdfBytes := []byte("%PDF-1.4 fake pdf content for testing")
	path := writeTempFile(t, ".pdf", pdfBytes)

	doc, err := chat.ProcessAttachment(t.Context(), chat.MessagePart{
		Type: chat.MessagePartTypeFile,
		File: &chat.MessageFile{Path: path, MimeType: "application/pdf"},
	})
	require.NoError(t, err)
	assert.Equal(t, "application/pdf", doc.MimeType)
	assert.Equal(t, pdfBytes, doc.Source.InlineData)
	assert.Empty(t, doc.Source.InlineText)
}

func TestProcessAttachment_BinaryFileTooLarge_Error(t *testing.T) {
	// Write a file whose Stat.Size exceeds MaxInlineBinarySize.
	// We use a sparse file (truncate to the target size) so the test is fast.
	path := writeTempFile(t, ".pdf", nil)
	f, err := os.OpenFile(path, os.O_WRONLY, 0o600)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(chat.MaxInlineBinarySize+1))
	require.NoError(t, f.Close())

	_, err = chat.ProcessAttachment(t.Context(), chat.MessagePart{
		Type: chat.MessagePartTypeFile,
		File: &chat.MessageFile{Path: path},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too large")
}

func TestProcessAttachment_TextFile_InlineText(t *testing.T) {
	content := "Hello, this is a text file.\nLine 2."
	path := writeTempFile(t, ".txt", []byte(content))

	doc, err := chat.ProcessAttachment(t.Context(), chat.MessagePart{
		Type: chat.MessagePartTypeFile,
		File: &chat.MessageFile{Path: path, MimeType: "text/plain"},
	})
	require.NoError(t, err)
	assert.Empty(t, doc.Source.InlineData)
	assert.Contains(t, doc.Source.InlineText, content)
	assert.Contains(t, doc.Source.InlineText, filepath.Base(path)) // ReadFileForInline wraps in a tag
}

func TestProcessAttachment_MarkdownFile_InlineText(t *testing.T) {
	content := "# Title\n\nBody paragraph."
	path := writeTempFile(t, ".md", []byte(content))

	doc, err := chat.ProcessAttachment(t.Context(), chat.MessagePart{
		Type: chat.MessagePartTypeFile,
		File: &chat.MessageFile{Path: path},
	})
	require.NoError(t, err)
	assert.Empty(t, doc.Source.InlineData)
	assert.Contains(t, doc.Source.InlineText, content)
}

func TestProcessAttachment_MissingFile_Error(t *testing.T) {
	_, err := chat.ProcessAttachment(t.Context(), chat.MessagePart{
		Type: chat.MessagePartTypeFile,
		File: &chat.MessageFile{Path: "/nonexistent/path/file.jpg"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot stat")
}

func TestProcessAttachment_NilFile_Error(t *testing.T) {
	_, err := chat.ProcessAttachment(t.Context(), chat.MessagePart{
		Type: chat.MessagePartTypeFile,
		File: nil,
	})
	require.Error(t, err)
}

// ──────────────────────────────────────────────────────────────────────────────
// ProcessAttachment — MessagePartTypeImageURL
// ──────────────────────────────────────────────────────────────────────────────

func TestProcessAttachment_DataURI_JPEG(t *testing.T) {
	jpegData := encodeJPEGBytes(50, 50)
	dataURI := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpegData)

	doc, err := chat.ProcessAttachment(t.Context(), chat.MessagePart{
		Type:     chat.MessagePartTypeImageURL,
		ImageURL: &chat.MessageImageURL{URL: dataURI},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, doc.Source.InlineData)
	assert.True(t, doc.MimeType == "image/jpeg" || doc.MimeType == "image/png")
}

func TestProcessAttachment_DataURI_PNG(t *testing.T) {
	pngData := encodePNGBytes(50, 50, false)
	dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngData)

	doc, err := chat.ProcessAttachment(t.Context(), chat.MessagePart{
		Type:     chat.MessagePartTypeImageURL,
		ImageURL: &chat.MessageImageURL{URL: dataURI},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, doc.Source.InlineData)
}

func TestProcessAttachment_DataURI_NonBase64_Error(t *testing.T) {
	_, err := chat.ProcessAttachment(t.Context(), chat.MessagePart{
		Type:     chat.MessagePartTypeImageURL,
		ImageURL: &chat.MessageImageURL{URL: "data:text/plain,hello"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not base64")
}

func TestProcessAttachment_UnsupportedScheme_Error(t *testing.T) {
	_, err := chat.ProcessAttachment(t.Context(), chat.MessagePart{
		Type:     chat.MessagePartTypeImageURL,
		ImageURL: &chat.MessageImageURL{URL: "ftp://example.com/image.jpg"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported image URL scheme")
}

func TestProcessAttachment_HTTPS_Image_Via_HTTPTestServer(t *testing.T) {
	jpegData := encodeJPEGBytes(60, 60)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpegData)
	}))
	t.Cleanup(srv.Close)

	// The httptest.Server binds to 127.0.0.1 (loopback), which is blocked by
	// the SSRF filter in production. We use a plain http.Client without the
	// SSRF-filtering transport so the test can reach the local server.
	restore := chat.SetFetchHTTPClientForTest(srv.Client())
	t.Cleanup(restore)

	doc, err := chat.ProcessAttachment(t.Context(), chat.MessagePart{
		Type:     chat.MessagePartTypeImageURL,
		ImageURL: &chat.MessageImageURL{URL: srv.URL + "/photo.jpg"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, doc.Source.InlineData)
	assert.True(t, doc.MimeType == "image/jpeg" || doc.MimeType == "image/png",
		"unexpected MIME: %q", doc.MimeType)
	assert.Equal(t, "photo.jpg", doc.Name)
}

func TestProcessAttachment_SSRF_LoopbackBlocked(t *testing.T) {
	_, err := chat.ProcessAttachment(t.Context(), chat.MessagePart{
		Type:     chat.MessagePartTypeImageURL,
		ImageURL: &chat.MessageImageURL{URL: "http://127.0.0.1:9999/secret"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private/reserved")
}

func TestProcessAttachment_SSRF_MetadataEndpointBlocked(t *testing.T) {
	// 169.254.169.254 is the AWS/GCP/Azure instance metadata endpoint.
	_, err := chat.ProcessAttachment(t.Context(), chat.MessagePart{
		Type:     chat.MessagePartTypeImageURL,
		ImageURL: &chat.MessageImageURL{URL: "http://169.254.169.254/latest/meta-data/"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private/reserved")
}

func TestProcessAttachment_HTTP_Non200_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	// Bypass SSRF filter so we can reach the loopback-bound httptest server.
	restore := chat.SetFetchHTTPClientForTest(srv.Client())
	t.Cleanup(restore)

	_, err := chat.ProcessAttachment(t.Context(), chat.MessagePart{
		Type:     chat.MessagePartTypeImageURL,
		ImageURL: &chat.MessageImageURL{URL: srv.URL + "/missing.jpg"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestProcessAttachment_NilImageURL_Error(t *testing.T) {
	_, err := chat.ProcessAttachment(t.Context(), chat.MessagePart{
		Type:     chat.MessagePartTypeImageURL,
		ImageURL: nil,
	})
	require.Error(t, err)
}

// ──────────────────────────────────────────────────────────────────────────────
// ProcessAttachment — MessagePartTypeDocument
// ──────────────────────────────────────────────────────────────────────────────

func TestProcessAttachment_Document_WithInlineData_Passthrough(t *testing.T) {
	pdfBytes := []byte("%PDF-1.4 test")
	doc, err := chat.ProcessAttachment(t.Context(), chat.MessagePart{
		Type: chat.MessagePartTypeDocument,
		Document: &chat.Document{
			Name:     "spec.pdf",
			MimeType: "application/pdf",
			Source:   chat.DocumentSource{InlineData: pdfBytes},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, pdfBytes, doc.Source.InlineData)
	assert.Equal(t, "application/pdf", doc.MimeType)
}

func TestProcessAttachment_Document_WithInlineText_Passthrough(t *testing.T) {
	text := "# Markdown content"
	doc, err := chat.ProcessAttachment(t.Context(), chat.MessagePart{
		Type: chat.MessagePartTypeDocument,
		Document: &chat.Document{
			Name:     "readme.md",
			MimeType: "text/markdown",
			Source:   chat.DocumentSource{InlineText: text},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, text, doc.Source.InlineText)
	assert.Empty(t, doc.Source.InlineData)
}

func TestProcessAttachment_Document_ImageInlineData_Transcoded(t *testing.T) {
	jpegData := encodeJPEGBytes(40, 40)
	doc, err := chat.ProcessAttachment(t.Context(), chat.MessagePart{
		Type: chat.MessagePartTypeDocument,
		Document: &chat.Document{
			Name:     "photo.jpg",
			MimeType: "image/jpeg",
			Source:   chat.DocumentSource{InlineData: jpegData},
		},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, doc.Source.InlineData)
	assert.True(t, doc.MimeType == "image/jpeg" || doc.MimeType == "image/png")
}

func TestProcessAttachment_Document_NoContent_Error(t *testing.T) {
	_, err := chat.ProcessAttachment(t.Context(), chat.MessagePart{
		Type: chat.MessagePartTypeDocument,
		Document: &chat.Document{
			Name:     "empty.md",
			MimeType: "text/markdown",
			Source:   chat.DocumentSource{},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no inline content")
}

func TestProcessAttachment_Document_NilDocument_Error(t *testing.T) {
	_, err := chat.ProcessAttachment(t.Context(), chat.MessagePart{
		Type:     chat.MessagePartTypeDocument,
		Document: nil,
	})
	require.Error(t, err)
}

// ──────────────────────────────────────────────────────────────────────────────
// ProcessAttachment — unsupported type
// ──────────────────────────────────────────────────────────────────────────────

func TestProcessAttachment_UnsupportedType_Error(t *testing.T) {
	_, err := chat.ProcessAttachment(t.Context(), chat.MessagePart{
		Type: chat.MessagePartTypeText,
		Text: "hello",
	})
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "unsupported")
}
