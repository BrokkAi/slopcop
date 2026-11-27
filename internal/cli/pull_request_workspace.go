package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/service"
)

// pullRequestLookupTimeout bounds the lookups an ignore makes, after it is
// done, to say whether the repository's pull requests are checked in another
// workspace. The ignore has already happened, so a slow answer is dropped.
const pullRequestLookupTimeout = 10 * time.Second

// pullRequestOwner is what one repository listing said about the workspace
// that checks the repository's pull requests. known is false when the
// service did not say; workspace is nil, with known true, when no workspace
// checks them.
type pullRequestOwner struct {
	known     bool
	workspace *service.PullRequestCheckWorkspace
}

// statusPullRequestChecks is the workspace that checks the repository's pull
// requests, as status reports it. A pull request's findings, and the ignores
// that apply to them, are that workspace's.
type statusPullRequestChecks struct {
	// On is false when no workspace checks the repository's pull requests.
	On          bool   `json:"on"`
	WorkspaceID string `json:"workspaceId,omitempty"`
	// WorkspaceName is empty for a personal workspace the account is not
	// in, which the service does not name.
	WorkspaceName string `json:"workspaceName,omitempty"`
	WorkspaceKind string `json:"workspaceKind,omitempty"`
	// Member says the account belongs to it, so --workspace reaches it.
	Member bool `json:"member"`
	// SameWorkspace says it is the workspace this command ran in, so a local
	// check gives the answer the pull request check gives.
	SameWorkspace bool `json:"sameWorkspace"`
	// Selector is what --workspace takes for it, when the account is in it.
	Selector string `json:"selector,omitempty"`
}

// resolvePullRequestChecks names the workspace in owner from the account's
// workspaces, and says whether it is used, the workspace this command ran
// in. It is nil when the service did not say.
func resolvePullRequestChecks(owner pullRequestOwner, used string, workspaces []service.Workspace) *statusPullRequestChecks {
	if !owner.known {
		return nil
	}
	if owner.workspace == nil {
		return &statusPullRequestChecks{}
	}
	checks := &statusPullRequestChecks{
		On:            true,
		WorkspaceID:   owner.workspace.WorkspaceID,
		WorkspaceName: owner.workspace.WorkspaceName,
		Member:        owner.workspace.Member,
		SameWorkspace: used != "" && strings.EqualFold(owner.workspace.WorkspaceID, used),
	}
	for _, workspace := range workspaces {
		if strings.EqualFold(workspace.ID, checks.WorkspaceID) {
			checks.WorkspaceName, checks.WorkspaceKind, checks.Member = workspace.Name, workspace.Kind, true
			checks.Selector = workspaceSelector(workspaces, workspace)
			break
		}
	}
	// The account's workspaces could not be read, but the service said the
	// account is in this one, so its id reaches it.
	if checks.Member && checks.Selector == "" {
		checks.Selector = checks.WorkspaceID
	}
	return checks
}

// elsewhere reports whether the pull requests are checked in a workspace
// other than the one this command ran in.
func (c *statusPullRequestChecks) elsewhere() bool {
	return c != nil && c.On && !c.SameWorkspace
}

// workspaceText names the workspace that checks the pull requests for a
// sentence.
func (c *statusPullRequestChecks) workspaceText() string {
	switch {
	case c.Member && c.WorkspaceName != "":
		return fmt.Sprintf("%q", c.WorkspaceName)
	case c.Member:
		return fmt.Sprintf("your personal workspace (%s)", c.WorkspaceID)
	case c.WorkspaceName != "":
		return fmt.Sprintf("%q (%s), a workspace your account is not in", c.WorkspaceName, c.WorkspaceID)
	default:
		return fmt.Sprintf("a personal workspace your account is not in (%s)", c.WorkspaceID)
	}
}

// sameAnswer says how to get the pull request check's answer locally: the
// --workspace that reaches its workspace, or, when the account is not in
// it, who decides.
func (c *statusPullRequestChecks) sameAnswer() string {
	if c.Member && c.Selector != "" {
		return fmt.Sprintf("`slopcop check -w %q` gives the same answer as the pull request check", c.Selector)
	}
	return "no --workspace of yours reaches it; that workspace's owner can turn its pull request checks off, so the one that has them on next checks them"
}

// statusLineText is the "PR checks" line of status.
func (c *statusPullRequestChecks) statusLineText() string {
	switch {
	case !c.On:
		return "not checked: no workspace has Check pull requests on for this repository"
	case c.SameWorkspace:
		return "the workspace above"
	default:
		return fmt.Sprintf("%s, not the workspace above, so local findings and ignores are not theirs; %s",
			c.workspaceText(), c.sameAnswer())
	}
}

// workspaceNameByID is the quoted name of the account's workspace with id,
// for a sentence, or "your workspace" when the account has no such one.
func workspaceNameByID(workspaces []service.Workspace, id string) string {
	for _, workspace := range workspaces {
		if strings.EqualFold(workspace.ID, id) {
			return workspaceName(workspace.Name, true)
		}
	}
	return workspaceName("", false)
}

// pullRequestChecksElsewhere is the workspace that checks the repository's
// pull requests, named, and the account's workspaces, when that is not the
// workspace repository was listed in, the one this command ran in; nil
// otherwise. The account's workspaces are asked for only then, or when this
// workspace has not added the repository and so its listing says nothing of
// it: then the other workspaces are asked, since that first check is the one
// that splits the repository across two workspaces.
func pullRequestChecksElsewhere(ctx context.Context, client *service.Client, repository statusRepository) (*statusPullRequestChecks, []service.Workspace) {
	owner := repository.pullRequests
	unlisted := !owner.known && repository.State == statusNotAdded && repository.Provider == "github" && repository.listedIn != ""
	if !unlisted && (!owner.known || owner.workspace == nil || strings.EqualFold(owner.workspace.WorkspaceID, repository.listedIn)) {
		return nil, nil
	}
	var workspaces []service.Workspace
	if whoami, err := client.Whoami(ctx); err == nil {
		workspaces = whoami.Workspaces
	}
	if unlisted {
		if len(workspaces) < 2 {
			return nil, nil
		}
		for _, asked := range repositoryElsewhere(ctx, client, workspaces, repository.URL, repository.listedIn) {
			if asked.Err == nil && asked.PullRequests.known {
				owner = asked.PullRequests
				break
			}
		}
	}
	checks := resolvePullRequestChecks(owner, repository.listedIn, workspaces)
	if !checks.elsewhere() {
		return nil, nil
	}
	return checks, workspaces
}

// warnPullRequestChecksElsewhere says, on stderr, that the repository's pull
// requests are checked in another workspace than the one this check ran in,
// so a finding it reports or that is ignored here may differ there.
// It is bounded like the ignore's lookups, so the check is not held up long,
// and reports whether it warned.
func warnPullRequestChecksElsewhere(cmd *cobra.Command, client *service.Client, repository statusRepository) bool {
	ctx, cancel := context.WithTimeout(cmd.Context(), pullRequestLookupTimeout)
	defer cancel()
	checks, workspaces := pullRequestChecksElsewhere(ctx, client, repository)
	if checks == nil {
		return false
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"slopcop: pull requests of %s are checked in %s, not in %s, which this check ran in. Findings and ignores belong to one workspace, so the pull request check may report findings this check does not; %s.\n",
		repositoryDisplayName(repository), checks.workspaceText(), workspaceNameByID(workspaces, repository.listedIn), checks.sameAnswer())
	return true
}

// warnPullRequestChecksAfterAdd is warnPullRequestChecksElsewhere after a
// check added the repository, listed again within the same bound: the check
// has already answered, so a slow listing is dropped.
func warnPullRequestChecksAfterAdd(cmd *cobra.Command, client *service.Client, repoRoot string) {
	ctx, cancel := context.WithTimeout(cmd.Context(), pullRequestLookupTimeout)
	defer cancel()
	if repository, err := checkRepository(ctx, client, repoRoot); err == nil {
		warnPullRequestChecksElsewhere(cmd, client, repository)
	}
}

// ignoreElsewhereNotice is warnIgnoreElsewhere, a variable so tests of
// ignore that run in this repository's own checkout do not ask the service
// about it.
var ignoreElsewhereNotice = warnIgnoreElsewhere

// warnIgnoreElsewhere says, on stderr, after an ignore, that the checkout's
// repository has its pull requests checked in another workspace than the one
// the ignore went to, so their check still reports the finding. The service
// does not say which repository an ignored finding is in, so, like the choice
// of workspace, it takes the checkout's and says so. It asks the service and
// says nothing when it cannot.
func warnIgnoreElsewhere(cmd *cobra.Command, client *service.Client, root string) {
	repoURL := checkoutRepoURL(root)
	if repoURL == "" || localRepository(repoURL).Provider != "github" {
		return
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), pullRequestLookupTimeout)
	defer cancel()
	repository, err := repositoryStatus(ctx, client, repoURL)
	if err != nil {
		return
	}
	checks, workspaces := pullRequestChecksElsewhere(ctx, client, repository)
	if checks == nil {
		return
	}
	next := "no --workspace of yours reaches it, so ask that workspace's owner to ignore it there"
	if checks.Member && checks.Selector != "" {
		next = fmt.Sprintf("a finding has its own id in each workspace, so run `slopcop check -w %q` and ignore the ids it prints with `slopcop ignore -w %q <id>`", checks.Selector, checks.Selector)
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"slopcop: if this finding is from %[2]s, the repository checked out here: this ignore applies in %[1]s, but pull requests of %[2]s are checked in %[3]s, whose check will still report the finding; %[4]s.\n",
		workspaceNameByID(workspaces, repository.listedIn), repositoryDisplayName(repository), checks.workspaceText(), next)
}
