package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	checkPath        = "/v1/check"
	reportPath       = "/v1/report"
	maxResponseBytes = 32 * 1024 * 1024
	defaultTimeout   = 10 * time.Minute
	// defaultRetryAfter is the wait when check_running names none, and
	// maxRetryAfter bounds a wait the service names.
	defaultRetryAfter = 5 * time.Second
	maxRetryAfter     = time.Minute
)

// Client posts checks to one service URL.
type Client struct {
	BaseURL   string
	Token     string
	UserAgent string
	HTTP      *http.Client
	// wait sleeps between device-flow polls; nil uses a real timer.
	wait func(context.Context, time.Duration) error
}

// New validates the service URL and builds a client. The token may be empty;
// the service then answers the contract's auth_required, which is the one
// error surface the caller prints.
func New(baseURL, token, userAgent string, timeout time.Duration) (*Client, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		return nil, fmt.Errorf("no check service URL configured")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("invalid service URL %q: %w", baseURL, err)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("service URL %q must name a host", baseURL)
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopback(parsed.Hostname())) {
		return nil, fmt.Errorf("service URL %q must be an absolute https URL (http is allowed only for localhost)", baseURL)
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Client{
		BaseURL:   trimmed,
		Token:     strings.TrimSpace(token),
		UserAgent: userAgent,
		HTTP:      &http.Client{Timeout: timeout},
	}, nil
}

// Timeout is the longest a check waits for its result.
func (c *Client) Timeout() time.Duration {
	if c.HTTP != nil && c.HTTP.Timeout > 0 {
		return c.HTTP.Timeout
	}
	return defaultTimeout
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Check posts one change and returns the report, asking again while the
// service answers check_running until ctx ends. An identical request attaches
// to the running check, so asking again never starts a second one. When ctx
// ends first, Check returns a *Pending. Other service failures come back as
// *Error so callers can print the closed code, message, and remedy.
func (c *Client) Check(ctx context.Context, request Request) (Response, error) {
	pending := &Pending{
		RetryAfter: int(defaultRetryAfter / time.Second),
		Message:    pendingMessage,
		Remedy:     pendingRemedy,
	}
	for {
		response, retryAfter, err := c.checkOnce(ctx, request)
		var serviceError *Error
		switch {
		case err == nil:
			return response, nil
		case ctx.Err() != nil:
			// The wait ran out mid-request; the service keeps the check.
			return Response{}, pending
		case !errors.As(err, &serviceError) || serviceError.Body.Code != CodeCheckRunning:
			return Response{}, err
		}
		pending.RetryAfter = int((retryAfter + time.Second - 1) / time.Second)
		if serviceError.Body.Message != "" {
			pending.Message = serviceError.Body.Message
			pending.Remedy = serviceError.Body.Remedy
		}
		// Asking again after the deadline would only be cut short.
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= retryAfter {
			return Response{}, pending
		}
		if err := c.sleep(ctx, retryAfter); err != nil {
			return Response{}, pending
		}
	}
}

// checkOnce posts the change once. It returns the service's Retry-After wait
// alongside a failure.
func (c *Client) checkOnce(ctx context.Context, request Request) (Response, time.Duration, error) {
	status, header, payload, err := c.postJSON(ctx, checkPath, request)
	if err != nil {
		return Response{}, 0, err
	}
	if status < 200 || status >= 300 {
		return Response{}, retryAfter(header), decodeError(status, payload)
	}

	var decoded Response
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return Response{}, 0, fmt.Errorf("decode check response: %w", err)
	}
	if decoded.Status == "" {
		return Response{}, 0, fmt.Errorf("check service returned no status")
	}
	if decoded.Findings == nil {
		decoded.Findings = []Finding{}
	}
	if decoded.Incomplete == nil {
		decoded.Incomplete = []IncompletePolicy{}
	}
	if decoded.InconclusiveElsewhere == nil {
		decoded.InconclusiveElsewhere = []IncompletePolicy{}
	}
	if decoded.Summary.IncompletePolicyIDs == nil {
		decoded.Summary.IncompletePolicyIDs = []string{}
	}
	// A service from before new findings were marked gates on every finding,
	// so every finding is one the check fails on. Such a service dismisses
	// none, but a dismissed finding is never made new.
	var marked struct {
		Summary struct {
			NewFindings *int `json:"newFindings"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(payload, &marked); err == nil && marked.Summary.NewFindings == nil {
		fresh := 0
		for index := range decoded.Findings {
			if !decoded.Findings[index].Dismissed {
				decoded.Findings[index].New = true
				fresh++
			}
		}
		decoded.Summary.NewFindings = fresh
		decoded.Summary.ExistingFindings = 0
	}
	return decoded, 0, nil
}

// Report posts one false-positive report. The service answers 200 for a
// report it recorded and for a finding already reported; any other answer
// comes back as *Error, so callers can print the closed code, message, and
// remedy.
func (c *Client) Report(ctx context.Context, request ReportRequest) (ReportResponse, error) {
	body, err := encodeJSON(request)
	if err != nil {
		return ReportResponse{}, fmt.Errorf("encode the report: %w", err)
	}
	if len(body) > MaxReportBodyBytes {
		return ReportResponse{}, fmt.Errorf("the report is %d bytes once encoded, over the service's %d; shorten the reason", len(body), MaxReportBodyBytes)
	}
	status, header, payload, err := c.postPayload(ctx, reportPath, body)
	if err != nil {
		return ReportResponse{}, err
	}
	if status < 200 || status >= 300 {
		failure := decodeError(status, payload)
		if wait, ok := reportRetryAfter(status, header); ok {
			// Reports are limited per hour, so the wait can be long: say it
			// after whatever the service or a proxy said.
			failure.Body.Remedy = strings.TrimSpace(failure.Body.Remedy + " The service asks to wait " + waitText(wait) + ".")
		}
		if status == http.StatusNotFound && failure.Body.Code == "" {
			// A 404 without the closed contract is a service from before
			// reports, not an unknown finding.
			failure.Body = ErrorBody{
				Message: "This service does not accept false-positive reports (HTTP 404).",
				Remedy:  "Check the service URL. If it is right, fix the code or ask a human instead: the finding still fails the check.",
			}
		}
		return ReportResponse{}, failure
	}
	var decoded ReportResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return ReportResponse{}, fmt.Errorf("decode report response: %w", err)
	}
	switch decoded.Status {
	case ReportRecorded, ReportAlreadyReported:
		return decoded, nil
	case "":
		return ReportResponse{}, fmt.Errorf("the report service returned no status")
	default:
		return ReportResponse{}, fmt.Errorf("the report service returned an unknown status %q", decoded.Status)
	}
}

// reportRetryAfter is a rate-limited report's Retry-After, bounded to a day.
func reportRetryAfter(status int, header http.Header) (time.Duration, bool) {
	seconds, ok := retryAfterSeconds(header)
	if status != http.StatusTooManyRequests || !ok || seconds == 0 {
		return 0, false
	}
	return time.Duration(min(seconds, 24*60*60)) * time.Second, true
}

// retryAfterSeconds parses a Retry-After header given in seconds.
func retryAfterSeconds(header http.Header) (int, bool) {
	seconds, err := strconv.Atoi(strings.TrimSpace(header.Get("Retry-After")))
	return seconds, err == nil && seconds >= 0
}

// waitText says a wait the way a person reads it: "45 seconds", "57 minutes",
// "3 hours".
func waitText(wait time.Duration) string {
	unit := func(n int, one string) string {
		if n == 1 {
			return "1 " + one
		}
		return strconv.Itoa(n) + " " + one + "s"
	}
	switch {
	case wait < 90*time.Second:
		return unit(int(wait.Round(time.Second)/time.Second), "second")
	case wait < 90*time.Minute:
		return unit(int(wait.Round(time.Minute)/time.Minute), "minute")
	default:
		return unit(int(wait.Round(time.Hour)/time.Hour), "hour")
	}
}

// encodeJSON encodes a request body as the service reads it, without HTML
// escaping: "<" in a reason is one byte, not six, so a reason within bounds
// stays within the service's body limit.
func encodeJSON(body any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(body); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

// retryAfter reads a Retry-After header in seconds, bounded, defaulting to
// five seconds. The seconds are bounded before they become a duration, so a
// huge wait cannot overflow into a negative one.
func retryAfter(header http.Header) time.Duration {
	seconds, ok := retryAfterSeconds(header)
	if !ok {
		return defaultRetryAfter
	}
	return time.Duration(min(seconds, int(maxRetryAfter/time.Second))) * time.Second
}

// postJSON sends one JSON request and returns the status, headers and raw
// body.
func (c *Client) postJSON(ctx context.Context, path string, body any) (int, http.Header, []byte, error) {
	payload, err := encodeJSON(body)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("encode request: %w", err)
	}
	return c.postPayload(ctx, path, payload)
}

// postPayload posts an encoded JSON body.
func (c *Client) postPayload(ctx context.Context, path string, payload []byte) (int, http.Header, []byte, error) {
	httpRequest, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(payload),
	)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("build request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		httpRequest.Header.Set("User-Agent", c.UserAgent)
	}
	if c.Token != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+c.Token)
	}

	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("reach %s: %w", c.BaseURL, err)
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("read response: %w", err)
	}
	if len(responseBody) > maxResponseBytes {
		return 0, nil, nil, fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
	}
	return response.StatusCode, response.Header, responseBody, nil
}

func decodeError(status int, payload []byte) *Error {
	var body ErrorBody
	excerpt := strings.TrimSpace(string(payload))
	if len(excerpt) > 200 {
		excerpt = excerpt[:200] + "..."
	}
	fallback := "Check the service URL, then retry."
	if excerpt != "" {
		fallback = fmt.Sprintf("Check the service URL, then retry. Response: %s", excerpt)
	}
	if err := json.Unmarshal(payload, &body); err != nil || (body.Message == "" && body.Code == "") {
		body = ErrorBody{Message: fmt.Sprintf("the SlopCop service returned HTTP %d", status), Remedy: fallback}
	} else if body.Remedy == "" && body.Message == "" {
		// A code alone (from a proxy or an older service) still gets a way on.
		body.Remedy = fallback
	}
	return &Error{StatusCode: status, Body: body}
}
