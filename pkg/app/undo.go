package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/docker/docker-agent/pkg/hooks/builtins"
	"github.com/docker/docker-agent/pkg/session"
)

var ErrNothingToUndo = errors.New("nothing to undo")

type UndoSnapshotResult struct {
	RestoredFiles int
}

// SnapshotInfo summarises one snapshot checkpoint for display.
type SnapshotInfo struct {
	// Files is the number of files captured in the checkpoint.
	Files int
}

type snapshotUndoer interface {
	UndoLastSnapshot(ctx context.Context, sess *session.Session) (files int, ok bool, err error)
}

type snapshotLister interface {
	ListSnapshots(sess *session.Session) []builtins.SnapshotInfo
}

type snapshotResetter interface {
	ResetSnapshot(ctx context.Context, sess *session.Session, keep int) (files int, ok bool, err error)
}

type snapshotsEnabledChecker interface {
	SnapshotsEnabled() bool
}

// SnapshotsEnabled reports whether automatic shadow-git snapshots are active
// for the current runtime. Returns false for runtimes that don't support
// snapshots at all (e.g. remote runtimes) or when the feature is turned off
// in the user config.
func (a *App) SnapshotsEnabled() bool {
	checker, ok := a.runtime.(snapshotsEnabledChecker)
	return ok && checker.SnapshotsEnabled()
}

func (a *App) UndoLastSnapshot(ctx context.Context) (UndoSnapshotResult, error) {
	if a.session == nil {
		return UndoSnapshotResult{}, ErrNothingToUndo
	}
	undoer, ok := a.runtime.(snapshotUndoer)
	if !ok {
		return UndoSnapshotResult{}, ErrNothingToUndo
	}
	files, ok, err := undoer.UndoLastSnapshot(ctx, a.session)
	if err != nil {
		return UndoSnapshotResult{}, fmt.Errorf("restoring snapshot: %w", err)
	}
	if !ok {
		return UndoSnapshotResult{}, ErrNothingToUndo
	}
	return UndoSnapshotResult{RestoredFiles: files}, nil
}

// ListSnapshots returns the snapshot checkpoints recorded for the current
// session in chronological order (oldest first). Returns nil when the runtime
// does not support snapshots or none have been captured yet.
func (a *App) ListSnapshots() []SnapshotInfo {
	if a.session == nil {
		return nil
	}
	lister, ok := a.runtime.(snapshotLister)
	if !ok {
		return nil
	}
	raw := lister.ListSnapshots(a.session)
	if len(raw) == 0 {
		return nil
	}
	out := make([]SnapshotInfo, len(raw))
	for i, s := range raw {
		out[i] = SnapshotInfo{Files: s.Files}
	}
	return out
}

// ResetSnapshot reverts every checkpoint past index keep so the workspace
// returns to the state captured at that snapshot. keep == 0 resets to the
// original pre-agent state. Returns ErrNothingToUndo when nothing changes.
func (a *App) ResetSnapshot(ctx context.Context, keep int) (UndoSnapshotResult, error) {
	if a.session == nil {
		return UndoSnapshotResult{}, ErrNothingToUndo
	}
	resetter, ok := a.runtime.(snapshotResetter)
	if !ok {
		return UndoSnapshotResult{}, ErrNothingToUndo
	}
	files, ok, err := resetter.ResetSnapshot(ctx, a.session, keep)
	if err != nil {
		return UndoSnapshotResult{}, fmt.Errorf("restoring snapshot: %w", err)
	}
	if !ok {
		return UndoSnapshotResult{}, ErrNothingToUndo
	}
	return UndoSnapshotResult{RestoredFiles: files}, nil
}
