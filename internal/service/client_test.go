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
		RepoURL:    "https://github.com/BrokkAi/slopcop",
		BaseCommit: "0123456789abcdef0123456789abcdef01234567",
		Change:     "ZGlmZiAtLWdpdA==",
		Selection:  &Selection{Files: []string{".slopcop/policies/x.rqlp"}},
		FailOn:     "warning",
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

	for _, key := range []string{"repoUrl", "baseCommit", "change", "selection", "failOn"} {
		if _, ok := captured[key]; !ok {
			t.Errorf("request is missing %q", key)
		}
	}
	if len(captured) != 5 {
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
