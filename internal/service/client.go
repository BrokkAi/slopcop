package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	checkPath        = "/v1/check"
	maxResponseBytes = 32 * 1024 * 1024
	defaultTimeout   = 10 * time.Minute
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

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Check posts one change and returns the report. Service failures come back as
// *Error so callers can print the closed code, message, and remedy.
func (c *Client) Check(ctx context.Context, request Request) (Response, error) {
	status, payload, err := c.postJSON(ctx, checkPath, request)
	if err != nil {
		return Response{}, err
	}
	if status < 200 || status >= 300 {
		return Response{}, decodeError(status, payload)
	}

	var decoded Response
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return Response{}, fmt.Errorf("decode check response: %w", err)
	}
	if decoded.Status == "" {
		return Response{}, fmt.Errorf("check service returned no status")
	}
	if decoded.Findings == nil {
		decoded.Findings = []Finding{}
	}
	if decoded.Incomplete == nil {
		decoded.Incomplete = []IncompletePolicy{}
	}
	if decoded.Summary.IncompletePolicyIDs == nil {
		decoded.Summary.IncompletePolicyIDs = []string{}
	}
	return decoded, nil
}

// postJSON sends one JSON request and returns the status and raw body.
func (c *Client) postJSON(ctx context.Context, path string, body any) (int, []byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return 0, nil, fmt.Errorf("encode request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(payload),
	)
	if err != nil {
		return 0, nil, fmt.Errorf("build request: %w", err)
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
		return 0, nil, fmt.Errorf("reach %s: %w", c.BaseURL, err)
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return 0, nil, fmt.Errorf("read response: %w", err)
	}
	if len(responseBody) > maxResponseBytes {
		return 0, nil, fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
	}
	return response.StatusCode, responseBody, nil
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
