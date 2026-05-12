package root

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/spf13/cobra"

	"github.com/docker/docker-agent/pkg/api"
	"github.com/docker/docker-agent/pkg/runregistry"
	"github.com/docker/docker-agent/pkg/runtime"
	"github.com/docker/docker-agent/pkg/telemetry"
)

type protoFlags struct {
	target string
}

func newProtoCmd() *cobra.Command {
	var flags protoFlags

	cmd := &cobra.Command{
		Use:   "proto",
		Short: "Drive a running docker-agent TUI over stdio JSON-RPC",
		Long: `Read newline-delimited JSON requests from stdin and write events and
responses to stdout. Designed for editor integrations and orchestrators
that want a single-process channel to a live run started with --listen.

Each input line is a JSON object with at least:
  {"id": "1", "type": "send", "message": "hello"}

Supported types: send, followup, resume, transcript, interrupt.
Output objects always carry a "type" field.`,
		GroupID: "advanced",
		Args:    cobra.NoArgs,
		RunE:    flags.run,
	}

	cmd.Flags().StringVar(&flags.target, "to", "", targetFlagUsage)
	return cmd
}

// targetFlagUsage is the canonical help text for --to.
const targetFlagUsage = "Target run pid, address (http://host:port), or session id (defaults to the most recent live run)"

func (f *protoFlags) run(cmd *cobra.Command, args []string) (commandErr error) {
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()
	telemetry.TrackCommand(ctx, "proto", args)
	defer func() { telemetry.TrackCommandError(ctx, "proto", args, commandErr) }()

	rec, err := runregistry.Find(f.target)
	if err != nil {
		return err
	}
	client, err := runtime.NewClient(rec.Addr)
	if err != nil {
		return err
	}

	w := newProtoWriter(cmd.OutOrStdout())
	w.send(map[string]any{
		"type":       "ready",
		"addr":       rec.Addr,
		"session_id": rec.SessionID,
	})

	go streamEvents(ctx, rec.Addr, rec.SessionID, w)
	return readCommands(ctx, cmd.InOrStdin(), client, rec.SessionID, w)
}

type protoWriter struct {
	mu  sync.Mutex
	out io.Writer
}

func newProtoWriter(out io.Writer) *protoWriter { return &protoWriter{out: out} }

func (w *protoWriter) send(obj any) {
	data, err := json.Marshal(obj)
	if err != nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_, _ = w.out.Write(append(data, '\n'))
}

type protoRequest struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type"`
	Message  string `json:"message,omitempty"`
	Reason   string `json:"reason,omitempty"`
	ToolName string `json:"tool_name,omitempty"`
	Decision string `json:"decision,omitempty"` // "approve" | "reject"
	Limit    int    `json:"limit,omitempty"`
}

func readCommands(ctx context.Context, in io.Reader, client *runtime.Client, sessionID string, w *protoWriter) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return nil
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var req protoRequest
		if err := json.Unmarshal(line, &req); err != nil {
			w.send(map[string]any{"type": "error", "message": "invalid json: " + err.Error()})
			continue
		}

		handled, err := dispatchProto(ctx, client, sessionID, req, w)
		if err != nil {
			w.send(map[string]any{"id": req.ID, "type": "error", "message": err.Error()})
			continue
		}
		// Skip the generic ack when the dispatcher already produced a
		// typed reply (e.g. transcript), to avoid two responses per
		// request.
		if !handled {
			w.send(map[string]any{"id": req.ID, "type": "ack"})
		}
	}
	return scanner.Err()
}

// dispatchProto routes a request to the runtime client. The bool return is
// true when dispatchProto already produced a typed reply on w; in that case
// the caller MUST NOT emit a generic "ack".
func dispatchProto(ctx context.Context, client *runtime.Client, sessionID string, req protoRequest, w *protoWriter) (bool, error) {
	switch req.Type {
	case "send":
		return false, client.SteerSession(ctx, sessionID, []api.Message{{Content: req.Message}})
	case "followup":
		return false, client.FollowUpSession(ctx, sessionID, []api.Message{{Content: req.Message}})
	case "resume":
		decision := req.Decision
		if decision == "" {
			decision = "approve"
		}
		return false, client.ResumeSession(ctx, sessionID, decision, req.Reason, req.ToolName)
	case "interrupt":
		return false, client.ResumeSession(ctx, sessionID, "reject", req.Reason, req.ToolName)
	case "transcript":
		sess, err := client.GetSession(ctx, sessionID)
		if err != nil {
			return false, err
		}
		messages := sess.Messages
		if req.Limit > 0 && len(messages) > req.Limit {
			messages = messages[len(messages)-req.Limit:]
		}
		w.send(map[string]any{"id": req.ID, "type": "transcript", "title": sess.Title, "messages": messages})
		return true, nil
	default:
		return false, fmt.Errorf("unknown request type %q", req.Type)
	}
}

// streamEvents forwards every SSE event received from the run's control
// plane as a {"type":"event","event":<raw json>} envelope on w.
func streamEvents(ctx context.Context, addr, sessionID string, w *protoWriter) {
	body, err := openEventStream(ctx, addr, sessionID)
	if err != nil {
		return
	}
	defer body.Close()

	_ = readEventStream(ctx, body, func(payload json.RawMessage) error {
		w.send(map[string]any{"type": "event", "event": payload})
		return nil
	})
}
