package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/docker/docker-agent/pkg/session"
)

var ErrNothingToUndo = errors.New("nothing to undo")

type UndoSnapshotResult struct {
	RestoredFiles int
}

type snapshotUndoer interface {
	UndoLastSnapshot(ctx context.Context, sess *session.Session) (files int, ok bool, err error)
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
