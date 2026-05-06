package modelcaps_test

import (
	"testing"

	"github.com/docker/docker-agent/pkg/attachment/modelcaps"
	"github.com/docker/docker-agent/pkg/modelsdev"
)

// buildStore creates an in-memory Store with the given models for testing.
func buildStore(providers map[string]modelsdev.Provider) *modelsdev.Store {
	db := &modelsdev.Database{Providers: providers}
	return modelsdev.NewDatabaseStore(db)
}

func TestLoadFromStore_VisionModel(t *testing.T) {
	store := buildStore(map[string]modelsdev.Provider{
		"anthropic": {
			Models: map[string]modelsdev.Model{
				"claude-3-5-sonnet": {
					Name: "Claude 3.5 Sonnet",
					Modalities: modelsdev.Modalities{
						Input:  []string{"text", "image", "pdf"},
						Output: []string{"text"},
					},
				},
			},
		},
	})

	mc := modelcaps.LoadFromStore(store, "anthropic/claude-3-5-sonnet")

	if !mc.Supports("image/jpeg") {
		t.Error("expected image/jpeg to be supported for vision model")
	}
	if !mc.Supports("image/png") {
		t.Error("expected image/png to be supported for vision model")
	}
	if !mc.Supports("application/pdf") {
		t.Error("expected application/pdf to be supported for pdf model")
	}
	if !mc.Supports("text/plain") {
		t.Error("expected text/plain to always be supported")
	}
}

func TestLoadFromStore_TextOnlyModel(t *testing.T) {
	store := buildStore(map[string]modelsdev.Provider{
		"openai": {
			Models: map[string]modelsdev.Model{
				"gpt-3.5-turbo": {
					Name: "GPT-3.5 Turbo",
					Modalities: modelsdev.Modalities{
						Input:  []string{"text"},
						Output: []string{"text"},
					},
				},
			},
		},
	})

	mc := modelcaps.LoadFromStore(store, "openai/gpt-3.5-turbo")

	if mc.Supports("image/jpeg") {
		t.Error("expected image/jpeg NOT to be supported for text-only model")
	}
	if mc.Supports("application/pdf") {
		t.Error("expected application/pdf NOT to be supported for text-only model")
	}
	// Text MIMEs are always allowed
	if !mc.Supports("text/plain") {
		t.Error("expected text/plain to always be supported")
	}
	if !mc.Supports("text/markdown") {
		t.Error("expected text/markdown to always be supported")
	}
}

func TestLoadFromStore_ModelNotFound(t *testing.T) {
	store := buildStore(map[string]modelsdev.Provider{})

	mc := modelcaps.LoadFromStore(store, "unknown/nonexistent-model")

	// Conservative fallback: only text is allowed
	if mc.Supports("image/jpeg") {
		t.Error("expected image/jpeg NOT to be supported for unknown model")
	}
	if mc.Supports("application/pdf") {
		t.Error("expected application/pdf NOT to be supported for unknown model")
	}
	if !mc.Supports("text/plain") {
		t.Error("expected text/plain to always be supported even for unknown model")
	}
}

func TestLoadFromStore_OfficeDocsAlwaysAllowed(t *testing.T) {
	// Even a text-only model must allow Office document MIMEs (they'll be TXT-enveloped).
	store := buildStore(map[string]modelsdev.Provider{
		"openai": {
			Models: map[string]modelsdev.Model{
				"gpt-4o": {
					Name: "GPT-4o",
					Modalities: modelsdev.Modalities{
						Input:  []string{"text"},
						Output: []string{"text"},
					},
				},
			},
		},
	})

	mc := modelcaps.LoadFromStore(store, "openai/gpt-4o")

	officeMIME := "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	if !mc.Supports(officeMIME) {
		t.Errorf("expected office MIME %q to always be supported", officeMIME)
	}
}

func TestCapsWith(t *testing.T) {
	mc := modelcaps.CapsWith(true, false)
	if !mc.Supports("image/jpeg") {
		t.Error("expected image/jpeg to be supported")
	}
	if mc.Supports("application/pdf") {
		t.Error("expected pdf NOT to be supported")
	}

	mc2 := modelcaps.CapsWith(false, false)
	if mc2.Supports("image/png") {
		t.Error("expected image/png NOT to be supported")
	}
}

// TestSupports_AudioVideoRejected verifies that audio/video MIMEs are NOT
// allowed by default — they require explicit model support declarations
// which Phase 1 does not implement.
func TestSupports_AudioVideoRejected(t *testing.T) {
	// Even a vision+pdf capable model should reject audio/video.
	mc := modelcaps.CapsWith(true, true)

	for _, mime := range []string{
		"audio/mp3",
		"audio/wav",
		"audio/ogg",
		"video/mp4",
		"video/webm",
		"application/octet-stream",
	} {
		if mc.Supports(mime) {
			t.Errorf("expected %q to NOT be supported (not in Phase 1 allowlist)", mime)
		}
	}
}
