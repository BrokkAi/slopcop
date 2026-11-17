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

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/gitx"
	"github.com/BrokkAi/slopcop/internal/report"
	"github.com/BrokkAi/slopcop/internal/service"
)

// workspaceFlagUsage is the --workspace help every command that takes it shows.
const workspaceFlagUsage = "workspace to run in, and bill, by name or id, instead of the one this checkout is mapped to or this login is bound to (also $SLOPCOP_WORKSPACE)"

// addWorkspaceFlag registers -w/--workspace on flags.
func addWorkspaceFlag(command *cobra.Command, persistent bool, target *string) {
	flags := command.Flags()
	if persistent {
		flags = command.PersistentFlags()
	}
	flags.StringVarP(target, "workspace", "w", "", workspaceFlagUsage)
}

// Where a command's workspace came from, in the order they win.
const (
	sourceFlag    = "flag"
	sourceEnv     = "env"
	sourceMapping = "mapping"
	sourceLogin   = "login"
)

// workspaceChoice is the workspace a command runs in and how it was chosen:
// --workspace, else $SLOPCOP_WORKSPACE, else the workspace this machine maps
// the checkout's repository to, else the one the login is bound to.
type workspaceChoice struct {
	// Value is the name or id given by the flag or the environment, the id
	// of a mapping, and empty for the login's own workspace.
	Value  string
	Source string
	// Name is the workspace's name when a mapping was made.
	Name string
	// RepoURL is the checkout's canonical repository URL, when it has one.
	RepoURL string
}

// chooseWorkspace picks the workspace for a command about repoURL on the
// service. repoURL is empty for a command run outside a checkout.
func chooseWorkspace(flagValue string, cfg config.Config, serviceURL, repoURL string) workspaceChoice {
	choice := workspaceChoice{Source: sourceLogin, RepoURL: repoURL}
	if value := strings.TrimSpace(flagValue); value != "" {
		choice.Value, choice.Source = value, sourceFlag
		return choice
	}
	if value := strings.TrimSpace(os.Getenv("SLOPCOP_WORKSPACE")); value != "" {
		choice.Value, choice.Source = value, sourceEnv
		return choice
	}
	if repoURL == "" {
		return choice
	}
	if mapped, ok := cfg.WorkspaceFor(serviceURL, repoURL); ok {
		choice.Value, choice.Source, choice.Name = mapped.ID, sourceMapping, mapped.Name
	}
	return choice
}

// checkoutRepoURL is the canonical URL of the repository checked out at
// root, or empty when root is not in a checkout with an origin remote.
func checkoutRepoURL(root string) string {
	repoRoot, err := gitx.Toplevel(root)
	if err != nil {
		return ""
	}
	origin, err := gitx.OriginURL(repoRoot)
	if err != nil {
		return ""
	}
	repoURL, err := gitx.CanonicalRepoURL(origin)
	if err != nil {
		return ""
	}
	return repoURL
}

// chooseWorkspaceAt is chooseWorkspace for the checkout at root, read from
// the stored configuration. A configuration that cannot be read maps nothing.
func chooseWorkspaceAt(flagValue, serviceURL, root string) workspaceChoice {
	cfg, _ := config.Load()
	repoURL := ""
	if strings.TrimSpace(flagValue) == "" && strings.TrimSpace(os.Getenv("SLOPCOP_WORKSPACE")) == "" {
		// Only a mapping needs the checkout, so git is not asked otherwise.
		repoURL = checkoutRepoURL(root)
	}
	return chooseWorkspace(flagValue, cfg, serviceURL, repoURL)
}

// sourceText says how a workspace was chosen, for status and whoami.
func sourceText(source string) string {
	switch source {
	case sourceFlag:
		return "--workspace"
	case sourceEnv:
		return "$SLOPCOP_WORKSPACE"
	case sourceMapping:
		return "this checkout's mapping (`slopcop workspace map`)"
	default:
		return "this login's workspace (`slopcop workspace use`)"
	}
}

// staleMapping is the refusal for a mapping whose workspace the account can
// no longer use: archived, left, or another account's. Nothing falls through
// to the login's workspace, which would then be billed unasked.
func staleMapping(choice workspaceChoice) *service.ErrorBody {
	workspace := choice.Value
	if choice.Name != "" {
		workspace = fmt.Sprintf("%q (%s)", choice.Name, choice.Value)
	}
	return &service.ErrorBody{
		Code: "workspace_not_found",
		Message: fmt.Sprintf(
			"This checkout of %s is mapped to workspace %s, which this login's account cannot use: it was archived, or the account is no longer a member. Nothing ran, and no other workspace was billed.",
			repositoryName(choice.RepoURL), workspace),
		Remedy: "At a terminal, run `slopcop workspace map <name|id>` to choose another workspace for this checkout, or `slopcop workspace unmap` to use the one this login is bound to.",
	}
}

// useWorkspace points every later request of client at the workspace chosen,
// for this one command. The login is bound to one workspace that every agent
// sharing it shares, so naming one per command or per checkout is how a check
// runs in the right one without moving the others. An id is sent as is, and
// the service refuses one that is not the account's; a name is looked up
// among the account's workspaces. A mapping is an id, so it costs no request,
// and a stale one is refused by the service with staleMapping's text.
func useWorkspace(ctx context.Context, client *service.Client, choice workspaceChoice) error {
	switch {
	case choice.Value == "":
		return nil
	case choice.Source == sourceMapping:
		client.Workspace = choice.Value
		client.WorkspaceRefusal = staleMapping(choice)
		return nil
	case uuidPattern.MatchString(choice.Value):
		client.Workspace = choice.Value
		return nil
	}
	whoami, err := client.Whoami(ctx)
	if err != nil {
		return err
	}
	workspace, err := findWorkspace(whoami.Workspaces, choice.Value)
	if err != nil {
		return err
	}
	client.Workspace = workspace.ID
	return nil
}

// chosenWorkspace is the account's workspace a choice names, for the
// commands that have already read whoami. A mapping the account cannot use
// is refused as the service would refuse it.
func chosenWorkspace(whoami service.Whoami, choice workspaceChoice) (service.Workspace, error) {
	switch choice.Source {
	case sourceLogin:
		return whoami.Workspace, nil
	case sourceMapping:
		for _, workspace := range whoami.Workspaces {
			if strings.EqualFold(workspace.ID, choice.Value) {
				return workspace, nil
			}
		}
		return service.Workspace{}, &service.Error{StatusCode: 404, Body: *staleMapping(choice)}
	default:
		return findWorkspace(whoami.Workspaces, choice.Value)
	}
}

// changeWorkspaceStep is how the person moves a repository's checks to
// another workspace. An agent passes it on; it cannot take the step.
const changeWorkspaceStep = "To check it in another workspace instead, the user runs `slopcop workspace map <name>` in the checkout at a terminal (`slopcop workspace list` names the workspaces), and its owner can remove it from this one with `slopcop repo disable`."

// noticeAdded says, on stderr, which workspace a check just added the
// repository to, why that one, and how the person changes it, and returns
// the same for the report. A check adds a repository without asking, so this
// is where the user, or the agent that ran the check for them, learns which
// workspace now checks and bills it.
func noticeAdded(cmd *cobra.Command, client *service.Client, repoURL string, choice workspaceChoice) *report.AddedWorkspace {
	added := &report.AddedWorkspace{ID: client.Workspace, Source: choice.Source, Change: changeWorkspaceStep}
	if whoami, err := client.Whoami(cmd.Context()); err == nil {
		workspace := whoami.Workspace
		for _, candidate := range whoami.Workspaces {
			if client.Workspace != "" && strings.EqualFold(candidate.ID, client.Workspace) {
				workspace = candidate
			}
		}
		if client.Workspace == "" || strings.EqualFold(workspace.ID, client.Workspace) {
			added.ID, added.Name = workspace.ID, workspace.Name
		}
	}
	stderr := cmd.ErrOrStderr()
	fmt.Fprintf(stderr, "slopcop: added %s to %s\n", repositoryName(repoURL), workspaceName(added.Name, added.Name != ""))
	fmt.Fprintf(stderr, "slopcop: that workspace now checks and bills it; it was chosen by %s. %s\n", sourceText(choice.Source), changeWorkspaceStep)
	return added
}

// workspaceName is a resolved workspace's name, quoted, for a question or
// notice about it; "your workspace" when it could not be resolved.
func workspaceName(name string, known bool) string {
	if !known {
		return "your workspace"
	}
	return fmt.Sprintf("%q", name)
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
