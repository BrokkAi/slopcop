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
	maxResponseBytes = 32 * 1024 * 1024
	defaultTimeout   = 10 * time.Minute
	// checkRunning is the retryable answer to a check that is still running.
	checkRunning = "check_running"
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
		case !errors.As(err, &serviceError) || serviceError.Body.Code != checkRunning:
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
	// so every finding is one the check fails on.
	var marked struct {
		Summary struct {
			NewFindings *int `json:"newFindings"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(payload, &marked); err == nil && marked.Summary.NewFindings == nil {
		for index := range decoded.Findings {
			decoded.Findings[index].New = true
		}
		decoded.Summary.NewFindings = len(decoded.Findings)
		decoded.Summary.ExistingFindings = 0
	}
	return decoded, 0, nil
}

// retryAfter reads a Retry-After header in seconds, bounded, defaulting to
// five seconds.
func retryAfter(header http.Header) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(header.Get("Retry-After")))
	if err != nil || seconds < 0 {
		return defaultRetryAfter
	}
	return min(time.Duration(seconds)*time.Second, maxRetryAfter)
}

// postJSON sends one JSON request and returns the status, headers and raw
// body.
func (c *Client) postJSON(ctx context.Context, path string, body any) (int, http.Header, []byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("encode request: %w", err)
	}
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
	if err := json.Unmarshal(payload, &body); err != nil || body.Message == "" {
		excerpt := strings.TrimSpace(string(payload))
		if len(excerpt) > 200 {
			excerpt = excerpt[:200] + "..."
		}
		body = ErrorBody{
			Message: fmt.Sprintf("the check service returned HTTP %d", status),
			Remedy:  "Check the service URL, then retry.",
		}
		if excerpt != "" {
			body.Remedy = fmt.Sprintf("Check the service URL, then retry. Response: %s", excerpt)
		}
	}
	return &Error{StatusCode: status, Body: body}
}
