package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/service"
)

const accountTimeout = time.Minute

// accountClient is a client for the login's own account routes, with the
// service URL it resolved.
func accountClient(serviceFlag, version string) (*service.Client, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return service.New(resolveServiceURL(cfg, serviceFlag), resolveToken(cfg), "slopcop/"+version, accountTimeout)
}

// serviceFailure prints a service failure's message and remedy, as a check
// does, and ends with exit 2. Any other error is returned as is.
func serviceFailure(cmd *cobra.Command, err error) error {
	var serviceError *service.Error
	if errors.As(err, &serviceError) {
		fmt.Fprintln(cmd.ErrOrStderr(), remedyText(serviceError))
		return silentExit{service.ExitIncomplete}
	}
	return err
}

func addFormatFlag(command *cobra.Command, format *string) {
	command.Flags().StringVar(format, "format", "human", "output format: human or json")
}

func checkFormat(format string) error {
	if format != "human" && format != "json" {
		return fmt.Errorf("invalid argument %q for --format: use human or json", format)
	}
	return nil
}

func writeJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

// planName is the plan as the product names it.
func planName(plan string) string {
	switch plan {
	case "pro":
		return "Pro"
	case "free":
		return "Free"
	default:
		return plan
	}
}

func newWhoamiCommand(serviceURL *string, version string) *cobra.Command {
	format := ""
	command := &cobra.Command{
		Use:   "whoami",
		Short: "Show the account and the workspace checks run in and bill",
		Long: `Show the account behind this login, the workspace it is bound to (the one
its checks and ignores run in, and bill), that workspace's plan, and the
service URL. Run it before a check when it matters which workspace pays.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkFormat(format); err != nil {
				return err
			}
			client, err := accountClient(*serviceURL, version)
			if err != nil {
				return err
			}
			whoami, err := client.Whoami(cmd.Context())
			if err != nil {
				return serviceFailure(cmd, err)
			}
			out := cmd.OutOrStdout()
			if format == "json" {
				return writeJSON(out, struct {
					Service   string            `json:"service"`
					Account   service.Account   `json:"account"`
					Workspace service.Workspace `json:"workspace"`
					Plan      string            `json:"plan"`
				}{client.BaseURL, whoami.Account, whoami.Workspace, whoami.Plan})
			}
			account := whoami.Account.Username
			if whoami.Account.Email != "" {
				account += " <" + whoami.Account.Email + ">"
			}
			fmt.Fprintf(out, "Account:   %s\n", account)
			fmt.Fprintf(out, "Workspace: %s (%s, %s)\n", whoami.Workspace.Name, whoami.Workspace.Kind, whoami.Workspace.Role)
			fmt.Fprintf(out, "Plan:      %s\n", planName(whoami.Plan))
			fmt.Fprintf(out, "Service:   %s\n", client.BaseURL)
			return nil
		},
	}
	addFormatFlag(command, &format)
	return command
}

func newWorkspaceCommand(serviceURL *string, version string) *cobra.Command {
	command := &cobra.Command{
		Use:   "workspace",
		Short: "List workspaces, or choose the one this login checks in",
		Long: `List your account's workspaces, or bind this login to another of them.

A login runs its checks and ignores in, and bills, one workspace: the one
active in the browser when the login was approved, until 'slopcop workspace
use' moves it. Moving it changes this login only, never the browser.`,
		Args: cobra.NoArgs,
	}
	command.AddCommand(newWorkspaceListCommand(serviceURL, version), newWorkspaceUseCommand(serviceURL, version))
	return command
}

func newWorkspaceListCommand(serviceURL *string, version string) *cobra.Command {
	format := ""
	command := &cobra.Command{
		Use:   "list",
		Short: "List the account's workspaces; * marks this login's",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkFormat(format); err != nil {
				return err
			}
			client, err := accountClient(*serviceURL, version)
			if err != nil {
				return err
			}
			whoami, err := client.Whoami(cmd.Context())
			if err != nil {
				return serviceFailure(cmd, err)
			}
			out := cmd.OutOrStdout()
			if format == "json" {
				return writeJSON(out, struct {
					Workspaces []service.Workspace `json:"workspaces"`
				}{whoami.Workspaces})
			}
			table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			for _, workspace := range whoami.Workspaces {
				marker := " "
				if workspace.Current {
					marker = "*"
				}
				fmt.Fprintf(table, "%s %s\t%s\t%s\t%s\n", marker, workspace.Name, workspace.Kind, workspace.Role, workspace.ID)
			}
			return table.Flush()
		},
	}
	addFormatFlag(command, &format)
	return command
}

func newWorkspaceUseCommand(serviceURL *string, version string) *cobra.Command {
	format := ""
	command := &cobra.Command{
		Use:   "use <name|id>",
		Short: "Bind this login to another workspace of the account",
		Long: `Bind this login to another workspace of your account, by name or id, so
its checks and ignores run in, and bill, that workspace. The browser's
workspace does not change.

Every agent and script using this login moves with it, so this asks whoever
runs it to confirm, and refuses when stdin is not a terminal. To check in
another workspace once, pass --workspace to that command instead.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkFormat(format); err != nil {
				return err
			}
			client, err := accountClient(*serviceURL, version)
			if err != nil {
				return err
			}
			whoami, err := client.Whoami(cmd.Context())
			if err != nil {
				return serviceFailure(cmd, err)
			}
			workspace, err := findWorkspace(whoami.Workspaces, args[0])
			if err != nil {
				return err
			}
			if !workspace.Current {
				question := fmt.Sprintf("Every agent and script using this login will then check in, and bill, %q. Bind it there?", workspace.Name)
				yes, err := confirmYes(cmd, question)
				if errors.Is(err, errNotATerminal) {
					return fmt.Errorf("workspace use moves every agent using this login, so it must be confirmed at a terminal; to check in %q once, pass --workspace %q to that command", workspace.Name, workspace.Name)
				}
				if err != nil {
					return err
				}
				if !yes {
					return fmt.Errorf("this login still checks in %q", whoami.Workspace.Name)
				}
			}
			if err := client.UseWorkspace(cmd.Context(), workspace.ID); err != nil {
				return serviceFailure(cmd, err)
			}
			workspace.Current = true
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), struct {
					Workspace service.Workspace `json:"workspace"`
				}{workspace})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "slopcop: this login now checks in, and bills, %s (%s)\n", workspace.Name, workspace.Kind)
			return nil
		},
	}
	addFormatFlag(command, &format)
	return command
}

// findWorkspace picks a workspace by id, or else by name, ignoring case. Two
// workspaces with the name need the id.
func findWorkspace(workspaces []service.Workspace, wanted string) (service.Workspace, error) {
	wanted = strings.TrimSpace(wanted)
	var named []service.Workspace
	for _, workspace := range workspaces {
		if strings.EqualFold(workspace.ID, wanted) {
			return workspace, nil
		}
		if strings.EqualFold(workspace.Name, wanted) {
			named = append(named, workspace)
		}
	}
	switch len(named) {
	case 1:
		return named[0], nil
	case 0:
		return service.Workspace{}, fmt.Errorf("no workspace of your account is named %q; run 'slopcop workspace list'", wanted)
	default:
		ids := make([]string, len(named))
		for index, workspace := range named {
			ids[index] = workspace.ID
		}
		return service.Workspace{}, fmt.Errorf("%d workspaces are named %q; use the id of one: %s", len(named), wanted, strings.Join(ids, ", "))
	}
}

func newRepoCommand(serviceURL *string, version string) *cobra.Command {
	command := &cobra.Command{
		Use:   "repo",
		Short: "List, enable or disable the workspace's repositories",
		Long: `List the repositories of this login's workspace, or of the one --workspace
names, or enable or disable one.

Each active repository after a workspace's first costs credits every 30 days;
a disabled one is not checked or charged, and keeps its reports, findings and
settings. Only the workspace's owner enables or disables. A first check adds
a repository, so there is no 'repo add'.`,
		Args: cobra.NoArgs,
	}
	workspace := ""
	addWorkspaceFlag(command, true, &workspace)
	command.AddCommand(
		newRepoListCommand(serviceURL, &workspace, version),
		newRepoStateCommand(serviceURL, &workspace, version, true),
		newRepoStateCommand(serviceURL, &workspace, version, false),
	)
	return command
}

// repositoryState is how a repository reads in a listing.
func repositoryState(repository service.Repository) string {
	switch {
	case repository.Active:
		return "active"
	case repository.Enabled:
		// Enabled, but past the workspace's active limit.
		return "enabled, over the limit"
	default:
		return "disabled"
	}
}

func newRepoListCommand(serviceURL, workspace *string, version string) *cobra.Command {
	format := ""
	command := &cobra.Command{
		Use:   "list",
		Short: "List the workspace's repositories and whether each is active",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkFormat(format); err != nil {
				return err
			}
			client, err := accountClient(*serviceURL, version)
			if err != nil {
				return err
			}
			if err := useWorkspace(cmd.Context(), client, *workspace); err != nil {
				return serviceFailure(cmd, err)
			}
			listed, err := client.Repositories(cmd.Context())
			if err != nil {
				return serviceFailure(cmd, err)
			}
			out := cmd.OutOrStdout()
			if format == "json" {
				return writeJSON(out, listed)
			}
			if len(listed.Repositories) == 0 {
				fmt.Fprintln(out, "slopcop: this workspace has no repositories yet; a check adds one")
				return nil
			}
			table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			for _, repository := range listed.Repositories {
				fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", repository.FullName, repository.Provider, repositoryState(repository), repository.ID)
			}
			if err := table.Flush(); err != nil {
				return err
			}
			if listed.ActiveRepositoryLimit != nil {
				fmt.Fprintf(out, "%d of %d active.\n", listed.ActiveRepositories, *listed.ActiveRepositoryLimit)
			}
			return nil
		},
	}
	addFormatFlag(command, &format)
	return command
}

func newRepoStateCommand(serviceURL, workspace *string, version string, enable bool) *cobra.Command {
	format := ""
	use, short, done := "disable", "Disable a repository: it is not checked or charged", "disabled"
	if enable {
		use, short, done = "enable", "Enable a repository, within the workspace's active limit", "enabled"
	}
	command := &cobra.Command{
		Use:   use + " <owner/name|id>",
		Short: short,
		Long: short + `.

The service cannot tell a person from an agent using the same login, so this
asks whoever runs it to type the repository's owner/name, and refuses when
stdin is not a terminal.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkFormat(format); err != nil {
				return err
			}
			client, err := accountClient(*serviceURL, version)
			if err != nil {
				return err
			}
			if err := useWorkspace(cmd.Context(), client, *workspace); err != nil {
				return serviceFailure(cmd, err)
			}
			listed, err := client.Repositories(cmd.Context())
			if err != nil {
				return serviceFailure(cmd, err)
			}
			repository, err := findRepository(listed.Repositories, args[0])
			if err != nil {
				return err
			}
			if err := confirmChange(cmd, repository.FullName, use); err != nil {
				return err
			}
			state, err := client.SetRepositoryEnabled(cmd.Context(), repository.ID, enable)
			if err != nil {
				return serviceFailure(cmd, err)
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), state)
			}
			name := state.FullName
			if name == "" {
				name = repository.FullName
			}
			suffix := ""
			if state.Enabled && !state.Active {
				suffix = ", but it is past the workspace's active limit"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "slopcop: %s %s%s\n", done, name, suffix)
			return nil
		},
	}
	addFormatFlag(command, &format)
	return command
}

// findRepository picks a repository by id, or else by owner/name, ignoring
// case. The same name with two providers needs the id.
func findRepository(repositories []service.Repository, wanted string) (service.Repository, error) {
	wanted = strings.TrimSpace(wanted)
	var named []service.Repository
	for _, repository := range repositories {
		if strings.EqualFold(repository.ID, wanted) {
			return repository, nil
		}
		if strings.EqualFold(repository.FullName, wanted) {
			named = append(named, repository)
		}
	}
	switch len(named) {
	case 1:
		return named[0], nil
	case 0:
		return service.Repository{}, fmt.Errorf("this workspace has no repository %q; run 'slopcop repo list'", wanted)
	default:
		choices := make([]string, len(named))
		for index, repository := range named {
			choices[index] = repository.Provider + " " + repository.ID
		}
		return service.Repository{}, fmt.Errorf("%d repositories are named %q; use the id of one: %s", len(named), wanted, strings.Join(choices, ", "))
	}
}

// confirmInput is where enable and disable read the person's confirmation:
// stdin, and only when it is a terminal. A variable so tests can stand in.
var confirmInput = func(cmd *cobra.Command) (io.Reader, bool) {
	file, ok := cmd.InOrStdin().(*os.File)
	if !ok {
		return nil, false
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return nil, false
	}
	return file, true
}

// confirmChange asks the person at the terminal to type the repository's
// name before its state changes. Agents and CI run with the person's own
// login, so this keeps enabling and disabling, which change what the
// workspace pays, a step the person takes.
func confirmChange(cmd *cobra.Command, name, action string) error {
	input, ok := confirmInput(cmd)
	if !ok {
		return fmt.Errorf("repo %s must be confirmed at a terminal; run it yourself, or change it on the Repositories page in SlopCop", action)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Type %s to %s it: ", name, action)
	answer, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if strings.TrimSpace(answer) != name {
		return fmt.Errorf("that is not %s, so nothing was changed", name)
	}
	return nil
}
