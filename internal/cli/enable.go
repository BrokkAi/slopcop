package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
		Long: `Add the repository the origin remote names to the workspace that checks it,
and run its first check. This is optional: a check adds a repository its
workspace has never added by itself, so opening your agent in the checkout is
enough. Use 'enable' to add one up front, to pick its workspace, or to turn a
disabled one back on.

Repositories are free; a check uses one of the workspace's free checks for the
month, then credits.

The workspace is the one --workspace names, else the one this checkout is
mapped to, else the login's. 'enable' adds the repository there without asking,
says which workspace that was, and maps this machine's checkouts of it to that
workspace, so later checks run there whatever workspace the login is bound to
('slopcop workspace map'). If it is the wrong one, 'slopcop workspace map
<name>' checks it in another from now on, and 'slopcop repo disable' removes
it. An account with more than one workspace is told when another of them
already has the repository.

A repository someone in the workspace disabled is enabled again too, but only
when 'enable' runs at a terminal: an agent does not undo that choice.`,
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
	// The workspace the command names, else the one this checkout is mapped
	// to, else the login's: what a check here would use.
	cfg, _ := config.Load()
	repoURL := checkoutRepoURL(repoRoot)
	choice := chooseWorkspace(workspace, cfg, client.BaseURL, repoURL)
	target, err := chosenWorkspace(whoami, choice)
	if err != nil {
		return serviceFailure(cmd, err)
	}
	client.Workspace = target.ID
	repository, err := lookupRepositoryStatus(cmd.Context(), client, repoRoot)
	if err != nil {
		return serviceFailure(cmd, err)
	}
	// An account with more than one workspace is told which others already
	// have the repository, so a second copy, checked and billed twice, is not
	// an accident. Nothing is asked: the lines after the add say how to move
	// or remove it.
	if repository.State == statusNotAdded && len(whoami.Workspaces) > 1 {
		holders, unanswered := workspacesWithRepository(cmd.Context(), client, whoami.Workspaces, repository.URL, target.ID)
		if len(unanswered) > 0 {
			fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: couldn't tell whether %s already has %s. 'slopcop repo list --workspace <name>' shows a workspace's repositories.\n",
				joinWorkspaceNames(unanswered), repository.FullName)
		}
		warnAlreadyElsewhere(cmd, repository.FullName, target, holders)
	}
	switch repository.State {
	case statusActive:
		fmt.Fprintf(cmd.OutOrStdout(), "slopcop: %s is already enabled in %s\n",
			repository.FullName, workspaceName(target.Name, true))
		return offerMapping(cmd, client.BaseURL, repoURL, target)
	case statusUnsupported:
		return fmt.Errorf("SlopCop checks repositories on github.com, gitlab.com and bitbucket.org, not %s", repository.URL)
	case statusDisabled, statusOverLimit:
		err = enableExisting(cmd, client, repository, target)
	case statusNotAdded:
		err = addRepository(cmd, repoRoot, repository, target, &enableCheck{workspaces: whoami.Workspaces, source: choice.Source}, serviceFlag, version)
	default:
		return fmt.Errorf("this repository is %s, so it cannot be enabled", repository.State)
	}
	// The checkout is mapped to the workspace the repository is now enabled
	// in, so later checks, the Stop hook's included, run there whatever the
	// login is bound to. A first check that was cut short may still have
	// added the repository; the workspace's own list tells.
	mapped := enabledIn(err)
	recovered := false
	var ran silentExit
	if !mapped && repository.State == statusNotAdded && errors.As(err, &ran) {
		// An interrupt has cancelled the command's context, and this is the
		// case the lookup is for, so it runs on its own short one.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(cmd.Context()), 10*time.Second)
		defer cancel()
		if listed, lookupErr := lookupRepositoryStatus(ctx, client, repoRoot); lookupErr == nil &&
			listed.State != statusNotAdded && listed.State != statusUnsupported {
			mapped, recovered = true, true
		}
	}
	if mapped {
		// The line the first check printed when it added the repository
		// already names the workspace checks here run in, and a mapping with
		// one workspace is no choice at all. A check cut short printed no
		// such line, so the mapping says so itself.
		record := recordMapping
		if len(whoami.Workspaces) <= 1 || (repository.State == statusNotAdded && !recovered) {
			record = saveMapping
		}
		if mapErr := record(cmd, client.BaseURL, repoURL, target); mapErr != nil {
			// The repository is enabled there, but later checks would not
			// know to run there: say so whatever the first check found.
			fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: could not map this checkout to %s: %v. Run `slopcop workspace map %q` to try again.\n",
				workspaceName(target.Name, true), mapErr, workspaceSelector(whoami.Workspaces, target))
			if err == nil || (errors.As(err, &ran) && ran.code == service.ExitClean) {
				err = mapErr
			}
		}
	}
	return err
}

// enabledIn reports whether the repository ended up enabled: nothing failed,
// or the first check ran and answered, with findings or without.
func enabledIn(err error) bool {
	var exit silentExit
	if errors.As(err, &exit) {
		return exit.code == service.ExitClean || exit.code == service.ExitFindings
	}
	return err == nil
}

// workspaceRepository is a repository's state in one workspace of the
// account. Err is set when the service would not list that workspace's
// repositories, so nothing is known of it there.
type workspaceRepository struct {
	Workspace service.Workspace
	State     string
	Err       error
}

// repositoryElsewhere asks each of the account's workspaces, other than the
// one with id except, for repoURL's state there.
func repositoryElsewhere(ctx context.Context, client *service.Client, workspaces []service.Workspace, repoURL, except string) []workspaceRepository {
	other := *client
	other.WorkspaceRefusal = nil
	var asked []workspaceRepository
	for _, workspace := range workspaces {
		if strings.EqualFold(workspace.ID, except) {
			continue
		}
		other.Workspace = workspace.ID
		repository, err := repositoryStatus(ctx, &other, repoURL)
		asked = append(asked, workspaceRepository{Workspace: workspace, State: repository.State, Err: err})
	}
	return asked
}

// held reports whether the workspace has the repository, enabled or not.
func (r workspaceRepository) held() bool {
	return r.Err == nil && r.State != statusNotAdded && r.State != statusUnsupported
}

// workspacesWithRepository lists the account's workspaces, other than the one
// with id except, that already have repoURL, enabled or not, and those whose
// repositories the service would not list, about which nothing is known.
func workspacesWithRepository(ctx context.Context, client *service.Client, workspaces []service.Workspace, repoURL, except string) (holders, unanswered []service.Workspace) {
	for _, asked := range repositoryElsewhere(ctx, client, workspaces, repoURL, except) {
		switch {
		case asked.Err != nil:
			unanswered = append(unanswered, asked.Workspace)
		case asked.held():
			holders = append(holders, asked.Workspace)
		}
	}
	return holders, unanswered
}

// joinWorkspaceNames is the workspaces' quoted names in a sentence.
func joinWorkspaceNames(workspaces []service.Workspace) string {
	names := make([]string, len(workspaces))
	for index, workspace := range workspaces {
		names[index] = workspaceName(workspace.Name, true)
	}
	return strings.Join(names, " and ")
}

// workspaceSelector is what --workspace takes for target: its name, or its id
// when another of the account's workspaces shares the name, which a name
// alone would be refused for.
func workspaceSelector(workspaces []service.Workspace, target service.Workspace) string {
	for _, workspace := range workspaces {
		if workspace.ID != target.ID && strings.EqualFold(workspace.Name, target.Name) {
			return target.ID
		}
	}
	return target.Name
}

// warnAlreadyElsewhere says, before the repository is added to target, which
// other workspaces of the account already have it, so a second copy that is
// checked and billed twice is not an accident.
func warnAlreadyElsewhere(cmd *cobra.Command, name string, target service.Workspace, holders []service.Workspace) {
	var others []service.Workspace
	for _, workspace := range holders {
		if workspace.ID != target.ID {
			others = append(others, workspace)
		}
	}
	if len(others) == 0 {
		return
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: %s is also in %s; adding it to %s too checks and bills it in each.\n",
		name, joinWorkspaceNames(others), workspaceName(target.Name, true))
}

func holdsRepository(holders []service.Workspace, workspace service.Workspace) bool {
	for _, holder := range holders {
		if holder.ID == workspace.ID {
			return true
		}
	}
	return false
}

// enableExisting turns an added but disabled repository back on. A repository
// past the active limit stays enabled but not active until the workspace makes
// room for it. Someone in the workspace chose to disable it, so this runs only
// at a terminal: an agent with the person's login does not undo that.
func enableExisting(cmd *cobra.Command, client *service.Client, repository statusRepository, target service.Workspace) error {
	if _, atTerminal := confirmInput(cmd); !atTerminal {
		return fmt.Errorf("%s %s; turning it back on is a step a person takes: run 'slopcop enable' at a terminal, or enable it on the Repositories page in SlopCop",
			repository.FullName, inactiveReason(repository.State))
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

// addRepository is the one path that adds a repository to a workspace from
// 'enable': the first check adds it, as any check of a repository the
// workspace has never added does, and says where.
func addRepository(cmd *cobra.Command, repoRoot string, repository statusRepository, target service.Workspace, enable *enableCheck, serviceFlag, version string) error {
	// The check runs in the workspace named here, and adds the repository
	// there because that workspace has never added it.
	return runCheck(cmd, &checkOptions{
		root:      repoRoot,
		mode:      modeStatic,
		workspace: target.ID,
		enable:    enable,
	}, serviceFlag, version)
}

// enableCheck is what the first check 'enable' runs knows of the choice: the
// account's workspaces, so an account with one is not told how to pick
// another, and how the workspace was chosen, which the check's own choice
// would name as --workspace.
type enableCheck struct {
	workspaces []service.Workspace
	source     string
}
