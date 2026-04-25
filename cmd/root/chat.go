package root

import (
	"github.com/spf13/cobra"

	"github.com/docker/docker-agent/pkg/chatserver"
	"github.com/docker/docker-agent/pkg/cli"
	"github.com/docker/docker-agent/pkg/config"
	"github.com/docker/docker-agent/pkg/telemetry"
)

type chatFlags struct {
	agentName  string
	listenAddr string
	runConfig  config.RuntimeConfig
}

func newChatCmd() *cobra.Command {
	var flags chatFlags

	cmd := &cobra.Command{
		Use:   "chat <agent-file>|<registry-ref>",
		Short: "Start an agent as an OpenAI-compatible chat completions server",
		Long: `Start an HTTP server that exposes the agent through an OpenAI-compatible
API at /v1/chat/completions and /v1/models. This lets tools that already
speak OpenAI's chat protocol (such as Open WebUI) drive a docker-agent
agent without any custom integration.`,
		Example: `  docker-agent serve chat ./agent.yaml
  docker-agent serve chat ./team.yaml --agent reviewer
  docker-agent serve chat agentcatalog/pirate --listen 127.0.0.1:9090`,
		Args: cobra.ExactArgs(1),
		RunE: flags.runChatCommand,
	}

	cmd.Flags().StringVarP(&flags.agentName, "agent", "a", "", "Name of the agent to expose (all agents if not specified)")
	cmd.Flags().StringVarP(&flags.listenAddr, "listen", "l", "127.0.0.1:8083", "Address to listen on")
	addRuntimeConfigFlags(cmd, &flags.runConfig)

	return cmd
}

func (f *chatFlags) runChatCommand(cmd *cobra.Command, args []string) (commandErr error) {
	ctx := cmd.Context()
	telemetry.TrackCommand(ctx, "serve", append([]string{"chat"}, args...))
	defer func() { // do not inline this defer so that commandErr is not resolved early
		telemetry.TrackCommandError(ctx, "serve", append([]string{"chat"}, args...), commandErr)
	}()

	out := cli.NewPrinter(cmd.OutOrStdout())
	agentFilename := args[0]

	ln, cleanup, err := newListener(ctx, f.listenAddr)
	if err != nil {
		return err
	}
	defer cleanup()

	out.Println("Listening on", ln.Addr().String())
	out.Println("OpenAI-compatible chat completions endpoint: http://" + ln.Addr().String() + "/v1/chat/completions")

	return chatserver.Run(ctx, agentFilename, f.agentName, &f.runConfig, ln)
}
