package root

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/docker/docker-agent/pkg/api"
	"github.com/docker/docker-agent/pkg/cli"
	"github.com/docker/docker-agent/pkg/runregistry"
	"github.com/docker/docker-agent/pkg/runtime"
	"github.com/docker/docker-agent/pkg/telemetry"
)

type sendFlags struct {
	target   string
	followUp bool
}

func newSendCmd() *cobra.Command {
	var flags sendFlags

	cmd := &cobra.Command{
		Use:   "send [message]",
		Short: "Send a message to a running docker-agent TUI",
		Long: `Send a message to the most recent docker-agent run that exposes a control
plane (started with run --listen). Use - to read the message from stdin.

The target can be selected explicitly with --to, accepting a pid, an address
(http://host:port), or a session id. Without --to, the most recent live run
is used.`,
		Example: `  docker-agent send "summarize the diff"
  echo "and now write tests" | docker-agent send -
  docker-agent send --to 12345 "hello"
  docker-agent send --to http://127.0.0.1:8765 "hi"`,
		GroupID: "advanced",
		Args:    cobra.ExactArgs(1),
		RunE:    flags.run,
	}

	cmd.Flags().StringVar(&flags.target, "to", "", targetFlagUsage)
	cmd.Flags().BoolVar(&flags.followUp, "followup", false, "Queue as an end-of-turn follow-up instead of mid-turn steering")
	return cmd
}

// targetFlagUsage is the canonical help text for --to across send/watch/proto.
const targetFlagUsage = "Target run pid, address (http://host:port), or session id (defaults to the most recent live run)"

func (f *sendFlags) run(cmd *cobra.Command, args []string) (commandErr error) {
	ctx := cmd.Context()
	telemetry.TrackCommand(ctx, "send", args)
	defer func() { telemetry.TrackCommandError(ctx, "send", args, commandErr) }()

	message, err := readMessage(cmd.InOrStdin(), args[0])
	if err != nil {
		return err
	}

	rec, err := runregistry.Find(f.target)
	if err != nil {
		return err
	}

	client, err := runtime.NewClient(rec.Addr)
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}

	msgs := []api.Message{{Content: message}}
	if f.followUp {
		err = client.FollowUpSession(ctx, rec.SessionID, msgs)
	} else {
		err = client.SteerSession(ctx, rec.SessionID, msgs)
	}
	if err != nil {
		return err
	}

	out := cli.NewPrinter(cmd.OutOrStdout())
	out.Println("Sent to", rec.Addr, "(session", rec.SessionID+")")
	return nil
}

func readMessage(stdin io.Reader, arg string) (string, error) {
	if arg != "-" {
		return arg, nil
	}
	buf, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("reading stdin: %w", err)
	}
	return strings.TrimRight(string(buf), "\n"), nil
}
