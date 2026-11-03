package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/service"
)

func newUnignoreCommand(serviceURL *string, version string) *cobra.Command {
	workspace := ""
	command := &cobra.Command{
		Use:   "unignore <finding-id>",
		Short: "Report an ignored finding again",
		Long: `Undo an ignore: report one ignored finding again.

The finding id is the one it was ignored from, or the same finding's id from
any later check of the repository. Later checks of the repository in your
workspace report the finding again. Unignoring a finding that is not ignored
is fine.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUnignore(cmd, *serviceURL, version, workspace, args[0])
		},
	}
	addWorkspaceFlag(command, false, &workspace)
	return command
}

func runUnignore(cmd *cobra.Command, serviceFlag, version, workspace, findingID string) error {
	response, err := sendFinding(cmd, serviceFlag, version, workspace, findingID, (*service.Client).Unignore)
	if err != nil {
		return err
	}
	forgetCheck(".")
	fmt.Fprintf(
		cmd.OutOrStdout(),
		"slopcop: finding %s is no longer ignored; later checks of this repository report it again\n",
		response.FindingID,
	)
	return nil
}
