package anthropic

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/docker/docker-agent/pkg/attachment"
	"github.com/docker/docker-agent/pkg/attachment/modelcaps"
	"github.com/docker/docker-agent/pkg/chat"
)

// convertDocument converts a chat.Document to Anthropic Beta API content blocks.
//
// Routing:
//   - image/* with InlineData → BetaImageBlockParam (base64 source)
//   - application/pdf with InlineData → BetaRequestDocumentBlock (base64)
//   - text with InlineText → BetaTextBlockParam with TXTEnvelope
//   - unsupported / no content → nil (logged as warning)
func convertDocument(ctx context.Context, doc chat.Document, modelID string) ([]anthropicsdk.BetaContentBlockParamUnion, error) {
	mc, _ := modelcaps.Load(modelID)
	strategy, reason := attachment.Decide(doc, mc)

	switch strategy {
	case attachment.StrategyDrop:
		slog.WarnContext(ctx, "attachment dropped", "reason", reason, "doc", doc.Name)
		return nil, nil

	case attachment.StrategyB64:
		mime := strings.ToLower(doc.MimeType)
		b64Data := base64.StdEncoding.EncodeToString(doc.Source.InlineData)

		if IsImageMime(mime) {
			return []anthropicsdk.BetaContentBlockParamUnion{
				{
					OfImage: &anthropicsdk.BetaImageBlockParam{
						Source: anthropicsdk.BetaImageBlockParamSourceUnion{
							OfBase64: &anthropicsdk.BetaBase64ImageSourceParam{
								Data:      b64Data,
								MediaType: anthropicsdk.BetaBase64ImageSourceMediaType(mime),
							},
						},
					},
				},
			}, nil
		}

		if IsAnthropicDocumentMime(mime) {
			// application/pdf → native document block
			return []anthropicsdk.BetaContentBlockParamUnion{
				{
					OfDocument: &anthropicsdk.BetaRequestDocumentBlockParam{
						Source: anthropicsdk.BetaRequestDocumentBlockSourceUnionParam{
							OfBase64: &anthropicsdk.BetaBase64PDFSourceParam{
								Data:      b64Data,
								MediaType: "application/pdf",
							},
						},
					},
				},
			}, nil
		}

		// Other binary: fall back to TXT envelope.
		slog.DebugContext(ctx, "anthropic: no native block for MIME, falling back to TXT envelope",
			"mime", doc.MimeType, "doc", doc.Name)
		envelope := attachment.TXTEnvelope(doc.Name, doc.MimeType, b64Data)
		return []anthropicsdk.BetaContentBlockParamUnion{
			{OfText: &anthropicsdk.BetaTextBlockParam{Text: envelope}},
		}, nil

	case attachment.StrategyTXT:
		envelope := attachment.TXTEnvelope(doc.Name, doc.MimeType, doc.Source.InlineText)
		return []anthropicsdk.BetaContentBlockParamUnion{
			{OfText: &anthropicsdk.BetaTextBlockParam{Text: envelope}},
		}, nil

	default:
		return nil, fmt.Errorf("unknown attachment strategy %d", strategy)
	}
}

// SupportedMIMETypes implements attachment.Advisor for the Anthropic client.
func (c *Client) SupportedMIMETypes() []string {
	mc, _ := modelcaps.Load(c.ModelConfig.Model)
	mimes := []string{"text/plain", "text/markdown", "text/html", "text/csv"}
	if mc.Supports("image/jpeg") {
		mimes = append(mimes, "image/jpeg", "image/png", "image/gif", "image/webp")
	}
	if mc.Supports("application/pdf") {
		mimes = append(mimes, "application/pdf")
	}
	return mimes
}
