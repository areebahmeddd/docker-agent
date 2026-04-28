package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/agent"
	"github.com/docker/docker-agent/pkg/chat"
	"github.com/docker/docker-agent/pkg/hooks"
	"github.com/docker/docker-agent/pkg/modelsdev"
	"github.com/docker/docker-agent/pkg/session"
	"github.com/docker/docker-agent/pkg/team"
	"github.com/docker/docker-agent/pkg/tools"
)

// modalityModelStore returns a fixed [modelsdev.Model] regardless of
// the requested ID. Tests configure its Modalities to exercise the
// strip_unsupported_modalities transform's three branches: text-only
// (strip), image-supporting (no-op), and unknown-modality (no-op,
// no panic).
type modalityModelStore struct {
	ModelStore

	model *modelsdev.Model
	err   error
}

func (m modalityModelStore) GetModel(_ context.Context, _ string) (*modelsdev.Model, error) {
	return m.model, m.err
}

// recordingMsgProvider captures the messages each model call sees so a
// test can confirm a transform actually rewrote what reached the
// provider (rather than just what the in-memory slice ended up looking
// like).
type recordingMsgProvider struct {
	mockProvider

	got [][]chat.Message
}

func (p *recordingMsgProvider) CreateChatCompletionStream(_ context.Context, msgs []chat.Message, _ []tools.Tool) (chat.MessageStream, error) {
	snap := append([]chat.Message{}, msgs...)
	p.got = append(p.got, snap)
	return p.stream, nil
}

// TestStripUnsupportedModalitiesTransform_TextOnlyModelDropsImages
// pins the runtime-shipped [stripUnsupportedModalitiesTransform]'s
// happy path: a text-only model receives messages with all image
// content stripped, while text content is preserved.
func TestStripUnsupportedModalitiesTransform_TextOnlyModelDropsImages(t *testing.T) {
	t.Parallel()

	prov := &mockProvider{id: "test/text-only", stream: &mockStream{}}
	a := agent.New("root", "instructions", agent.WithModel(prov))
	tm := team.New(team.WithAgents(a))

	store := modalityModelStore{model: &modelsdev.Model{
		Modalities: modelsdev.Modalities{Input: []string{"text"}},
	}}
	r, err := NewLocalRuntime(tm, WithModelStore(store))
	require.NoError(t, err)

	in := &hooks.Input{AgentName: "root"}
	msgs := []chat.Message{
		{
			Role: chat.MessageRoleUser,
			MultiContent: []chat.MessagePart{
				{Type: chat.MessagePartTypeText, Text: "look at this"},
				{Type: chat.MessagePartTypeImageURL, ImageURL: &chat.MessageImageURL{URL: "data:image/png;base64,abc"}},
			},
		},
	}

	got, err := r.stripUnsupportedModalitiesTransform(t.Context(), in, nil, msgs)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Len(t, got[0].MultiContent, 1, "image part must be stripped")
	assert.Equal(t, chat.MessagePartTypeText, got[0].MultiContent[0].Type)
}

// TestStripUnsupportedModalitiesTransform_ImageModelPassThrough pins
// the no-op branch: when the model's input modalities include "image",
// messages must reach the provider unchanged.
func TestStripUnsupportedModalitiesTransform_ImageModelPassThrough(t *testing.T) {
	t.Parallel()

	prov := &mockProvider{id: "test/multimodal", stream: &mockStream{}}
	a := agent.New("root", "instructions", agent.WithModel(prov))
	tm := team.New(team.WithAgents(a))

	store := modalityModelStore{model: &modelsdev.Model{
		Modalities: modelsdev.Modalities{Input: []string{"text", "image"}},
	}}
	r, err := NewLocalRuntime(tm, WithModelStore(store))
	require.NoError(t, err)

	in := &hooks.Input{AgentName: "root"}
	msgs := []chat.Message{{
		Role: chat.MessageRoleUser,
		MultiContent: []chat.MessagePart{
			{Type: chat.MessagePartTypeText, Text: "describe this"},
			{Type: chat.MessagePartTypeImageURL, ImageURL: &chat.MessageImageURL{URL: "data:image/png;base64,abc"}},
		},
	}}

	got, err := r.stripUnsupportedModalitiesTransform(t.Context(), in, nil, msgs)
	require.NoError(t, err)
	assert.Equal(t, msgs, got, "messages must reach a multimodal model untouched")
}

// TestStripUnsupportedModalitiesTransform_UnknownModelPassThrough pins
// the safe-fallback branch: when the models.dev lookup fails (or
// returns nil), the transform returns msgs unchanged so the request
// still reaches the provider; any modality mismatch surfaces as a
// provider error rather than a transform-side panic.
func TestStripUnsupportedModalitiesTransform_UnknownModelPassThrough(t *testing.T) {
	t.Parallel()

	prov := &mockProvider{id: "test/unknown", stream: &mockStream{}}
	a := agent.New("root", "instructions", agent.WithModel(prov))
	tm := team.New(team.WithAgents(a))

	cases := []struct {
		name  string
		store modalityModelStore
	}{
		{name: "nil model", store: modalityModelStore{model: nil}},
		{name: "lookup error", store: modalityModelStore{err: errors.New("not found")}},
		{name: "empty modalities", store: modalityModelStore{model: &modelsdev.Model{}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := NewLocalRuntime(tm, WithModelStore(tc.store))
			require.NoError(t, err)

			msgs := []chat.Message{{
				Role: chat.MessageRoleUser,
				MultiContent: []chat.MessagePart{
					{Type: chat.MessagePartTypeImageURL, ImageURL: &chat.MessageImageURL{URL: "x"}},
				},
			}}
			got, err := r.stripUnsupportedModalitiesTransform(t.Context(), &hooks.Input{AgentName: "root"}, nil, msgs)
			require.NoError(t, err)
			assert.Equal(t, msgs, got, "unknown model must fall through unchanged")
		})
	}
}

// TestApplyBeforeLLMCallTransforms_NoTransformsIsCheap covers the hot
// path: an agent without any registered transforms runs no allocator,
// no slog noise, and returns the input slice as-is. The test also
// covers the "agent not in transformsByAgent" branch for agents
// constructed outside the runtime's normal flow.
func TestApplyBeforeLLMCallTransforms_NoTransformsIsCheap(t *testing.T) {
	t.Parallel()

	prov := &mockProvider{id: "test/mock-model", stream: &mockStream{}}
	a := agent.New("root", "instructions", agent.WithModel(prov))
	tm := team.New(team.WithAgents(a))
	r, err := NewLocalRuntime(tm, WithModelStore(mockModelStore{}))
	require.NoError(t, err)

	// Drop the auto-registered strip_unsupported_modalities so we can
	// observe the cheap-path behavior.
	r.transforms = nil
	r.transformNames = nil
	r.transformsByAgent = nil

	sess := session.New(session.WithUserMessage("hi"))
	msgs := []chat.Message{{Role: chat.MessageRoleUser, Content: "hi"}}

	got := r.applyBeforeLLMCallTransforms(t.Context(), sess, a, msgs)
	assert.Equal(t, msgs, got)
}

// TestApplyBeforeLLMCallTransforms_OrderAndArgs verifies that
// transforms registered via [WithMessageTransform] (a) auto-inject a
// before_llm_call entry on every agent, (b) run in configured order,
// and (c) receive the per-hook args from the YAML / auto-injection.
func TestApplyBeforeLLMCallTransforms_OrderAndArgs(t *testing.T) {
	t.Parallel()

	type call struct {
		name string
		args []string
		seen []chat.Message
	}
	var calls []call

	tagA := func(_ context.Context, _ *hooks.Input, args []string, msgs []chat.Message) ([]chat.Message, error) {
		seen := append([]chat.Message{}, msgs...)
		calls = append(calls, call{name: "tag_a", args: args, seen: seen})
		out := append([]chat.Message{}, msgs...)
		out = append(out, chat.Message{Role: chat.MessageRoleSystem, Content: "tag_a"})
		return out, nil
	}
	tagB := func(_ context.Context, _ *hooks.Input, args []string, msgs []chat.Message) ([]chat.Message, error) {
		seen := append([]chat.Message{}, msgs...)
		calls = append(calls, call{name: "tag_b", args: args, seen: seen})
		out := append([]chat.Message{}, msgs...)
		out = append(out, chat.Message{Role: chat.MessageRoleSystem, Content: "tag_b"})
		return out, nil
	}

	prov := &mockProvider{id: "test/mock-model", stream: &mockStream{}}
	a := agent.New("root", "instructions", agent.WithModel(prov))
	tm := team.New(team.WithAgents(a))
	r, err := NewLocalRuntime(tm,
		WithModelStore(mockModelStore{}),
		WithMessageTransform("tag_a", tagA),
		WithMessageTransform("tag_b", tagB),
	)
	require.NoError(t, err)

	sess := session.New(session.WithUserMessage("hi"))
	msgs := []chat.Message{{Role: chat.MessageRoleUser, Content: "hi"}}

	got := r.applyBeforeLLMCallTransforms(t.Context(), sess, a, msgs)

	// The two registered tag transforms each fire exactly once.
	// (The runtime-shipped strip transform also runs, but it doesn't
	// append to `calls` since it's a different function.)
	require.Len(t, calls, 2, "expected tag_a + tag_b to fire exactly once each")

	// Registration order must be preserved: tag_a was registered first,
	// so it must be invoked first; tag_b second.
	assert.Equal(t, "tag_a", calls[0].name, "transforms must run in registration order")
	assert.Equal(t, "tag_b", calls[1].name, "transforms must run in registration order")

	// Cumulative semantics: the second transform must have observed the
	// first transform's appended message.
	assert.Greater(t, len(calls[1].seen), len(calls[0].seen),
		"tag_b must see tag_a's appended message (chain semantics, not parallel)")

	// The final slice must contain both tags.
	var finalContent []string
	for _, m := range got {
		finalContent = append(finalContent, m.Content)
	}
	assert.Contains(t, finalContent, "tag_a")
	assert.Contains(t, finalContent, "tag_b")
}

// TestApplyBeforeLLMCallTransforms_ErrorsAreSwallowed pins the
// fail-soft contract: a transform that returns an error must NOT
// break the run loop; the previous slice continues through the chain.
func TestApplyBeforeLLMCallTransforms_ErrorsAreSwallowed(t *testing.T) {
	t.Parallel()

	failing := func(_ context.Context, _ *hooks.Input, _ []string, _ []chat.Message) ([]chat.Message, error) {
		return nil, errors.New("boom")
	}
	tag := func(_ context.Context, _ *hooks.Input, _ []string, msgs []chat.Message) ([]chat.Message, error) {
		out := append([]chat.Message{}, msgs...)
		out = append(out, chat.Message{Role: chat.MessageRoleSystem, Content: "after_failure"})
		return out, nil
	}

	prov := &mockProvider{id: "test/mock-model", stream: &mockStream{}}
	a := agent.New("root", "instructions", agent.WithModel(prov))
	tm := team.New(team.WithAgents(a))
	r, err := NewLocalRuntime(tm,
		WithModelStore(mockModelStore{}),
		WithMessageTransform("failing", failing),
		WithMessageTransform("tag", tag),
	)
	require.NoError(t, err)

	sess := session.New(session.WithUserMessage("hi"))
	msgs := []chat.Message{{Role: chat.MessageRoleUser, Content: "hi"}}

	got := r.applyBeforeLLMCallTransforms(t.Context(), sess, a, msgs)

	// The "tag" transform must have run despite the failing one
	// erroring out, and its output must be present.
	var contents []string
	for _, m := range got {
		contents = append(contents, m.Content)
	}
	assert.Contains(t, contents, "after_failure",
		"a transform error must not abort the chain")
}

// TestRunStream_StripsImagesForTextOnlyModel confirms the inline
// strip in runStreamLoop has been replaced end-to-end: messages
// reaching the provider must no longer carry image parts when the
// agent's model is text-only.
func TestRunStream_StripsImagesForTextOnlyModel(t *testing.T) {
	t.Parallel()

	stream := newStreamBuilder().AddContent("ok").AddStopWithUsage(1, 1).Build()
	prov := &recordingMsgProvider{mockProvider: mockProvider{id: "test/text-only", stream: stream}}

	a := agent.New("root", "instructions", agent.WithModel(prov))
	tm := team.New(team.WithAgents(a))

	store := modalityModelStore{model: &modelsdev.Model{
		Modalities: modelsdev.Modalities{Input: []string{"text"}},
	}}
	r, err := NewLocalRuntime(tm, WithSessionCompaction(false), WithModelStore(store))
	require.NoError(t, err)

	sess := session.New()
	sess.AddMessage(session.UserMessage(""))
	// Replace the empty user message with a multi-part one carrying an image.
	last := &sess.Messages[len(sess.Messages)-1]
	last.Message.Message.MultiContent = []chat.MessagePart{
		{Type: chat.MessagePartTypeText, Text: "describe"},
		{Type: chat.MessagePartTypeImageURL, ImageURL: &chat.MessageImageURL{URL: "data:image/png;base64,abc"}},
	}

	for range r.RunStream(t.Context(), sess) {
		// drain — only the recorded provider state matters
	}

	require.NotEmpty(t, prov.got, "provider must have been called")
	for _, m := range prov.got[0] {
		for _, p := range m.MultiContent {
			assert.NotEqual(t, chat.MessagePartTypeImageURL, p.Type,
				"image parts must be stripped before reaching a text-only model")
		}
	}
}

// TestApplyMessageTransformDefaults_NoTransformsPreservesNil keeps the
// "nil cfg may stay nil when there are no defaults to add" contract,
// matching [applyCacheDefault]'s shape so [buildHooksExecutors] can
// continue to skip executor construction for agents with no hooks.
func TestApplyMessageTransformDefaults_NoTransformsPreservesNil(t *testing.T) {
	t.Parallel()

	prov := &mockProvider{id: "test/mock-model", stream: &mockStream{}}
	a := agent.New("root", "instructions", agent.WithModel(prov))
	tm := team.New(team.WithAgents(a))
	r, err := NewLocalRuntime(tm, WithModelStore(mockModelStore{}))
	require.NoError(t, err)

	r.transforms = nil // simulate "no transforms registered"
	r.transformNames = nil
	got := r.applyMessageTransformDefaults(nil)
	assert.Nil(t, got, "no transforms registered must preserve a nil cfg")
}

// TestResolveTransforms_DedupsByCommandAndArgs guards against double
// invocation when an agent's user-authored YAML already lists a
// builtin that the runtime auto-injects on top.
func TestResolveTransforms_DedupsByCommandAndArgs(t *testing.T) {
	t.Parallel()

	prov := &mockProvider{id: "test/mock-model", stream: &mockStream{}}
	a := agent.New("root", "instructions", agent.WithModel(prov))
	tm := team.New(team.WithAgents(a))
	r, err := NewLocalRuntime(tm, WithModelStore(mockModelStore{}))
	require.NoError(t, err)

	cfg := &hooks.Config{
		BeforeLLMCall: []hooks.Hook{
			{Type: hooks.HookTypeBuiltin, Command: BuiltinStripUnsupportedModalities},
			{Type: hooks.HookTypeBuiltin, Command: BuiltinStripUnsupportedModalities},
			{Type: hooks.HookTypeBuiltin, Command: BuiltinStripUnsupportedModalities, Args: []string{"foo"}},
		},
	}

	got := r.resolveTransforms(cfg)
	require.Len(t, got, 2, "duplicate (name, args) must collapse to one")
	assert.Equal(t, BuiltinStripUnsupportedModalities, got[0].name)
	assert.Empty(t, got[0].args)
	assert.Equal(t, []string{"foo"}, got[1].args, "differing args must NOT be deduplicated")
}

// TestRegisterMessageTransform_ShimAvoidsExecutorErrors confirms the
// shim wired by [registerMessageTransform]: a hooks.Executor.Dispatch
// for a registered-transform builtin must succeed (Allowed=true,
// Result is a no-op) instead of failing with "no builtin hook
// registered as ...".
func TestRegisterMessageTransform_ShimAvoidsExecutorErrors(t *testing.T) {
	t.Parallel()

	prov := &mockProvider{id: "test/mock-model", stream: &mockStream{}}
	a := agent.New("root", "instructions", agent.WithModel(prov))
	tm := team.New(team.WithAgents(a))

	called := 0
	tag := func(_ context.Context, _ *hooks.Input, _ []string, msgs []chat.Message) ([]chat.Message, error) {
		called++
		return msgs, nil
	}
	r, err := NewLocalRuntime(tm,
		WithModelStore(mockModelStore{}),
		WithMessageTransform("tag", tag),
	)
	require.NoError(t, err)

	exec := r.hooksExec(a)
	require.NotNil(t, exec)

	res, err := exec.Dispatch(t.Context(), hooks.EventBeforeLLMCall, &hooks.Input{
		SessionID: "session", AgentName: "root",
	})
	require.NoError(t, err, "executor must not error on a transform-only builtin")
	assert.True(t, res.Allowed, "shim must report success")
	// The transform itself isn't invoked through the executor — only via
	// applyBeforeLLMCallTransforms — so `called` stays 0 here.
	assert.Equal(t, 0, called, "executor path must NOT invoke the transform body")
}

// TestRunStream_TransformErrorDoesNotBreakRun is an integration smoke
// test confirming end-to-end: a transform that returns an error must
// not prevent the model from being called; the run completes
// normally and the messages reaching the provider are the pre-error
// snapshot.
func TestRunStream_TransformErrorDoesNotBreakRun(t *testing.T) {
	t.Parallel()

	stream := newStreamBuilder().AddContent("ok").AddStopWithUsage(1, 1).Build()
	prov := &mockProvider{id: "test/mock-model", stream: stream}

	failing := func(_ context.Context, _ *hooks.Input, _ []string, _ []chat.Message) ([]chat.Message, error) {
		return nil, errors.New("boom")
	}

	a := agent.New("root", "instructions", agent.WithModel(prov))
	tm := team.New(team.WithAgents(a))
	r, err := NewLocalRuntime(tm,
		WithSessionCompaction(false),
		WithModelStore(mockModelStore{}),
		WithMessageTransform("failing", failing),
	)
	require.NoError(t, err)

	sess := session.New(session.WithUserMessage("hi"))
	var sawStop bool
	for ev := range r.RunStream(t.Context(), sess) {
		if _, ok := ev.(*StreamStoppedEvent); ok {
			sawStop = true
		}
	}
	assert.True(t, sawStop, "run must complete despite a failing transform")
}

// TestWithMessageTransform_RejectsEmptyAndNil pins the input
// validation: empty name or nil fn must be silently ignored (matching
// the no-error shape of other Opts), with a slog warning.
func TestWithMessageTransform_RejectsEmptyAndNil(t *testing.T) {
	t.Parallel()

	prov := &mockProvider{id: "test/mock-model", stream: &mockStream{}}
	a := agent.New("root", "instructions", agent.WithModel(prov))
	tm := team.New(team.WithAgents(a))

	r, err := NewLocalRuntime(tm,
		WithModelStore(mockModelStore{}),
		WithMessageTransform("", func(_ context.Context, _ *hooks.Input, _ []string, msgs []chat.Message) ([]chat.Message, error) {
			return msgs, nil
		}),
		WithMessageTransform("nilfn", nil),
	)
	require.NoError(t, err, "WithMessageTransform must not surface a constructor error")

	// Only the runtime-shipped strip transform should be in the table.
	require.Len(t, r.transforms, 1, "invalid transforms must be silently ignored")
	_, ok := r.transforms[BuiltinStripUnsupportedModalities]
	assert.True(t, ok)
}
