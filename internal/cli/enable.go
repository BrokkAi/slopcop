package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
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
so later checks can run. A repository that is already added but disabled is
enabled again.

Repositories are free; a check uses one of the workspace's free checks for the
month, then credits. This changes what the workspace checks, so it asks for
the repository's owner/name at a terminal and refuses when stdin is not one. A
check adds a repository the workspace has never added by itself, so 'enable' is
optional for that; turning a disabled repository back on stays a step you take
here.

An account with more than one workspace is asked which one checks the
repository, unless --workspace names it, and is told when another of its
workspaces already has the repository, so it is not added twice by accident.

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
	// The workspace the command names, else the one this checkout is mapped
	// to, else the login's: what a check here would use, and the choice the
	// question below starts from.
	cfg, _ := config.Load()
	repoURL := checkoutRepoURL(repoRoot)
	choice := chooseWorkspace(workspace, cfg, client.BaseURL, repoURL)
	named := choice.Source == sourceFlag || choice.Source == sourceEnv
	target, err := chosenWorkspace(whoami, choice)
	if err != nil {
		return serviceFailure(cmd, err)
	}
	// asked is whether the person was asked, and answered, which workspace.
	asked := false
	client.Workspace = target.ID
	repository, err := lookupRepositoryStatus(cmd.Context(), client, repoRoot)
	if err != nil {
		return serviceFailure(cmd, err)
	}
	// Adding is where a repository lands in the wrong workspace: a login checks
	// in one workspace for every checkout on the machine, so a work repository
	// is one 'enable' away from a personal workspace. An account with a choice
	// makes it here, knowing which of its workspaces already have the repository.
	// An agent or a script is not asked and costs no extra requests: the
	// confirmation below refuses it without a terminal.
	if _, atTerminal := confirmInput(cmd); atTerminal && repository.State == statusNotAdded && len(whoami.Workspaces) > 1 {
		holders, unanswered := workspacesWithRepository(cmd.Context(), client, whoami.Workspaces, repository.URL, target.ID)
		if len(unanswered) > 0 {
			// Without an answer the "already has it" mark and the warning below
			// cannot be relied on, and the person is about to choose by them.
			fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: couldn't tell whether %s already has %s. 'slopcop repo list --workspace <name>' shows a workspace's repositories.\n",
				joinWorkspaceNames(unanswered), repository.FullName)
		}
		if !named {
			asked = true
			chosen, err := askWorkspace(cmd, repository.FullName, whoami.Workspaces, target, holders)
			if err != nil {
				return err
			}
			if chosen.ID != target.ID {
				target = chosen
				client.Workspace = target.ID
				if repository, err = lookupRepositoryStatus(cmd.Context(), client, repoRoot); err != nil {
					return serviceFailure(cmd, err)
				}
			}
		}
		if repository.State == statusNotAdded {
			warnAlreadyElsewhere(cmd, repository.FullName, target, holders)
		}
	}
	switch repository.State {
	case statusActive:
		fmt.Fprintf(cmd.OutOrStdout(), "slopcop: %s is already enabled in %s\n",
			repository.FullName, workspaceName(target.Name, true))
		if asked {
			// The answer to "which workspace" is the choice a mapping
			// records, so it is not asked a second time.
			return recordMapping(cmd, client.BaseURL, repoURL, target)
		}
		return offerMapping(cmd, client.BaseURL, repoURL, target)
	case statusUnsupported:
		return fmt.Errorf("SlopCop checks repositories on github.com, gitlab.com and bitbucket.org, not %s", repository.URL)
	case statusDisabled, statusOverLimit:
		err = enableExisting(cmd, client, repository, target)
	case statusNotAdded:
		err = addRepository(cmd, repoRoot, repository, target, serviceFlag, version)
	default:
		return fmt.Errorf("this repository is %s, so it cannot be enabled", repository.State)
	}
	// The person confirmed this workspace for the repository at the
	// terminal, so the checkout is mapped to it and later checks, the Stop
	// hook's included, run there whatever the login is bound to. A first
	// check that was cut short may still have added the repository; the
	// workspace's own list tells.
	mapped := enabledIn(err)
	var ran silentExit
	if !mapped && repository.State == statusNotAdded && errors.As(err, &ran) {
		// An interrupt has cancelled the command's context, and this is the
		// case the lookup is for, so it runs on its own short one.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(cmd.Context()), 10*time.Second)
		defer cancel()
		if listed, lookupErr := lookupRepositoryStatus(ctx, client, repoRoot); lookupErr == nil &&
			listed.State != statusNotAdded && listed.State != statusUnsupported {
			mapped = true
		}
	}
	if mapped {
		if mapErr := recordMapping(cmd, client.BaseURL, repoURL, target); mapErr != nil {
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

// workspacesWithRepository lists the account's workspaces, other than the one
// with id except, that already have repoURL, enabled or not, and those whose
// repositories the service would not list, about which nothing is known.
func workspacesWithRepository(ctx context.Context, client *service.Client, workspaces []service.Workspace, repoURL, except string) (holders, unanswered []service.Workspace) {
	current := client.Workspace
	defer func() { client.Workspace = current }()
	for _, workspace := range workspaces {
		if strings.EqualFold(workspace.ID, except) {
			continue
		}
		client.Workspace = workspace.ID
		repository, err := repositoryStatus(ctx, client, repoURL)
		switch {
		case err != nil:
			unanswered = append(unanswered, workspace)
		case repository.State != statusNotAdded && repository.State != statusUnsupported:
			holders = append(holders, workspace)
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

// askWorkspace asks the person at the terminal which of the account's
// workspaces should check, and bill, the repository, naming the login's own
// and those that already have it. Without a terminal it answers target
// unasked: the confirmation that follows refuses there anyway.
func askWorkspace(cmd *cobra.Command, name string, workspaces []service.Workspace, target service.Workspace, holders []service.Workspace) (service.Workspace, error) {
	input, ok := confirmInput(cmd)
	if !ok {
		return target, nil
	}
	out := cmd.ErrOrStderr()
	fmt.Fprintf(out, "slopcop: your account has %d workspaces. Which one should check, and bill, %s?\n", len(workspaces), name)
	for index, workspace := range workspaces {
		notes := []string{workspace.Kind}
		if workspace.ID == target.ID {
			notes = append(notes, "this login's")
		}
		if holdsRepository(holders, workspace) {
			notes = append(notes, "already has it")
		}
		fmt.Fprintf(out, "  %d  %s (%s)\n", index+1, workspace.Name, strings.Join(notes, ", "))
	}
	fmt.Fprint(out, "Number: ")
	answer, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return target, err
	}
	number, err := strconv.Atoi(strings.TrimSpace(answer))
	if err != nil || number < 1 || number > len(workspaces) {
		return target, fmt.Errorf("that is not a number from 1 to %d, so nothing was changed", len(workspaces))
	}
	return workspaces[number-1], nil
}

// warnAlreadyElsewhere says, before the repository is added to target, which
// other workspaces of the account already have it, so a second copy that is
// checked and billed twice is a choice and not an accident.
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
	fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: %s is already in %s. Adding it to %s too checks and bills it in each; to check it there instead, stop here and pass --workspace to your checks.\n",
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
// room for it.
func enableExisting(cmd *cobra.Command, client *service.Client, repository statusRepository, target service.Workspace) error {
	notifyChange(cmd, "enabling", repository.FullName, target.Name)
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
func addRepository(cmd *cobra.Command, repoRoot string, repository statusRepository, target service.Workspace, serviceFlag, version string) error {
	notifyChange(cmd, "adding", repository.FullName, target.Name)
	if err := confirmChange(cmd, repository.FullName, "add"); err != nil {
		return err
	}
	// The check runs in the workspace named here, and adds the repository
	// there because that workspace has never added it.
	return runCheck(cmd, &checkOptions{
		root:      repoRoot,
		mode:      modeStatic,
		workspace: target.ID,
	}, serviceFlag, version)
}

// notifyChange says what adding or enabling a repository changes before the
// person confirms it.
func notifyChange(cmd *cobra.Command, action, repository, workspace string) {
	fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: %s %s in %s lets that workspace check it.\n",
		action, repository, workspaceName(workspace, true))
	fmt.Fprintln(cmd.ErrOrStderr(), "slopcop: the repository is free; each check uses one of the workspace's free checks for the month, then credits.")
}
