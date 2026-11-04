package cli

import (
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

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
)

type statusOptions struct {
	root      string
	format    string
	workspace string
}

// statusReport is the human and json answer. Account, Workspace and Plan come
// from whoami; Repository describes the checkout the origin remote names.
type statusReport struct {
	Service    string            `json:"service"`
	Account    service.Account   `json:"account"`
	Workspace  service.Workspace `json:"workspace"`
	Plan       string            `json:"plan"`
	Repository statusRepository  `json:"repository"`
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
}

func newStatusCommand(serviceURL *string, version string) *cobra.Command {
	opts := &statusOptions{}
	command := &cobra.Command{
		Use:   "status",
		Short: "Show whether this checkout's repository is checked in the workspace",
		Long: `Show the account and workspace that bill checks, and whether the repository
the origin remote names is added to that workspace and active.

A repository that is not added is added by its first check. A disabled one is
not checked or charged; the workspace's owner enables it with
'slopcop repo enable'. SlopCop checks repositories on github.com, gitlab.com
and bitbucket.org.`,
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
	workspace := whoami.Workspace
	if wanted := resolveWorkspaceName(opts.workspace); wanted != "" {
		workspace, err = findWorkspace(whoami.Workspaces, wanted)
		if err != nil {
			return err
		}
		client.Workspace = workspace.ID
	}
	repository := localRepository(repoURL)
	if repository.Provider != "" {
		listed, err := client.Repositories(cmd.Context())
		if err != nil {
			return serviceFailure(cmd, err)
		}
		for _, candidate := range listed.Repositories {
			if strings.EqualFold(candidate.Provider, repository.Provider) &&
				strings.EqualFold(candidate.FullName, repository.FullName) {
				repository.ID = candidate.ID
				repository.Enabled = candidate.Enabled
				repository.Active = candidate.Active
				repository.State = statusState(candidate)
				break
			}
		}
	}
	answer := statusReport{
		Service:    client.BaseURL,
		Account:    whoami.Account,
		Workspace:  workspace,
		Plan:       whoami.Plan,
		Repository: repository,
	}
	if opts.format == "json" {
		return writeJSON(cmd.OutOrStdout(), answer)
	}
	writeStatus(cmd.OutOrStdout(), answer)
	return nil
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
	fmt.Fprintf(out, "Plan:       %s\n", planName(answer.Plan))
	fmt.Fprintf(out, "Service:    %s\n", answer.Service)
}

// statusText is the state as a person reads it, with the next step where
// there is one.
func statusText(repository statusRepository) string {
	switch repository.State {
	case statusNotAdded:
		return "not added (a first check adds it to the workspace)"
	case statusUnsupported:
		return "unsupported (SlopCop checks github.com, gitlab.com and bitbucket.org)"
	case statusOverLimit:
		return "enabled, over the limit"
	default:
		return repository.State
	}
}
