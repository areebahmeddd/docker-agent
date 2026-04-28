package lifecycle

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"math/rand/v2"
	"sync"
	"time"
)

// Connector creates new sessions for a Supervisor. Implementations are
// transport-specific: stdio MCP, remote MCP, LSP stdio.
//
// The same Connector is reused across reconnects: Connect is called once
// per attempt, and the returned Session is closed before the next Connect.
type Connector interface {
	// Connect establishes a new underlying connection (e.g. spawns a process,
	// dials HTTP, runs the initialize handshake) and returns a Session that
	// the supervisor will Wait on and Close.
	//
	// Connect must honour ctx for the connect/initialize phase, but the
	// returned Session is owned by the supervisor and may outlive ctx; the
	// supervisor will call Close(ctx) when it is done.
	//
	// Errors should be classified via Classify before returning so the
	// supervisor can apply restart policy via errors.Is.
	Connect(ctx context.Context) (Session, error)
}

// Session is the supervisor's view of an active connection.
type Session interface {
	// Wait blocks until the session is closed by the peer or by Close.
	// It returns nil for a graceful close.
	Wait() error

	// Close terminates the session. Close must be idempotent and safe to
	// call concurrently with an in-flight Wait.
	Close(ctx context.Context) error
}

// Restart controls how the Supervisor reacts to an unexpected disconnect.
type Restart int

const (
	// RestartOnFailure: reconnect after the session ends with a non-nil
	// error or after a forced reconnect via RestartAndWait. This is the
	// default (the zero value of Restart) and matches the historical
	// mcp.Toolset behaviour.
	RestartOnFailure Restart = iota

	// RestartNever: the supervisor transitions to Failed when the session
	// ends and never reconnects.
	RestartNever

	// RestartAlways: reconnect even after a clean (nil) Wait result.
	RestartAlways
)

// Backoff parameters for restart attempts. Zero values fall back to the
// defaults that match the historical MCP behaviour: 1s, 2s, 4s, 8s, 16s.
type Backoff struct {
	Initial    time.Duration // first wait (default 1s)
	Max        time.Duration // cap (default 32s)
	Multiplier float64       // (default 2.0)
	Jitter     float64       // 0..1 fraction of the delay; 0 disables (default 0)
}

// delay returns the wait time before attempt n (0-based).
func (b Backoff) delay(attempt int, randFloat func() float64) time.Duration {
	initial := b.Initial
	if initial <= 0 {
		initial = time.Second
	}
	mul := b.Multiplier
	if mul <= 0 {
		mul = 2
	}
	maxDelay := b.Max
	if maxDelay <= 0 {
		maxDelay = 32 * time.Second
	}
	d := time.Duration(float64(initial) * math.Pow(mul, float64(attempt)))
	d = min(d, maxDelay)
	if b.Jitter > 0 {
		// Apply ±Jitter * d random offset.
		j := b.Jitter
		if j > 1 {
			j = 1
		}
		offset := (randFloat()*2 - 1) * j * float64(d)
		d = time.Duration(float64(d) + offset)
		d = max(d, 0)
	}
	return d
}

// Policy controls how a Supervisor manages a connection over time.
//
// All fields are optional: the zero value gives the historical
// mcp.Toolset behaviour (RestartOnFailure, 5 attempts, 1s..32s backoff,
// no jitter, no callbacks).
type Policy struct {
	// Restart controls reconnect behaviour. Defaults to RestartOnFailure.
	Restart Restart

	// MaxAttempts is the maximum number of consecutive restart attempts
	// after a disconnect. Zero means use the default (5). A negative value
	// disables the limit.
	MaxAttempts int

	// Backoff controls the inter-attempt wait. Zero fields use defaults.
	Backoff Backoff

	// OnDisconnect, when non-nil, is called when the session ends. It
	// receives the Wait() result. Useful for cache invalidation.
	OnDisconnect func(err error)

	// OnRestart, when non-nil, is called after each successful reconnect.
	// Useful for re-fetching server-side state (tools, prompts).
	OnRestart func()

	// OnFailed, when non-nil, is called once the supervisor has given up
	// restarting (state moved to Failed).
	OnFailed func(err error)

	// Logger is used for lifecycle logs. Defaults to slog.Default().
	Logger *slog.Logger
}

func (p Policy) restart() Restart { return p.Restart }

func (p Policy) maxAttempts() int {
	if p.MaxAttempts == 0 {
		return 5
	}
	return p.MaxAttempts
}

func (p Policy) logger() *slog.Logger {
	if p.Logger != nil {
		return p.Logger
	}
	return slog.Default()
}

// Supervisor manages the lifecycle of a single connection: initial connect,
// watcher goroutine, restart with backoff, graceful Stop.
//
// It is the shared implementation for MCP (stdio + remote) and LSP
// transports; per-transport behaviour is captured in the Connector.
//
// Supervisor is safe for concurrent use. Public methods are short, hold
// the lock only for state transitions, and never block on I/O.
type Supervisor struct {
	name      string
	connector Connector
	policy    Policy
	tracker   *Tracker

	// startMu serializes Start calls so two concurrent first-callers do
	// not both invoke Connector.Connect.
	startMu sync.Mutex

	// mu guards the rest of the fields.
	mu           sync.Mutex
	session      Session
	stopping     bool
	watcherAlive bool
	forceRestart bool          // set by RestartAndWait so the watcher reconnects
	restarted    chan struct{} // closed and replaced on each successful restart

	// done is closed when the supervisor enters a terminal state (Stopped
	// via Stop, or Failed because tryRestart gave up). It lets
	// RestartAndWait return promptly instead of waiting for its timeout
	// when no further restart will ever happen.
	//
	// done is replaced with a fresh channel when Start brings the
	// supervisor back out of a terminal state, so the same supervisor
	// can be Failed → Start → Failed → Start without RestartAndWait
	// seeing a stale closed channel. mu protects writes; readers either
	// hold mu when capturing the reference or rely on it being captured
	// inside the same critical section.
	done chan struct{}

	// randFloat is used for Backoff jitter; tests may override.
	randFloat func() float64
}

// New returns a Supervisor that drives connector with policy. The name is
// used in lifecycle log messages and should uniquely identify the toolset.
func New(name string, connector Connector, policy Policy) *Supervisor {
	return &Supervisor{
		name:      name,
		connector: connector,
		policy:    policy,
		tracker:   NewTracker(),
		randFloat: rand.Float64,
		restarted: make(chan struct{}),
		done:      make(chan struct{}),
	}
}

// signalDone closes the done channel if it is not already closed. It is
// the only way to transition the supervisor into a terminal-from
// -RestartAndWait's-view state; callers should signalDone whenever they
// enter Stopped or Failed.
//
// It takes mu so that a concurrent Start can replace `done` without
// racing with the close.
func (s *Supervisor) signalDone() {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.done:
		// Already closed; nothing to do.
	default:
		close(s.done)
	}
}

// State returns a snapshot of the supervisor's current state.
func (s *Supervisor) State() StateInfo { return s.tracker.Snapshot() }

// IsReady reports whether the supervisor is in a state that should serve
// requests (Ready or Degraded).
func (s *Supervisor) IsReady() bool { return s.tracker.State().IsUsable() }

// MarkReadyForTesting forces the supervisor into StateReady without going
// through Connect. It is intended only for tests that exercise per-request
// code paths in code that depends on a started supervisor without driving
// the full Connect/Wait lifecycle. Production code must not call this.
//
// The supervisor's session is left nil; callers that need real Wait/Close
// behaviour should use a Connector via Start.
func (s *Supervisor) MarkReadyForTesting() {
	s.tracker.Set(StateReady)
}

// Restarted returns a channel that is closed the next time the supervisor
// completes a successful restart. The returned channel is replaced after
// each restart, so callers should re-read it on each new wait.
//
// This is used by RestartAndWait and by callers that need to coordinate
// with reconnects (e.g. retrying a tool call after ErrSessionMissing).
func (s *Supervisor) Restarted() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restarted
}

// Start performs the initial connect. If the underlying Connector returns
// an error, Start propagates it and the supervisor remains in StateStopped
// (the caller is expected to retry, e.g. on the next conversation turn).
//
// On success the watcher goroutine is launched (if not already alive) and
// the supervisor enters StateReady.
//
// Concurrent Start calls serialize via an internal mutex.
func (s *Supervisor) Start(ctx context.Context) error {
	s.startMu.Lock()
	defer s.startMu.Unlock()

	s.mu.Lock()
	if s.session != nil {
		s.mu.Unlock()
		return nil
	}
	if s.stopping {
		s.mu.Unlock()
		return ErrNotStarted
	}
	s.mu.Unlock()

	s.tracker.Set(StateStarting)

	// Detach from caller's ctx for the connection itself: the session
	// must outlive the request that triggered Start (e.g. an HTTP handler).
	// The connect *handshake* still uses the caller's ctx so cancellation
	// is honoured during initialize.
	sess, err := s.connector.Connect(ctx)
	if err != nil {
		s.tracker.Fail(StateStopped, err)
		return err
	}

	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		_ = sess.Close(context.WithoutCancel(ctx))
		s.tracker.Set(StateStopped)
		return ErrNotStarted
	}
	s.session = sess
	spawnWatcher := !s.watcherAlive
	if spawnWatcher {
		s.watcherAlive = true
	}
	// Recovering from a terminal state (Failed → Start, or a watcher
	// that previously exited): refresh `done` so RestartAndWait callers
	// don't see a stale close, and clear forceRestart so a leftover
	// flag from a prior session doesn't force-restart this fresh one
	// on its first disconnect.
	select {
	case <-s.done:
		s.done = make(chan struct{})
	default:
	}
	s.forceRestart = false
	s.mu.Unlock()

	s.tracker.Set(StateReady)
	s.tracker.ResetRestarts()

	if spawnWatcher {
		// The watcher must outlive ctx; the only way to stop it is Stop.
		watcherCtx := context.WithoutCancel(ctx)
		go s.watch(watcherCtx)
	}

	s.policy.logger().Debug("supervisor: ready", "name", s.name)
	return nil
}

// Stop tears the supervisor down. It is idempotent and safe to call
// regardless of state. Stop blocks until the underlying session is closed.
func (s *Supervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return nil
	}
	s.stopping = true
	sess := s.session
	s.session = nil
	s.mu.Unlock()

	s.tracker.Set(StateStopped)
	s.signalDone()

	if sess == nil {
		return nil
	}
	if err := sess.Close(context.WithoutCancel(ctx)); err != nil {
		// Honor cancellation: a context-cancelled close is not an error.
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	return nil
}

// RestartAndWait closes the current session (if any) so the watcher
// reconnects, then blocks until the next successful reconnect, ctx
// cancellation, supervisor shutdown (Stop or Failed), or timeout.
// It is the supervisor-level analogue of the previous
// forceReconnectAndWait helper.
//
// Note: RestartAndWait does NOT recover the supervisor from a terminal
// state (Stopped/Failed). Callers that want "restart even if Failed"
// should consult State() first and call Start when terminal; the
// Toolset.Restart implementations in pkg/tools/mcp and pkg/tools/builtin
// do exactly this.
func (s *Supervisor) RestartAndWait(ctx context.Context, timeout time.Duration) error {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return ErrNotStarted
	}
	restartCh := s.restarted
	doneCh := s.done
	state := s.tracker.State()
	sess := s.session
	// Mark the next disconnect as forced so the watcher reconnects even
	// if Wait() returns nil (Close on a clean session typically does).
	s.forceRestart = true
	s.mu.Unlock()

	// Only force-close if we're currently Ready/Degraded. If the watcher
	// has already detected the disconnect (Restarting) we must not close
	// a connection that tryRestart may be establishing concurrently.
	if state.IsUsable() && sess != nil {
		_ = sess.Close(context.WithoutCancel(ctx))
	}

	select {
	case <-restartCh:
		return nil
	case <-doneCh:
		// Stop or terminal Failed; report the latest error if any so
		// the caller can surface it.
		if err := s.tracker.LastError(); err != nil {
			return err
		}
		return ErrNotStarted
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(timeout):
		return errors.New("timed out waiting for supervisor reconnect")
	}
}

// watch runs in a single goroutine for the lifetime of the supervisor,
// from first Start until Stop. It blocks on session.Wait, reacts to
// disconnects, and triggers restarts according to policy.
func (s *Supervisor) watch(ctx context.Context) {
	defer func() {
		s.mu.Lock()
		s.watcherAlive = false
		s.mu.Unlock()
	}()

	log := s.policy.logger()

	for {
		s.mu.Lock()
		sess := s.session
		s.mu.Unlock()
		if sess == nil {
			// Defensive: should not happen because Start always sets a session
			// before spawning the watcher and tryRestart sets it before
			// returning true.
			return
		}

		waitErr := sess.Wait()

		s.mu.Lock()
		if s.stopping {
			s.mu.Unlock()
			return
		}
		forced := s.forceRestart
		s.forceRestart = false
		s.session = nil
		s.mu.Unlock()

		s.tracker.Fail(StateRestarting, waitErr)
		log.Warn("supervisor: session lost", "name", s.name, "error", waitErr, "forced", forced)

		if cb := s.policy.OnDisconnect; cb != nil {
			cb(waitErr)
		}

		if !forced && !s.shouldRestart(waitErr) {
			s.tracker.Fail(StateFailed, waitErr)
			if cb := s.policy.OnFailed; cb != nil {
				cb(waitErr)
			}
			s.signalDone()
			return
		}

		if !s.tryRestart(ctx) {
			// tryRestart already set Failed/Stopped as appropriate.
			return
		}

		if cb := s.policy.OnRestart; cb != nil {
			cb()
		}
	}
}

func (s *Supervisor) shouldRestart(err error) bool {
	switch s.policy.restart() {
	case RestartNever:
		return false
	case RestartAlways:
		return true
	case RestartOnFailure:
		// Match historical behaviour: any non-nil Wait return triggers
		// restart. A nil error means a clean close, which we treat as
		// "no restart" unless RestartAlways is set.
		if err == nil {
			return false
		}
		// Permanent classifications never restart.
		if IsPermanent(err) {
			return false
		}
		return true
	}
	return false
}

// tryRestart loops with backoff. Returns true once a successful restart
// has happened (and updates state to Ready), or false if it gave up
// (Failed) or Stop was called (Stopped).
func (s *Supervisor) tryRestart(ctx context.Context) bool {
	maxAttempts := s.policy.maxAttempts()
	log := s.policy.logger()

	for attempt := 0; ; attempt++ {
		if maxAttempts > 0 && attempt >= maxAttempts {
			log.Error("supervisor: giving up after max attempts",
				"name", s.name, "attempts", attempt)
			lastErr := s.tracker.LastError()
			s.tracker.Fail(StateFailed, lastErr)
			if cb := s.policy.OnFailed; cb != nil {
				cb(lastErr)
			}
			s.signalDone()
			return false
		}

		delay := s.policy.Backoff.delay(attempt, s.randFloat)
		log.Debug("supervisor: restart attempt",
			"name", s.name, "attempt", attempt+1, "backoff", delay)

		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return false
		}

		s.mu.Lock()
		if s.stopping {
			s.mu.Unlock()
			return false
		}
		s.mu.Unlock()

		sess, err := s.connector.Connect(ctx)
		if err != nil {
			s.tracker.Fail(StateRestarting, err)
			s.tracker.IncRestarts()
			log.Warn("supervisor: restart failed",
				"name", s.name, "attempt", attempt+1, "error", err)
			continue
		}

		s.mu.Lock()
		if s.stopping {
			s.mu.Unlock()
			_ = sess.Close(context.WithoutCancel(ctx))
			return false
		}
		s.session = sess
		// Signal anyone waiting via Restarted() / RestartAndWait.
		close(s.restarted)
		s.restarted = make(chan struct{})
		s.mu.Unlock()

		s.tracker.Set(StateReady)
		s.tracker.ResetRestarts()
		log.Info("supervisor: restarted",
			"name", s.name, "attempt", attempt+1)
		return true
	}
}
