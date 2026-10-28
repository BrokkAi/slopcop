package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStartDevice(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"deviceCode": "device-1",
			"userCode": "ABCD-EFGH",
			"verificationUri": "https://slopcop.com/device",
			"expiresIn": 600,
			"interval": 2
		}`))
	}))
	defer server.Close()

	client, err := New(server.URL, "", "slopcop/test", 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	start, err := client.StartDevice(context.Background())
	if err != nil {
		t.Fatalf("StartDevice: %v", err)
	}
	if path != "/api/device" {
		t.Errorf("path = %s", path)
	}
	if start.DeviceCode != "device-1" || start.UserCode != "ABCD-EFGH" {
		t.Fatalf("start = %+v", start)
	}
	if start.VerificationURI != "https://slopcop.com/device" || start.Interval != 2 {
		t.Fatalf("start = %+v", start)
	}
}

func TestAwaitDeviceTokenPendingThenAuthorized(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/token" {
			t.Errorf("path = %s", r.URL.Path)
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"status": "pending"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status": "authorized", "token": "issued-token"}`))
	}))
	defer server.Close()

	client, err := New(server.URL, "", "", 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var delays []time.Duration
	client.wait = func(context.Context, time.Duration) error {
		delays = append(delays, 0)
		return nil
	}
	token, err := client.AwaitDeviceToken(context.Background(), DeviceStart{
		DeviceCode:      "device-1",
		UserCode:        "ABCD-EFGH",
		VerificationURI: "https://slopcop.com/device",
		ExpiresIn:       600,
		Interval:        1,
	}, nil)
	if err != nil {
		t.Fatalf("AwaitDeviceToken: %v", err)
	}
	if token != "issued-token" {
		t.Fatalf("token = %q", token)
	}
	if calls != 2 || len(delays) != 1 {
		t.Fatalf("calls = %d, waits = %d", calls, len(delays))
	}
}

func TestAwaitDeviceTokenSlowDownRaisesInterval(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code": "slow_down"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status": "authorized", "token": "issued-token"}`))
	}))
	defer server.Close()

	client, err := New(server.URL, "", "", 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var delays []time.Duration
	client.wait = func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	}
	if _, err := client.AwaitDeviceToken(context.Background(), DeviceStart{
		DeviceCode: "device-1",
		Interval:   1,
		ExpiresIn:  600,
	}, nil); err != nil {
		t.Fatalf("AwaitDeviceToken: %v", err)
	}
	if len(delays) != 1 || delays[0] != 6*time.Second {
		t.Fatalf("delays = %v, want one 6s wait", delays)
	}
}

// A retryable failure, such as a brief outage or too many polls, leaves the
// login code valid: polling waits as long as the service asks, never less
// than the interval, and goes on until the code is approved.
func TestAwaitDeviceTokenWaitsOutRetryableFailures(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		switch calls {
		case 1:
			w.Header().Set("Retry-After", "9")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"code": "service_unavailable", "message": "SlopCop is unavailable right now.", "remedy": "Retry shortly.", "retryable": true}`))
		case 2:
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code": "rate_limited", "message": "Too many requests.", "remedy": "Wait, then retry.", "retryable": true}`))
		default:
			_, _ = w.Write([]byte(`{"status": "authorized", "token": "issued-token"}`))
		}
	}))
	defer server.Close()

	client, err := New(server.URL, "", "", 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var delays []time.Duration
	client.wait = func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	}
	var notices []string
	token, err := client.AwaitDeviceToken(context.Background(), DeviceStart{
		DeviceCode: "device-1",
		Interval:   5,
		ExpiresIn:  600,
	}, func(line string) { notices = append(notices, line) })
	if err != nil || token != "issued-token" {
		t.Fatalf("token = %q, err = %v", token, err)
	}
	if len(delays) != 2 || delays[0] != 9*time.Second || delays[1] != 5*time.Second {
		t.Fatalf("delays = %v, want 9s then the 5s interval", delays)
	}
	if len(notices) != 2 || notices[0] != "slopcop: SlopCop is unavailable right now; polling again in 9s" {
		t.Fatalf("notices = %q", notices)
	}
}

// A failure the service does not mark retryable still ends the login.
func TestAwaitDeviceTokenStopsOnANonRetryableFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code": "invalid_request", "message": "Bad request.", "remedy": "Fix it.", "retryable": false}`))
	}))
	defer server.Close()

	client, err := New(server.URL, "", "", 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client.wait = func(context.Context, time.Duration) error {
		t.Fatal("waited after a failure that is not retryable")
		return nil
	}
	var serviceError *Error
	_, err = client.AwaitDeviceToken(context.Background(), DeviceStart{DeviceCode: "device-1", Interval: 5, ExpiresIn: 600}, nil)
	if !errors.As(err, &serviceError) || serviceError.Body.Code != "invalid_request" {
		t.Fatalf("err = %v, want invalid_request", err)
	}
}

func TestAwaitDeviceTokenClosedCodes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code": "access_denied"}`))
	}))
	defer server.Close()

	client, err := New(server.URL, "", "", 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.AwaitDeviceToken(context.Background(), DeviceStart{
		DeviceCode: "device-1",
		UserCode:   "ABCD-EFGH",
		ExpiresIn:  600,
		Interval:   1,
	}, nil)
	var serviceError *Error
	if !errors.As(err, &serviceError) {
		t.Fatalf("expected *Error, got %v", err)
	}
	if serviceError.Body.Code != "access_denied" {
		t.Fatalf("code = %q", serviceError.Body.Code)
	}
	text := serviceError.RemedyText()
	if text == "" {
		t.Fatal("expected a printable remedy")
	}
}

func TestAwaitDeviceTokenRejectsAuthorizationWithoutToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status": "authorized"}`))
	}))
	defer server.Close()

	client, err := New(server.URL, "", "", 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.AwaitDeviceToken(context.Background(), DeviceStart{
		DeviceCode: "device-1",
		ExpiresIn:  600,
		Interval:   1,
	}, nil)
	if err == nil {
		t.Fatal("expected an error for an authorized response without a token")
	}
}
