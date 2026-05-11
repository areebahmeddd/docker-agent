package anthropic

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/docker/docker-agent/pkg/attachment"
	"github.com/docker/docker-agent/pkg/attachment/modelcaps"
	"github.com/docker/docker-agent/pkg/chat"
	"github.com/docker/docker-agent/pkg/modelsdev"
)

// convertDocument converts a chat.Document to standard Anthropic SDK content blocks
// (not the Beta API).
//
// Routing:
//   - image/* with InlineData → ImageBlockParam (base64 source)
//   - application/pdf with InlineData → DocumentBlockParam (base64)
//   - text with InlineText → TextBlockParam with TXTEnvelope
//   - unsupported / no content → nil (logged as warning)
func convertDocument(ctx context.Context, doc chat.Document, modelID string) ([]anthropic.ContentBlockParamUnion, error) {
	store, err := modelsdev.NewStore()
	if err != nil {
		// Fall back to conservative text-only caps when the store is unavailable.
		return convertDocumentWithCaps(ctx, doc, modelcaps.ModelCapabilities{})
	}
	return convertDocumentFromStore(ctx, doc, modelID, store)
}

// convertDocumentFromStore is the store-injectable variant of convertDocument,
// used by tests to inject a fake in-memory modelsdev.Store.
func convertDocumentFromStore(ctx context.Context, doc chat.Document, modelID string, store *modelsdev.Store) ([]anthropic.ContentBlockParamUnion, error) {
	mc := modelcaps.LoadFromStore(store, modelID)
	return convertDocumentWithCaps(ctx, doc, mc)
}

// convertDocumentWithCaps is the caps-injectable variant used by tests.
func convertDocumentWithCaps(ctx context.Context, doc chat.Document, mc modelcaps.ModelCapabilities) ([]anthropic.ContentBlockParamUnion, error) {
	strategy, reason := attachment.Decide(doc, mc)

	switch strategy {
	case attachment.StrategyDrop:
		slog.WarnContext(ctx, "attachment dropped", "reason", reason, "doc", doc.Name)
		return nil, nil

	case attachment.StrategyB64:
		mime := strings.ToLower(doc.MimeType)
		b64Data := base64.StdEncoding.EncodeToString(doc.Source.InlineData)

		if IsImageMime(mime) {
			return []anthropic.ContentBlockParamUnion{
				{
					OfImage: &anthropic.ImageBlockParam{
						Source: anthropic.ImageBlockParamSourceUnion{
							OfBase64: &anthropic.Base64ImageSourceParam{
								Data:      b64Data,
								MediaType: anthropic.Base64ImageSourceMediaType(mime),
							},
						},
					},
				},
			}, nil
		}

		// Gate PDF block strictly on application/pdf — IsAnthropicDocumentMime also
		// matches text/plain, which must NOT be sent as a DocumentBlockParam.
		if mime == "application/pdf" {
			return []anthropic.ContentBlockParamUnion{
				{
					OfDocument: &anthropic.DocumentBlockParam{
						Source: anthropic.DocumentBlockParamSourceUnion{
							OfBase64: &anthropic.Base64PDFSourceParam{
								Data:      b64Data,
								MediaType: "application/pdf",
							},
						},
					},
				},
			}, nil
		}

		// Unexpected binary MIME — defensive drop.
		slog.WarnContext(ctx, "anthropic: unexpected binary MIME in StrategyB64, dropping",
			"mime", doc.MimeType, "doc", doc.Name)
		return nil, nil

	case attachment.StrategyTXT:
		envelope := attachment.TXTEnvelope(doc.Name, doc.MimeType, doc.Source.InlineText)
		return []anthropic.ContentBlockParamUnion{
			{OfText: &anthropic.TextBlockParam{Text: envelope}},
		}, nil

	default:
		return nil, fmt.Errorf("unknown attachment strategy %d", strategy)
	}
}
