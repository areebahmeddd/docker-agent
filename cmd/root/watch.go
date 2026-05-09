package root

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/docker/docker-agent/pkg/cli"
	"github.com/docker/docker-agent/pkg/telemetry"
)

type watchFlags struct {
	target string
}

func newWatchCmd() *cobra.Command {
	var flags watchFlags

	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Stream events from a running docker-agent TUI",
		Long: `Connect to the SSE event stream of the most recent docker-agent run that
exposes a control plane (started with run --listen) and print each event
as one JSON line on stdout.`,
		Example: `  docker-agent watch
  docker-agent watch --to 12345 | jq`,
		GroupID: "advanced",
		Args:    cobra.NoArgs,
		RunE:    flags.run,
	}

	cmd.Flags().StringVar(&flags.target, "to", "", "Target run pid (defaults to the most recent live run)")
	return cmd
}

func (f *watchFlags) run(cmd *cobra.Command, args []string) (commandErr error) {
	ctx := cmd.Context()
	telemetry.TrackCommand(ctx, "watch", args)
	defer func() { telemetry.TrackCommandError(ctx, "watch", args, commandErr) }()

	rec, err := resolveTarget(f.target)
	if err != nil {
		return err
	}

	url := rec.Addr + "/api/sessions/" + rec.SessionID + "/events"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %s: %s", resp.Status, string(body))
	}

	out := cli.NewPrinter(cmd.OutOrStdout())
	out.Println("Watching", rec.Addr, "(session", rec.SessionID+")")

	return printSSE(ctx, resp.Body, cmd.OutOrStdout())
}

func printSSE(ctx context.Context, body io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return nil
		}
		line := scanner.Bytes()
		after, ok := bytes.CutPrefix(line, []byte("data: "))
		if !ok {
			continue
		}
		if _, err := fmt.Fprintln(out, string(after)); err != nil {
			return err
		}
	}
	return scanner.Err()
}
