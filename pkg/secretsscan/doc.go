// Package secretsscan recognises common API tokens, cloud credentials,
// and other secret material in arbitrary text.
//
// [ContainsSecrets] reports whether any rule matches the input;
// [Redact] replaces every detected secret span with [RedactionMarker]
// while preserving the surrounding text. Both are safe for concurrent
// use and idempotent.
//
// The ruleset is derived from the MIT-licensed
// github.com/docker/mcp-gateway/pkg/secretsscan package, which adapted
// it from github.com/aquasecurity/trivy/pkg/fanal/secret.
package secretsscan
