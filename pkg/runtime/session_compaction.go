package runtime

import (
	"context"
	"errors"
	"log/slog"

	"github.com/docker/docker-agent/pkg/agent"
	"github.com/docker/docker-agent/pkg/chat"
	"github.com/docker/docker-agent/pkg/compaction"
	"github.com/docker/docker-agent/pkg/hooks"
	"github.com/docker/docker-agent/pkg/model/provider"
	"github.com/docker/docker-agent/pkg/model/provider/options"
	"github.com/docker/docker-agent/pkg/runtime/compactor"
	"github.com/docker/docker-agent/pkg/session"
	"github.com/docker/docker-agent/pkg/team"
)

// Compaction reasons reported to BeforeCompaction / AfterCompaction hooks.
const (
	compactionReasonThreshold = "threshold"
	compactionReasonOverflow  = "overflow"
	compactionReasonManual    = "manual"
)

// doCompact orchestrates a session compaction. It is intentionally thin:
// the heavy lifting (extracting the conversation, running the LLM, computing
// the kept-tail boundary) lives in [pkg/runtime/compactor]; this function
// owns only what's runtime-private: hook dispatch, session mutation, event
// emission, and persistence.
//
// reason is one of [compactionReasonThreshold] (proactive 90% trigger),
// [compactionReasonOverflow] (post-overflow recovery) or
// [compactionReasonManual] (user-invoked /compact). It is forwarded to
// BeforeCompaction / AfterCompaction hooks.
//
// Hook integration:
//   - BeforeCompaction fires first. If a hook denies (Decision: "block"),
//     the runtime returns immediately without emitting any compaction
//     events; the conversation is left untouched.
//   - If a BeforeCompaction hook supplies a non-empty Summary in
//     HookSpecificOutput, the runtime applies that summary verbatim and
//     skips the LLM-based summarization entirely. The kept-tail policy
//     ([compactor.MaxKeepTokens]) still applies so conversation
//     continuity is identical to the LLM path.
//   - AfterCompaction fires after the summary has been applied; it is
//     observational.
//
// If no hooks are configured for any of these events, control flow is
// behaviourally identical to the original, hookless implementation.
//
// Note: the runtime does NOT re-fire session_start with Source="compact".
// session_start hook output is held as transient context that is threaded
// into every model call (see [LocalRuntime.executeSessionStartHooks]), so
// env / cwd / OS info is automatically present after a compaction without
// any extra dispatch.
func (r *LocalRuntime) doCompact(ctx context.Context, sess *session.Session, a *agent.Agent, additionalPrompt, reason string, events chan Event) {
	contextLimit := r.compactionContextLimit(ctx, a)

	// before_compaction: hooks can veto or supply a custom summary.
	pre := r.executeBeforeCompactionHooks(ctx, sess, a, reason, contextLimit, events)
	if pre != nil && !pre.Allowed {
		slog.Info("Session compaction skipped by before_compaction hook",
			"session_id", sess.ID,
			"agent", a.Name(),
			"reason", reason,
			"hook_message", pre.Message,
		)
		return
	}

	slog.Debug("Generating summary for session", "session_id", sess.ID, "reason", reason)
	events <- SessionCompaction(sess.ID, "started", a.Name())
	defer func() {
		events <- SessionCompaction(sess.ID, "completed", a.Name())
	}()

	result, err := r.produceSummary(ctx, sess, a, additionalPrompt, contextLimit, pre)
	if err != nil {
		slog.Error("Failed to generate session summary", "error", err)
		events <- Error(err.Error())
		return
	}
	if result == nil {
		// Empty summary — bail without applying anything.
		return
	}

	// Apply the summary to the session. This is intrinsically
	// runtime-private: it mutates session-internal state and persists
	// through the runtime's session store.
	sess.InputTokens = result.InputTokens
	sess.OutputTokens = 0
	sess.Messages = append(sess.Messages, session.Item{
		Summary:        result.Summary,
		FirstKeptEntry: result.FirstKeptEntry,
		Cost:           result.Cost,
	})
	_ = r.sessionStore.UpdateSession(ctx, sess)

	slog.Debug("Generated session summary", "session_id", sess.ID, "summary_length", len(result.Summary))
	events <- SessionSummary(sess.ID, result.Summary, a.Name(), result.FirstKeptEntry)

	// after_compaction: observational. Fired only when a summary was
	// actually applied to the session.
	r.executeAfterCompactionHooks(ctx, sess, a, reason, contextLimit, result.Summary, events)
}

// produceSummary returns a structured compaction result, choosing
// between a hook-supplied summary (if before_compaction returned one)
// and the default LLM-based strategy.
func (r *LocalRuntime) produceSummary(
	ctx context.Context,
	sess *session.Session,
	a *agent.Agent,
	additionalPrompt string,
	contextLimit int64,
	pre *hooks.Result,
) (*compactor.Result, error) {
	if pre != nil && pre.Summary != "" {
		slog.Debug("Using compaction summary from before_compaction hook",
			"session_id", sess.ID, "agent", a.Name(), "summary_length", len(pre.Summary))
		return &compactor.Result{
			Summary:        pre.Summary,
			FirstKeptEntry: compactor.ComputeFirstKeptEntry(sess, a),
			// Estimate the summary's token count for session bookkeeping;
			// no LLM was called so the cost is zero.
			InputTokens: compaction.EstimateMessageTokens(&chat.Message{
				Role:    chat.MessageRoleAssistant,
				Content: pre.Summary,
			}),
			Cost: 0,
		}, nil
	}

	if contextLimit <= 0 {
		return nil, errors.New("failed to get model definition")
	}

	return compactor.RunLLM(ctx, compactor.LLMArgs{
		Session:          sess,
		Agent:            a,
		AdditionalPrompt: additionalPrompt,
		ContextLimit:     contextLimit,
		RunAgent:         r.runCompactionAgent,
	})
}

// compactionContextLimit returns the agent's model context limit, or 0
// when it can't be resolved. Failure is non-fatal here: a
// before_compaction hook may supply its own summary and never need the
// model definition. The LLM strategy itself enforces ContextLimit > 0.
func (r *LocalRuntime) compactionContextLimit(ctx context.Context, a *agent.Agent) int64 {
	if a == nil || a.Model() == nil {
		return 0
	}
	summaryModel := provider.CloneWithOptions(ctx, a.Model(),
		options.WithStructuredOutput(nil),
		options.WithMaxTokens(compactor.MaxSummaryTokens),
	)
	m, err := r.modelsStore.GetModel(ctx, summaryModel.ID())
	if err != nil || m == nil {
		return 0
	}
	return int64(m.Limit.Context)
}

// runCompactionAgent runs an agent against a sub-session for compaction.
// It is the runtime-side glue [pkg/runtime/compactor] invokes via callback,
// which avoids creating an import cycle on [pkg/runtime].
func (r *LocalRuntime) runCompactionAgent(ctx context.Context, a *agent.Agent, sess *session.Session) error {
	t := team.New(team.WithAgents(a))
	rt, err := New(t, WithSessionCompaction(false))
	if err != nil {
		return err
	}
	_, err = rt.Run(ctx, sess)
	return err
}
