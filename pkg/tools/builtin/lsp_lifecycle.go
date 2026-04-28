package builtin

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"

	"github.com/docker/docker-agent/pkg/concurrent"
	"github.com/docker/docker-agent/pkg/tools/lifecycle"
)

// lspConnector adapts the LSP server lifecycle to lifecycle.Connector. It
// spawns the LSP server process, runs the initialize/initialized
// handshake, and returns an lspSession that the supervisor can Wait on
// and Close.
type lspConnector struct {
	h *lspHandler
}

// Connect spawns the LSP server, performs the LSP handshake, and returns
// a Session bound to the running process.
//
// On success the active process state (cmd, stdin, stdout, capabilities,
// serverInfo) is published on h under h.mu so that per-request methods
// can use them without going through the supervisor.
func (c *lspConnector) Connect(ctx context.Context) (lifecycle.Session, error) {
	h := c.h

	slog.Debug("Starting LSP server", "command", h.command, "args", h.args)

	// The process must outlive the caller's request context (which is
	// often cancelled when an HTTP/agent turn ends). The supervisor calls
	// Close() to shut it down on Stop or restart.
	processCtx, processCancel := context.WithCancel(context.Background())

	cmd := exec.CommandContext(processCtx, h.command, h.args...)
	cmd.Env = append(os.Environ(), h.env...)
	cmd.Dir = h.workingDir

	stdin, err := cmd.StdinPipe()
	if err != nil {
		processCancel()
		return nil, fmt.Errorf("%w: stdin pipe: %w", lifecycle.ErrServerUnavailable, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		processCancel()
		return nil, fmt.Errorf("%w: stdout pipe: %w", lifecycle.ErrServerUnavailable, err)
	}

	stderrBuf := &concurrent.Buffer{}
	cmd.Stderr = stderrBuf

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		processCancel()
		// exec.ErrNotFound / fs.PathError → unavailable, supervisor backs off.
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %w", lifecycle.ErrServerUnavailable, err)
		}
		return nil, fmt.Errorf("failed to start LSP server: %w", err)
	}

	bufStdout := bufio.NewReader(stdout)

	// Publish the active session state under h.mu so per-request methods
	// see consistent fields (cmd / stdin / stdout, etc).
	h.mu.Lock()
	h.cmd = cmd
	h.cancel = processCancel
	h.stdin = stdin
	h.stdout = bufStdout
	h.initialized.Store(false)
	// Reset open-files state: a fresh server has no knowledge of files
	// the previous one had open.
	h.openFilesMu.Lock()
	h.openFiles = make(map[string]int)
	h.openFilesMu.Unlock()
	h.capabilities = nil
	h.serverInfo = nil
	h.mu.Unlock()

	// Drain stderr in a separate goroutine; exits when processCtx is done.
	go h.readNotifications(processCtx, stderrBuf)

	// Honour ctx cancellation during the handshake by closing stdin if
	// the caller goes away. The supervisor's watcher uses a detached ctx
	// for restarts, so cancellation here means the user pressed Ctrl-C
	// during the initial Start.
	handshakeDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = stdin.Close()
		case <-handshakeDone:
		}
	}()

	// Run initialize+initialized under h.mu so that no concurrent
	// per-request method tries to send to stdin until we are ready.
	h.mu.Lock()
	err = h.initializeLocked()
	h.mu.Unlock()
	close(handshakeDone)

	if err != nil {
		// Tear down the partially-started session, including the handler
		// fields we just published so a subsequent Start sees a clean slate.
		h.mu.Lock()
		h.cmd = nil
		h.stdin = nil
		h.stdout = nil
		h.cancel = nil
		h.initialized.Store(false)
		h.mu.Unlock()
		_ = stdin.Close()
		processCancel()
		_ = cmd.Wait()
		// Map handshake failures to typed errors so the supervisor's
		// policy can react (init notification → retryable, ctx cancel →
		// abort).
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, lifecycle.Classify(err)
	}

	slog.Debug("LSP server initialized", "command", h.command)

	// Notify the runtime that the tool catalogue may have changed: the
	// capabilities we just received gate which lsp_* tools are visible.
	// This fires on both initial connect and reconnect, so a model that
	// was given the full catalogue before init now sees the refined one.
	h.mu.Lock()
	handler := h.toolsChangedHandler
	h.mu.Unlock()
	if handler != nil {
		handler()
	}

	return &lspSession{h: h, processCancel: processCancel, stdin: stdin}, nil
}

// lspSession is a single live LSP server session. Its Wait blocks on the
// process exiting; its Close performs the LSP shutdown handshake and
// terminates the process.
type lspSession struct {
	h             *lspHandler
	processCancel context.CancelFunc
	stdin         io.WriteCloser

	mu     sync.Mutex
	closed bool
	// waitDone is closed once cmd.Wait has returned; reading from it
	// either returns the result or blocks until the wait completes.
	waitOnce sync.Once
	waitErr  error
	waitDone chan struct{}
}

// Wait blocks until the LSP process exits and returns the exit status,
// mapping clean exits and signal-induced exits to nil/typed errors as
// the supervisor expects.
func (s *lspSession) Wait() error {
	s.waitOnce.Do(func() {
		s.waitDone = make(chan struct{})
		go func() {
			defer close(s.waitDone)
			s.h.mu.Lock()
			cmd := s.h.cmd
			s.h.mu.Unlock()
			if cmd == nil {
				return
			}
			err := cmd.Wait()
			if err != nil {
				// An *exec.ExitError after a signal-induced shutdown
				// (Close→cancel) is expected; treat it as a clean exit
				// so the supervisor only restarts on actual crashes.
				s.mu.Lock()
				closed := s.closed
				s.mu.Unlock()
				if closed {
					s.waitErr = nil
					return
				}
				s.waitErr = fmt.Errorf("%w: %w", lifecycle.ErrServerCrashed, err)
				return
			}
			s.waitErr = nil
		}()
	})
	<-s.waitDone
	return s.waitErr
}

// Close performs the LSP shutdown handshake and tears down the process.
// It is idempotent and safe to call concurrently with an in-flight Wait.
func (s *lspSession) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	slog.Debug("Stopping LSP server")

	h := s.h
	h.mu.Lock()
	if h.initialized.Load() {
		// Best-effort shutdown handshake; ignore errors because the
		// process is going away regardless.
		_, _ = h.sendRequestLocked("shutdown", nil)
		_ = h.sendNotificationLocked("exit", nil)
	}
	cmd := h.cmd
	h.cancel = nil
	h.cmd = nil
	h.stdin = nil
	h.stdout = nil
	h.initialized.Store(false)
	h.openFilesMu.Lock()
	h.openFiles = make(map[string]int)
	h.openFilesMu.Unlock()
	h.capabilities = nil
	h.serverInfo = nil
	h.mu.Unlock()

	_ = s.stdin.Close()
	s.processCancel()

	if cmd != nil {
		// Wait for the process to exit so resources are released.
		_ = cmd.Wait()
	}

	// Honour cancellation: a context-cancelled close is not an error.
	if ctx.Err() != nil {
		return nil
	}
	slog.Debug("LSP server stopped")
	return nil
}

// readLspProcessHandle is exported for tests in the package to inspect
// per-session state when verifying lifecycle behaviour. It is unused in
// production paths.
//
// minimise churn between commits.
//
//nolint:unused // consumed by upcoming lifecycle tests; kept now to
func (h *lspHandler) readLspProcessHandle() *exec.Cmd {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cmd
}
