package runtime

import (
	"context"
	"log/slog"
	"slices"

	"github.com/docker/docker-agent/pkg/chat"
	"github.com/docker/docker-agent/pkg/hooks"
	"github.com/docker/docker-agent/pkg/secretsscan"
)

// BuiltinRedactSecrets is the registered name of the runtime-shipped
// before_llm_call message transform that scrubs secret material from
// outgoing chat messages.
//
// It pairs with the redact_secrets pre_tool_use builtin in
// pkg/hooks/builtins; both are gated on the agent's RedactSecrets
// flag so a single switch covers the two leak vectors (outgoing chat
// content and outgoing tool args).
const BuiltinRedactSecrets = "redact_secrets"

// redactSecretsTransform is the [MessageTransform] registered under
// [BuiltinRedactSecrets]. It is a no-op for agents that did not opt
// in via the redact_secrets flag.
//
// Resolving the agent through r.team.Agent (rather than holding a
// direct reference) means the transform automatically picks up the
// final flag value even when the option is applied late in the
// construction chain. A missing agent silently falls through; the
// run-loop owner of the input gets a chance to surface a clearer
// error than this transform ever could.
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

	out := make([]chat.Message, len(msgs))
	for i, m := range msgs {
		out[i] = redactMessage(m)
	}
	return out, nil
}

// redactMessage scrubs every text-bearing field of m: [chat.Message.Content],
// the text parts of [chat.Message.MultiContent], and the JSON-encoded
// arguments of any [chat.Message.ToolCalls] (so a previous turn's tool
// call doesn't carry a secret forward into the next LLM call).
//
// MultiContent and ToolCalls slices are cloned before being mutated
// so the caller's message history is left untouched. We don't bother
// with copy-on-write: secretsscan.Redact is essentially free on
// inputs that don't match a rule (it returns the same string), so the
// only cost on a clean conversation is the per-message slice clone —
// negligible compared to the LLM call this transform is gating.
func redactMessage(m chat.Message) chat.Message {
	m.Content = secretsscan.Redact(m.Content)

	if len(m.MultiContent) > 0 {
		m.MultiContent = slices.Clone(m.MultiContent)
		for i := range m.MultiContent {
			if m.MultiContent[i].Type == chat.MessagePartTypeText {
				m.MultiContent[i].Text = secretsscan.Redact(m.MultiContent[i].Text)
			}
		}
	}

	if len(m.ToolCalls) > 0 {
		m.ToolCalls = slices.Clone(m.ToolCalls)
		for i := range m.ToolCalls {
			m.ToolCalls[i].Function.Arguments = secretsscan.Redact(m.ToolCalls[i].Function.Arguments)
		}
	}

	return m
}
