// Package modelcaps provides model capability queries for the attachment system.
// It translates models.dev modality information into MIME-type support decisions
// used by the attachment routing logic.
package modelcaps

import (
	"context"
	"strings"

	"github.com/docker/docker-agent/pkg/modelsdev"
)

// ModelCapabilities describes what MIME types a given model can accept as
// document attachments.
type ModelCapabilities struct {
	// supportsImage is true when the model accepts image/* MIME types.
	supportsImage bool
	// supportsPDF is true when the model accepts application/pdf.
	supportsPDF bool
	// modelFound is false when models.dev has no record for this model,
	// which causes conservative fallback behaviour (text-only).
	modelFound bool
}

// Supports returns true when the model can accept an attachment with the given
// MIME type.
//
// Resolution rules (in order):
//  1. image/* → requires supportsImage
//  2. application/pdf → requires supportsPDF
//  3. All other types (text/*, application/vnd.openxmlformats-officedocument.*,
//     etc.) → always supported; a TXT envelope works universally.
func (mc ModelCapabilities) Supports(mimeType string) bool {
	mt := strings.ToLower(mimeType)
	if strings.HasPrefix(mt, "image/") {
		return mc.supportsImage
	}
	if mt == "application/pdf" {
		return mc.supportsPDF
	}
	// All other MIME types are text-based or can be safely wrapped in a TXT
	// envelope, so we allow them unconditionally.
	return true
}

// Load fetches (or returns from cache) the capability record for the given
// model ID.  The model ID should be in "provider/model" format as used by
// models.dev (e.g. "anthropic/claude-3-5-sonnet-20241022").
//
// When the model is not found in the models.dev database, Load returns a
// conservative capability set that only allows text MIME types.  The returned
// error is always nil; capability detection failures are silent and safe.
func Load(modelID string) (ModelCapabilities, error) {
	store, err := modelsdev.NewStore()
	if err != nil {
		// Cannot load the store at all — return text-only conservative caps.
		return ModelCapabilities{modelFound: false}, nil
	}

	// Use a background context for the lookup; capability detection is best-effort
	// and should not block the main request flow.
	model, err := store.GetModel(context.Background(), modelID)
	if err != nil {
		// Model not found or other error — conservative: text-only.
		return ModelCapabilities{modelFound: false}, nil
	}

	mc := ModelCapabilities{modelFound: true}
	for _, input := range model.Modalities.Input {
		switch strings.ToLower(input) {
		case "image":
			mc.supportsImage = true
		case "pdf":
			mc.supportsPDF = true
		}
	}
	return mc, nil
}

// CapsWith constructs a ModelCapabilities value directly from booleans. This is
// intended for use in tests and provider implementations that need to create a
// capabilities value without hitting the network.
func CapsWith(supportsImage, supportsPDF bool) ModelCapabilities {
	return ModelCapabilities{
		supportsImage: supportsImage,
		supportsPDF:   supportsPDF,
		modelFound:    true,
	}
}

// LoadFromStore is like Load but accepts an explicit *modelsdev.Store, making
// it convenient for tests that inject a pre-populated in-memory store.
func LoadFromStore(store *modelsdev.Store, modelID string) ModelCapabilities {
	model, err := store.GetModel(context.Background(), modelID)
	if err != nil {
		return ModelCapabilities{modelFound: false}
	}

	mc := ModelCapabilities{modelFound: true}
	for _, input := range model.Modalities.Input {
		switch strings.ToLower(input) {
		case "image":
			mc.supportsImage = true
		case "pdf":
			mc.supportsPDF = true
		}
	}
	return mc
}
