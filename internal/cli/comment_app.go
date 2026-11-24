package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"

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

// publishWithApp asks the service to have the SlopCop GitHub App write the
// comments, with the SlopCop login that ran the check. handled is false, with
// the reason, when the service did not post them and the workflow's token
// may; err is a failure to ask at all.
func publishWithApp(cmd *cobra.Command, opts *commentOptions, data []byte, parsed prcomment.Report, target prcomment.Target, version string) (result prcomment.Result, handled bool, reason string, err error) {
	if parsed.CheckID == "" {
		return result, false, "the report has no check id, so the static check did not answer", nil
	}
	if !fullCommitPattern.MatchString(target.Commit) {
		return result, false, "the pull request's head commit is unknown (pass --commit)", nil
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
		return result, false, "", err
	}
	serviceFlag := ""
	if opts.serviceURL != nil {
		serviceFlag = *opts.serviceURL
	}
	if resolveToken(cfg, resolveServiceURL(cfg, serviceFlag)) == "" {
		return result, false, "there is no SlopCop login (set SLOPCOP_TOKEN)", nil
	}
	client, err := workspaceClient(cmd, serviceFlag, version, opts.workspace)
	if err != nil {
		return result, false, "", err
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
		return result, false, "the service cannot comment this check", nil
	}
	if err != nil {
		return result, false, "", err
	}
	if response.Status != "posted" {
		return result, false, appReason(response.Status), nil
	}
	result = prcomment.Render(parsed, target.Commit)
	result.Posted = response.Posted
	result.Inline = response.Inline
	result.Elsewhere = response.Elsewhere
	result.SummaryURL = response.SummaryURL
	return result, true, "", nil
}

// appReason says why the service did not post.
func appReason(status string) string {
	switch status {
	case "off":
		return "the service does not post comments from the App"
	case "not_connected":
		return "no GitHub connection of the workspace covers this repository"
	case "not_granted":
		return "the App's installation has not accepted Pull requests: Read and write"
	case "stale":
		return "the pull request's head is not the commit the check ran on"
	case "change_mismatch":
		return "the pull request does not change the files the check did"
	default:
		return "the service answered " + status
	}
}
