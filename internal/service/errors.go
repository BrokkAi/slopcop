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

// RemedyText is the message, remedy, and install link an agent prints
// verbatim, exactly the fields the service enumerated for this failure.
func (e *Error) RemedyText() string {
	lines := []string{e.Error()}
	if remedy := strings.TrimSpace(e.Body.Remedy); remedy != "" {
		lines = append(lines, remedy)
	}
	if install := strings.TrimSpace(e.Body.InstallURL); install != "" {
		lines = append(lines, install)
	}
	return strings.Join(lines, "\n")
}
