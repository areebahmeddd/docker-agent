package root

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/docker/docker-agent/pkg/cli"
	"github.com/docker/docker-agent/pkg/runregistry"
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
  docker-agent watch --to 12345 | jq
  docker-agent watch --to http://127.0.0.1:8765`,
		GroupID: "advanced",
		Args:    cobra.NoArgs,
		RunE:    flags.run,
	}

	cmd.Flags().StringVar(&flags.target, "to", "", targetFlagUsage)
	return cmd
}

func (f *watchFlags) run(cmd *cobra.Command, args []string) (commandErr error) {
	ctx := cmd.Context()
	telemetry.TrackCommand(ctx, "watch", args)
	defer func() { telemetry.TrackCommandError(ctx, "watch", args, commandErr) }()

	rec, err := runregistry.Find(f.target)
	if err != nil {
		return err
	}

	body, err := openEventStream(ctx, rec.Addr, rec.SessionID)
	if err != nil {
		return err
	}
	defer body.Close()

	out := cli.NewPrinter(cmd.OutOrStdout())
	out.Println("Watching", rec.Addr, "(session", rec.SessionID+")")

	stdout := cmd.OutOrStdout()
	return readEventStream(ctx, body, func(payload json.RawMessage) error {
		_, err := fmt.Fprintln(stdout, string(payload))
		return err
	})
}
