package cli

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/service"
)

// uuidPattern is the UUID shape the service gives every finding and
// workspace.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

const ignoreTimeout = time.Minute

func newIgnoreCommand(serviceURL *string, version string) *cobra.Command {
	workspace := ""
	command := &cobra.Command{
		Use:   "ignore <finding-id>",
		Short: "Stop reporting a false-positive finding",
		Long: `Stop reporting one finding that is a false positive.

The finding id is the one a check printed for it. Ignoring covers that one
finding in its repository for your workspace, not its rule; later checks of
the repository never report it again. Ignoring a finding twice is fine.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIgnore(cmd, *serviceURL, version, workspace, args[0])
		},
	}
	addWorkspaceFlag(command, false, &workspace)
	return command
}

func runIgnore(cmd *cobra.Command, serviceFlag, version, workspace, findingID string) error {
	response, err := sendFinding(cmd, serviceFlag, version, workspace, findingID, (*service.Client).Ignore)
	if err != nil {
		return err
	}
	forgetFinding(".", strings.TrimSpace(findingID))
	fmt.Fprintf(
		cmd.OutOrStdout(),
		"slopcop: ignored finding %s; later checks of this repository will not report it\n",
		response.FindingID,
	)
	return nil
}

// sendFinding checks the finding id, then sends it with send in the
// workspace named. A service failure prints its message and remedy and exits
// incomplete.
func sendFinding(
	cmd *cobra.Command,
	serviceFlag, version, workspace, findingID string,
	send func(*service.Client, context.Context, string) (service.IgnoreResponse, error),
) (service.IgnoreResponse, error) {
	findingID = strings.TrimSpace(findingID)
	if !uuidPattern.MatchString(findingID) {
		return service.IgnoreResponse{}, fmt.Errorf("finding id %q is not a UUID; use the id a check printed for the finding", findingID)
	}
	client, err := workspaceClient(cmd, serviceFlag, version, workspace)
	if err != nil {
		return service.IgnoreResponse{}, err
	}
	response, err := send(client, cmd.Context(), findingID)
	if err != nil {
		return service.IgnoreResponse{}, serviceFailure(cmd, err)
	}
	return response, nil
}

// workspaceClient is a client for the service a command names, in the
// workspace it names, after the notices every service command prints. A
// service failure prints its message and remedy and exits incomplete.
func workspaceClient(cmd *cobra.Command, serviceFlag, version, workspace string) (*service.Client, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	target := resolveServiceURL(cfg, serviceFlag)
	client, err := service.New(target, resolveToken(cfg, target), "slopcop/"+version, ignoreTimeout)
	if err != nil {
		return nil, err
	}
	noticeService(cmd.ErrOrStderr(), client.BaseURL)
	noticeUpdates(client, cmd.ErrOrStderr(), version)
	if err := useWorkspace(cmd.Context(), client, workspace); err != nil {
		return nil, serviceFailure(cmd, err)
	}
	return client, nil
}
