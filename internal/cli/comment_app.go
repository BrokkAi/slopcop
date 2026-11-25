package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// appRefusedTarget reports that the service checked the pull request and
// found the report is not for it: its head moved on, or it does not make the
// check's change. The workflow's token must not comment it either.
type appRefusedTarget struct{ reason string }

func (e *appRefusedTarget) Error() string {
	return "the SlopCop GitHub App did not comment: " + e.reason + "; the report is not for this pull request as it is now, so GITHUB_TOKEN does not comment it either"
}

// publishWithApp asks the service to have the SlopCop GitHub App write the
// comments, with the SlopCop login that ran the check. handled is false, with
// the reason, when the service did not post them and the workflow's token
// may; err is a failure to ask at all, or an *appRefusedTarget when the
// service found the report is not for the pull request.
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
		Certainty     string          `json:"certainty"`
		Workspace     string          `json:"workspace"`
		Service       string          `json:"service"`
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
	serviceFlag = commentService(cmd.ErrOrStderr(), cfg, serviceFlag, raw.Service)
	if resolveToken(cfg, resolveServiceURL(cfg, serviceFlag)) == "" {
		return result, false, "there is no SlopCop login (set SLOPCOP_TOKEN)", nil
	}
	// The check's own workspace, which args can have chosen, unless the
	// caller names one.
	workspace := opts.workspace
	if workspace == "" {
		workspace = raw.Workspace
	}
	client, err := workspaceClient(cmd, serviceFlag, version, workspace)
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
	// The report says what the check ran with, which args can have changed
	// from the action's inputs.
	if request.FailOn == "" && raw.Summary != nil {
		request.FailOn = raw.Summary.FailOn
	}
	if request.Certainty == "" {
		request.Certainty = raw.Certainty
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
	switch response.Status {
	case "posted":
	case "stale", "change_mismatch":
		return result, false, "", &appRefusedTarget{reason: appReason(response.Status)}
	default:
		return result, false, appReason(response.Status), nil
	}
	result = prcomment.Render(parsed, target.Commit)
	result.Posted = response.Posted
	result.Inline = response.Inline
	result.Elsewhere = response.Elsewhere
	result.SummaryURL = response.SummaryURL
	return result, true, "", nil
}

// commentService is the --service to comment through. --service and
// $SLOPCOP_SERVICE win; without them it is the service the report's check
// ran against, so the App path asks the service that knows the check. A
// report is a file anyone can write, so it never moves a login: it is used
// only when this machine stores a login for that exact service, and never
// with $SLOPCOP_TOKEN, which may have been issued by another. Either way
// stderr says which service the command asks.
func commentService(stderr io.Writer, cfg config.Config, serviceFlag, reported string) string {
	if strings.TrimSpace(serviceFlag) != "" || strings.TrimSpace(os.Getenv("SLOPCOP_SERVICE")) != "" {
		return serviceFlag
	}
	// The parse also refuses control characters, so the URL is safe to print.
	reported = strings.TrimRight(reportedService(strings.TrimSpace(reported)), "/")
	if reported == "" {
		return serviceFlag
	}
	// The whole URL, path included: a service can sit under a path on a
	// host that serves another one.
	fallback := resolveServiceURL(cfg, "")
	if reported == fallback {
		return serviceFlag
	}
	switch {
	case strings.TrimSpace(os.Getenv("SLOPCOP_TOKEN")) != "":
		fmt.Fprintf(stderr, "slopcop: the check ran against %s, but $SLOPCOP_TOKEN may be another service's, so this asks %s; pass --service %s to ask the check's\n", reported, fallback, reported)
	case cfg.TokenFor(reported) == "":
		fmt.Fprintf(stderr, "slopcop: the check ran against %s, which this machine has no login for, so this asks %s\n", reported, fallback)
	default:
		fmt.Fprintf(stderr, "slopcop: asking %s, the service the check ran against\n", reported)
		return reported
	}
	return serviceFlag
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
