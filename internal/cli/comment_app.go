package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/prcomment"
	"github.com/BrokkAi/slopcop/internal/service"
)

var fullCommitPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

// publishWithToken writes the comments with the workflow's own token.
func publishWithToken(cmd *cobra.Command, opts *commentOptions, parsed prcomment.Report, target prcomment.Target, version string) (prcomment.Result, error) {
	token := firstOf(os.Getenv("GITHUB_TOKEN"), os.Getenv("GH_TOKEN"))
	if token == "" {
		return prcomment.Result{}, fmt.Errorf("set GITHUB_TOKEN to a token that may comment on %s#%d", target.Repo, target.PR)
	}
	client := &prcomment.GitHub{
		BaseURL:   firstOf(opts.apiURL, os.Getenv("GITHUB_API_URL"), "https://api.github.com"),
		Token:     token,
		UserAgent: "slopcop/" + version,
	}
	return prcomment.Publish(cmd.Context(), client, parsed, target)
}

// appDeclined is why the service did not post, and, when the person can
// change that, what to do.
type appDeclined struct {
	reason string
	fix    string
}

// publishWithApp asks the service to have the SlopCop GitHub App write the
// comments, with the SlopCop login that ran the check. handled is false, with
// why, when the service did not post them and the workflow's token may; err
// is a failure to ask at all.
func publishWithApp(cmd *cobra.Command, opts *commentOptions, data []byte, parsed prcomment.Report, target prcomment.Target, version string) (result prcomment.Result, handled bool, declined appDeclined, err error) {
	if parsed.CheckID == "" {
		return result, false, appDeclined{reason: "the report has no check id, so the static check did not answer"}, nil
	}
	if !fullCommitPattern.MatchString(target.Commit) {
		return result, false, appDeclined{reason: "the pull request's head commit is unknown (pass --commit)"}, nil
	}
	// The parts of the report the service takes as they are.
	var raw struct {
		Review        json.RawMessage `json:"review"`
		ReviewSkipped string          `json:"reviewSkipped"`
		Summary       *struct {
			FailOn string `json:"failOn"`
		} `json:"summary"`
	}
	_ = json.Unmarshal(data, &raw)
	cfg, err := config.Load()
	if err != nil {
		return result, false, declined, err
	}
	serviceFlag := ""
	if opts.serviceURL != nil {
		serviceFlag = *opts.serviceURL
	}
	if resolveToken(cfg, resolveServiceURL(cfg, serviceFlag)) == "" {
		return result, false, appDeclined{reason: "there is no SlopCop login (set SLOPCOP_TOKEN)"}, nil
	}
	client, err := workspaceClient(cmd, serviceFlag, version, opts.workspace)
	if err != nil {
		return result, false, declined, err
	}
	request := service.PullRequestCommentRequest{
		PullRequest:   target.PR,
		HeadCommit:    target.Commit,
		FailOn:        opts.failOn,
		Certainty:     opts.certainty,
		ReviewSkipped: raw.ReviewSkipped,
	}
	if request.FailOn == "" && raw.Summary != nil {
		request.FailOn = raw.Summary.FailOn
	}
	if len(raw.Review) > 0 && string(raw.Review) != "null" {
		request.Review = raw.Review
	}
	response, err := client.CommentPullRequest(cmd.Context(), parsed.CheckID, request)
	var refused *service.Error
	if errors.As(err, &refused) && refused.StatusCode == http.StatusNotFound {
		// A service from before the endpoint, or a check this login did not
		// start.
		return result, false, appDeclined{reason: "the service cannot comment this check"}, nil
	}
	if err != nil {
		return result, false, declined, err
	}
	if response.Status != "posted" {
		return result, false, appReason(response), nil
	}
	result = prcomment.Render(parsed, target.Commit)
	result.Posted = response.Posted
	result.Inline = response.Inline
	result.Elsewhere = response.Elsewhere
	result.SummaryURL = response.SummaryURL
	return result, true, declined, nil
}

// appReason says why the service did not post, and what would let it.
func appReason(response service.PullRequestCommentResponse) appDeclined {
	switch response.Status {
	case "off":
		return appDeclined{reason: "the service does not post comments from the App"}
	case "not_connected":
		return notConnected(response.NotConnected)
	case "not_granted":
		return appDeclined{reason: "the App's installation has not accepted Pull requests: Read and write"}
	case "stale":
		return appDeclined{reason: "the pull request's head is not the commit the check ran on"}
	case "change_mismatch":
		return appDeclined{reason: "the pull request does not change the files the check did"}
	default:
		return appDeclined{reason: "the service answered " + response.Status}
	}
}

// notConnected says which workspace has no installation for the App to post
// through, and how to give it one. The web app's Repositories page acts on
// the browser's active workspace, so the fix names the workspace to be in.
func notConnected(answer service.NotConnected) appDeclined {
	workspace := "the workspace that ran the check"
	switch {
	case answer.WorkspaceKind == "personal":
		workspace = "your personal workspace"
	case answer.WorkspaceName != "":
		workspace = fmt.Sprintf("the %q workspace", answer.WorkspaceName)
	}
	repository := "this repository"
	if answer.Repository != "" {
		repository = answer.Repository
	}
	repositoriesPage := "the SlopCop Repositories page"
	connect := "click Connect GitHub on the Repositories page"
	if answer.ConnectURL != "" {
		repositoriesPage = answer.ConnectURL
		connect = "open " + answer.ConnectURL + " and click Connect GitHub"
	}
	switch answer.Reason {
	case "no_github_connection":
		return appDeclined{
			reason: workspace + ", which ran the check, has no GitHub connection",
			fix: fmt.Sprintf("To let the App comment, %s with %s active. Installing the App on GitHub is not enough: the workspace needs its own connection.",
				connect, workspace),
		}
	case "repository_not_covered":
		owner, _, _ := strings.Cut(repository, "/")
		if answer.InstallationSettingsURL != "" {
			return appDeclined{
				reason: fmt.Sprintf("the SlopCop App's installation on %s, which %s's GitHub connection holds, does not include %s", owner, workspace, repository),
				fix: fmt.Sprintf("To let the App comment, add %s under Repository access at %s. It then shows on %s with %s active.",
					repository, answer.InstallationSettingsURL, repositoriesPage, workspace),
			}
		}
		install := "Install the SlopCop App on the account that owns " + repository
		if answer.InstallURL != "" {
			install += " (" + answer.InstallURL + ")"
		}
		return appDeclined{
			reason: fmt.Sprintf("no SlopCop App installation that %s's GitHub connection holds includes %s", workspace, repository),
			fix: fmt.Sprintf("To let the App comment: %s with %s selected, or add it to the installation there if there is one; then %s with %s active.",
				install, repository, connect, workspace),
		}
	case "not_github":
		return appDeclined{reason: "the App comments only on GitHub pull requests, and " + repository + " is not on GitHub"}
	case "no_workspace":
		return appDeclined{reason: "the SlopCop login has no workspace, so it has no GitHub connection"}
	default:
		return appDeclined{reason: "no GitHub connection of the workspace covers this repository"}
	}
}
