package builtins

import (
	"context"
	"fmt"

	"github.com/docker/docker-agent/pkg/hooks"
	"github.com/docker/docker-agent/pkg/secretsscan"
)

// RedactSecretsName is the type alias for the builtin's name
// (matches the constant pattern used by add_environment_info etc.).
type RedactSecretsName = string

// RedactSecrets is the registered name of the builtin pre_tool_use
// hook that scrubs secret material from a tool call's arguments
// before the runtime hands them to the tool.
//
// The builtin pairs with the runtime-shipped before_llm_call message
// transform (see pkg/runtime/redact_secrets.go) — together they bracket
// every place where chat content can leak secrets to a third party:
// the LLM provider on input, and a tool process on invocation. Either
// can be enabled in isolation; the agent flag wires both up at once.
const RedactSecrets RedactSecretsName = "redact_secrets"

// redactSecrets is the [hooks.BuiltinFunc] for [RedactSecrets].
// It walks every value in [hooks.Input.ToolInput] and replaces secret
// spans (per [secretsscan]) with [secretsscan.RedactionMarker], then
// returns the modified map via [hooks.HookSpecificOutput.UpdatedInput].
//
// Returning a nil [hooks.Output] when nothing changed is the
// no-overhead path: the runtime's hook executor treats a nil output
// as "this builtin contributed nothing", so an unaffected tool call
// flows through with no allocation beyond the input scan.
//
// Maps and slices nested inside ToolInput are walked recursively so
// secrets buried in structured arguments (a JSON object passed as a
// shell command's --config payload, a list of HTTP headers, ...) are
// caught alongside top-level string values. Non-string scalars
// (numbers, booleans, nil) are passed through unchanged.
func redactSecrets(_ context.Context, in *hooks.Input, _ []string) (*hooks.Output, error) {
	if in == nil || len(in.ToolInput) == 0 {
		return nil, nil
	}

	updated, changed := redactMap(in.ToolInput)
	if !changed {
		return nil, nil
	}

	return &hooks.Output{
		SystemMessage: fmt.Sprintf("redact_secrets: redacted secret material from arguments of tool %q", in.ToolName),
		HookSpecificOutput: &hooks.HookSpecificOutput{
			HookEventName: hooks.EventPreToolUse,
			UpdatedInput:  updated,
		},
	}, nil
}

// redactMap returns a redacted shallow copy of m alongside a
// "did anything change" flag. Returning a fresh map (rather than
// mutating in place) keeps the executor's [hooks.Result.ModifiedInput]
// merge predictable: the caller only sees a copy when a redaction
// actually happened.
//
// changed is the OR of every nested update so the top-level caller
// can short-circuit when the entire payload was already clean.
func redactMap(m map[string]any) (map[string]any, bool) {
	out := make(map[string]any, len(m))
	changed := false
	for k, v := range m {
		nv, c := redactValue(v)
		out[k] = nv
		if c {
			changed = true
		}
	}
	return out, changed
}

// redactValue recursively scrubs secrets out of v. Strings go through
// [secretsscan.Redact]; maps and slices are walked element-wise so
// secrets nested inside structured arguments (json.Unmarshal-style
// map[string]any payloads, []any lists) are still caught. Every
// other Go type passes through unchanged because the secret scanner
// only operates on text.
func redactValue(v any) (any, bool) {
	switch val := v.(type) {
	case string:
		redacted := secretsscan.Redact(val)
		return redacted, redacted != val
	case map[string]any:
		return redactMap(val)
	case []any:
		out := make([]any, len(val))
		changed := false
		for i, item := range val {
			nv, c := redactValue(item)
			out[i] = nv
			if c {
				changed = true
			}
		}
		if !changed {
			// Preserve identity when nothing changed so downstream
			// consumers that compare slice headers (rare, but the
			// hooks.Result.ModifiedInput merge spreads the value back
			// into the live ToolInput) don't see a spurious diff.
			return val, false
		}
		return out, true
	default:
		return v, false
	}
}
