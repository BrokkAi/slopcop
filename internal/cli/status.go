package cli

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/gitx"
	"github.com/BrokkAi/slopcop/internal/report"
	"github.com/BrokkAi/slopcop/internal/service"
)

// The states status reports for a checkout's repository.
const (
	statusActive      = "active"
	statusDisabled    = "disabled"
	statusOverLimit   = "over_limit"
	statusNotAdded    = "not_added"
	statusUnsupported = "unsupported"
	// statusUnknown is a workspace whose repositories the service would not
	// list: it may have the repository.
	statusUnknown = "unknown"
)

// elsewhereTimeout bounds the lookups behind status's other workspaces, so
// an account with many does not hold up the answer.
const elsewhereTimeout = 15 * time.Second

type statusOptions struct {
	root      string
	format    string
	workspace string
}

// statusReport is the human and json answer. Account, Workspace, Plan and
// Login come from whoami; Repository describes the checkout the origin remote
// names.
type statusReport struct {
	Service   string            `json:"service"`
	Account   service.Account   `json:"account"`
	Workspace service.Workspace `json:"workspace"`
	// Source says how Workspace was chosen: flag, env, mapping or login.
	Source string `json:"workspaceSource"`
	// Plan is Workspace's plan. It is empty when Workspace is not the
	// login's own: whoami answers only that one's plan.
	Plan string `json:"plan,omitempty"`
	// Login is the login's own workspace and its plan, when Workspace is
	// another one.
	Login      *statusLogin     `json:"login,omitempty"`
	Repository statusRepository `json:"repository"`
	// OtherWorkspaces are the account's other workspaces that have the
	// repository, or may: each can run its pull request checks, whatever
	// Workspace is.
	OtherWorkspaces []statusElsewhere `json:"otherWorkspaces,omitempty"`
	// PullRequestChecks is the workspace that checks the repository's pull
	// requests, which may not be Workspace. It is absent when the service
	// did not say.
	PullRequestChecks *statusPullRequestChecks `json:"pullRequestChecks,omitempty"`
}

// statusLogin is the workspace the login is bound to, and that workspace's
// plan.
type statusLogin struct {
	Workspace service.Workspace `json:"workspace"`
	Plan      string            `json:"plan"`
}

// statusElsewhere is another workspace of the account and the repository's
// state there: active, disabled, over_limit, or unknown when the service
// would not say.
type statusElsewhere struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Role  string `json:"role"`
	State string `json:"state"`
}

// statusRepository is the checkout's repository, with the workspace's record
// when the service has one.
type statusRepository struct {
	URL      string `json:"url"`
	FullName string `json:"fullName"`
	Provider string `json:"provider"`
	State    string `json:"state"`
	ID       string `json:"id,omitempty"`
	Enabled  bool   `json:"enabled"`
	Active   bool   `json:"active"`
	// listedIn is the workspace whose listing this is: the one the command
	// ran in. It is empty when the service was not asked.
	listedIn string
	// pullRequests is what that listing said about the workspace that checks
	// the repository's pull requests.
	pullRequests pullRequestOwner
}

func newStatusCommand(serviceURL *string, version string) *cobra.Command {
	opts := &statusOptions{}
	command := &cobra.Command{
		Use:   "status",
		Short: "Show whether this checkout's repository is checked in the workspace",
		Long: `Show the account and workspace that bill checks, and whether the repository
the origin remote names is added to that workspace and active.

"Chosen by" says why that workspace: --workspace, then $SLOPCOP_WORKSPACE,
then the workspace this machine maps the checkout to ('slopcop workspace
map'), then the one the login is bound to.

"Plan" is that workspace's plan. The service tells a login the plan of the
workspace it is bound to and no other, so when the workspace above is another
one there is no "Plan" line: a "Login" line names the login's own workspace
and its plan instead.

"Also in" names the account's other workspaces that have the repository.

"PR checks" names the workspace that checks the repository's pull requests on
GitHub. It does not follow the workspace above: each workspace turns them on
for a repository in the web app, and a pull request's findings, and the
ignores that apply to them, are that workspace's. When it is another
workspace, the line gives the --workspace that makes a local check give the
pull request check's answer.

A repository that is not added is added by its first check. A disabled one is not checked; the workspace's owner
enables it with 'slopcop repo enable'. SlopCop checks repositories on
github.com, gitlab.com and bitbucket.org.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runStatus(cmd, opts, *serviceURL, version)
		},
	}
	flags := command.Flags()
	flags.StringVar(&opts.root, "root", ".", "repository path")
	addFormatFlag(command, &opts.format)
	addWorkspaceFlag(command, false, &opts.workspace)
	return command
}

func runStatus(cmd *cobra.Command, opts *statusOptions, serviceFlag, version string) error {
	if err := checkFormat(opts.format); err != nil {
		return err
	}
	repoRoot, err := gitx.Toplevel(opts.root)
	if err != nil {
		return err
	}
	origin, err := gitx.OriginURL(repoRoot)
	if err != nil {
		return err
	}
	repoURL, err := gitx.CanonicalRepoURL(origin)
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
	choice := chooseWorkspace(opts.workspace, cfg, client.BaseURL, repoURL)
	workspace, err := chosenWorkspace(whoami, choice)
	if err != nil {
		return serviceFailure(cmd, err)
	}
	if choice.Source != sourceLogin {
		client.Workspace = workspace.ID
	}
	repository, err := repositoryStatus(cmd.Context(), client, repoURL)
	if err != nil {
		return serviceFailure(cmd, err)
	}
	answer := statusReport{
		Service:    client.BaseURL,
		Account:    whoami.Account,
		Workspace:  workspace,
		Source:     choice.Source,
		Repository: repository,
	}
	// whoami's plan is the plan of the login's workspace, and of no other.
	if strings.EqualFold(workspace.ID, whoami.Workspace.ID) {
		answer.Plan = whoami.Plan
	} else {
		answer.Login = &statusLogin{Workspace: whoami.Workspace, Plan: whoami.Plan}
	}
	owner := repository.pullRequests
	if repository.State != statusUnsupported && len(whoami.Workspaces) > 1 {
		var elsewhere pullRequestOwner
		answer.OtherWorkspaces, elsewhere = otherWorkspaces(cmd.Context(), client, whoami.Workspaces, repoURL, workspace.ID)
		// A workspace without the repository does not list it, and so says
		// nothing of its pull requests; one that has it does.
		if !owner.known {
			owner = elsewhere
		}
	}
	if repository.Provider == "github" {
		answer.PullRequestChecks = resolvePullRequestChecks(owner, workspace.ID, whoami.Workspaces)
	}
	if opts.format == "json" {
		return writeJSON(cmd.OutOrStdout(), answer)
	}
	writeStatus(cmd.OutOrStdout(), answer)
	return nil
}

// otherWorkspaces are the account's workspaces, other than the one with id
// except, that have repoURL, and those the service would not answer for.
// Pull request checks are turned on per workspace and do not follow the
// workspace a checkout checks in, so these are where they may run instead.
//
// owner is what the first of them to say reported about the workspace that
// checks the repository's pull requests.
func otherWorkspaces(ctx context.Context, client *service.Client, workspaces []service.Workspace, repoURL, except string) (others []statusElsewhere, owner pullRequestOwner) {
	ctx, cancel := context.WithTimeout(ctx, elsewhereTimeout)
	defer cancel()
	for _, asked := range repositoryElsewhere(ctx, client, workspaces, repoURL, except) {
		if !owner.known && asked.Err == nil {
			owner = asked.PullRequests
		}
		state := asked.State
		switch {
		case asked.Err != nil:
			state = statusUnknown
		case !asked.held():
			continue
		}
		others = append(others, statusElsewhere{
			ID: asked.Workspace.ID, Name: asked.Workspace.Name,
			Kind: asked.Workspace.Kind, Role: asked.Workspace.Role, State: state,
		})
	}
	return others, owner
}

// lookupRepositoryStatus describes the repository the checkout's origin remote
// names, and its state in the workspace client points at.
func lookupRepositoryStatus(ctx context.Context, client *service.Client, repoRoot string) (statusRepository, error) {
	origin, err := gitx.OriginURL(repoRoot)
	if err != nil {
		return statusRepository{}, err
	}
	repoURL, err := gitx.CanonicalRepoURL(origin)
	if err != nil {
		return statusRepository{}, err
	}
	return repositoryStatus(ctx, client, repoURL)
}

// repositoryStatus describes repoURL in the workspace client points at. A
// provider SlopCop does not check is unsupported and is not listed.
func repositoryStatus(ctx context.Context, client *service.Client, repoURL string) (statusRepository, error) {
	repository := localRepository(repoURL)
	if repository.Provider == "" {
		return repository, nil
	}
	listed, err := client.Repositories(ctx)
	if err != nil {
		return repository, err
	}
	repository.listedIn = listed.WorkspaceID
	for _, candidate := range listed.Repositories {
		if strings.EqualFold(candidate.Provider, repository.Provider) &&
			strings.EqualFold(candidate.FullName, repository.FullName) {
			repository.ID = candidate.ID
			repository.Enabled = candidate.Enabled
			repository.Active = candidate.Active
			repository.State = statusState(candidate)
			repository.pullRequests.workspace, repository.pullRequests.known = candidate.PullRequestCheckWorkspace()
			break
		}
	}
	return repository, nil
}

// localRepository describes the checkout from its canonical URL before the
// service is asked. A provider SlopCop does not check can never be added.
func localRepository(repoURL string) statusRepository {
	repository := statusRepository{
		URL:      repoURL,
		FullName: repositoryName(repoURL),
		State:    statusNotAdded,
	}
	parsed, err := url.Parse(repoURL)
	if err != nil {
		repository.State = statusUnsupported
		return repository
	}
	repository.Provider = repositoryProviders[parsed.Hostname()]
	if repository.Provider == "" {
		repository.State = statusUnsupported
	}
	return repository
}

// statusState maps the workspace's record to the state status prints.
func statusState(repository service.Repository) string {
	switch {
	case repository.Active:
		return statusActive
	case repository.Enabled:
		// Enabled, but past the workspace's active limit.
		return statusOverLimit
	default:
		return statusDisabled
	}
}

// writeStatus prints the answer for a person or an agent.
func writeStatus(out io.Writer, answer statusReport) {
	name := report.Printable(answer.Repository.FullName)
	if answer.Repository.Provider != "" {
		name += " (" + answer.Repository.Provider + ")"
	}
	fmt.Fprintf(out, "Repository: %s\n", name)
	fmt.Fprintf(out, "State:      %s\n", statusText(answer.Repository))
	account := answer.Account.Username
	if answer.Account.Email != "" {
		account += " <" + answer.Account.Email + ">"
	}
	fmt.Fprintf(out, "Account:    %s\n", account)
	fmt.Fprintf(out, "Workspace:  %s (%s, %s)\n", answer.Workspace.Name, answer.Workspace.Kind, answer.Workspace.Role)
	fmt.Fprintf(out, "Chosen by:  %s\n", sourceText(answer.Source))
	if len(answer.OtherWorkspaces) > 0 {
		others := make([]string, len(answer.OtherWorkspaces))
		for index, other := range answer.OtherWorkspaces {
			others[index] = fmt.Sprintf("%q (%s)", other.Name, elsewhereText(other.State))
		}
		fmt.Fprintf(out, "Also in:    %s\n", strings.Join(others, ", "))
	}
	switch {
	case answer.PullRequestChecks != nil:
		fmt.Fprintf(out, "PR checks:  %s\n", answer.PullRequestChecks.statusLineText())
	case len(answer.OtherWorkspaces) > 0 && answer.Repository.Provider == "github":
		fmt.Fprintf(out, "PR checks:  %s\n", pullRequestChecksText)
	}
	if login := answer.Login; login != nil {
		fmt.Fprintf(out, "Login:      %s (%s, %s), %s plan\n",
			login.Workspace.Name, login.Workspace.Kind, login.Workspace.Role, planName(login.Plan))
	} else {
		fmt.Fprintf(out, "Plan:       %s\n", planName(answer.Plan))
	}
	fmt.Fprintf(out, "Service:    %s\n", answer.Service)
}

// pullRequestChecksText is what status says of pull request checks when
// more than one workspace has the repository and the service did not say
// which workspace checks them, so it says where to look.
const pullRequestChecksText = "turned on per workspace in the web app, not by the workspace above; a pull request's SlopCop check names the workspace that ran it"

// elsewhereText is a repository's state in another workspace as a person
// reads it.
func elsewhereText(state string) string {
	switch state {
	case statusOverLimit:
		return "enabled, over the limit"
	case statusUnknown:
		return "could not be asked"
	default:
		return state
	}
}

// statusText is the state as a person reads it, with the next step where
// there is one.
func statusText(repository statusRepository) string {
	switch repository.State {
	case statusNotAdded:
		return "not added (the next check adds it)"
	case statusUnsupported:
		return "unsupported (SlopCop checks github.com, gitlab.com and bitbucket.org)"
	case statusOverLimit:
		return "enabled, over the limit"
	default:
		return repository.State
	}
}
