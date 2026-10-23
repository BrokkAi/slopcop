package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const reportedFinding = "6f1c2a9e-3b4d-4e5f-8a7b-0c1d2e3f4a5b"

func TestReportSendsTheContractRequest(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/report" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"reportId": "11111111-2222-4333-8444-555555555555",
			"findingId": "` + reportedFinding + `",
			"checkId": "2b3c4d5e-6f70-4812-9a3b-4c5d6e7f8091",
			"policyId": "bifrost.security.xss",
			"kind": "false_positive",
			"status": "recorded"
		}`))
	}))
	defer server.Close()

	client, err := New(server.URL, "secret", "slopcop/test", 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	response, err := client.Report(context.Background(), ReportRequest{
		FindingID: reportedFinding,
		Kind:      ReportFalsePositive,
		Reason:    "escapeHtml already encodes the value on line 40",
	})
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	want := ReportResponse{
		ReportID:  "11111111-2222-4333-8444-555555555555",
		FindingID: reportedFinding,
		CheckID:   "2b3c4d5e-6f70-4812-9a3b-4c5d6e7f8091",
		PolicyID:  "bifrost.security.xss",
		Kind:      ReportFalsePositive,
		Status:    ReportRecorded,
	}
	if response != want {
		t.Errorf("response = %+v, want %+v", response, want)
	}
	wantRequest := map[string]any{
		"findingId": reportedFinding,
		"kind":      "false_positive",
		"reason":    "escapeHtml already encodes the value on line 40",
	}
	if len(captured) != len(wantRequest) {
		t.Errorf("request = %v, want exactly %v", captured, wantRequest)
	}
	for key, value := range wantRequest {
		if captured[key] != value {
			t.Errorf("request[%q] = %v, want %v", key, captured[key], value)
		}
	}
}

func TestReportAnswers(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		// wantStatus is the report status on success; wantCode and
		// wantMessage describe the failure otherwise.
		wantStatus  string
		wantCode    string
		wantMessage string
		wantRemedy  string
		// wantPlain is a failure that is not the service's contract.
		wantPlain bool
	}{
		{name: "recorded", status: 200, body: `{"status": "recorded", "policyId": "p"}`, wantStatus: ReportRecorded},
		{name: "already reported", status: 200, body: `{"status": "already_reported", "policyId": "p"}`, wantStatus: ReportAlreadyReported},
		{name: "unknown status", status: 200, body: `{"status": "queued"}`, wantPlain: true},
		{name: "no status", status: 200, body: `{}`, wantPlain: true},
		{name: "not json", status: 200, body: `recorded`, wantPlain: true},
		{
			name: "auth required", status: 401, wantCode: CodeAuthRequired,
			body:        `{"code": "auth_required", "message": "This check needs a SlopCop login.", "remedy": "Sign in, then retry.", "retryable": false}`,
			wantMessage: "This check needs a SlopCop login.", wantRemedy: "Sign in, then retry.",
		},
		{
			name: "auth expired", status: 401, wantCode: CodeAuthExpired,
			body:        `{"code": "auth_expired", "message": "This SlopCop login has expired.", "remedy": "Sign in again.", "retryable": false}`,
			wantMessage: "This SlopCop login has expired.", wantRemedy: "Sign in again.",
		},
		{
			name: "finding not found", status: 404, wantCode: CodeFindingNotFound,
			body:        `{"code": "finding_not_found", "message": "SlopCop has no finding with this id that you can see.", "remedy": "Report a finding id from a check you ran.", "retryable": false}`,
			wantMessage: "SlopCop has no finding with this id that you can see.", wantRemedy: "Report a finding id from a check you ran.",
		},
		{
			name: "invalid report", status: 422, wantCode: CodeInvalidReport,
			body:        `{"code": "invalid_report", "message": "The report is malformed.", "remedy": "Give a reason of 1 to 2000 characters.", "retryable": false}`,
			wantMessage: "The report is malformed.", wantRemedy: "Give a reason of 1 to 2000 characters.",
		},
		{
			name: "rate limited", status: 429, wantCode: CodeRateLimited,
			body:        `{"code": "rate_limited", "message": "This report was rate limited.", "remedy": "Wait and retry.", "retryable": true}`,
			wantMessage: "This report was rate limited.", wantRemedy: "Wait and retry.",
		},
		{
			name: "service unavailable", status: 503, wantCode: CodeServiceUnavailable,
			body:        `{"code": "service_unavailable", "message": "The check service could not complete this request right now.", "remedy": "Wait a moment and retry.", "retryable": true}`,
			wantMessage: "The check service could not complete this request right now.", wantRemedy: "Wait a moment and retry.",
		},
		{
			name: "a service from before reports", status: 404, body: `<!DOCTYPE html><title>404</title>`,
			wantMessage: "This service does not accept false-positive reports (HTTP 404).",
			wantRemedy:  "Check the service URL. If it is right, fix the code or ask a human instead: the finding still fails the check.",
		},
		{
			name: "an unexpected failure", status: 502, body: `bad gateway`,
			wantMessage: "the SlopCop service returned HTTP 502",
			wantRemedy:  "Check the service URL, then retry. Response: bad gateway",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			client, err := New(server.URL, "secret", "", 0)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			response, err := client.Report(context.Background(), ReportRequest{
				FindingID: reportedFinding, Kind: ReportFalsePositive, Reason: "unreachable",
			})
			if tc.wantStatus != "" {
				if err != nil || response.Status != tc.wantStatus || response.PolicyID != "p" {
					t.Fatalf("Report = %+v, %v; want status %q", response, err, tc.wantStatus)
				}
				return
			}
			if err == nil {
				t.Fatalf("Report = %+v, want a failure", response)
			}
			var serviceError *Error
			if tc.wantPlain {
				if errors.As(err, &serviceError) {
					t.Fatalf("a 200 answer is not a service failure: %+v", serviceError)
				}
				return
			}
			if !errors.As(err, &serviceError) {
				t.Fatalf("expected *Error, got %v", err)
			}
			if serviceError.StatusCode != tc.status || serviceError.Body.Code != tc.wantCode ||
				serviceError.Body.Message != tc.wantMessage || serviceError.Body.Remedy != tc.wantRemedy {
				t.Errorf("error = %+v", serviceError)
			}
		})
	}
}

// A rate-limited report says how long to wait, and a coded failure without a
// message keeps its code rather than reading as a service from before reports.
func TestReportFailuresKeepWhatTheServiceSaid(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		retryAfter string
		body       string
		wantCode   string
		wantRemedy string
	}{
		{name: "rate limited", status: http.StatusTooManyRequests, retryAfter: "3400",
			body:     `{"code":"rate_limited","message":"Too many reports.","remedy":"Wait and retry.","retryable":true}`,
			wantCode: "rate_limited", wantRemedy: "Wait and retry. The service asks to wait 57 minutes."},
		{name: "an absurd wait is bounded", status: http.StatusTooManyRequests, retryAfter: "99999999999",
			body:     `{"code":"rate_limited","message":"Too many reports.","remedy":"Wait and retry.","retryable":true}`,
			wantCode: "rate_limited", wantRemedy: "Wait and retry. The service asks to wait 24 hours."},
		{name: "a proxy's 429 keeps its excerpt", status: http.StatusTooManyRequests, retryAfter: "60",
			body:     `<html>slow down</html>`,
			wantCode: "", wantRemedy: "Check the service URL, then retry. Response: <html>slow down</html> The service asks to wait 60 seconds."},
		{name: "coded 404 without a message", status: http.StatusNotFound,
			body:     `{"code":"finding_not_found","remedy":"Copy the id from the latest check."}`,
			wantCode: "finding_not_found", wantRemedy: "Copy the id from the latest check."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client, err := New(server.URL, "secret", "", 0)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Report(context.Background(), ReportRequest{FindingID: reportedFinding, Kind: ReportFalsePositive, Reason: "x"})
			var serviceError *Error
			if !errors.As(err, &serviceError) {
				t.Fatalf("error = %v", err)
			}
			if serviceError.Body.Code != tc.wantCode || serviceError.Body.Remedy != tc.wantRemedy {
				t.Errorf("body = %+v", serviceError.Body)
			}
		})
	}
}

// "<", ">" and "&" travel as themselves, so a reason within 2000 characters
// stays within the service's body limit.
func TestEncodeJSONDoesNotEscapeHTML(t *testing.T) {
	body, err := encodeJSON(ReportRequest{Reason: "a < b && c > d"})
	if err != nil {
		t.Fatal(err)
	}
	if want := `"reason":"a < b && c > d"`; !json.Valid(body) || !strings.Contains(string(body), want) {
		t.Errorf("body = %s, want it to contain %s", body, want)
	}
}

// A body over the service's limit is refused before it is sent.
func TestReportRefusesABodyOverTheLimit(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	client, err := New(server.URL, "secret", "", 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Report(context.Background(), ReportRequest{
		FindingID: reportedFinding,
		Kind:      ReportFalsePositive,
		Reason:    strings.Repeat("\x01", MaxReportBodyBytes/6+1),
	})
	if err == nil || !strings.Contains(err.Error(), "over the service's 65536") || requests != 0 {
		t.Fatalf("err = %v, requests = %d", err, requests)
	}
}
