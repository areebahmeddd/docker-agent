package chat

// Package-level attach-time processing pipeline.
//
// The attach-time pipeline runs once when a user adds a file or image to a
// message. It produces a fully-resolved [Document] whose Source.InlineData or
// Source.InlineText is populated. Provider-level convertDocument functions then
// consume the Document at inference time — they never perform I/O or resizing.
//
//	producer (app.go / runner.go)
//	  └─ ProcessAttachment(ctx, part) → Document
//	       └─ session.UserMessage(…, MessagePart{Type: MessagePartTypeDocument, Document: &doc})
//	            └─ per-provider convertDocument(ctx, doc, modelID) → wire format

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// attachHTTPTimeout is the maximum time allowed to fetch a remote image URL.
	attachHTTPTimeout = 10 * time.Second

	// attachMaxRemoteBytes is the maximum number of bytes read from a remote URL.
	attachMaxRemoteBytes = 20 * 1024 * 1024 // 20 MB

	// MaxInlineBinarySize is the maximum byte size of a binary file (PDF, etc.)
	// that will be read into memory for inline attachment. Matches the remote
	// fetch cap so local and remote paths behave consistently.
	MaxInlineBinarySize = 20 * 1024 * 1024 // 20 MB
)

// privateIPNets lists address ranges that must not be dialled by the
// attach-time URL fetcher. Blocking these prevents SSRF attacks against
// cloud metadata services, internal APIs, and loopback services.
var privateIPNets = func() []*net.IPNet {
	blocks := []string{
		// Loopback
		"127.0.0.0/8",
		"::1/128",
		// Link-local — covers AWS/GCP/Azure metadata endpoints (169.254.169.254)
		"169.254.0.0/16",
		"fe80::/10",
		// RFC-1918 private ranges
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		// IPv6 unique-local
		"fc00::/7",
	}
	nets := make([]*net.IPNet, 0, len(blocks))
	for _, b := range blocks {
		_, ipNet, err := net.ParseCIDR(b)
		if err != nil {
			panic("attachment: invalid built-in CIDR " + b + ": " + err.Error())
		}
		nets = append(nets, ipNet)
	}
	return nets
}()

// isPrivateIP reports whether ip falls in any of the blocked address ranges.
func isPrivateIP(ip net.IP) bool {
	for _, block := range privateIPNets {
		if block.Contains(ip) {
			return true
		}
	}
	return false
}

// checkSafeHostCtx resolves host to IP addresses and returns an error if any
// resolved address is in a private/reserved range. This is called both at
// dial time (where a context is available) and on redirect destinations.
func checkSafeHostCtx(ctx context.Context, host string) error {
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return fmt.Errorf("attachment: cannot resolve host %q: %w", host, err)
	}
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip == nil {
			continue
		}
		if isPrivateIP(ip) {
			return fmt.Errorf("attachment: URL resolves to private/reserved address %s (SSRF protection)", addr)
		}
	}
	return nil
}

// safeHTTPClient is a shared HTTP client used by fetchRemoteImage.
// It enforces SSRF protection by refusing connections to private and
// reserved IP ranges at both dial time and on each redirect hop.
//
// Tests may override this variable to inject a custom client that bypasses
// SSRF filtering (e.g. to reach httptest.Server on loopback). Only do this
// in test code guarded by a t.Cleanup restore.
var safeHTTPClient = newSafeHTTPClient()

func newSafeHTTPClient() *http.Client {
	return &http.Client{
		Timeout: attachHTTPTimeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, _, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, fmt.Errorf("attachment: malformed dial address %q: %w", addr, err)
				}
				if err := checkSafeHostCtx(ctx, host); err != nil {
					return nil, err
				}
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			},
		},
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			return checkSafeHostCtx(req.Context(), req.URL.Hostname())
		},
	}
}

// SetFetchHTTPClientForTest replaces the HTTP client used by fetchRemoteImage
// and returns a restore function. Only for use in tests.
//
//	defer chat.SetFetchHTTPClientForTest(t, myClient)()
func SetFetchHTTPClientForTest(client *http.Client) (restore func()) {
	prev := safeHTTPClient
	safeHTTPClient = client
	return func() { safeHTTPClient = prev }
}

// ProcessAttachment converts a raw [MessagePart] into a [Document] with fully
// resolved Source.InlineData or Source.InlineText. It is called once when a
// message is assembled — never at inference time.
//
// Supported input types:
//
//   - [MessagePartTypeFile]: reads the file from the local filesystem, detects
//     its MIME type, and either inlines text content (text/* files) or reads
//     binary bytes (images are transcoded+resized via [ResizeImage]; PDFs and
//     other supported types are read verbatim).
//
//   - [MessagePartTypeImageURL]: handles data: URIs (decoded inline) and
//     http(s):// URLs (fetched with a 10-second timeout). The image bytes are
//     then passed through [ResizeImage] to normalise to JPEG or PNG.
//
//   - [MessagePartTypeDocument]: if Source.InlineData or Source.InlineText is
//     already set, the document is returned as-is after applying image
//     transcoding to any image/* InlineData. A Document with no inline content
//     is an error.
//
// The context is forwarded to any network operations; filesystem and image
// decoding operations are not yet context-aware.
func ProcessAttachment(ctx context.Context, part MessagePart) (Document, error) {
	doc, _, err := ProcessAttachmentWithMetadata(ctx, part)
	return doc, err
}

// ProcessAttachmentWithMetadata is like [ProcessAttachment] but also returns
// the [ImageResizeResult] when the attachment was an image that went through
// [ResizeImage]. The metadata is nil for non-image attachments.
//
// Callers that need to emit a dimension note (for model coordinate-mapping)
// should use this variant and call [FormatDimensionNote] on the returned
// metadata.
func ProcessAttachmentWithMetadata(ctx context.Context, part MessagePart) (Document, *ImageResizeResult, error) {
	switch part.Type {
	case MessagePartTypeFile:
		return processFilePart(part)
	case MessagePartTypeImageURL:
		return processImageURLPart(ctx, part)
	case MessagePartTypeDocument:
		return processDocumentPart(part)
	default:
		return Document{}, nil, fmt.Errorf("ProcessAttachment: unsupported part type %q", part.Type)
	}
}

// processFilePart handles MessagePartTypeFile: reads from disk, detects MIME,
// routes to text-inline or binary-inline as appropriate.
func processFilePart(part MessagePart) (Document, *ImageResizeResult, error) {
	if part.File == nil {
		return Document{}, nil, errors.New("ProcessAttachment: file part has nil File field")
	}
	absPath := part.File.Path
	name := filepath.Base(absPath)

	fi, err := os.Stat(absPath)
	if err != nil {
		return Document{}, nil, fmt.Errorf("ProcessAttachment: cannot stat %q: %w", absPath, err)
	}
	if !fi.Mode().IsRegular() {
		return Document{}, nil, fmt.Errorf("ProcessAttachment: %q is not a regular file", absPath)
	}

	mimeType := DetectMimeType(absPath)

	// Route by MIME type. Note: MIME-type check must precede IsTextFile because
	// some binary formats (e.g. PDF) may pass the text heuristic when the file
	// content happens to be printable ASCII.
	switch {
	case IsImageMimeType(mimeType):
		if fi.Size() > MaxInlineBinarySize {
			return Document{}, nil, fmt.Errorf("ProcessAttachment: image file %q too large to inline (%d bytes, max %d)", absPath, fi.Size(), MaxInlineBinarySize)
		}
		data, err := os.ReadFile(absPath)
		if err != nil {
			return Document{}, nil, fmt.Errorf("ProcessAttachment: read image %q: %w", absPath, err)
		}
		return transcodeImageWithMeta(name, data, mimeType)

	case mimeType == "application/pdf" || (IsSupportedMimeType(mimeType) && !IsTextFile(absPath)):
		// PDF and other supported binary types — read verbatim.
		// The !IsTextFile guard ensures that binary formats whose extension
		// is unknown but content is ASCII-printable are not incorrectly inlined.
		if fi.Size() > MaxInlineBinarySize {
			return Document{}, nil, fmt.Errorf("ProcessAttachment: binary file %q too large to inline (%d bytes, max %d)", absPath, fi.Size(), MaxInlineBinarySize)
		}
		data, err := os.ReadFile(absPath)
		if err != nil {
			return Document{}, nil, fmt.Errorf("ProcessAttachment: read binary file %q: %w", absPath, err)
		}
		return Document{
			Name:     name,
			MimeType: mimeType,
			Size:     int64(len(data)),
			Source:   DocumentSource{InlineData: data},
		}, nil, nil

	case IsTextFile(absPath):
		if fi.Size() > MaxInlineFileSize {
			return Document{}, nil, fmt.Errorf("ProcessAttachment: text file %q too large to inline (%d bytes, max %d)", absPath, fi.Size(), MaxInlineFileSize)
		}
		content, err := ReadFileForInline(absPath)
		if err != nil {
			return Document{}, nil, fmt.Errorf("ProcessAttachment: read text file %q: %w", absPath, err)
		}
		return Document{
			Name:     name,
			MimeType: mimeType,
			Size:     fi.Size(),
			Source:   DocumentSource{InlineText: content},
		}, nil, nil

	default:
		// Unknown binary — read verbatim and let modelcaps gate it at inference time.
		if fi.Size() > MaxInlineBinarySize {
			return Document{}, nil, fmt.Errorf("ProcessAttachment: file %q too large to inline (%d bytes, max %d)", absPath, fi.Size(), MaxInlineBinarySize)
		}
		data, err := os.ReadFile(absPath)
		if err != nil {
			return Document{}, nil, fmt.Errorf("ProcessAttachment: read file %q: %w", absPath, err)
		}
		return Document{
			Name:     name,
			MimeType: mimeType,
			Size:     int64(len(data)),
			Source:   DocumentSource{InlineData: data},
		}, nil, nil
	}
}

// processImageURLPart handles MessagePartTypeImageURL.
// Supports:
//   - data: URIs (data:<mime>;base64,<payload>)
//   - http:// and https:// URLs (fetched with attachHTTPTimeout)
func processImageURLPart(ctx context.Context, part MessagePart) (Document, *ImageResizeResult, error) {
	if part.ImageURL == nil {
		return Document{}, nil, errors.New("ProcessAttachment: image-url part has nil ImageURL field")
	}
	rawURL := part.ImageURL.URL

	var (
		data     []byte
		mimeType string
		name     string
		err      error
	)

	switch {
	case strings.HasPrefix(rawURL, "data:"):
		mimeType, data, err = parseDataURI(rawURL)
		if err != nil {
			return Document{}, nil, fmt.Errorf("ProcessAttachment: parse data URI: %w", err)
		}
		name = "image"

	case strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://"):
		data, mimeType, name, err = fetchRemoteImage(ctx, rawURL)
		if err != nil {
			return Document{}, nil, fmt.Errorf("ProcessAttachment: fetch remote image %q: %w", rawURL, err)
		}

	default:
		return Document{}, nil, fmt.Errorf("ProcessAttachment: unsupported image URL scheme (must be data: or http(s)://): %q", rawURL)
	}

	// When content detection returns an image type, prefer it over whatever
	// the URI or Content-Type header said. (Only image types are trusted from
	// the sniffer to avoid promoting an unknown binary to a valid MIME.)
	if detected := DetectMimeTypeByContent(data); IsImageMimeType(detected) {
		mimeType = detected
	}

	return transcodeImageWithMeta(name, data, mimeType)
}

// processDocumentPart handles MessagePartTypeDocument.
// Images with InlineData are transcoded; other already-resolved documents pass through.
func processDocumentPart(part MessagePart) (Document, *ImageResizeResult, error) {
	if part.Document == nil {
		return Document{}, nil, errors.New("ProcessAttachment: document part has nil Document field")
	}
	doc := *part.Document

	if len(doc.Source.InlineData) > 0 {
		if IsImageMimeType(doc.MimeType) {
			// Apply transcoding/resizing to normalise the image.
			return transcodeImageWithMeta(doc.Name, doc.Source.InlineData, doc.MimeType)
		}
		// Non-image binary — pass through unchanged.
		return doc, nil, nil
	}

	if doc.Source.InlineText != "" {
		// Already resolved text — pass through unchanged.
		return doc, nil, nil
	}

	return Document{}, nil, fmt.Errorf("ProcessAttachment: document %q has no inline content (InlineData and InlineText are both empty)", doc.Name)
}

// transcodeImageWithMeta runs bytes through ResizeImage to normalise the image
// to JPEG or PNG within provider limits, then wraps the result in a Document.
// Returns the [ImageResizeResult] so callers can emit dimension notes.
func transcodeImageWithMeta(name string, data []byte, mimeType string) (Document, *ImageResizeResult, error) {
	result, err := ResizeImage(data, mimeType)
	if err != nil {
		return Document{}, nil, fmt.Errorf("ProcessAttachment: transcode image %q: %w", name, err)
	}
	doc := Document{
		Name:     name,
		MimeType: result.MimeType,
		Size:     int64(len(result.Data)),
		Source:   DocumentSource{InlineData: result.Data},
	}
	return doc, result, nil
}

// parseDataURI parses a data URI of the form "data:<mime>;base64,<payload>".
// Returns the MIME type and decoded bytes.
func parseDataURI(uri string) (mimeType string, data []byte, err error) {
	rest, ok := strings.CutPrefix(uri, "data:")
	if !ok {
		return "", nil, errors.New("not a data URI")
	}

	header, payload, ok := strings.Cut(rest, ",")
	if !ok {
		return "", nil, errors.New("data URI missing comma separator")
	}

	// Header is "<mime>[;charset=…];base64" or "<mime>" (plain text, unsupported here).
	if !strings.HasSuffix(header, ";base64") {
		return "", nil, errors.New("data URI is not base64-encoded (only base64 data URIs are supported)")
	}
	mimeType = strings.TrimSuffix(header, ";base64")

	// Strip any charset parameter (e.g. "image/png;charset=utf-8;base64" → "image/png").
	if idx := strings.Index(mimeType, ";"); idx >= 0 {
		mimeType = mimeType[:idx]
	}
	mimeType = strings.TrimSpace(mimeType)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	data, err = base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", nil, fmt.Errorf("base64 decode: %w", err)
	}
	return mimeType, data, nil
}

// fetchRemoteImage fetches an image from an http(s) URL.
// The safeHTTPClient enforces a timeout and blocks connections to
// private/reserved IP ranges (SSRF protection).
// Returns the image bytes, detected MIME type, and a filename derived from the URL.
func fetchRemoteImage(ctx context.Context, rawURL string) (data []byte, mimeType, name string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return nil, "", "", fmt.Errorf("create request: %w", err)
	}

	resp, err := safeHTTPClient.Do(req)
	if err != nil {
		return nil, "", "", fmt.Errorf("HTTP GET: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", "", fmt.Errorf("HTTP %d for %q", resp.StatusCode, rawURL)
	}

	data, err = io.ReadAll(io.LimitReader(resp.Body, attachMaxRemoteBytes+1))
	if err != nil {
		return nil, "", "", fmt.Errorf("read response body: %w", err)
	}
	if int64(len(data)) > attachMaxRemoteBytes {
		return nil, "", "", fmt.Errorf("remote image too large (max %d bytes)", attachMaxRemoteBytes)
	}

	// Determine MIME type: prefer Content-Type header, fall back to content sniff.
	mimeType = resp.Header.Get("Content-Type")
	if ct, _, parseErr := mime.ParseMediaType(mimeType); parseErr == nil {
		mimeType = ct
	}
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = DetectMimeTypeByContent(data)
	}

	// Derive a filename from the URL path.
	name = filepath.Base(req.URL.Path)
	if name == "" || name == "." || name == "/" {
		name = "image"
	}

	return data, mimeType, name, nil
}
