// Package secretsscan provides a small, dependency-free scanner that
// recognises common API tokens, cloud credentials, and other secret
// material in arbitrary text. It exposes two operations:
//
//   - [ContainsSecrets] reports whether any rule matches the input
//     (used for fail-closed checks).
//   - [Redact] replaces the secret span of every matching rule with
//     [RedactionMarker], leaving the surrounding text untouched.
//
// The ruleset is derived from the MIT-licensed
// github.com/docker/mcp-gateway/pkg/secretsscan package, which itself
// adapted the patterns from
// github.com/aquasecurity/trivy/pkg/fanal/secret/builtin-rules.go.
// Every rule pairs a keyword shortlist (cheap [strings.Contains]
// pre-filter) with a regular expression; only inputs that contain
// one of the keywords pay the regex cost.
//
// The package is intentionally allocation-light and concurrency-safe
// so it can be called on every outgoing chat message and tool input
// without measurable overhead.
package secretsscan
