package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/service"
)

// workspaceFlagUsage is the --workspace help every command that takes it shows.
const workspaceFlagUsage = "workspace to run in, and bill, by name or id, instead of the one this login is bound to (also $SLOPCOP_WORKSPACE)"

// addWorkspaceFlag registers -w/--workspace on flags.
func addWorkspaceFlag(command *cobra.Command, persistent bool, target *string) {
	flags := command.Flags()
	if persistent {
		flags = command.PersistentFlags()
	}
	flags.StringVarP(target, "workspace", "w", "", workspaceFlagUsage)
}

// resolveWorkspaceName is --workspace, else $SLOPCOP_WORKSPACE, else empty for
// the workspace the login is bound to.
func resolveWorkspaceName(flagValue string) string {
	if value := strings.TrimSpace(flagValue); value != "" {
		return value
	}
	return strings.TrimSpace(os.Getenv("SLOPCOP_WORKSPACE"))
}

// useWorkspace points every later request of client at the workspace named,
// by id or by name, for this one command. The login is bound to one
// workspace that every agent sharing it shares, so naming one per command is
// how an agent checks in the right one without moving the others. An id is
// sent as is, and the service refuses one that is not the account's; a name
// is looked up among the account's workspaces.
func useWorkspace(ctx context.Context, client *service.Client, flagValue string) error {
	wanted := resolveWorkspaceName(flagValue)
	if wanted == "" {
		return nil
	}
	if uuidPattern.MatchString(wanted) {
		client.Workspace = wanted
		return nil
	}
	whoami, err := client.Whoami(ctx)
	if err != nil {
		return err
	}
	workspace, err := findWorkspace(whoami.Workspaces, wanted)
	if err != nil {
		return err
	}
	client.Workspace = workspace.ID
	return nil
}

// workspaceName is the name of the workspace client's requests run in, for a
// question about it. It falls back to "your workspace" when it cannot tell.
func workspaceName(ctx context.Context, client *service.Client) string {
	whoami, err := client.Whoami(ctx)
	if err != nil {
		return "your workspace"
	}
	if client.Workspace == "" {
		return fmt.Sprintf("%q", whoami.Workspace.Name)
	}
	for _, workspace := range whoami.Workspaces {
		if strings.EqualFold(workspace.ID, client.Workspace) {
			return fmt.Sprintf("%q", workspace.Name)
		}
	}
	return "your workspace"
}

// errNotATerminal is confirmYes's answer when stdin is not a terminal, as for
// an agent or a CI job.
var errNotATerminal = errors.New("stdin is not a terminal")

// confirmYes asks the person at the terminal a yes-or-no question, on
// stderr, and reports whether they answered yes. Anything but y or yes is
// no. Agents and CI run with the person's own login, so a step that changes
// what a workspace pays, or where every agent's checks go, waits for the
// person; when stdin is not a terminal it answers errNotATerminal.
func confirmYes(cmd *cobra.Command, question string) (bool, error) {
	input, ok := confirmInput(cmd)
	if !ok {
		return false, errNotATerminal
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "%s [y/N] ", question)
	answer, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}
