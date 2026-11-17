package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/gitx"
	"github.com/BrokkAi/slopcop/internal/service"
)

func newEnableCommand(serviceURL *string, version string) *cobra.Command {
	var root, workspace string
	command := &cobra.Command{
		Use:   "enable",
		Short: "Add this checkout's repository to the workspace that checks it",
		Long: `Add the repository the origin remote names to the workspace that checks and
bills it, so later checks can run. A repository that is already added but
disabled is enabled again.

This changes what the workspace checks, and may cost credits every 30 days, so
it asks for the repository's owner/name at a terminal and refuses when stdin is
not one. An agent runs with your login, and the service cannot tell it from
you, so adding a repository stays a step you take yourself. Once a person has
confirmed it, 'check --add-repository --workspace <name|id>' carries the same
approval without a terminal.

Adding a repository through 'enable' also runs its first check, and maps this
machine's checkouts of it to that workspace, so later checks run there
whatever workspace the login is bound to ('slopcop workspace map').`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runEnable(cmd, root, workspace, *serviceURL, version)
		},
	}
	command.Flags().StringVar(&root, "root", ".", "repository path")
	addWorkspaceFlag(command, false, &workspace)
	return command
}

func runEnable(cmd *cobra.Command, root, workspace, serviceFlag, version string) error {
	repoRoot, err := gitx.Toplevel(root)
	if err != nil {
		return err
	}
	client, err := accountClient(cmd, serviceFlag, version)
	if err != nil {
		return err
	}
	whoami, err := client.Whoami(cmd.Context())
	if err != nil {
		return serviceFailure(cmd, err)
	}
	cfg, _ := config.Load()
	repoURL := checkoutRepoURL(repoRoot)
	target, err := chosenWorkspace(whoami, chooseWorkspace(workspace, cfg, client.BaseURL, repoURL))
	if err != nil {
		return serviceFailure(cmd, err)
	}
	client.Workspace = target.ID
	repository, err := lookupRepositoryStatus(cmd.Context(), client, repoRoot)
	if err != nil {
		return serviceFailure(cmd, err)
	}
	switch repository.State {
	case statusActive:
		fmt.Fprintf(cmd.OutOrStdout(), "slopcop: %s is already enabled in %s\n",
			repository.FullName, workspaceName(target.Name, true))
		return offerMapping(cmd, client.BaseURL, repoURL, target)
	case statusUnsupported:
		return fmt.Errorf("SlopCop checks repositories on github.com, gitlab.com and bitbucket.org, not %s", repository.URL)
	case statusDisabled, statusOverLimit:
		if err := enableExisting(cmd, client, repository, target); err != nil {
			return err
		}
		return recordMapping(cmd, client.BaseURL, repoURL, target)
	case statusNotAdded:
		return addRepository(cmd, client, repoRoot, repository, target, serviceFlag, version, func() error {
			return recordMapping(cmd, client.BaseURL, repoURL, target)
		})
	default:
		return fmt.Errorf("this repository is %s, so it cannot be enabled", repository.State)
	}
}

// enableExisting turns an added but disabled repository back on. A repository
// past the active limit stays enabled but not active until the workspace makes
// room for it.
func enableExisting(cmd *cobra.Command, client *service.Client, repository statusRepository, target service.Workspace) error {
	notifyCost(cmd, "enabling", repository.FullName, target.Name)
	if err := confirmChange(cmd, repository.FullName, "enable"); err != nil {
		return err
	}
	state, err := client.SetRepositoryEnabled(cmd.Context(), repository.ID, true)
	if err != nil {
		return serviceFailure(cmd, err)
	}
	name := state.FullName
	if name == "" {
		name = repository.FullName
	}
	suffix := ""
	if state.Enabled && !state.Active {
		suffix = ", but it is past the workspace's active limit"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "slopcop: enabled %s in %s%s\n",
		name, workspaceName(target.Name, true), suffix)
	return nil
}

// addRepository is the one path that adds a repository to a workspace: a
// person confirms it at a terminal, then the first check carries that
// approval to the service.
func addRepository(cmd *cobra.Command, client *service.Client, repoRoot string, repository statusRepository, target service.Workspace, serviceFlag, version string, confirmed func() error) error {
	notifyCost(cmd, "adding", repository.FullName, target.Name)
	if err := confirmChange(cmd, repository.FullName, "add"); err != nil {
		return err
	}
	// runCheck sends --add-repository with the workspace named here, so the
	// approval cannot add the repository to another one.
	added := false
	checkErr := runCheck(cmd, &checkOptions{
		root:          repoRoot,
		mode:          modeStatic,
		workspace:     target.ID,
		addRepository: true,
		added:         &added,
	}, serviceFlag, version)
	// The checkout is mapped only once the service has added the
	// repository, whatever the first check then found: a check that never
	// reached the service leaves an earlier mapping as it was.
	if !added && checkErr != nil {
		// A first check that timed out or was interrupted may have added
		// the repository without saying so; the workspace's own list tells.
		if listed, err := lookupRepositoryStatus(cmd.Context(), client, repoRoot); err == nil &&
			listed.State != statusNotAdded && listed.State != statusUnsupported {
			added = true
		}
	}
	if added {
		if err := confirmed(); err != nil {
			return err
		}
	}
	return checkErr
}

func notifyCost(cmd *cobra.Command, action, repository, workspace string) {
	fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: %s %s in %s lets that workspace check and bill it.\n",
		action, repository, workspaceName(workspace, true))
	fmt.Fprintln(cmd.ErrOrStderr(), "slopcop: every active repository after the workspace's first costs credits every 30 days.")
}
