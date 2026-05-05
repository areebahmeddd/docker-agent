package oaistream

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"

	"github.com/openai/openai-go/v3"

	"github.com/docker/docker-agent/pkg/attachment"
	"github.com/docker/docker-agent/pkg/attachment/modelcaps"
	"github.com/docker/docker-agent/pkg/chat"
)

// convertDocument converts a chat.Document to zero or more
// ChatCompletionContentPartUnionParam values using the OpenAI Chat Completions
// format.  It is also used by all oaistream-based providers (Mistral, xAI,
// Ollama, Nebius, MiniMax, GitHub Copilot, Azure, Requesty).
//
// Routing:
//   - image/* with InlineData → data-URI image part
//   - other binary MIMEs with InlineData → text part with TXTEnvelope fallback
//   - text MIMEs with InlineText → text part with TXTEnvelope
//   - unsupported / no content → nil (logged as warning)
func convertDocument(ctx context.Context, doc chat.Document, modelID string) ([]openai.ChatCompletionContentPartUnionParam, error) {
	mc, _ := modelcaps.Load(modelID)
	strategy, reason := attachment.Decide(doc, mc)

	switch strategy {
	case attachment.StrategyDrop:
		slog.WarnContext(ctx, "attachment dropped", "reason", reason, "doc", doc.Name)
		return nil, nil

	case attachment.StrategyB64:
		mime := strings.ToLower(doc.MimeType)
		if strings.HasPrefix(mime, "image/") {
			// Build an OpenAI image part with a data URI.
			dataURI := fmt.Sprintf("data:%s;base64,%s",
				doc.MimeType,
				base64.StdEncoding.EncodeToString(doc.Source.InlineData))
			return []openai.ChatCompletionContentPartUnionParam{
				openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{
					URL: dataURI,
				}),
			}, nil
		}
		// Non-image binary (PDF, Office docs…): OpenAI Chat Completions has no
		// native document block, so fall back to a TXT envelope.
		slog.DebugContext(ctx, "oaistream: no native block for MIME, falling back to TXT envelope",
			"mime", doc.MimeType, "doc", doc.Name)
		envelope := attachment.TXTEnvelope(doc.Name, doc.MimeType,
			base64.StdEncoding.EncodeToString(doc.Source.InlineData))
		return []openai.ChatCompletionContentPartUnionParam{
			openai.TextContentPart(envelope),
		}, nil

	case attachment.StrategyTXT:
		envelope := attachment.TXTEnvelope(doc.Name, doc.MimeType, doc.Source.InlineText)
		return []openai.ChatCompletionContentPartUnionParam{
			openai.TextContentPart(envelope),
		}, nil

	default:
		return nil, fmt.Errorf("unknown attachment strategy %d", strategy)
	}
}

// SupportedMIMETypesForModel returns the MIME types that the given model
// supports as document attachments, based on models.dev capability data.
// This implements the attachment.Advisor interface for oaistream-based clients.
func SupportedMIMETypesForModel(modelID string) []string {
	mc, _ := modelcaps.Load(modelID)
	var mimes []string
	// Always support text MIMEs.
	mimes = append(mimes, "text/plain", "text/markdown", "text/html", "text/csv")
	if mc.Supports("image/jpeg") {
		mimes = append(mimes, "image/jpeg", "image/png", "image/gif", "image/webp")
	}
	if mc.Supports("application/pdf") {
		mimes = append(mimes, "application/pdf")
	}
	return mimes
}
