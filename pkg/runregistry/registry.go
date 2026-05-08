// Package runregistry persists discovery records for running docker-agent
// processes that expose a control plane (see run --listen). Records live as
// per-pid JSON files under <data dir>/runs so external tools can enumerate
// live runs and connect to them.
package runregistry

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/docker/docker-agent/pkg/paths"
)

// Record describes a running docker-agent that exposes a control plane.
type Record struct {
	PID       int       `json:"pid"`
	Addr      string    `json:"addr"`
	SessionID string    `json:"session_id"`
	Agent     string    `json:"agent,omitempty"`
	StartedAt time.Time `json:"started_at"`
}

// Dir is the directory holding run records.
func Dir() string {
	return filepath.Join(paths.GetDataDir(), "runs")
}

// Write atomically persists a record for the current process and returns a
// cleanup func that removes it. Cleanup is safe to call more than once.
func Write(rec Record) (func(), error) {
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return nil, fmt.Errorf("creating run registry dir: %w", err)
	}

	path := filepath.Join(Dir(), strconv.Itoa(rec.PID)+".json")
	buf, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		return nil, err
	}

	return func() { _ = os.Remove(path) }, nil
}

// List returns every record currently registered. Stale records (whose pid is
// no longer alive) are skipped and best-effort removed.
func List() ([]Record, error) {
	entries, err := os.ReadDir(Dir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var out []Record
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(Dir(), e.Name())
		buf, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var rec Record
		if err := json.Unmarshal(buf, &rec); err != nil {
			continue
		}
		if !pidAlive(rec.PID) {
			_ = os.Remove(path)
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

// Latest returns the most recently started live record, or false when none.
func Latest() (Record, bool, error) {
	records, err := List()
	if err != nil || len(records) == 0 {
		return Record{}, false, err
	}
	latest := records[0]
	for _, r := range records[1:] {
		if r.StartedAt.After(latest.StartedAt) {
			latest = r
		}
	}
	return latest, true, nil
}

// pidAlive reports whether the given pid corresponds to a live process.
// Uses os.FindProcess + signal 0, the cross-platform idiom for liveness.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
