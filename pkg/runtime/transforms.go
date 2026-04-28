package runtime

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/docker/docker-agent/pkg/agent"
	"github.com/docker/docker-agent/pkg/chat"
	"github.com/docker/docker-agent/pkg/hooks"
	"github.com/docker/docker-agent/pkg/session"
)

// MessageTransform is the in-process-only handler signature for a
// before_llm_call builtin that rewrites the chat messages about to be
// sent to the model. It receives the full message slice in configured
// order and returns the (possibly-rewritten) replacement.
//
// Transforms are intentionally a runtime-private contract: the cost of
// JSON-roundtripping a full conversation through the cross-process
// hook protocol would be prohibitive, so command and model hooks
// cannot rewrite messages. Embedders register transforms via
// [WithMessageTransform]; the runtime ships [BuiltinStripUnsupportedModalities]
// out of the box.
//
// Transforms run AFTER the standard before_llm_call gate (see
// [LocalRuntime.executeBeforeLLMCallHooks]) — a hook that wants to
// abort the call should target the gate, not a transform.
//
// Returning a non-nil error logs a warning and falls through to the
// previous message slice; a transform failure must never break the run
// loop.
type MessageTransform func(ctx context.Context, in *hooks.Input, args []string, msgs []chat.Message) ([]chat.Message, error)

// resolvedTransform pairs a registered [MessageTransform] with the
// hook-config args that selected it. Pre-resolved once during
// [LocalRuntime.buildHooksExecutors] so per-call dispatch is a flat
// slice walk.
type resolvedTransform struct {
	name string
	args []string
	fn   MessageTransform
}

// WithMessageTransform registers a [MessageTransform] under name. The
// transform is auto-applied to every agent for the before_llm_call
// event (the runtime injects a corresponding `{type: builtin, command:
// name}` entry into each agent's hook config), giving custom
// redactors / scrubbers / modality strippers an always-on lifecycle
// without any per-agent YAML.
//
// Empty name or nil fn are silently ignored, matching the no-error
// shape of the other [Opt] helpers; explicit registration via
// [LocalRuntime.RegisterMessageTransform] (called internally) returns
// errors for those cases.
func WithMessageTransform(name string, fn MessageTransform) Opt {
	return func(r *LocalRuntime) {
		if name == "" || fn == nil {
			return
		}
		if err := r.registerMessageTransform(name, fn); err != nil {
			slog.Warn("Failed to register message transform; ignoring", "name", name, "error", err)
		}
	}
}

// registerMessageTransform records fn under name in the runtime's
// transform table AND registers a no-op [hooks.BuiltinFunc] shim for
// the same name on the runtime's hooks registry. The shim makes the
// auto-injected `{type: builtin, command: name}` entry resolvable by
// the standard [hooks.Executor] (so it doesn't fail with "no builtin
// hook registered as ..."), while the actual rewrite happens through
// the typed transform path in [applyBeforeLLMCallTransforms].
//
// Re-registering an existing name replaces the previous transform but
// does NOT change its position in the auto-injection order, so users
// of [WithMessageTransform] get a stable, predictable chain regardless
// of how often they patch in test code.
func (r *LocalRuntime) registerMessageTransform(name string, fn MessageTransform) error {
	if name == "" {
		return errors.New("message transform name must not be empty")
	}
	if fn == nil {
		return errors.New("message transform function must not be nil")
	}
	if r.transforms == nil {
		r.transforms = make(map[string]MessageTransform)
	}
	if _, exists := r.transforms[name]; !exists {
		r.transformNames = append(r.transformNames, name)
	}
	r.transforms[name] = fn
	// No-op shim: the standard hooks executor will see the
	// auto-injected builtin entry and dispatch this; returning nil
	// signals "ran cleanly, no opinion" so aggregate() doesn't
	// surface a warning. The actual message rewrite happens through
	// applyBeforeLLMCallTransforms.
	return r.hooksRegistry.RegisterBuiltin(name, noopBuiltin)
}

// noopBuiltin is the [hooks.BuiltinFunc] companion used by every
// registered [MessageTransform]: it accepts the JSON-serialized input
// and returns nothing. Pulled out as a package-level value so all
// transforms share the same function pointer (cheap dedup, easier to
// recognize in logs).
func noopBuiltin(_ context.Context, _ *hooks.Input, _ []string) (*hooks.Output, error) {
	return nil, nil
}

// applyMessageTransformDefaults appends a `{type: builtin, command:
// name}` entry to cfg.BeforeLLMCall for every registered
// [MessageTransform], mirroring the role of [applyCacheDefault] for
// the cache_response stop builtin and of [builtins.ApplyAgentDefaults]
// for the date / env / prompt-files turn_start builtins.
//
// Transforms are auto-injected in registration order (see
// [registerMessageTransform]), giving callers a stable, predictable
// chain even though the underlying lookup table is a map.
//
// The helper accepts (and may return) a nil cfg so callers can chain
// it after the other default helpers without an extra branch. It is a
// no-op when no transforms are registered, in which case it preserves
// the cfg-may-be-nil contract.
func (r *LocalRuntime) applyMessageTransformDefaults(cfg *hooks.Config) *hooks.Config {
	if len(r.transformNames) == 0 {
		return cfg
	}
	if cfg == nil {
		cfg = &hooks.Config{}
	}
	for _, name := range r.transformNames {
		cfg.BeforeLLMCall = append(cfg.BeforeLLMCall, hooks.Hook{
			Type:    hooks.HookTypeBuiltin,
			Command: name,
		})
	}
	return cfg
}

// resolveTransforms walks cfg.BeforeLLMCall in configured order and
// returns the registered [MessageTransform]s to apply, deduplicated by
// (name, args) so a user-authored YAML entry that overlaps the
// runtime's auto-injected builtin doesn't run the transform twice.
//
// Returns nil for an empty resolution so callers can short-circuit
// cheaply on the (common) no-transforms path.
func (r *LocalRuntime) resolveTransforms(cfg *hooks.Config) []resolvedTransform {
	if cfg == nil || len(cfg.BeforeLLMCall) == 0 || len(r.transforms) == 0 {
		return nil
	}
	var out []resolvedTransform
	seen := make(map[string]bool)
	for _, h := range cfg.BeforeLLMCall {
		if h.Type != hooks.HookTypeBuiltin {
			continue
		}
		fn, ok := r.transforms[h.Command]
		if !ok {
			continue
		}
		key := transformDedupKey(h.Command, h.Args)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, resolvedTransform{name: h.Command, args: h.Args, fn: fn})
	}
	return out
}

// transformDedupKey mirrors [hooks.dedupKey]'s (command, args) shape so
// transforms are deduplicated on the same axis as the standard hook
// executor. Type is always `builtin` for transforms, so it's not part
// of the key.
func transformDedupKey(name string, args []string) string {
	var b strings.Builder
	b.WriteString(name)
	for _, a := range args {
		b.WriteByte(0)
		b.WriteString(a)
	}
	return b.String()
}

// applyBeforeLLMCallTransforms dispatches the agent's pre-resolved
// [MessageTransform] chain just before the model call, AFTER
// [LocalRuntime.executeBeforeLLMCallHooks] has run its gate. Transforms
// rewrite (or drop) messages but cannot abort the call — that
// responsibility lives with the gate.
//
// Returns the (possibly-rewritten) message slice. Errors from
// individual transforms are logged at warn level and the chain
// continues with the previous slice, matching the executor's "warn,
// don't break the loop" stance for non-fail-closed events.
func (r *LocalRuntime) applyBeforeLLMCallTransforms(
	ctx context.Context,
	sess *session.Session,
	a *agent.Agent,
	msgs []chat.Message,
) []chat.Message {
	transforms := r.transformsByAgent[a.Name()]
	if len(transforms) == 0 {
		return msgs
	}
	in := &hooks.Input{
		SessionID:     sess.ID,
		AgentName:     a.Name(),
		HookEventName: hooks.EventBeforeLLMCall,
		Cwd:           r.workingDir,
	}
	for _, t := range transforms {
		out, err := t.fn(ctx, in, t.args, msgs)
		if err != nil {
			slog.Warn("Message transform failed; continuing with previous messages",
				"transform", t.name, "agent", a.Name(), "error", err)
			continue
		}
		msgs = out
	}
	return msgs
}
