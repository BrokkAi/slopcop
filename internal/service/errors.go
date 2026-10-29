package service

import (
	"fmt"
	"strings"
)

// ErrorBody is the closed failure contract returned by the service.
type ErrorBody struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Remedy     string `json:"remedy"`
	Retryable  bool   `json:"retryable"`
	InstallURL string `json:"installUrl,omitempty"`
	// TopUpURL is where to add credits, for a workspace out of credits.
	TopUpURL string `json:"topUpUrl,omitempty"`
}

// knownErrors fills in the message and remedy of a closed code the service
// answered without them, so the caller still prints a useful failure.
var knownErrors = map[string]ErrorBody{
	"client_outdated": {
		Message: "This version of slopcop is older than the check service accepts.",
		Remedy:  "Upgrade slopcop to the latest release, then retry: https://github.com/BrokkAi/slopcop#install",
	},
	"finding_not_found": {
		Message: "SlopCop has no such finding for this login.",
		Remedy:  "Use a finding id from a check you ran with this login.",
	},
}

// personCodes are the refusals only a person can clear: signing in, paying,
// installing or granting access, or choosing what the workspace checks. An
// agent that retries or rewrites the change gets the same answer.
var personCodes = map[string]bool{
	"auth_required":                      true,
	"auth_expired":                       true,
	"account_required":                   true,
	"repo_not_indexed":                   true,
	"app_not_installed":                  true,
	"app_no_repo_access":                 true,
	"gitlab_not_connected":               true,
	"no_access":                          true,
	"insufficient_credits":               true,
	"repository_disabled":                true,
	"repository_add_unconfirmed":         true,
	"workspace_repository_limit_reached": true,
	"active_repository_limit_reached":    true,
	"owner_required":                     true,
	"browser_required":                   true,
}

// Error is a non-2xx response from the check API.
type Error struct {
	StatusCode int
	Body       ErrorBody
}

// Error returns the service's message when the response carried the contract.
func (e *Error) Error() string {
	switch {
	case e.Body.Message != "":
		return e.Body.Message
	case e.Body.Code != "":
		return fmt.Sprintf("the check service rejected the request (%s)", e.Body.Code)
	case e.StatusCode > 0:
		return fmt.Sprintf("the check service returned HTTP %d", e.StatusCode)
	default:
		return "the check service rejected the request"
	}
}

// RemedyText is the message, remedy, and install or top-up link an agent
// prints verbatim, exactly the fields the service enumerated for this failure.
func (e *Error) RemedyText() string {
	lines := []string{e.Error()}
	if remedy := strings.TrimSpace(e.Body.Remedy); remedy != "" {
		lines = append(lines, remedy)
	}
	if install := strings.TrimSpace(e.Body.InstallURL); install != "" {
		lines = append(lines, install)
	}
	if topUp := strings.TrimSpace(e.Body.TopUpURL); topUp != "" {
		lines = append(lines, topUp)
	}
	return strings.Join(lines, "\n")
}

// NeedsPerson reports whether only a person can clear this refusal.
func (e *Error) NeedsPerson() bool {
	return personCodes[e.Body.Code]
}
