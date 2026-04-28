package runtime

import (
	"context"
	"log/slog"
	"slices"

	"github.com/docker/docker-agent/pkg/chat"
	"github.com/docker/docker-agent/pkg/hooks"
)

// BuiltinStripUnsupportedModalities is the name of the runtime-shipped
// before_llm_call message transform that drops image content from the
// outgoing messages when the agent's current model doesn't list image
// in its input modalities. It's the runtime-shipped peer of
// [BuiltinCacheResponse] (a stop hook) — the constant exists mostly
// for log filtering and diagnostics.
//
// Sending images to a text-only model produces hard provider errors
// (HTTP 400 from OpenAI, "image input is not supported" from
// Anthropic text variants, etc.); promoting the strip into a
// registered transform replaces an inline branch in runStreamLoop and
// opens the door to a family of message-mutating transforms
// (redactors, scrubbers, ...).
const BuiltinStripUnsupportedModalities = "strip_unsupported_modalities"

// modalityImage is the canonical models.dev modality name for image
// input. A constant instead of a literal so a typo trips a compile
// error and the contract with [modelsdev.Modalities.Input] is
// discoverable from the runtime side.
const modalityImage = "image"

// stripUnsupportedModalitiesTransform is the [MessageTransform]
// registered under [BuiltinStripUnsupportedModalities]. It resolves
// the agent (and therefore its current model) from
// [hooks.Input.AgentName], looks up the model's input modalities, and
// applies [stripImageContent] when image is missing from the list.
//
// The transform is a no-op for every "we don't know enough to act"
// case (missing agent, missing model, models.dev miss, empty
// modalities, image already supported): erring on the side of "send
// the messages as-is" matches the previous inline behavior in
// runStreamLoop, where an unknown model also fell through.
func (r *LocalRuntime) stripUnsupportedModalitiesTransform(
	ctx context.Context,
	in *hooks.Input,
	msgs []chat.Message,
) ([]chat.Message, error) {
	if in == nil || in.AgentName == "" {
		return msgs, nil
	}
	a, err := r.team.Agent(in.AgentName)
	if err != nil || a == nil || a.Model() == nil {
		return msgs, nil
	}
	m, err := r.modelsStore.GetModel(ctx, a.Model().ID())
	if err != nil || m == nil {
		// Unknown model: keep the previous (inline) behavior of
		// passing messages through untouched. The model call will
		// surface any modality mismatch as a provider error.
		return msgs, nil
	}
	if len(m.Modalities.Input) == 0 || slices.Contains(m.Modalities.Input, modalityImage) {
		return msgs, nil
	}
	return stripImageContent(msgs), nil
}

// stripImageContent returns a copy of messages with all image-related
// content removed. Text content is preserved; image parts in
// [chat.Message.MultiContent] are filtered out, and file attachments
// with image MIME types are dropped.
//
// Lives next to [stripUnsupportedModalitiesTransform] (rather than in
// streaming.go where it originated) so the builtin's storage,
// transform, and helper are co-located. Kept as an unexported helper
// because the only legitimate caller is the transform itself — direct
// use bypasses the modality check.
func stripImageContent(messages []chat.Message) []chat.Message {
	result := make([]chat.Message, len(messages))
	for i, msg := range messages {
		result[i] = msg

		if len(msg.MultiContent) == 0 {
			continue
		}

		var filtered []chat.MessagePart
		for _, part := range msg.MultiContent {
			switch part.Type {
			case chat.MessagePartTypeImageURL:
				// Drop image URL parts entirely.
				continue
			case chat.MessagePartTypeFile:
				// Drop file parts that are images.
				if part.File != nil && chat.IsImageMimeType(part.File.MimeType) {
					continue
				}
			}
			filtered = append(filtered, part)
		}

		if len(filtered) != len(msg.MultiContent) {
			result[i].MultiContent = filtered
			slog.Debug("Stripped image content from message",
				"role", msg.Role,
				"original_parts", len(msg.MultiContent),
				"remaining_parts", len(filtered))
		}
	}
	return result
}
