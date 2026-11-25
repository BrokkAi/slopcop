package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// PullRequestCommentRequest asks the service to have the SlopCop GitHub App
// comment a check's findings on the pull request it ran on. It carries no
// GitHub token: the App posts with its own.
type PullRequestCommentRequest struct {
	PullRequest int `json:"pullRequest"`
	// HeadCommit is the pull request's head commit the check ran on. The
	// service refuses a pull request whose head has moved on.
	HeadCommit string `json:"headCommit"`
	// FailOn and Certainty are the ones the check ran with, so the service
	// answers the same static findings the report holds.
	FailOn    string `json:"failOn,omitempty"`
	Certainty string `json:"certainty,omitempty"`
	// Review is the report's own review object, unchanged: the review ran
	// here, so only its findings come from the caller.
	Review        json.RawMessage `json:"review,omitempty"`
	ReviewSkipped string          `json:"reviewSkipped,omitempty"`
}

// PullRequestCommentResponse is what the service did. Status is "posted",
// or why it did not post: "off" (the deployment does not post comments),
// "not_connected" (no GitHub connection of the workspace covers the
// repository), "not_granted" (the installation has not accepted Pull
// requests write), "stale" (the pull request's head moved on) or
// "change_mismatch" (the pull request does not change what the check did).
type PullRequestCommentResponse struct {
	CheckID    string `json:"checkId"`
	Status     string `json:"status"`
	Outcome    string `json:"outcome"`
	Headline   string `json:"headline"`
	Posted     int    `json:"posted"`
	Inline     int    `json:"inline"`
	Elsewhere  int    `json:"elsewhere"`
	SummaryURL string `json:"summaryUrl"`
	NotConnected
}

// NotConnected is what a "not_connected" answer adds, so the caller can say
// which workspace to fix and where. A service from before it sends none of
// it.
type NotConnected struct {
	// Reason is "no_github_connection" (the workspace that ran the check has
	// no usable GitHub connection: none, or one that expired or was
	// revoked), "repository_not_covered" (it has one, but no App
	// installation it holds includes the repository), "not_github" (the
	// repository is on GitLab or Bitbucket) or "no_workspace" (the login has
	// no workspace).
	Reason        string `json:"reason"`
	WorkspaceName string `json:"workspaceName"`
	// WorkspaceKind is "personal" or "team".
	WorkspaceKind string `json:"workspaceKind"`
	// WorkspaceRole is the caller's role in the workspace, "owner" or
	// "member": only an owner connects GitHub.
	WorkspaceRole string `json:"workspaceRole"`
	// Repository is the check's repository as owner/name.
	Repository string `json:"repository"`
	// ConnectURL is the workspace's own settings page, at its Integrations
	// section, where Connect GitHub lives whichever workspace the browser
	// has active.
	ConnectURL string `json:"connectUrl"`
	// InstallURL is the App's install page on GitHub.
	InstallURL string `json:"installUrl"`
	// InstallationSettingsURL is the Repository access page of the
	// installation the workspace's connection holds on the repository's
	// owner; empty when it holds none there.
	InstallationSettingsURL string `json:"installationSettingsUrl"`
}

// CommentPullRequest asks the service to comment check checkID on a pull
// request. Service failures come back as *Error; a service from before this
// endpoint answers 404.
func (c *Client) CommentPullRequest(ctx context.Context, checkID string, request PullRequestCommentRequest) (PullRequestCommentResponse, error) {
	path := "/v1/check/" + url.PathEscape(checkID) + "/pull-request-comments"
	status, _, payload, err := c.postJSON(ctx, path, request)
	if err != nil {
		return PullRequestCommentResponse{}, err
	}
	if status < 200 || status >= 300 {
		return PullRequestCommentResponse{}, decodeError(status, payload)
	}
	var decoded PullRequestCommentResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return PullRequestCommentResponse{}, fmt.Errorf("decode pull request comment response: %w", err)
	}
	if decoded.Status == "" {
		return PullRequestCommentResponse{}, fmt.Errorf("decode pull request comment response: it has no status")
	}
	return decoded, nil
}
