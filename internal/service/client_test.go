package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestCheckSendsTheContractRequest(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/check" {
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
			"status": "findings",
			"baseCommit": "0123456789abcdef0123456789abcdef01234567",
			"applied": true,
			"summary": {"files": 1, "findings": 1, "incompletePolicies": 0, "failOn": "warning"},
			"findings": [{"policyId": "a.b", "severity": "warning", "certainty": "definite", "path": "main.go", "startLine": 3, "endLine": 3, "title": "avoid"}],
			"incomplete": []
		}`))
	}))
	defer server.Close()

	client, err := New(server.URL, "secret", "slopcop/test", 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	response, err := client.Check(context.Background(), Request{
		ContractVersion: "check-v2",
		SinceCommit:     strings.Repeat("1", 40),
		ChangeCommit:    strings.Repeat("2", 40),
		RepoURL:         "https://github.com/BrokkAi/slopcop",
		BaseCommit:      "0123456789abcdef0123456789abcdef01234567",
		Change:          "UEFDSw==",
		Selection:       &Selection{Files: []string{".slopcop/policies/x.rqlp"}},
		FailOn:          "warning",
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if response.Status != StatusFindings || response.ExitCode() != 1 {
		t.Fatalf("status = %q, exit = %d", response.Status, response.ExitCode())
	}
	if len(response.Findings) != 1 || response.Findings[0].PolicyID != "a.b" {
		t.Fatalf("findings = %+v", response.Findings)
	}

	for _, key := range []string{"contractVersion", "sinceCommit", "changeCommit", "repoUrl", "baseCommit", "change", "selection", "failOn"} {
		if _, ok := captured[key]; !ok {
			t.Errorf("request is missing %q", key)
		}
	}
	if len(captured) != 8 {
		t.Errorf("request carries unexpected fields: %v", captured)
	}
}

func TestCheckDecodesAnIncompleteResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": "incomplete",
			"baseCommit": "0123456789abcdef0123456789abcdef01234567",
			"applied": true,
			"summary": {"files": 1, "findings": 0, "incompletePolicies": 2, "incompletePolicyIds": ["acme.slow"], "omittedFindings": 7, "failOn": "finding"},
			"findings": [],
			"incomplete": [{"policyId": "acme.slow", "reason": "timed_out"}],
			"failure": "The analysis could not be completed."
		}`))
	}))
	defer server.Close()

	client, err := New(server.URL, "secret", "", 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	response, err := client.Check(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if response.ExitCode() != 2 || response.Failure != "The analysis could not be completed." {
		t.Fatalf("response = %+v", response)
	}
	if len(response.Incomplete) != 1 || response.Incomplete[0] != (IncompletePolicy{PolicyID: "acme.slow", Reason: "timed_out"}) {
		t.Fatalf("incomplete = %+v", response.Incomplete)
	}
	summary := response.Summary
	if summary.IncompletePolicies != 2 || summary.OmittedFindings != 7 || len(summary.IncompletePolicyIDs) != 1 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestCheckFillsMissingLists(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status": "clean", "baseCommit": "0123456789abcdef0123456789abcdef01234567", "summary": {}}`))
	}))
	defer server.Close()

	client, err := New(server.URL, "secret", "", 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	response, err := client.Check(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if response.Findings == nil || response.Incomplete == nil || response.Summary.IncompletePolicyIDs == nil {
		t.Fatalf("a report would print null lists: %+v", response)
	}
}

func TestCheckDecodesTheClosedErrorContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{
			"code": "base_not_found",
			"message": "The service does not have the base commit this change was made against.",
			"remedy": "Push the base commit, or rebase onto the upstream branch and retry.",
			"retryable": false
		}`))
	}))
	defer server.Close()

	client, err := New(server.URL, "secret", "", 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Check(context.Background(), Request{
		RepoURL:    "https://github.com/BrokkAi/slopcop",
		BaseCommit: "0123456789abcdef0123456789abcdef01234567",
		Change:     "ZGlmZiAtLWdpdA==",
	})
	var serviceError *Error
	if !errors.As(err, &serviceError) {
		t.Fatalf("expected *Error, got %v", err)
	}
	if serviceError.Body.Code != "base_not_found" || serviceError.StatusCode != 409 {
		t.Fatalf("error = %+v", serviceError)
	}
	text := serviceError.RemedyText()
	for _, want := range []string{
		"The service does not have the base commit",
		"Push the base commit",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("RemedyText() = %q, want %q", text, want)
		}
	}
}

func TestCheckPrintsTheTopUpLinkWhenCreditsRanOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{
			"code": "insufficient_credits",
			"message": "Your SlopCop workspace is out of credits and its grace period has ended, so checks are refused in every repository but its free one.",
			"remedy": "Add credits on the Credits page in SlopCop, then check again.",
			"retryable": false,
			"topUpUrl": "https://slopcop.com/credits"
		}`))
	}))
	defer server.Close()

	client, err := New(server.URL, "secret", "", 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Check(context.Background(), Request{
		RepoURL:    "https://github.com/BrokkAi/slopcop",
		BaseCommit: "0123456789abcdef0123456789abcdef01234567",
		Change:     "UEFDSw==",
	})
	var serviceError *Error
	if !errors.As(err, &serviceError) {
		t.Fatalf("expected *Error, got %v", err)
	}
	if serviceError.Body.Code != CodeInsufficientCredits || serviceError.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("error = %+v", serviceError)
	}
	want := "Your SlopCop workspace is out of credits and its grace period has ended, so checks are refused in every repository but its free one.\n" +
		"Add credits on the Credits page in SlopCop, then check again.\n" +
		"Add credits: https://slopcop.com/credits"
	if text := serviceError.RemedyText(); text != want {
		t.Errorf("RemedyText() = %q, want %q", text, want)
	}
}

func TestCheckUnexpectedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	client, err := New(server.URL, "", "", 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Check(context.Background(), Request{})
	var serviceError *Error
	if !errors.As(err, &serviceError) {
		t.Fatalf("expected *Error, got %v", err)
	}
	if serviceError.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", serviceError.StatusCode)
	}
	if serviceError.Error() == "" {
		t.Fatal("expected a printable error")
	}
}

func TestCheckReadsTheNewFindingSplit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": "findings",
			"baseCommit": "0123456789abcdef0123456789abcdef01234567",
			"summary": {"files": 1, "findings": 2, "newFindings": 1, "existingFindings": 1, "failOn": "finding"},
			"findings": [
				{"policyId": "a", "severity": "warning", "certainty": "definite", "path": "x.go", "startLine": 1, "endLine": 1, "title": "t", "new": true},
				{"policyId": "b", "severity": "error", "certainty": "definite", "path": "x.go", "startLine": 9, "endLine": 9, "title": "u", "new": false}
			]
		}`))
	}))
	defer server.Close()

	client, err := New(server.URL, "secret", "", 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	response, err := client.Check(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !response.Findings[0].New || response.Findings[1].New ||
		response.Summary.NewFindings != 1 || response.Summary.ExistingFindings != 1 {
		t.Fatalf("response = %+v", response)
	}
}

func TestCheckAsksAgainUntilTheCheckFinishes(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		if requests < 3 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"code": "check_running", "message": "Still running.", "remedy": "Wait.", "retryable": true}`))
			return
		}
		_, _ = w.Write([]byte(`{"status": "clean", "baseCommit": "0123456789abcdef0123456789abcdef01234567", "summary": {}}`))
	}))
	defer server.Close()

	client, err := New(server.URL, "secret", "", 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var waits []time.Duration
	client.wait = func(_ context.Context, delay time.Duration) error {
		waits = append(waits, delay)
		return nil
	}
	response, err := client.Check(context.Background(), Request{})
	if err != nil || response.Status != StatusClean {
		t.Fatalf("Check = %+v, %v", response, err)
	}
	if requests != 3 || len(waits) != 2 || waits[0] != 7*time.Second {
		t.Fatalf("requests = %d, waits = %v", requests, waits)
	}

	// When the wait would pass the deadline, the check is pending.
	requests = 0
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = client.Check(ctx, Request{})
	var pending *Pending
	if !errors.As(err, &pending) {
		t.Fatalf("expected *Pending, got %v", err)
	}
	if pending.RetryAfter != 7 || pending.RemedyText() != "Still running.\nWait." || requests != 1 {
		t.Fatalf("pending = %+v, requests = %d", pending, requests)
	}
}

func TestNewValidatesTheServiceURL(t *testing.T) {
	for _, raw := range []string{"", "ftp://example.com", "http://example.com", "not a url"} {
		if _, err := New(raw, "", "", 0); err == nil {
			t.Errorf("New(%q) accepted an unusable URL", raw)
		}
	}
	for _, raw := range []string{"https://example.com", "http://localhost:8080", "http://127.0.0.1:9"} {
		if _, err := New(raw, "", "", 0); err != nil {
			t.Errorf("New(%q): %v", raw, err)
		}
	}
}

func TestCheckDecodesIDsAndDismissedFindings(t *testing.T) {
	const findingID = "6f1c2a9e-3b4d-4e5f-8a7b-0c1d2e3f4a5b"
	const dismissedID = "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"
	cases := []struct {
		name    string
		payload string
		want    Response
	}{
		{
			name: "a service that names checks and findings",
			payload: `{
				"status": "findings",
				"checkId": "2b3c4d5e-6f70-4812-9a3b-4c5d6e7f8091",
				"baseCommit": "0123456789abcdef0123456789abcdef01234567",
				"summary": {"files": 1, "findings": 3, "newFindings": 1, "existingFindings": 1, "dismissedFindings": 1, "failOn": "finding"},
				"findings": [
					{"id": "` + findingID + `", "policyId": "a", "severity": "warning", "certainty": "definite", "path": "x.go", "startLine": 1, "endLine": 1, "title": "t", "new": true, "dismissed": false},
					{"id": "9e8d7c6b-5a49-4382-a716-151413121110", "policyId": "b", "severity": "error", "certainty": "definite", "path": "x.go", "startLine": 9, "endLine": 9, "title": "u", "new": false, "dismissed": false},
					{"id": "` + dismissedID + `", "policyId": "c", "severity": "warning", "certainty": "likely", "path": "x.go", "startLine": 4, "endLine": 4, "title": "v", "new": false, "dismissed": true}
				]
			}`,
			want: Response{
				CheckID: "2b3c4d5e-6f70-4812-9a3b-4c5d6e7f8091",
				Summary: Summary{Findings: 3, NewFindings: 1, ExistingFindings: 1, DismissedFindings: 1},
				Findings: []Finding{
					{ID: findingID, PolicyID: "a", New: true},
					{ID: "9e8d7c6b-5a49-4382-a716-151413121110", PolicyID: "b"},
					{ID: dismissedID, PolicyID: "c", Dismissed: true},
				},
			},
		},
		{
			name: "a service from before ids and dismissals",
			payload: `{
				"status": "findings",
				"baseCommit": "0123456789abcdef0123456789abcdef01234567",
				"summary": {"files": 1, "findings": 2, "newFindings": 1, "existingFindings": 1, "failOn": "finding"},
				"findings": [
					{"policyId": "a", "severity": "warning", "certainty": "definite", "path": "x.go", "startLine": 1, "endLine": 1, "title": "t", "new": true},
					{"policyId": "b", "severity": "error", "certainty": "definite", "path": "x.go", "startLine": 9, "endLine": 9, "title": "u", "new": false}
				]
			}`,
			want: Response{
				Summary: Summary{Findings: 2, NewFindings: 1, ExistingFindings: 1},
				Findings: []Finding{
					{PolicyID: "a", New: true},
					{PolicyID: "b"},
				},
			},
		},
		{
			name: "a service from before new findings were marked",
			payload: `{
				"status": "findings",
				"baseCommit": "0123456789abcdef0123456789abcdef01234567",
				"summary": {"files": 1, "findings": 2, "failOn": "finding"},
				"findings": [
					{"policyId": "a", "severity": "warning", "certainty": "definite", "path": "x.go", "startLine": 1, "endLine": 1, "title": "t"},
					{"policyId": "c", "severity": "warning", "certainty": "definite", "path": "x.go", "startLine": 4, "endLine": 4, "title": "v", "dismissed": true}
				]
			}`,
			want: Response{
				Summary: Summary{Findings: 2, NewFindings: 1},
				Findings: []Finding{
					{PolicyID: "a", New: true},
					{PolicyID: "c", Dismissed: true},
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.payload))
			}))
			defer server.Close()

			client, err := New(server.URL, "secret", "", 0)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			response, err := client.Check(context.Background(), Request{})
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if response.CheckID != tc.want.CheckID {
				t.Errorf("checkId = %q, want %q", response.CheckID, tc.want.CheckID)
			}
			got := response.Summary
			if got.Findings != tc.want.Summary.Findings || got.NewFindings != tc.want.Summary.NewFindings ||
				got.ExistingFindings != tc.want.Summary.ExistingFindings ||
				got.DismissedFindings != tc.want.Summary.DismissedFindings {
				t.Errorf("summary = %+v, want %+v", got, tc.want.Summary)
			}
			if len(response.Findings) != len(tc.want.Findings) {
				t.Fatalf("findings = %+v", response.Findings)
			}
			for index, want := range tc.want.Findings {
				found := response.Findings[index]
				if found.ID != want.ID || found.PolicyID != want.PolicyID ||
					found.New != want.New || found.Dismissed != want.Dismissed {
					t.Errorf("finding %d = %+v, want id %q new %v dismissed %v",
						index, found, want.ID, want.New, want.Dismissed)
				}
			}
		})
	}
}

func TestRetryAfterBoundsAHugeWait(t *testing.T) {
	cases := map[string]time.Duration{
		"":                    defaultRetryAfter,
		"soon":                defaultRetryAfter,
		"-1":                  defaultRetryAfter,
		"0":                   0,
		"7":                   7 * time.Second,
		"3600":                maxRetryAfter,
		"9999999999":          maxRetryAfter,
		"9223372036854775807": maxRetryAfter,
	}
	for value, want := range cases {
		header := http.Header{}
		if value != "" {
			header.Set("Retry-After", value)
		}
		if got := retryAfter(header); got != want {
			t.Errorf("retryAfter(%q) = %v, want %v", value, got, want)
		}
	}
}

// A failure without the closed contract quotes the response on one line, cut
// on a character boundary and without control or bidi characters; a code
// alone gets the service's remedy for it.
func TestDecodeErrorExcerptsAndCodeRemedies(t *testing.T) {
	const generic = "Check the service URL, then retry."
	cases := []struct {
		name        string
		payload     string
		wantMessage string
		wantRemedy  string
	}{
		{name: "empty", payload: "", wantMessage: "the SlopCop service returned HTTP 502", wantRemedy: generic},
		{name: "html on several lines", payload: "<html>\n  <body>bad\tgateway</body>\n</html>\n",
			wantMessage: "the SlopCop service returned HTTP 502", wantRemedy: generic + " Response: <html> <body>bad gateway</body> </html>"},
		{name: "terminal escapes and bidi overrides", payload: "\x1b]0;owned\x07\x1b[2Jtext‮gnp.exe⁦",
			wantMessage: "the SlopCop service returned HTTP 502", wantRemedy: generic + " Response: �]0;owned��[2Jtext�gnp.exe�"},
		{name: "long multibyte text", payload: strings.Repeat("é", 300),
			wantMessage: "the SlopCop service returned HTTP 502", wantRemedy: generic + " Response: " + strings.Repeat("é", 200) + "..."},
		{name: "only the start of a large body", payload: "a" + strings.Repeat(" ", maxExcerptBytes) + "b",
			wantMessage: "the SlopCop service returned HTTP 502", wantRemedy: generic + " Response: a..."},
		{name: "sign-in code alone", payload: `{"code":"auth_required"}`, wantRemedy: "Sign in, then retry."},
		{name: "expired code alone", payload: `{"code":"auth_expired"}`, wantRemedy: "Sign in again."},
		{name: "rate-limited code alone", payload: `{"code":"rate_limited"}`, wantRemedy: "Wait and retry."},
		{name: "repository code alone", payload: `{"code":"repo_not_indexed"}`,
			wantRemedy: "A private repository needs the SlopCop GitHub App: install it on the repository's owner with access to the repository, run slopcop login so this login can use that installation, then retry. If the remote URL is wrong, fix it first."},
		{name: "unknown code alone", payload: `{"code":"teapot"}`, wantRemedy: generic + ` Response: {"code":"teapot"}`},
		{name: "a code and a message keep the service's text", payload: `{"code":"rate_limited","message":"Slow down."}`,
			wantMessage: "Slow down."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			failure := decodeError(http.StatusBadGateway, []byte(tc.payload))
			if failure.Body.Message != tc.wantMessage || failure.Body.Remedy != tc.wantRemedy {
				t.Errorf("body = %+v, want message %q, remedy %q", failure.Body, tc.wantMessage, tc.wantRemedy)
			}
			if !utf8.ValidString(failure.RemedyText()) {
				t.Errorf("RemedyText is not valid UTF-8: %q", failure.RemedyText())
			}
		})
	}
}

// Every field a failure prints is kept printable, not only the excerpt: a
// hostile message, remedy or link cannot drive the terminal.
func TestRemedyTextKeepsEveryFieldPrintable(t *testing.T) {
	failure := &Error{StatusCode: http.StatusPaymentRequired, Body: ErrorBody{
		Code:       CodeInsufficientCredits,
		Message:    "out\x1b[2J of credits",
		Remedy:     "add\x07 credits‮",
		InstallURL: "https://install.example/\x1b]8;;x\x07",
		TopUpURL:   "https://top-up.example/\r⁦",
	}}
	want := "out�[2J of credits\nadd� credits�\nhttps://install.example/�]8;;x�\nAdd credits: https://top-up.example/��"
	if got := failure.RemedyText(); got != want {
		t.Errorf("RemedyText = %q, want %q", got, want)
	}
	pending := &Pending{Message: "still\x1b[2J running", Remedy: "run\x07 again"}
	if got, want := pending.RemedyText(), "still�[2J running\nrun� again"; got != want {
		t.Errorf("Pending.RemedyText = %q, want %q", got, want)
	}
}
