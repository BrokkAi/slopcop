package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSARIFFileWritesSARIFBesideTheJSONReport(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	base := runGit(t, repo, "rev-parse", "HEAD")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": "findings",
			"baseCommit": "` + base + `",
			"applied": true,
			"summary": {"files": 1, "findings": 1, "newFindings": 1, "incompletePolicies": 0, "failOn": "finding"},
			"findings": [{"id": "f-1", "policyId": "rule.a", "severity": "warning", "certainty": "definite", "path": "a.go", "startLine": 3, "endLine": 3, "title": "a finding", "new": true}],
			"incomplete": []
		}`))
	}))
	defer server.Close()

	sarifPath := filepath.Join(t.TempDir(), "slopcop.sarif")
	code, stdout, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL, "--format", "json", "--sarif-file", sarifPath)
	if code != 1 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	var report struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil || report.Status != "findings" {
		t.Fatalf("stdout is not the json report: %v\n%s", err, stdout)
	}
	data, err := os.ReadFile(sarifPath)
	if err != nil {
		t.Fatal(err)
	}
	var sarif struct {
		Version string `json:"version"`
		Runs    []struct {
			Results []struct {
				RuleID string `json:"ruleId"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(data, &sarif); err != nil {
		t.Fatalf("sarif file is not json: %v", err)
	}
	if sarif.Version != "2.1.0" || len(sarif.Runs) == 0 || len(sarif.Runs[0].Results) != 1 || sarif.Runs[0].Results[0].RuleID != "rule.a" {
		t.Fatalf("sarif = %s", data)
	}
}

const commentReport = `{"status":"findings","staticStatus":"clean","exitReason":"review_findings","checkId":"chk",
 "summary":{"newFindings":0,"reviewFindings":1},"findings":[],
 "review":{"status":"findings","error":"","reviewers":[{"name":"1-sol","family":"sol","status":"findings"}],
  "findings":[{"id":"rv-1","reviewer":"1-sol","priority":"P2","path":"a.go","startLine":2,"title":"a review finding"}]}}`

func tempFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func clearGitHubEnv(t *testing.T) {
	for _, name := range []string{"GITHUB_REPOSITORY", "GITHUB_EVENT_PATH", "GITHUB_SHA", "GITHUB_TOKEN", "GH_TOKEN", "GITHUB_API_URL", "GITHUB_OUTPUT", "GITHUB_STEP_SUMMARY"} {
		t.Setenv(name, "")
	}
}

func TestCommentDryRunWritesTheSummaryAndOutputs(t *testing.T) {
	isolateUserConfig(t)
	clearGitHubEnv(t)
	outputs := filepath.Join(t.TempDir(), "output")
	summary := filepath.Join(t.TempDir(), "summary")
	t.Setenv("GITHUB_OUTPUT", outputs)
	t.Setenv("GITHUB_STEP_SUMMARY", summary)

	code, stdout, stderr := runCLI(t, "comment", "--report", tempFile(t, "r.json", commentReport), "--dry-run")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "SlopCop found 1 new finding, all from the review") || !strings.Contains(stdout, "a review finding") {
		t.Errorf("stdout = %s", stdout)
	}
	if strings.Contains(stdout, "<!-- slopcop:summary -->") {
		t.Errorf("a dry run carries the pull request marker: %s", stdout)
	}
	got, err := os.ReadFile(outputs)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"status=findings\n", "exit-reason=review_findings\n", "static-findings=0\n", "review-findings=1\n", "headline=SlopCop found 1 new finding, all from the review\n"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("outputs lack %q:\n%s", want, got)
		}
	}
	if job, _ := os.ReadFile(summary); !strings.Contains(string(job), "a review finding") {
		t.Errorf("job summary = %s", job)
	}
}

func TestCommentReadsThePullRequestFromTheEvent(t *testing.T) {
	isolateUserConfig(t)
	clearGitHubEnv(t)
	var review struct {
		CommitID string `json:"commit_id"`
		Comments []struct {
			Path string `json:"path"`
			Line int    `json:"line"`
		} `json:"comments"`
	}
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/user":
			w.WriteHeader(http.StatusForbidden)
		case r.URL.Path == "/repos/o/r/pulls/12/files":
			_, _ = w.Write([]byte(`[{"filename":"a.go","status":"modified","patch":"@@ -1,2 +1,3 @@\n one\n+two\n three"}]`))
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/repos/o/r/pulls/12/reviews":
			_ = json.NewDecoder(r.Body).Decode(&review)
			_, _ = w.Write([]byte(`{"id":1}`))
		case r.URL.Path == "/repos/o/r/issues/12/comments":
			_, _ = w.Write([]byte(`{"id":2,"html_url":"https://github.test/o/r/pull/12#issuecomment-2"}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	t.Setenv("GITHUB_REPOSITORY", "o/r")
	t.Setenv("GITHUB_TOKEN", "tok")
	t.Setenv("GITHUB_EVENT_PATH", tempFile(t, "event.json", `{"pull_request":{"number":12,"head":{"sha":"headsha"}}}`))

	code, _, stderr := runCLI(t, "comment", "--report", tempFile(t, "r.json", commentReport), "--api-url", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s, calls = %v", code, stderr, paths)
	}
	if review.CommitID != "headsha" || len(review.Comments) != 1 || review.Comments[0].Path != "a.go" || review.Comments[0].Line != 2 {
		t.Errorf("review = %+v", review)
	}
	if !strings.Contains(stderr, "issuecomment-2") {
		t.Errorf("stderr does not link the summary: %s", stderr)
	}
}

func TestCommentWithoutAPullRequestOnlySummarizes(t *testing.T) {
	isolateUserConfig(t)
	clearGitHubEnv(t)
	t.Setenv("GITHUB_EVENT_PATH", tempFile(t, "event.json", `{"ref":"refs/heads/main"}`))
	code, _, stderr := runCLI(t, "comment", "--report", tempFile(t, "r.json", commentReport), "--repo", "o/r")
	if code != 0 || !strings.Contains(stderr, "no pull request to comment on") {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
}

func TestCommentRefusesAHumanReport(t *testing.T) {
	isolateUserConfig(t)
	clearGitHubEnv(t)
	code, _, stderr := runCLI(t, "comment", "--report", tempFile(t, "r.txt", "No new findings | Analysis complete\n"), "--dry-run")
	if code != 2 || !strings.Contains(stderr, "read the check report") {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
}

func TestCommentThatCannotPostStillWritesTheOutputs(t *testing.T) {
	isolateUserConfig(t)
	clearGitHubEnv(t)
	outputs := filepath.Join(t.TempDir(), "output")
	t.Setenv("GITHUB_OUTPUT", outputs)
	code, _, stderr := runCLI(t, "comment", "--report", tempFile(t, "r.json", commentReport), "--repo", "o/r", "--pr", "3")
	if code != 2 || !strings.Contains(stderr, "set GITHUB_TOKEN") {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if got, _ := os.ReadFile(outputs); !strings.Contains(string(got), "exit-reason=review_findings\n") {
		t.Errorf("outputs = %s", got)
	}
}

const headCommit = "0123456789abcdef0123456789abcdef01234567"

// appService is a SlopCop service that answers a pull request comment
// request with status, and records what it was sent.
func appService(t *testing.T, status string, sent *map[string]any) *httptest.Server {
	t.Helper()
	return appServiceAnswering(t, `{"checkId":"chk","status":"`+status+`","outcome":"findings","posted":1,"inline":1,"elsewhere":0,"summaryUrl":"https://github.test/o/r/pull/12#issuecomment-9"}`, sent)
}

// appServiceAnswering is a SlopCop service whose comment endpoint answers
// body, recording what it was sent.
func appServiceAnswering(t *testing.T, body string, sent *map[string]any) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/v1/check/chk/pull-request-comments" {
			if got := r.Header.Get("Authorization"); got != "Bearer slopcop-token" {
				t.Errorf("authorization = %q", got)
			}
			_ = json.NewDecoder(r.Body).Decode(sent)
			_, _ = w.Write([]byte(body))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"not_found"}`))
	}))
	t.Cleanup(server.Close)
	return server
}

// commentGitHub is a GitHub that takes the token path's writes, counting them.
func commentGitHub(t *testing.T, writes *int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/user":
			w.WriteHeader(http.StatusForbidden)
		case r.URL.Path == "/repos/o/r/pulls/12/files":
			_, _ = w.Write([]byte(`[{"filename":"a.go","status":"modified","patch":"@@ -1,2 +1,3 @@\n one\n+two\n three"}]`))
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[]`))
		default:
			*writes++
			_, _ = w.Write([]byte(`{"id":2,"html_url":"https://github.test/o/r/pull/12#issuecomment-2"}`))
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func appCommentEnv(t *testing.T) {
	isolateUserConfig(t)
	clearGitHubEnv(t)
	t.Setenv("SLOPCOP_TOKEN", "slopcop-token")
	t.Setenv("SLOPCOP_NO_UPDATE_NOTICE", "1")
	t.Setenv("GITHUB_REPOSITORY", "o/r")
	t.Setenv("GITHUB_TOKEN", "workflow-token")
	t.Setenv("GITHUB_EVENT_PATH", tempFile(t, "event.json", `{"pull_request":{"number":12,"head":{"sha":"`+headCommit+`"}}}`))
}

func TestCommentAsksTheAppFirst(t *testing.T) {
	appCommentEnv(t)
	var sent map[string]any
	service := appService(t, "posted", &sent)
	writes := 0
	github := commentGitHub(t, &writes)

	code, _, stderr := runCLI(t, "comment", "--report", tempFile(t, "r.json", commentReport), "--service", service.URL, "--api-url", github.URL, "--certainty", "all", "--via", "auto")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if writes != 0 {
		t.Errorf("the workflow token wrote %d times", writes)
	}
	if !strings.Contains(stderr, "SlopCop GitHub App added 1 inline comment(s); summary at https://github.test/o/r/pull/12#issuecomment-9") {
		t.Errorf("stderr = %s", stderr)
	}
	if sent["pullRequest"] != float64(12) || sent["headCommit"] != headCommit || sent["certainty"] != "all" {
		t.Errorf("sent = %v", sent)
	}
	// The report's review goes as it is, and no GitHub token goes at all.
	review, _ := sent["review"].(map[string]any)
	if findings, _ := review["findings"].([]any); len(findings) != 1 {
		t.Errorf("review = %v", sent["review"])
	}
	if data, _ := json.Marshal(sent); strings.Contains(string(data), "workflow-token") {
		t.Errorf("the GitHub token was sent: %s", data)
	}
}

func TestCommentFallsBackToTheWorkflowTokenWhenTheAppDoesNotPost(t *testing.T) {
	for _, status := range []string{"off", "not_connected", "not_granted"} {
		t.Run(status, func(t *testing.T) {
			appCommentEnv(t)
			var sent map[string]any
			service := appService(t, status, &sent)
			writes := 0
			github := commentGitHub(t, &writes)
			code, _, stderr := runCLI(t, "comment", "--report", tempFile(t, "r.json", commentReport), "--service", service.URL, "--api-url", github.URL, "--via", "auto")
			if code != 0 {
				t.Fatalf("exit = %d, stderr = %s", code, stderr)
			}
			if writes == 0 || !strings.Contains(stderr, "commenting with GITHUB_TOKEN instead") {
				t.Errorf("writes = %d, stderr = %s", writes, stderr)
			}
		})
	}
}

const (
	noConnectionAnswer = `{"checkId":"chk","status":"not_connected","reason":"no_github_connection",` +
		`"workspaceId":"w1","workspaceName":"Acme","workspaceKind":"team","repository":"o/r",` +
		`"connectUrl":"https://slopcop.test/repositories","installUrl":"https://github.com/apps/slopcop/installations/new"}`
	notCoveredAnswer = `{"checkId":"chk","status":"not_connected","reason":"repository_not_covered",` +
		`"workspaceId":"w1","workspaceName":"David","workspaceKind":"personal","repository":"o/r",` +
		`"connectUrl":"https://slopcop.test/repositories","installUrl":"https://github.com/apps/slopcop/installations/new"}`
)

func TestCommentViaAppSaysWhichWorkspaceToConnect(t *testing.T) {
	for _, tc := range []struct {
		name, answer string
		want         []string
	}{
		{"no connection", noConnectionAnswer, []string{
			`the SlopCop GitHub App did not comment: the "Acme" workspace, which ran the check, has no GitHub connection.`,
			`open https://slopcop.test/repositories and click Connect GitHub with the "Acme" workspace active`,
			"Installing the App on GitHub is not enough",
		}},
		{"repository not covered", notCoveredAnswer, []string{
			"no SlopCop App installation that your personal workspace's GitHub connection holds includes o/r.",
			"Install the SlopCop App on the account that owns o/r (https://github.com/apps/slopcop/installations/new) with o/r selected",
			"open https://slopcop.test/repositories and click Connect GitHub with your personal workspace active",
		}},
		{"repository not on the installation", strings.Replace(notCoveredAnswer, `"installUrl"`,
			`"installationSettingsUrl":"https://github.com/organizations/o/settings/installations/7","installUrl"`, 1), []string{
			"the SlopCop App's installation on o, which your personal workspace's GitHub connection holds, does not include o/r.",
			"add o/r under Repository access at https://github.com/organizations/o/settings/installations/7. It then shows on https://slopcop.test/repositories with your personal workspace active.",
		}},
		{"not on GitHub", `{"checkId":"chk","status":"not_connected","reason":"not_github","repository":"g/r"}`, []string{
			"the App comments only on GitHub pull requests, and g/r is not on GitHub",
		}},
		// A service from before the reason: the words it always had.
		{"older service", `{"checkId":"chk","status":"not_connected"}`, []string{
			"the SlopCop GitHub App did not comment: no GitHub connection of the workspace covers this repository",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			appCommentEnv(t)
			var sent map[string]any
			service := appServiceAnswering(t, tc.answer, &sent)
			writes := 0
			github := commentGitHub(t, &writes)
			code, _, stderr := runCLI(t, "comment", "--report", tempFile(t, "r.json", commentReport), "--service", service.URL, "--api-url", github.URL, "--via", "app")
			if code == 0 || writes != 0 {
				t.Fatalf("exit = %d, writes = %d, stderr = %s", code, writes, stderr)
			}
			for _, want := range tc.want {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr lacks %q:\n%s", want, stderr)
				}
			}
		})
	}
}

func TestCommentViaAutoSaysOnceHowToLetTheAppComment(t *testing.T) {
	appCommentEnv(t)
	var sent map[string]any
	service := appServiceAnswering(t, noConnectionAnswer, &sent)
	writes := 0
	github := commentGitHub(t, &writes)
	code, _, stderr := runCLI(t, "comment", "--report", tempFile(t, "r.json", commentReport), "--service", service.URL, "--api-url", github.URL, "--via", "auto")
	if code != 0 || writes == 0 {
		t.Fatalf("exit = %d, writes = %d, stderr = %s", code, writes, stderr)
	}
	for _, want := range []string{
		`slopcop: the SlopCop GitHub App did not comment (the "Acme" workspace, which ran the check, has no GitHub connection); commenting with GITHUB_TOKEN instead.`,
		`slopcop: To let the App comment, open https://slopcop.test/repositories and click Connect GitHub with the "Acme" workspace active.`,
	} {
		if strings.Count(stderr, want) != 1 {
			t.Errorf("stderr has %q %d times:\n%s", want, strings.Count(stderr, want), stderr)
		}
	}
}

func TestCommentViaAppDoesNotFallBack(t *testing.T) {
	appCommentEnv(t)
	var sent map[string]any
	service := appService(t, "not_granted", &sent)
	writes := 0
	github := commentGitHub(t, &writes)
	code, _, stderr := runCLI(t, "comment", "--report", tempFile(t, "r.json", commentReport), "--service", service.URL, "--api-url", github.URL, "--via", "app")
	if code == 0 || writes != 0 || !strings.Contains(stderr, "has not accepted Pull requests: Read and write") {
		t.Fatalf("exit = %d, writes = %d, stderr = %s", code, writes, stderr)
	}
	code, _, stderr = runCLI(t, "comment", "--report", tempFile(t, "r.json", commentReport), "--via", "bot")
	if code == 0 || !strings.Contains(stderr, "--via") {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
}

func TestCommentDefaultsToTheWorkflowToken(t *testing.T) {
	appCommentEnv(t)
	sent := map[string]any{}
	service := appService(t, "posted", &sent)
	writes := 0
	github := commentGitHub(t, &writes)
	code, _, stderr := runCLI(t, "comment", "--report", tempFile(t, "r.json", commentReport), "--service", service.URL, "--api-url", github.URL)
	if code != 0 || writes == 0 || len(sent) != 0 {
		t.Fatalf("exit = %d, writes = %d, sent to the service = %v, stderr = %s", code, writes, sent, stderr)
	}
}
