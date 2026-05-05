package bedrock

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/docker/docker-agent/pkg/attachment"
	"github.com/docker/docker-agent/pkg/attachment/modelcaps"
	"github.com/docker/docker-agent/pkg/chat"
)

// imageFormatFromMIME maps a MIME type to a Bedrock ImageFormat.
// Returns ("", false) when the MIME type is not a supported image.
func imageFormatFromMIME(mimeType string) (types.ImageFormat, bool) {
	switch strings.ToLower(mimeType) {
	case "image/jpeg":
		return types.ImageFormatJpeg, true
	case "image/png":
		return types.ImageFormatPng, true
	case "image/gif":
		return types.ImageFormatGif, true
	case "image/webp":
		return types.ImageFormatWebp, true
	default:
		return "", false
	}
}

// convertDocument converts a chat.Document to zero or more Bedrock ContentBlocks.
//
// Routing:
//   - image/* with InlineData → ContentBlockMemberImage
//   - application/pdf with InlineData → ContentBlockMemberDocument
//   - text with InlineText → ContentBlockMemberText with TXTEnvelope
//   - other binary → ContentBlockMemberText with TXTEnvelope fallback
//   - unsupported / no content → nil (logged as warning)
func convertDocument(ctx context.Context, doc chat.Document, modelID string) ([]types.ContentBlock, error) {
	mc, _ := modelcaps.Load(modelID)
	strategy, reason := attachment.Decide(doc, mc)

	switch strategy {
	case attachment.StrategyDrop:
		slog.WarnContext(ctx, "attachment dropped", "reason", reason, "doc", doc.Name)
		return nil, nil

	case attachment.StrategyB64:
		mime := strings.ToLower(doc.MimeType)

		// Native image block
		if format, ok := imageFormatFromMIME(mime); ok {
			return []types.ContentBlock{
				&types.ContentBlockMemberImage{
					Value: types.ImageBlock{
						Format: format,
						Source: &types.ImageSourceMemberBytes{
							Value: doc.Source.InlineData,
						},
					},
				},
			}, nil
		}

		// Native PDF/document block
		if mime == "application/pdf" {
			return []types.ContentBlock{
				&types.ContentBlockMemberDocument{
					Value: types.DocumentBlock{
						Format: types.DocumentFormatPdf,
						Name:   aws.String(sanitizeDocumentName(doc.Name)),
						Source: &types.DocumentSourceMemberBytes{
							Value: doc.Source.InlineData,
						},
					},
				},
			}, nil
		}

		// Other Office docs
		if df, ok := officeDocumentFormat(mime); ok {
			return []types.ContentBlock{
				&types.ContentBlockMemberDocument{
					Value: types.DocumentBlock{
						Format: df,
						Name:   aws.String(sanitizeDocumentName(doc.Name)),
						Source: &types.DocumentSourceMemberBytes{
							Value: doc.Source.InlineData,
						},
					},
				},
			}, nil
		}

		// Unknown binary: TXT envelope fallback
		slog.DebugContext(ctx, "bedrock: no native block for MIME, falling back to TXT envelope",
			"mime", doc.MimeType, "doc", doc.Name)
		envelope := attachment.TXTEnvelope(doc.Name, doc.MimeType,
			fmt.Sprintf("[binary content, %d bytes]", len(doc.Source.InlineData)))
		return []types.ContentBlock{
			&types.ContentBlockMemberText{Value: envelope},
		}, nil

	case attachment.StrategyTXT:
		envelope := attachment.TXTEnvelope(doc.Name, doc.MimeType, doc.Source.InlineText)
		return []types.ContentBlock{
			&types.ContentBlockMemberText{Value: envelope},
		}, nil

	default:
		return nil, fmt.Errorf("unknown attachment strategy %d", strategy)
	}
}

// officeDocumentFormat maps Office MIME types to Bedrock DocumentFormats.
func officeDocumentFormat(mime string) (types.DocumentFormat, bool) {
	switch mime {
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return types.DocumentFormatDocx, true
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return types.DocumentFormatXlsx, true
	case "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		// Bedrock doesn't have a native PPTX format — treat as unsupported.
		return "", false
	case "text/csv":
		return types.DocumentFormatCsv, true
	case "text/plain":
		return types.DocumentFormatTxt, true
	case "text/html":
		return types.DocumentFormatHtml, true
	case "text/markdown", "text/x-markdown":
		return types.DocumentFormatMd, true
	default:
		return "", false
	}
}

// sanitizeDocumentName replaces characters that Bedrock disallows in document names.
// Bedrock document names must be alphanumeric + hyphens only.
func sanitizeDocumentName(name string) string {
	var sb strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			sb.WriteRune(r)
		} else {
			sb.WriteRune('-')
		}
	}
	result := sb.String()
	if result == "" {
		return "document"
	}
	return result
}

// SupportedMIMETypes implements attachment.Advisor for the Bedrock client.
func (c *Client) SupportedMIMETypes() []string {
	mc, _ := modelcaps.Load(c.ModelConfig.Model)
	mimes := []string{
		"text/plain", "text/markdown", "text/html", "text/csv",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation",
	}
	if mc.Supports("image/jpeg") {
		mimes = append(mimes, "image/jpeg", "image/png", "image/gif", "image/webp")
	}
	if mc.Supports("application/pdf") {
		mimes = append(mimes, "application/pdf")
	}
	return mimes
}
