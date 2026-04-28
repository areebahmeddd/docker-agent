package lifecycle_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/docker/docker-agent/pkg/tools/lifecycle"
)

// fakeSession is a controllable session: its Wait blocks until either
// Close is called or fail is invoked.
type fakeSession struct {
	mu     sync.Mutex
	closed bool
	failCh chan error
}

func newFakeSession() *fakeSession {
	return &fakeSession{failCh: make(chan error, 1)}
}

func (f *fakeSession) Wait() error {
	err := <-f.failCh
	return err
}

func (f *fakeSession) Close(context.Context) error {
	f.mu.Lock()
	if !f.closed {
		f.closed = true
		// Closed sessions return nil from Wait by convention.
		select {
		case f.failCh <- nil:
		default:
		}
	}
	f.mu.Unlock()
	return nil
}

func (f *fakeSession) fail(err error) {
	select {
	case f.failCh <- err:
	default:
	}
}

// scriptedConnector returns sessions and errors from a scripted slice.
type scriptedConnector struct {
	mu        sync.Mutex
	scripts   []scriptStep
	idx       int
	calls     int32
	delivered chan *fakeSession
}

type scriptStep struct {
	err     error
	session *fakeSession
}

func newScriptedConnector(steps ...scriptStep) *scriptedConnector {
	return &scriptedConnector{
		scripts:   steps,
		delivered: make(chan *fakeSession, len(steps)),
	}
}

func (c *scriptedConnector) Connect(context.Context) (lifecycle.Session, error) {
	atomic.AddInt32(&c.calls, 1)
	c.mu.Lock()
	if c.idx >= len(c.scripts) {
		c.mu.Unlock()
		return nil, errors.New("scripted connector exhausted")
	}
	step := c.scripts[c.idx]
	c.idx++
	c.mu.Unlock()

	if step.err != nil {
		return nil, step.err
	}
	c.delivered <- step.session
	return step.session, nil
}

func (c *scriptedConnector) Calls() int { return int(atomic.LoadInt32(&c.calls)) }

// fastBackoff is a minimal backoff for tests so we don't sit in time.Sleep.
var fastBackoff = lifecycle.Backoff{
	Initial:    1 * time.Millisecond,
	Max:        2 * time.Millisecond,
	Multiplier: 2,
}

func TestSupervisor_StartFailurePropagates(t *testing.T) {
	t.Parallel()

	want := errors.New("boom")
	c := newScriptedConnector(scriptStep{err: want})
	s := lifecycle.New("test", c, lifecycle.Policy{})

	err := s.Start(t.Context())
	assert.Check(t, errors.Is(err, want))
	assert.Check(t, is.Equal(s.State().State, lifecycle.StateStopped))
}

func TestSupervisor_StartSucceedsAndReadies(t *testing.T) {
	t.Parallel()

	sess := newFakeSession()
	c := newScriptedConnector(scriptStep{session: sess})
	s := lifecycle.New("test", c, lifecycle.Policy{})

	assert.NilError(t, s.Start(t.Context()))
	assert.Check(t, is.Equal(s.State().State, lifecycle.StateReady))
	assert.Check(t, s.IsReady())
	assert.NilError(t, s.Stop(t.Context()))
	assert.Check(t, is.Equal(s.State().State, lifecycle.StateStopped))
}

func TestSupervisor_StartIsIdempotent(t *testing.T) {
	t.Parallel()

	sess := newFakeSession()
	c := newScriptedConnector(scriptStep{session: sess})
	s := lifecycle.New("test", c, lifecycle.Policy{})

	assert.NilError(t, s.Start(t.Context()))
	assert.NilError(t, s.Start(t.Context()))
	assert.Check(t, is.Equal(c.Calls(), 1))
	assert.NilError(t, s.Stop(t.Context()))
}

func TestSupervisor_RestartAfterDisconnect(t *testing.T) {
	t.Parallel()

	sess1 := newFakeSession()
	sess2 := newFakeSession()
	c := newScriptedConnector(
		scriptStep{session: sess1},
		scriptStep{session: sess2},
	)

	restarted := make(chan struct{}, 1)
	s := lifecycle.New("test", c, lifecycle.Policy{
		Backoff: fastBackoff,
		OnRestart: func() {
			select {
			case restarted <- struct{}{}:
			default:
			}
		},
	})

	assert.NilError(t, s.Start(t.Context()))

	// Make session 1 fail; supervisor should reconnect to session 2.
	sess1.fail(errors.New("crash"))

	select {
	case <-restarted:
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor did not restart")
	}

	assert.Check(t, is.Equal(s.State().State, lifecycle.StateReady))
	assert.Check(t, is.Equal(c.Calls(), 2))

	assert.NilError(t, s.Stop(t.Context()))
}

func TestSupervisor_GivesUpAfterMaxAttempts(t *testing.T) {
	t.Parallel()

	sess1 := newFakeSession()
	c := newScriptedConnector(
		scriptStep{session: sess1},
		scriptStep{err: errors.New("fail-1")},
		scriptStep{err: errors.New("fail-2")},
		scriptStep{err: errors.New("fail-3")},
	)

	failed := make(chan error, 1)
	s := lifecycle.New("test", c, lifecycle.Policy{
		MaxAttempts: 3,
		Backoff:     fastBackoff,
		OnFailed: func(err error) {
			select {
			case failed <- err:
			default:
			}
		},
	})

	assert.NilError(t, s.Start(t.Context()))
	sess1.fail(errors.New("crash"))

	select {
	case <-failed:
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor did not call OnFailed")
	}

	assert.Check(t, is.Equal(s.State().State, lifecycle.StateFailed))
}

func TestSupervisor_RestartNeverGoesToFailed(t *testing.T) {
	t.Parallel()

	sess1 := newFakeSession()
	c := newScriptedConnector(scriptStep{session: sess1})

	failed := make(chan struct{}, 1)
	s := lifecycle.New("test", c, lifecycle.Policy{
		Restart: lifecycle.RestartNever,
		OnFailed: func(error) {
			select {
			case failed <- struct{}{}:
			default:
			}
		},
	})

	assert.NilError(t, s.Start(t.Context()))
	sess1.fail(errors.New("crash"))

	select {
	case <-failed:
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor did not transition to Failed")
	}

	assert.Check(t, is.Equal(s.State().State, lifecycle.StateFailed))
	assert.Check(t, is.Equal(c.Calls(), 1))
}

func TestSupervisor_RestartAndWait(t *testing.T) {
	t.Parallel()

	sess1 := newFakeSession()
	sess2 := newFakeSession()
	c := newScriptedConnector(
		scriptStep{session: sess1},
		scriptStep{session: sess2},
	)
	s := lifecycle.New("test", c, lifecycle.Policy{Backoff: fastBackoff})

	assert.NilError(t, s.Start(t.Context()))

	err := s.RestartAndWait(t.Context(), 2*time.Second)
	assert.NilError(t, err)
	assert.Check(t, is.Equal(s.State().State, lifecycle.StateReady))

	assert.NilError(t, s.Stop(t.Context()))
}

func TestSupervisor_StopIdempotent(t *testing.T) {
	t.Parallel()

	sess := newFakeSession()
	c := newScriptedConnector(scriptStep{session: sess})
	s := lifecycle.New("test", c, lifecycle.Policy{})

	assert.NilError(t, s.Start(t.Context()))
	assert.NilError(t, s.Stop(t.Context()))
	assert.NilError(t, s.Stop(t.Context()))
}

func TestSupervisor_StopBeforeStart(t *testing.T) {
	t.Parallel()
	c := newScriptedConnector()
	s := lifecycle.New("test", c, lifecycle.Policy{})
	assert.NilError(t, s.Stop(t.Context()))
}

// TestSupervisor_PermanentErrorsDontRestart verifies that wait errors that
// are classified as Permanent (e.g. ErrAuthRequired) cause the supervisor
// to enter Failed without consuming restart attempts.
func TestSupervisor_PermanentErrorsDontRestart(t *testing.T) {
	t.Parallel()

	sess1 := newFakeSession()
	c := newScriptedConnector(scriptStep{session: sess1})

	failedCh := make(chan error, 1)
	s := lifecycle.New("test", c, lifecycle.Policy{
		Backoff: fastBackoff,
		OnFailed: func(err error) {
			select {
			case failedCh <- err:
			default:
			}
		},
	})

	assert.NilError(t, s.Start(t.Context()))
	sess1.fail(lifecycle.ErrAuthRequired)

	select {
	case got := <-failedCh:
		assert.Check(t, errors.Is(got, lifecycle.ErrAuthRequired))
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor did not transition to Failed")
	}
	assert.Check(t, is.Equal(c.Calls(), 1), "must not retry on permanent error")
}

func TestBackoff_Defaults(t *testing.T) {
	t.Parallel()

	b := lifecycle.Backoff{}
	d0 := lifecycle.ExportedBackoffDelay(b, 0, func() float64 { return 0 })
	d1 := lifecycle.ExportedBackoffDelay(b, 1, func() float64 { return 0 })
	d2 := lifecycle.ExportedBackoffDelay(b, 2, func() float64 { return 0 })
	d6 := lifecycle.ExportedBackoffDelay(b, 6, func() float64 { return 0 })

	assert.Check(t, is.Equal(d0, time.Second))
	assert.Check(t, is.Equal(d1, 2*time.Second))
	assert.Check(t, is.Equal(d2, 4*time.Second))
	// 1<<6 = 64s, capped to default Max = 32s.
	assert.Check(t, is.Equal(d6, 32*time.Second))
}

func TestBackoff_Jitter(t *testing.T) {
	t.Parallel()

	b := lifecycle.Backoff{Initial: 100 * time.Millisecond, Jitter: 0.5}
	// random ≈ 1 → +50% offset
	d := lifecycle.ExportedBackoffDelay(b, 0, func() float64 { return 1 })
	assert.Check(t, d == 150*time.Millisecond)

	// random ≈ 0 → -50% offset
	d = lifecycle.ExportedBackoffDelay(b, 0, func() float64 { return 0 })
	assert.Check(t, d == 50*time.Millisecond)
}
