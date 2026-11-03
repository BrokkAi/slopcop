package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const ignoredPage = `{
	"repository": {"fullName": "BrokkAi/slopcop"},
	"total": 3,
	"page": 0,
	"pageSize": 25,
	"ignores": [
		{
			"id": "6f1c9a52-0f6e-4f0b-9a4e-1b7c2a0d3e01",
			"policyId": "unused-import",
			"findingId": "` + findingID + `",
			"title": "Unused import\u001b[31m os",
			"path": "src/main\u0007.go",
			"startLine": 12,
			"severity": "warning",
			"ignoredAt": "2026-11-03T11:31:00+01:00",
			"ignoredBy": {"username": "ryan", "displayName": "Ryan Svihla"}
		},
		{
			"id": "6f1c9a52-0f6e-4f0b-9a4e-1b7c2a0d3e02",
			"policyId": "dead-code",
			"findingId": null,
			"title": null,
			"path": null,
			"startLine": null,
			"severity": "note",
			"ignoredAt": "2026-11-02T09:05:00Z",
			"ignoredBy": null
		},
		{
			"id": "6f1c9a52-0f6e-4f0b-9a4e-1b7c2a0d3e03",
			"policyId": "long-function",
			"findingId": "1b4e28ba-2fa1-11d2-883f-0016d3cca427",
			"title": "Function is too long",
			"path": "lib/util.go",
			"startLine": null,
			"severity": "unrated",
			"ignoredAt": "2026-11-01T08:00:00Z",
			"ignoredBy": {"username": "dave", "displayName": ""}
		}
	]
}`

// ignoredServer answers every request with status and body, and records the
// last request.
func ignoredServer(t *testing.T, status int, body string, last **http.Request) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*last = r
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestIgnoredListsTheRepositorysIgnores(t *testing.T) {
	isolateUserConfig(t)
	t.Setenv("SLOPCOP_TOKEN", "secret")
	repo := newRepo(t, "git@github.com:BrokkAi/slopcop.git")
	var request *http.Request
	server := ignoredServer(t, http.StatusOK, ignoredPage, &request)

	code, stdout, stderr := runCLI(t, "ignored", "--root", repo, "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if request.Method != http.MethodGet || request.URL.Path != "/v1/ignores" {
		t.Errorf("request = %s %s", request.Method, request.URL.Path)
	}
	want := url.Values{"repoUrl": {"https://github.com/BrokkAi/slopcop"}, "page": {"0"}}
	if got := request.URL.Query(); got.Encode() != want.Encode() {
		t.Errorf("query = %v, want %v", got, want)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer secret" {
		t.Errorf("authorization = %q", got)
	}
	if got := request.Header.Get("SlopCop-Workspace"); got != "" {
		t.Errorf("workspace = %q", got)
	}
	wantOut := "slopcop: 3 ignored findings in BrokkAi/slopcop (page 1 of 1)\n" +
		"  " + findingID + " [warning] src/main�.go:12 unused-import: Unused import�[31m os (ignored 2026-11-03 10:31 UTC by Ryan Svihla (@ryan))\n" +
		"  (finding no longer recorded) [note] dead-code (ignored 2026-11-02 09:05 UTC by an unrecorded account)\n" +
		"  1b4e28ba-2fa1-11d2-883f-0016d3cca427 [unrated] lib/util.go long-function: Function is too long (ignored 2026-11-01 08:00 UTC by @dave)\n" +
		"slopcop: undo one with `slopcop unignore <id>`\n"
	if stdout != wantOut {
		t.Errorf("stdout =\n%s\nwant\n%s", stdout, wantOut)
	}
}

func TestIgnoredAsksForThePageNamed(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	var request *http.Request
	server := ignoredServer(t, http.StatusOK, `{
		"repository": {"fullName": "BrokkAi/slopcop"},
		"total": 60, "page": 1, "pageSize": 25,
		"ignores": [{
			"id": "6f1c9a52-0f6e-4f0b-9a4e-1b7c2a0d3e01", "policyId": "p", "findingId": "`+findingID+`",
			"title": "t", "path": "a.go", "startLine": 1, "severity": "error",
			"ignoredAt": "2026-11-03T10:31:00Z", "ignoredBy": null
		}]
	}`, &request)

	code, stdout, stderr := runCLI(t, "ignored", "--page", "2", "--root", repo, "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if got := request.URL.Query().Get("page"); got != "1" {
		t.Errorf("page = %q, want the zero-based 1", got)
	}
	for _, want := range []string{
		"slopcop: 60 ignored findings in BrokkAi/slopcop (page 2 of 3)\n",
		"slopcop: see the next page with `slopcop ignored --page 3`\n",
		"slopcop: undo one with `slopcop unignore <id>`\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	}
}

func TestIgnoredSaysTheLastPageHasNoNext(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	var request *http.Request
	server := ignoredServer(t, http.StatusOK, `{
		"repository": {"fullName": "BrokkAi/slopcop"},
		"total": 26, "page": 1, "pageSize": 25,
		"ignores": [{
			"id": "6f1c9a52-0f6e-4f0b-9a4e-1b7c2a0d3e01", "policyId": "p", "findingId": null,
			"title": null, "path": null, "startLine": null, "severity": "error",
			"ignoredAt": "2026-11-03T10:31:00Z", "ignoredBy": null
		}]
	}`, &request)

	code, stdout, _ := runCLI(t, "ignored", "--page", "2", "--root", repo, "--service", server.URL)
	if code != 0 || !strings.Contains(stdout, "(page 2 of 2)") || strings.Contains(stdout, "next page") {
		t.Errorf("exit = %d, stdout = %q", code, stdout)
	}
	// No listed finding has an id to undo.
	if strings.Contains(stdout, "slopcop unignore") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestIgnoredSaysWhenNothingIsIgnored(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	var request *http.Request
	server := ignoredServer(t, http.StatusOK,
		`{"repository": {"fullName": "BrokkAi/slopcop"}, "total": 0, "page": 0, "pageSize": 25, "ignores": []}`, &request)

	code, stdout, stderr := runCLI(t, "ignored", "--root", repo, "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if want := "slopcop: no ignored findings in BrokkAi/slopcop\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestIgnoredSaysWhenThePageIsPastTheEnd(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	var request *http.Request
	server := ignoredServer(t, http.StatusOK,
		`{"repository": {"fullName": "BrokkAi/slopcop"}, "total": 3, "page": 4, "pageSize": 25, "ignores": []}`, &request)

	code, stdout, _ := runCLI(t, "ignored", "--page", "5", "--root", repo, "--service", server.URL)
	if want := "slopcop: no ignored findings on page 5 of 1 in BrokkAi/slopcop\n"; code != 0 || stdout != want {
		t.Errorf("exit = %d, stdout = %q, want %q", code, stdout, want)
	}
}

func TestIgnoredWritesJSON(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	var request *http.Request
	server := ignoredServer(t, http.StatusOK, ignoredPage, &request)

	code, stdout, stderr := runCLI(t, "ignored", "--format", "json", "--root", repo, "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	var decoded struct {
		Repository struct {
			FullName string `json:"fullName"`
		} `json:"repository"`
		Total   int              `json:"total"`
		Ignores []map[string]any `json:"ignores"`
	}
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	if decoded.Repository.FullName != "BrokkAi/slopcop" || decoded.Total != 3 || len(decoded.Ignores) != 3 {
		t.Errorf("decoded = %+v", decoded)
	}
	gone := decoded.Ignores[1]
	if value, ok := gone["findingId"]; !ok || value != nil {
		t.Errorf("gone finding = %v, want a null findingId", gone)
	}
	if decoded.Ignores[0]["findingId"] != findingID {
		t.Errorf("first = %v", decoded.Ignores[0])
	}
}

func TestIgnoredRunsInTheWorkspaceNamed(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	var request *http.Request
	server := ignoredServer(t, http.StatusOK, ignoredPage, &request)

	code, _, stderr := runCLI(t, "ignored", "-w", teamID, "--root", repo, "--service", server.URL)
	if got := request.Header.Get("SlopCop-Workspace"); code != 0 || got != teamID {
		t.Errorf("exit = %d, workspace = %q, stderr = %s", code, got, stderr)
	}
}

func TestIgnoredPrintsTheServiceRemedy(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	var request *http.Request
	server := ignoredServer(t, http.StatusNotFound, `{
		"code": "workspace_repository_not_found",
		"message": "This workspace has no such repository.",
		"remedy": "Run a check of it first.",
		"retryable": false
	}`, &request)

	code, stdout, stderr := runCLI(t, "ignored", "--root", repo, "--service", server.URL)
	if code != 2 || stdout != "" {
		t.Fatalf("exit = %d, stdout = %q", code, stdout)
	}
	if want := "This workspace has no such repository.\nRun a check of it first.\nList them with `slopcop repo list`.\n"; stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
}

func TestIgnoredAddsTheLoginHint(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	var request *http.Request
	server := ignoredServer(t, http.StatusUnauthorized,
		`{"code": "auth_required", "message": "This needs a SlopCop login.", "remedy": "Sign in, then retry.", "retryable": false}`, &request)

	code, _, stderr := runCLI(t, "ignored", "--root", repo, "--service", server.URL)
	if code != 2 || !strings.Contains(stderr, "Sign in with `slopcop login`.") {
		t.Errorf("exit = %d, stderr = %q", code, stderr)
	}
}

func TestIgnoredRefusesBadInputLocally(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	noOrigin := newRepo(t, "")
	var request *http.Request
	server := ignoredServer(t, http.StatusOK, ignoredPage, &request)

	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"--page", "0", "--root", repo}, "--page"},
		{[]string{"--page", "10002", "--root", repo}, "--page"},
		{[]string{"--format", "sarif", "--root", repo}, "--format"},
		{[]string{"--root", noOrigin}, "no origin remote"},
		{[]string{"extra", "--root", repo}, "unknown command"},
	} {
		args := append([]string{"ignored", "--service", server.URL}, test.args...)
		code, stdout, stderr := runCLI(t, args...)
		if code != 2 || stdout != "" || !strings.Contains(stderr, test.want) {
			t.Errorf("%v: exit = %d, stdout = %q, stderr = %q", test.args, code, stdout, stderr)
		}
	}
	if request != nil {
		t.Errorf("a refused command reached the service: %s", request.URL)
	}
}
