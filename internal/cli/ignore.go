package cli

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/review"
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
the repository never report it again. Ignoring a finding twice is fine.

Ignores belong to the workspace, and so do pull request checks: when another
workspace checks the repository's pull requests, their check still reports
the finding, and this command says so and names that workspace.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIgnore(cmd, *serviceURL, version, workspace, args[0])
		},
	}
	addWorkspaceFlag(command, false, &workspace)
	return command
}

func runIgnore(cmd *cobra.Command, serviceFlag, version, workspace, findingID string) error {
	response, client, err := sendFinding(cmd, serviceFlag, version, workspace, findingID, (*service.Client).Ignore)
	if err != nil {
		return err
	}
	forgetFinding(".", strings.TrimSpace(findingID))
	fmt.Fprintf(
		cmd.OutOrStdout(),
		"slopcop: ignored finding %s; later checks of this repository will not report it\n",
		response.FindingID,
	)
	ignoreElsewhereNotice(cmd, client, ".")
	return nil
}

// sendFinding checks the finding id, then sends it with send in the
// workspace named, and returns the client it sent it with. A service failure
// prints its message and remedy and exits incomplete.
func sendFinding(
	cmd *cobra.Command,
	serviceFlag, version, workspace, findingID string,
	send func(*service.Client, context.Context, string) (service.IgnoreResponse, error),
) (service.IgnoreResponse, *service.Client, error) {
	findingID = strings.TrimSpace(findingID)
	if review.IsFindingID(findingID) {
		return service.IgnoreResponse{}, nil, fmt.Errorf("%s is a local review finding's id, which `slopcop ignore` does not take; run `slopcop override %s --reason \"why it does not hold\"` instead", findingID, findingID)
	}
	if !uuidPattern.MatchString(findingID) {
		return service.IgnoreResponse{}, nil, fmt.Errorf("finding id %q is not a UUID; use the id a check printed for the finding", findingID)
	}
	client, err := workspaceClient(cmd, serviceFlag, version, workspace)
	if err != nil {
		return service.IgnoreResponse{}, nil, err
	}
	response, err := send(client, cmd.Context(), findingID)
	if err != nil {
		return service.IgnoreResponse{}, nil, serviceFailure(cmd, err)
	}
	return response, client, nil
}

// workspaceClient is a client for the service a command names, in the
// workspace it names, after the notices every service command prints. A
// service failure prints its message and remedy and exits incomplete.
func workspaceClient(cmd *cobra.Command, serviceFlag, version, workspace string) (*service.Client, error) {
	return workspaceClientAt(cmd, serviceFlag, version, workspace, ".")
}

// workspaceClientAt is workspaceClient for a command about the checkout at
// root: with no workspace named, it runs in the one the checkout is mapped
// to, so an ignore reaches the workspace whose check reported the finding.
func workspaceClientAt(cmd *cobra.Command, serviceFlag, version, workspace, root string) (*service.Client, error) {
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
	noticeUpdates(cmd, client, version)
	if err := useWorkspace(cmd.Context(), client, chooseWorkspaceAt(workspace, client.BaseURL, root)); err != nil {
		return nil, serviceFailure(cmd, err)
	}
	return client, nil
}
