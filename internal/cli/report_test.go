package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/config"
)

const (
	reportID  = "6f1c2a9e-3b4d-4e5f-8a7b-0c1d2e3f4a5b"
	reportWhy = "escapeHtml on line 40 already encodes the value this rule flags"
)

// reportService answers every report with status and body, and keeps what
// each request sent.
type reportService struct {
	*httptest.Server
	requests []map[string]any
	auth     []string
}

func newReportService(t *testing.T, status int, body string) *reportService {
	t.Helper()
	service := &reportService{}
	service.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/report" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		var decoded map[string]any
		if err := json.NewDecoder(r.Body).Decode(&decoded); err != nil {
			t.Errorf("decode request: %v", err)
		}
		service.requests = append(service.requests, decoded)
		service.auth = append(service.auth, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(service.Close)
	return service
}

func reportAnswer(status string) string {
	return `{
		"reportId": "11111111-2222-4333-8444-555555555555",
		"findingId": "` + reportID + `",
		"checkId": "2b3c4d5e-6f70-4812-9a3b-4c5d6e7f8091",
		"policyId": "bifrost.security.xss",
		"kind": "false_positive",
		"status": "` + status + `"
	}`
}

func TestReportRecordsAFalsePositive(t *testing.T) {
	cases := []struct {
		name   string
		status string
		format string
		want   string
	}{
		{
			name: "recorded", status: "recorded", format: "human",
			want: "slopcop: reported finding " + reportID + " (bifrost.security.xss) as a false positive\n" +
				"It no longer fails checks of this repository in the same workspace (or with the same operator token), and the team sees the report and its reason on the SlopCop dashboard.\n",
		},
		{
			name: "already reported", status: "already_reported", format: "human",
			want: "slopcop: finding " + reportID + " (bifrost.security.xss) was already reported as a false positive\n" +
				"It no longer fails checks of this repository in the same workspace (or with the same operator token), and the team sees the earlier report on the SlopCop dashboard.\n",
		},
		{name: "recorded json", status: "recorded", format: "json"},
		{name: "already reported json", status: "already_reported", format: "json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateUserConfig(t)
			t.Setenv("SLOPCOP_TOKEN", "agent-token")
			server := newReportService(t, http.StatusOK, reportAnswer(tc.status))

			// The id is accepted in either case and the reason is trimmed.
			code, stdout, stderr := runCLI(t,
				"report", strings.ToUpper(reportID), "--reason", "  "+reportWhy+"\n",
				"--service", server.URL, "--format", tc.format, "--root", t.TempDir(),
			)
			if code != 0 {
				t.Fatalf("exit = %d, stderr = %s", code, stderr)
			}
			if stderr != "" {
				t.Errorf("stderr = %q", stderr)
			}
			if len(server.requests) != 1 {
				t.Fatalf("requests = %d", len(server.requests))
			}
			wantRequest := map[string]any{"findingId": reportID, "kind": "false_positive", "reason": reportWhy}
			if got := server.requests[0]; len(got) != len(wantRequest) ||
				got["findingId"] != wantRequest["findingId"] || got["kind"] != wantRequest["kind"] ||
				got["reason"] != wantRequest["reason"] {
				t.Errorf("request = %v, want %v", got, wantRequest)
			}
			if server.auth[0] != "Bearer agent-token" {
				t.Errorf("Authorization = %q", server.auth[0])
			}
			if tc.format == "human" {
				if stdout != tc.want {
					t.Errorf("stdout = %q, want %q", stdout, tc.want)
				}
				return
			}
			var document map[string]any
			if err := json.Unmarshal([]byte(stdout), &document); err != nil {
				t.Fatalf("stdout is not json: %v\n%s", err, stdout)
			}
			if document["status"] != tc.status || document["findingId"] != reportID ||
				document["policyId"] != "bifrost.security.xss" || document["kind"] != "false_positive" ||
				document["reportId"] != "11111111-2222-4333-8444-555555555555" ||
				document["checkId"] != "2b3c4d5e-6f70-4812-9a3b-4c5d6e7f8091" {
				t.Errorf("json = %v", document)
			}
		})
	}
}

func TestReportPrintsTheServiceFailure(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		code    string
		message string
		remedy  string
		// hint is the slopcop command added after a remedy to sign in.
		hint string
	}{
		{"auth required", 401, "auth_required", "This check needs a SlopCop login.", "Sign in, then retry.", "Sign in with `slopcop login`."},
		{"auth expired", 401, "auth_expired", "This SlopCop login has expired.", "Sign in again.", "Sign in with `slopcop login`."},
		{"finding not found", 404, "finding_not_found", "SlopCop has no finding with this id that you can see.", "Report the id of a finding from a check you ran.", ""},
		{"invalid report", 422, "invalid_report", "The report is malformed.", "Give a reason of 1 to 2000 characters.", ""},
		{"rate limited", 429, "rate_limited", "This report was rate limited.", "Wait and retry.", ""},
		{"service unavailable", 503, "service_unavailable", "The check service could not complete this request right now.", "Wait a moment and retry.", ""},
	}
	for _, tc := range cases {
		for _, format := range []string{"human", "json"} {
			t.Run(tc.name+" "+format, func(t *testing.T) {
				isolateUserConfig(t)
				body, _ := json.Marshal(map[string]any{
					"code": tc.code, "message": tc.message, "remedy": tc.remedy, "retryable": false,
				})
				server := newReportService(t, tc.status, string(body))

				code, stdout, stderr := runCLI(t,
					"report", reportID, "--reason", reportWhy, "--service", server.URL, "--format", format,
				)
				if code != 2 {
					t.Fatalf("exit = %d, want 2", code)
				}
				if stdout != "" {
					t.Errorf("a failed report printed %q on stdout", stdout)
				}
				want := tc.message + "\n" + tc.remedy + "\n"
				if tc.hint != "" {
					want = tc.message + "\n" + tc.remedy + "\n" + tc.hint + "\n"
				}
				if stderr != want {
					t.Errorf("stderr = %q, want the service's text verbatim: %q", stderr, want)
				}
			})
		}
	}

	t.Run("a service from before reports", func(t *testing.T) {
		isolateUserConfig(t)
		server := newReportService(t, http.StatusNotFound, `<!DOCTYPE html><title>Not found</title>`)
		code, stdout, stderr := runCLI(t, "report", reportID, "--reason", reportWhy, "--service", server.URL)
		if code != 2 || stdout != "" {
			t.Fatalf("exit = %d, stdout = %q", code, stdout)
		}
		if !strings.Contains(stderr, "does not accept false-positive reports") ||
			!strings.Contains(stderr, "the finding still fails the check") {
			t.Errorf("stderr = %q", stderr)
		}
	})

	t.Run("an unknown status", func(t *testing.T) {
		isolateUserConfig(t)
		server := newReportService(t, http.StatusOK, reportAnswer("queued"))
		code, stdout, stderr := runCLI(t, "report", reportID, "--reason", reportWhy, "--service", server.URL)
		if code != 2 || stdout != "" || !strings.Contains(stderr, `unknown status "queued"`) {
			t.Errorf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
		}
	})
}

func TestReportValidatesBeforeSending(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
		// usage failures also point at the help.
		usage bool
	}{
		{name: "not a uuid", args: []string{"report", "finding-1", "--reason", reportWhy}, want: `"finding-1" is not a finding id`},
		{name: "short uuid", args: []string{"report", reportID[:35], "--reason", reportWhy}, want: "is not a finding id"},
		{name: "braced uuid", args: []string{"report", "{" + reportID + "}", "--reason", reportWhy}, want: "is not a finding id"},
		{name: "non-hex uuid", args: []string{"report", "6f1c2a9e-3b4d-4e5f-8a7b-0c1d2e3f4a5g", "--reason", reportWhy}, want: "is not a finding id"},
		{name: "uuid without hyphens", args: []string{"report", strings.ReplaceAll(reportID, "-", "") + "abcd", "--reason", reportWhy}, want: "is not a finding id"},
		{name: "missing reason", args: []string{"report", reportID}, want: "--reason is required", usage: true},
		{name: "empty reason", args: []string{"report", reportID, "--reason", ""}, want: "the reason is empty"},
		{name: "blank reason", args: []string{"report", reportID, "--reason", " \t\n "}, want: "the reason is empty"},
		{name: "oversized reason", args: []string{"report", reportID, "--reason", strings.Repeat("é", 2001)}, want: "the reason is 2001 characters; keep it to 2000"},
		{name: "invalid utf-8", args: []string{"report", reportID, "--reason", "bad \xff byte"}, want: "not valid UTF-8"},
		{name: "no finding id", args: []string{"report", "--reason", reportWhy}, want: "report takes one finding id, got 0", usage: true},
		{name: "two finding ids", args: []string{"report", reportID, reportID, "--reason", reportWhy}, want: "got 2 argument(s)", usage: true},
		{name: "sarif", args: []string{"report", reportID, "--reason", reportWhy, "--format", "sarif"}, want: `unknown format "sarif" (expected human or json)`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateUserConfig(t)
			server := newReportService(t, http.StatusOK, reportAnswer("recorded"))
			code, stdout, stderr := runCLI(t, append(tc.args, "--service", server.URL)...)
			if code != 2 {
				t.Fatalf("exit = %d, want 2", code)
			}
			if len(server.requests) != 0 {
				t.Errorf("an invalid report reached the service: %v", server.requests)
			}
			if stdout != "" {
				t.Errorf("stdout = %q", stdout)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr = %q, want %q", stderr, tc.want)
			}
			if strings.Contains(stderr, "slopcop report --help") != tc.usage {
				t.Errorf("stderr = %q, usage hint wanted: %v", stderr, tc.usage)
			}
		})
	}

	t.Run("a reason of 2000 characters", func(t *testing.T) {
		isolateUserConfig(t)
		server := newReportService(t, http.StatusOK, reportAnswer("recorded"))
		reason := strings.Repeat("é", 2000)
		code, _, stderr := runCLI(t, "report", reportID, "--reason", reason, "--service", server.URL)
		if code != 0 {
			t.Fatalf("exit = %d, stderr = %s", code, stderr)
		}
		if len(server.requests) != 1 || server.requests[0]["reason"] != reason {
			t.Errorf("requests = %d", len(server.requests))
		}
	})
}

// A report signs in and finds the service exactly as a check does.
func TestReportResolvesTheTokenAndServiceLikeCheck(t *testing.T) {
	cases := []struct {
		name string
		// stored is what slopcop login saved; envToken and envService are
		// $SLOPCOP_TOKEN and $SLOPCOP_SERVICE; flag is --service.
		storedToken, envToken string
		storedService         bool
		envService, flag      bool
		wantServer            string
		wantAuth              string
	}{
		{name: "stored login", storedToken: "stored-token", storedService: true, wantServer: "stored", wantAuth: "Bearer stored-token"},
		{name: "token from the environment", storedToken: "stored-token", envToken: "env-token", storedService: true, wantServer: "stored", wantAuth: "Bearer env-token"},
		{name: "service from the environment", storedToken: "stored-token", storedService: true, envService: true, wantServer: "env", wantAuth: "Bearer stored-token"},
		{name: "service flag first", storedToken: "stored-token", storedService: true, envService: true, flag: true, wantServer: "flag", wantAuth: "Bearer stored-token"},
		{name: "no token", flag: true, wantServer: "flag", wantAuth: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateUserConfig(t)
			servers := map[string]*reportService{
				"stored": newReportService(t, http.StatusOK, reportAnswer("recorded")),
				"env":    newReportService(t, http.StatusOK, reportAnswer("recorded")),
				"flag":   newReportService(t, http.StatusOK, reportAnswer("recorded")),
			}
			stored := config.Config{Token: tc.storedToken}
			if tc.storedService {
				stored.ServiceURL = servers["stored"].URL
			}
			if _, err := config.Save(stored); err != nil {
				t.Fatalf("Save: %v", err)
			}
			t.Setenv("SLOPCOP_TOKEN", tc.envToken)
			if tc.envService {
				t.Setenv("SLOPCOP_SERVICE", servers["env"].URL)
			}
			args := []string{"report", reportID, "--reason", reportWhy}
			if tc.flag {
				args = append(args, "--service", servers["flag"].URL)
			}
			code, _, stderr := runCLI(t, args...)
			if code != 0 {
				t.Fatalf("exit = %d, stderr = %s", code, stderr)
			}
			for name, server := range servers {
				want := 0
				if name == tc.wantServer {
					want = 1
				}
				if len(server.requests) != want {
					t.Errorf("%s service got %d request(s), want %d", name, len(server.requests), want)
				}
			}
			if auth := servers[tc.wantServer].auth; len(auth) == 1 && auth[0] != tc.wantAuth {
				t.Errorf("Authorization = %q, want %q", auth[0], tc.wantAuth)
			}
		})
	}
}
