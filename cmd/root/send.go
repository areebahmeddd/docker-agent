package root

import (
	"errors"
	"fmt"
	"io"
	"strconv"
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

The target can be selected explicitly with --to <addr|pid>; otherwise the
most recent live run is used.`,
		Example: `  docker-agent send "summarize the diff"
  echo "and now write tests" | docker-agent send -
  docker-agent send --to 12345 "hello"`,
		GroupID: "advanced",
		Args:    cobra.ExactArgs(1),
		RunE:    flags.run,
	}

	cmd.Flags().StringVar(&flags.target, "to", "", "Target run pid (defaults to the most recent live run)")
	cmd.Flags().BoolVar(&flags.followUp, "followup", false, "Queue as an end-of-turn follow-up instead of mid-turn steering")
	return cmd
}

func (f *sendFlags) run(cmd *cobra.Command, args []string) (commandErr error) {
	ctx := cmd.Context()
	telemetry.TrackCommand(ctx, "send", args)
	defer func() { telemetry.TrackCommandError(ctx, "send", args, commandErr) }()

	message, err := readMessage(cmd.InOrStdin(), args[0])
	if err != nil {
		return err
	}

	rec, err := resolveTarget(f.target)
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

// resolveTarget returns the registry record matching --to. An empty target
// resolves to the most recent live run; a numeric target is matched by pid.
func resolveTarget(target string) (runregistry.Record, error) {
	if target == "" {
		rec, ok, err := runregistry.Latest()
		if err != nil {
			return runregistry.Record{}, err
		}
		if !ok {
			return runregistry.Record{}, errors.New("no live docker-agent run found; start one with: docker-agent run --listen 127.0.0.1:0")
		}
		return rec, nil
	}

	pid, err := strconv.Atoi(target)
	if err != nil {
		return runregistry.Record{}, fmt.Errorf("--to must be a pid, got %q", target)
	}

	records, err := runregistry.List()
	if err != nil {
		return runregistry.Record{}, err
	}
	for _, r := range records {
		if r.PID == pid {
			return r, nil
		}
	}
	return runregistry.Record{}, fmt.Errorf("no live run with pid %d", pid)
}
