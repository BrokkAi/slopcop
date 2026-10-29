package cli

import (
	"errors"
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
	findingID = strings.TrimSpace(findingID)
	if !uuidPattern.MatchString(findingID) {
		return fmt.Errorf("finding id %q is not a UUID; use the id a check printed for the finding", findingID)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	client, err := service.New(resolveServiceURL(cfg, serviceFlag), resolveToken(cfg), "slopcop/"+version, ignoreTimeout)
	if err != nil {
		return err
	}
	if err := useWorkspace(cmd.Context(), client, workspace); err != nil {
		return serviceFailure(cmd, err)
	}
	response, err := client.Ignore(cmd.Context(), findingID)
	if err != nil {
		var serviceError *service.Error
		if errors.As(err, &serviceError) {
			fmt.Fprintln(cmd.ErrOrStderr(), remedyText(serviceError))
			return silentExit{service.ExitIncomplete}
		}
		return err
	}
	fmt.Fprintf(
		cmd.OutOrStdout(),
		"slopcop: ignored finding %s; later checks of this repository will not report it\n",
		response.FindingID,
	)
	return nil
}
