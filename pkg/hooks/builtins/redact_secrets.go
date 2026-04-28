package builtins

import (
	"context"
	"fmt"

	"github.com/docker/docker-agent/pkg/hooks"
	"github.com/docker/docker-agent/pkg/secretsscan"
)

// RedactSecrets is the registered name of the pre_tool_use builtin
// that scrubs secret material from a tool call's arguments before the
// runtime hands them to the tool.
//
// It pairs with the runtime-shipped before_llm_call message transform
// (see pkg/runtime/redact_secrets.go); together they bracket the two
// places where chat content can leak secrets to a third party (the
// LLM provider on input, and a tool process on invocation). The
// redact_secrets agent flag enables both at once.
const RedactSecrets = "redact_secrets"

// redactSecrets is the [hooks.BuiltinFunc] registered under
// [RedactSecrets]. It walks every value in [hooks.Input.ToolInput]
// (recursively into nested maps and slices) and replaces secret spans
// with [secretsscan.RedactionMarker]. When nothing matched it returns
// a nil [hooks.Output] so unaffected tool calls take the cheap path
// through the executor.
func redactSecrets(_ context.Context, in *hooks.Input, _ []string) (*hooks.Output, error) {
	if in == nil || len(in.ToolInput) == 0 {
		return nil, nil
	}
	updated, changed := redactAny(in.ToolInput)
	if !changed {
		return nil, nil
	}
	return &hooks.Output{
		SystemMessage: fmt.Sprintf("redact_secrets: redacted secret material from arguments of tool %q", in.ToolName),
		HookSpecificOutput: &hooks.HookSpecificOutput{
			HookEventName: hooks.EventPreToolUse,
			UpdatedInput:  updated.(map[string]any),
		},
	}, nil
}

// redactAny recursively scrubs secrets out of v, returning the new
// value and a "did anything change" flag. Strings go through
// [secretsscan.Redact]; map[string]any / []any are walked to catch
// secrets nested inside JSON-style payloads (e.g. an MCP tool's
// structured arguments). Every other Go type passes through unchanged
// because the scanner only operates on text.
func redactAny(v any) (any, bool) {
	switch val := v.(type) {
	case string:
		redacted := secretsscan.Redact(val)
		return redacted, redacted != val
	case map[string]any:
		out := make(map[string]any, len(val))
		changed := false
		for k, item := range val {
			nv, c := redactAny(item)
			out[k] = nv
			changed = changed || c
		}
		return out, changed
	case []any:
		out := make([]any, len(val))
		changed := false
		for i, item := range val {
			nv, c := redactAny(item)
			out[i] = nv
			changed = changed || c
		}
		return out, changed
	default:
		return v, false
	}
}
