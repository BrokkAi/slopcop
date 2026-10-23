package service

import (
	"fmt"
	"strings"
)

// The closed failure codes the CLI acts on or its skill and README name,
// mirrored from server/src/check.rs. Whatever the code, the CLI prints the
// service's message and remedy verbatim.
const (
	CodeAuthRequired       = "auth_required"
	CodeAuthExpired        = "auth_expired"
	CodeDeviceFlowDisabled = "device_flow_disabled"
	// CodeRepoNotIndexed keeps its name for contract stability. It now means
	// this login cannot check the repository: a private one needs the
	// SlopCop GitHub App and a `slopcop login` that can use it.
	CodeRepoNotIndexed = "repo_not_indexed"
	CodeNoAccess       = "no_access"
	// CodeIndexingInProgress comes only from a service from before checks
	// stopped waiting on indexing. It is printed like any other failure.
	CodeIndexingInProgress = "indexing_in_progress"
	CodeBaseNotFound       = "base_not_found"
	CodePayloadTooLarge    = "payload_too_large"
	CodeRateLimited        = "rate_limited"
	CodeServiceUnavailable = "service_unavailable"
	// CodeCheckRunning is the retryable answer to a check still running.
	CodeCheckRunning = "check_running"
	// CodeFindingNotFound answers a report of a finding the service does not
	// know, or one this login may not see.
	CodeFindingNotFound = "finding_not_found"
	// CodeInvalidReport answers a malformed report: an unknown kind, or an
	// empty or oversized reason.
	CodeInvalidReport = "invalid_report"
	// CodeInsufficientCredits refuses a check once the workspace's credits
	// have run out and its grace period has ended. The free repository is
	// still checked; TopUpURL links to the page that adds credits.
	CodeInsufficientCredits = "insufficient_credits"
)

// ErrorBody is the closed failure contract returned by the service.
type ErrorBody struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Remedy     string `json:"remedy"`
	Retryable  bool   `json:"retryable"`
	InstallURL string `json:"installUrl,omitempty"`
	TopUpURL   string `json:"topUpUrl,omitempty"`
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
		return fmt.Sprintf("the SlopCop service rejected the request (%s)", e.Body.Code)
	case e.StatusCode > 0:
		return fmt.Sprintf("the SlopCop service returned HTTP %d", e.StatusCode)
	default:
		return "the SlopCop service rejected the request"
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
		lines = append(lines, "Add credits: "+topUp)
	}
	return strings.Join(lines, "\n")
}
