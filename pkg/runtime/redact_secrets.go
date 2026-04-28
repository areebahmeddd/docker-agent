package runtime

import (
	"context"
	"log/slog"

	"github.com/docker/docker-agent/pkg/chat"
	"github.com/docker/docker-agent/pkg/hooks"
	"github.com/docker/docker-agent/pkg/secretsscan"
	"github.com/docker/docker-agent/pkg/tools"
)

// BuiltinRedactSecrets is the registered name of the runtime-shipped
// before_llm_call message transform that scrubs secret material from
// the chat messages about to be sent to the LLM.
//
// It pairs with the redact_secrets pre_tool_use builtin hook in
// pkg/hooks/builtins (the constant of the same name there is the
// matching string): both leak vectors — outgoing chat content on one
// side, tool arguments on the other — share a single agent-level
// switch (AgentConfig.RedactSecrets / agent.RedactSecrets()). The
// transform constant exists for log filtering and parity with
// [BuiltinStripUnsupportedModalities] / [BuiltinCacheResponse].
const BuiltinRedactSecrets = "redact_secrets"

// redactSecretsTransform is the [MessageTransform] registered under
// [BuiltinRedactSecrets]. It is no-op for every agent that didn't
// opt in via the redact_secrets flag — the flag is the single source
// of truth, so adding the pre_tool_use builtin manually in YAML does
// NOT enable the LLM-side scrubbing (and vice versa). Coupling the
// two via the same flag avoids surprising "secrets leak to LLM but
// not to tools" gaps when only one side is configured.
//
// Resolving the agent through r.team.Agent (rather than holding a
// direct reference) means the transform automatically picks up the
// final flag value even when WithRedactSecrets is applied late in
// the construction chain. A missing agent (corrupt input, deleted
// since registration) silently falls through; the run-loop owner of
// the input gets a chance to surface a clearer error than this
// transform ever could.
func (r *LocalRuntime) redactSecretsTransform(
	_ context.Context,
	in *hooks.Input,
	msgs []chat.Message,
) ([]chat.Message, error) {
	if in == nil || in.AgentName == "" {
		return msgs, nil
	}
	a, err := r.team.Agent(in.AgentName)
	if err != nil || a == nil {
		slog.Debug("redact_secrets: skipping, agent not resolvable",
			"agent_name", in.AgentName, "error", err)
		return msgs, nil
	}
	if !a.RedactSecrets() {
		return msgs, nil
	}
	return redactMessageSecrets(msgs), nil
}

// redactMessageSecrets returns a copy of messages with every text
// field passed through [secretsscan.Redact]. Messages whose text
// fields contain no secrets are returned by reference inside the
// new slice — the per-message [chat.Message] value is only cloned
// when at least one field actually changes — so the typical
// "no secrets anywhere" case allocates exactly one slice header.
//
// The fields scrubbed are the ones a model can actually read from
// the wire: [chat.Message.Content], [chat.Message.MultiContent]
// text parts, and [tools.FunctionCall.Arguments] (the
// JSON-encoded argument blob a previous turn's tool call carries
// forward into the conversation history). Reasoning fields,
// thinking signatures, and image payloads are left untouched —
// they're either opaque to text scanning or unlikely vehicles for
// secret leakage in the first place.
//
// Lives in this file (rather than secretsscan) because it depends
// on the chat package; secretsscan stays free of internal
// imports so it can be reused from the hooks builtin without
// dragging in chat.
func redactMessageSecrets(msgs []chat.Message) []chat.Message {
	out := make([]chat.Message, len(msgs))
	for i, m := range msgs {
		out[i] = redactSingleMessage(m)
	}
	return out
}

// redactSingleMessage scrubs every text-bearing field of m and
// returns the (possibly-modified) result. Split out from
// [redactMessageSecrets] so the per-message logic is easy to test
// and reason about — and so a future addition (e.g. scrubbing a
// new chat.Message field) lives next to the existing rules rather
// than buried in a slice loop.
func redactSingleMessage(m chat.Message) chat.Message {
	if scrubbed := secretsscan.Redact(m.Content); scrubbed != m.Content {
		m.Content = scrubbed
	}

	if len(m.MultiContent) > 0 {
		// Walk-and-rebuild only when at least one part needs a
		// rewrite, otherwise preserve the original slice header.
		// Nothing is gained from cloning a slice of unchanged
		// pointers and it would force every downstream comparator
		// (history dedup, cache-key hashing) to do extra work.
		var newParts []chat.MessagePart
		for j, part := range m.MultiContent {
			if part.Type != chat.MessagePartTypeText || part.Text == "" {
				continue
			}
			scrubbed := secretsscan.Redact(part.Text)
			if scrubbed == part.Text {
				continue
			}
			if newParts == nil {
				newParts = make([]chat.MessagePart, len(m.MultiContent))
				copy(newParts, m.MultiContent)
			}
			newParts[j].Text = scrubbed
		}
		if newParts != nil {
			m.MultiContent = newParts
		}
	}

	if len(m.ToolCalls) > 0 {
		var newCalls []tools.ToolCall
		for j, call := range m.ToolCalls {
			if call.Function.Arguments == "" {
				continue
			}
			scrubbed := secretsscan.Redact(call.Function.Arguments)
			if scrubbed == call.Function.Arguments {
				continue
			}
			if newCalls == nil {
				newCalls = make([]tools.ToolCall, len(m.ToolCalls))
				copy(newCalls, m.ToolCalls)
			}
			newCalls[j].Function.Arguments = scrubbed
		}
		if newCalls != nil {
			m.ToolCalls = newCalls
		}
	}

	return m
}
