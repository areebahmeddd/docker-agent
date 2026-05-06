package openai

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"

	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"

	"github.com/docker/docker-agent/pkg/attachment"
	"github.com/docker/docker-agent/pkg/attachment/modelcaps"
	"github.com/docker/docker-agent/pkg/chat"
)

// convertDocumentToResponseInput converts a chat.Document to zero or more
// ResponseInputContentUnionParam values for the OpenAI Responses API.
//
// Routing:
//   - image/* with InlineData → OfInputImage with a data URI
//   - other binary with InlineData → OfInputText with TXTEnvelope fallback
//   - text MIMEs with InlineText → OfInputText with TXTEnvelope
//   - unsupported / no content → nil (logged as warning)
func convertDocumentToResponseInput(ctx context.Context, doc chat.Document, modelID string) ([]responses.ResponseInputContentUnionParam, error) {
	mc, _ := modelcaps.Load(modelID)
	return convertDocumentToResponseInputWithCaps(ctx, doc, mc)
}

// convertDocumentToResponseInputWithCaps is the caps-injectable variant used by tests.
func convertDocumentToResponseInputWithCaps(ctx context.Context, doc chat.Document, mc modelcaps.ModelCapabilities) ([]responses.ResponseInputContentUnionParam, error) {
	strategy, reason := attachment.Decide(doc, mc)

	switch strategy {
	case attachment.StrategyDrop:
		slog.WarnContext(ctx, "attachment dropped", "reason", reason, "doc", doc.Name)
		return nil, nil

	case attachment.StrategyB64:
		mime := strings.ToLower(doc.MimeType)
		if strings.HasPrefix(mime, "image/") {
			dataURI := fmt.Sprintf("data:%s;base64,%s",
				doc.MimeType,
				base64.StdEncoding.EncodeToString(doc.Source.InlineData))
			return []responses.ResponseInputContentUnionParam{
				{
					OfInputImage: &responses.ResponseInputImageParam{
						ImageURL: param.NewOpt(dataURI),
						Detail:   responses.ResponseInputImageDetail(responses.ResponseInputImageContentDetailAuto),
					},
				},
			}, nil
		}
		// Non-image binary: no native document block in the Responses API.
		slog.DebugContext(ctx, "openai responses: no native block for MIME, falling back to TXT envelope",
			"mime", doc.MimeType, "doc", doc.Name)
		envelope := attachment.TXTEnvelope(doc.Name, doc.MimeType,
			base64.StdEncoding.EncodeToString(doc.Source.InlineData))
		return []responses.ResponseInputContentUnionParam{
			{OfInputText: &responses.ResponseInputTextParam{Text: envelope}},
		}, nil

	case attachment.StrategyTXT:
		envelope := attachment.TXTEnvelope(doc.Name, doc.MimeType, doc.Source.InlineText)
		return []responses.ResponseInputContentUnionParam{
			{OfInputText: &responses.ResponseInputTextParam{Text: envelope}},
		}, nil

	default:
		return nil, fmt.Errorf("unknown attachment strategy %d", strategy)
	}
}
