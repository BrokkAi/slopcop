package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Device-flow paths from the login contract.
const (
	deviceStartPath = "/api/device"
	deviceTokenPath = "/api/token"

	defaultDeviceInterval = 5 * time.Second
	defaultDeviceExpiry   = 15 * time.Minute
	slowDownStep          = 5 * time.Second
)

// DeviceStart is the browser-approval challenge the service hands back.
type DeviceStart struct {
	DeviceCode      string `json:"deviceCode"`
	UserCode        string `json:"userCode"`
	VerificationURI string `json:"verificationUri"`
	ExpiresIn       int    `json:"expiresIn"`
	Interval        int    `json:"interval"`
}

// StartDevice begins the device flow.
func (c *Client) StartDevice(ctx context.Context) (DeviceStart, error) {
	var start DeviceStart
	status, _, payload, err := c.postJSON(ctx, deviceStartPath, map[string]any{})
	if err != nil {
		return start, err
	}
	if status < 200 || status >= 300 {
		return start, deviceFailure(status, payload, DeviceStart{})
	}
	if err := json.Unmarshal(payload, &start); err != nil {
		return start, fmt.Errorf("decode device response: %w", err)
	}
	if start.DeviceCode == "" || start.UserCode == "" || start.VerificationURI == "" {
		return start, errors.New("device response is missing deviceCode, userCode, or verificationUri")
	}
	return start, nil
}

// AwaitDeviceToken polls until the browser approves the code. The notify
// callback, when set, receives one line whenever the service asks the client
// to slow down or a poll fails in a way that leaves the code valid.
func (c *Client) AwaitDeviceToken(
	ctx context.Context,
	start DeviceStart,
	notify func(string),
) (string, error) {
	interval := time.Duration(start.Interval) * time.Second
	if interval <= 0 {
		interval = defaultDeviceInterval
	}
	expires := time.Duration(start.ExpiresIn) * time.Second
	if expires <= 0 {
		expires = defaultDeviceExpiry
	}
	deadline := time.Now().Add(expires)

	for {
		token, wait, err := c.pollDeviceToken(ctx, start, &interval, notify)
		if err != nil {
			return "", err
		}
		if token != "" {
			return token, nil
		}
		if !time.Now().Before(deadline) {
			if wait.cause != nil {
				return "", fmt.Errorf(
					"this login code expired while the login service kept failing (%v); run `slopcop login` again",
					wait.cause,
				)
			}
			return "", errors.New("this login code expired; run `slopcop login` again")
		}
		if err := c.sleep(ctx, max(interval, wait.pause)); err != nil {
			return "", err
		}
	}
}

// pollWait is a poll that got no token yet: how long to wait beyond the
// interval before asking again, and the transient failure it met, if any.
type pollWait struct {
	pause time.Duration
	cause error
}

// pollDeviceToken asks once for the token. With no token and no error, the
// login is still waiting.
func (c *Client) pollDeviceToken(
	ctx context.Context,
	start DeviceStart,
	interval *time.Duration,
	notify func(string),
) (string, pollWait, error) {
	status, header, payload, err := c.postJSON(ctx, deviceTokenPath, map[string]any{
		"deviceCode": start.DeviceCode,
	})
	if err != nil {
		if ctx.Err() != nil {
			return "", pollWait{}, err
		}
		// A dropped connection leaves the code valid, as a brief outage does.
		notifyRetry(notify, "could not reach the login service", *interval)
		return "", pollWait{cause: err}, nil
	}
	if status == 202 {
		return "", pollWait{}, nil
	}
	if status >= 200 && status < 300 {
		var body struct {
			Status string `json:"status"`
			Token  string `json:"token"`
			Code   string `json:"code"`
		}
		if err := json.Unmarshal(payload, &body); err != nil {
			return "", pollWait{}, fmt.Errorf("decode token response: %w", err)
		}
		if body.Token != "" {
			return body.Token, pollWait{}, nil
		}
		if body.Status == "authorized" {
			return "", pollWait{}, errors.New("the login service authorized the code without returning a token")
		}
		if body.Status == "pending" || body.Code == "" {
			return "", pollWait{}, nil
		}
	}

	failure := deviceFailure(status, payload, start)
	switch failure.Body.Code {
	case "authorization_pending":
		return "", pollWait{}, nil
	case "slow_down":
		*interval += slowDownStep
		if notify != nil {
			notify(fmt.Sprintf(
				"slopcop: the login service asked to slow down; waiting %ds",
				int(interval.Seconds()),
			))
		}
		return "", pollWait{}, nil
	}
	// A failure the service marks retryable, such as a brief outage or too
	// many polls, leaves the code valid, and so does a proxy's busy or
	// unreachable-upstream answer, which carries no code: wait as long as it
	// asks, then poll again until the code expires.
	if failure.Body.Retryable || (failure.Body.Code == "" && transientStatus(status)) {
		pause := retryAfter(header)
		notifyRetry(notify, strings.TrimSuffix(failure.Body.Message, "."), max(*interval, pause))
		return "", pollWait{pause: pause, cause: failure}, nil
	}
	return "", pollWait{}, failure
}

// transientStatus is an HTTP status a proxy answers while the service behind
// it is busy or unreachable.
func transientStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// notifyRetry says, when notify is set, why the login polls again and when.
func notifyRetry(notify func(string), why string, wait time.Duration) {
	if notify != nil {
		notify(fmt.Sprintf("slopcop: %s; polling again in %ds", why, int(wait.Seconds())))
	}
}

// deviceFailure maps the device-flow closed codes to the CLI error surface.
// The service may send the full error body or just a code, so both are read.
func deviceFailure(status int, payload []byte, start DeviceStart) *Error {
	var body struct {
		Code      string `json:"code"`
		Status    string `json:"status"`
		Message   string `json:"message"`
		Remedy    string `json:"remedy"`
		Retryable bool   `json:"retryable"`
	}
	_ = json.Unmarshal(payload, &body)
	code := body.Code
	if code == "" {
		code = body.Status
	}
	failure := &Error{StatusCode: status}
	failure.Body.Code = code
	failure.Body.Message = strings.TrimSpace(body.Message)
	failure.Body.Remedy = strings.TrimSpace(body.Remedy)
	failure.Body.Retryable = body.Retryable
	switch code {
	case "authorization_pending":
		failure.Body.Message = "This login has not been approved yet."
		failure.Body.Remedy = deviceRemedy(start)
	case "slow_down":
		failure.Body.Message = "This login is being polled too quickly."
		failure.Body.Remedy = deviceRemedy(start)
	case "expired_token":
		failure.Body.Message = "This login code expired."
		failure.Body.Remedy = "Run `slopcop login` again."
	case "access_denied":
		failure.Body.Message = "This login request was denied."
		failure.Body.Remedy = "Run `slopcop login` again and approve the code."
	default:
		if failure.Body.Message == "" {
			failure.Body.Message = fmt.Sprintf("the login service returned HTTP %d", status)
		}
		if failure.Body.Remedy == "" {
			failure.Body.Remedy = "Check the service URL, then retry."
		}
	}
	return failure
}

func deviceRemedy(start DeviceStart) string {
	if start.VerificationURI == "" {
		return "Approve the login in the browser, then retry."
	}
	return fmt.Sprintf("Open %s and enter code %s.", start.VerificationURI, start.UserCode)
}

// sleep waits between polls. Tests replace it through Client.wait.
func (c *Client) sleep(ctx context.Context, delay time.Duration) error {
	if c.wait != nil {
		return c.wait(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
