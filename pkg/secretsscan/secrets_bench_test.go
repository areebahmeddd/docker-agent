package secretsscan_test

import (
	"strings"
	"testing"

	"github.com/docker/docker-agent/pkg/secretsscan"
)

// BenchmarkRedactCleanInput is the headline scenario: most messages
// contain no secrets, so the keyword pre-filter must skip every rule's
// regex. Optimisations to the per-rule loop (e.g. lower-casing once
// outside the loop) show up here as a multi-x speedup; a regression
// that re-introduces per-rule allocation is the kind of thing this
// benchmark catches.
func BenchmarkRedactCleanInput(b *testing.B) {
	text := strings.Repeat("the quick brown fox jumps over the lazy dog. ", 200)
	b.ReportAllocs()
	for b.Loop() {
		_ = secretsscan.Redact(text)
	}
}

// BenchmarkRedactWithSecret exercises the full path: lower-case +
// keyword hit + regex match + cursor-rebuild redaction.
func BenchmarkRedactWithSecret(b *testing.B) {
	text := strings.Repeat("noise ", 100) +
		"ghp_cxLeRrvbJfmYdUtr70xnNE3Q7Gvli43s19PD" +
		strings.Repeat(" trailing", 100)
	b.ReportAllocs()
	for b.Loop() {
		_ = secretsscan.Redact(text)
	}
}
