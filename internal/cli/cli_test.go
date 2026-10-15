package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/config"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newRepo(t *testing.T, origin string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main", ".")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	if origin != "" {
		runGit(t, dir, "remote", "add", "origin", origin)
	}
	writeFile(t, dir, "README.md", "hello\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-qm", "base")
	return dir
}

func isolateUserConfig(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("SLOPCOP_TOKEN", "")
	t.Setenv("SLOPCOP_SERVICE", "")
	t.Setenv("CODEX_HOME", "")
}

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := execute(context.Background(), args, "test", &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestCleanTreeStillAsksTheService(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	wantBase := runGit(t, repo, "rev-parse", "HEAD")

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/check" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": "clean",
			"baseCommit": "` + wantBase + `",
			"applied": true,
			"summary": {"files": 0, "findings": 0, "incompletePolicies": 0, "failOn": "finding"},
			"findings": [],
			"incomplete": []
		}`))
	}))
	defer server.Close()

	code, stdout, stderr := runCLI(t,
		"check", "--root", repo, "--service", server.URL, "--format", "json",
	)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if captured == nil {
		t.Fatal("service was not called for a clean tree")
	}
	if captured["change"] != "" {
		t.Errorf("change = %v, want an empty patch", captured["change"])
	}
	if captured["baseCommit"] != wantBase {
		t.Errorf("baseCommit = %v, want %s", captured["baseCommit"], wantBase)
	}
	var response struct {
		Status     string `json:"status"`
		BaseCommit string `json:"baseCommit"`
	}
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		t.Fatalf("stdout is not json: %v\n%s", err, stdout)
	}
	if response.Status != "clean" || response.BaseCommit != wantBase {
		t.Fatalf("response = %+v", response)
	}
}

func TestCleanTreeServiceFailureIsNotACleanReport(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{
			"code": "service_unavailable",
			"message": "The check service is unavailable.",
			"remedy": "Try again shortly.",
			"retryable": true
		}`))
	}))
	defer server.Close()

	code, stdout, stderr := runCLI(t,
		"check", "--root", repo, "--service", server.URL,
	)
	if code != 2 {
		t.Fatalf("exit = %d", code)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want no report", stdout)
	}
	if strings.Contains(stdout, "0 findings") {
		t.Fatalf("a failed check printed a clean report: %q", stdout)
	}
	if !strings.Contains(stderr, "The check service is unavailable.") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestCheckSendsTheChangeAndAppliesFailOn(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "git@github.com:BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	writeFile(t, repo, ".slopcop/policies/local.rqlp", "(policy :id \"local\")\n")
	wantBase := runGit(t, repo, "rev-parse", "HEAD")

	var captured []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/check" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		captured = append(captured, body)
		// The service gates on failOn: a warning fails only below error.
		status := "findings"
		if body["failOn"] == "error" {
			status = "clean"
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": "` + status + `",
			"baseCommit": "` + wantBase + `",
			"applied": true,
			"summary": {"files": 2, "findings": 1, "incompletePolicies": 0, "failOn": "` + body["failOn"].(string) + `"},
			"findings": [{"policyId": "local", "severity": "warning", "certainty": "definite", "path": "demo.go", "startLine": 1, "endLine": 1, "title": "example"}],
			"incomplete": []
		}`))
	}))
	defer server.Close()

	code, stdout, stderr := runCLI(t,
		"check", "--root", repo, "--service", server.URL,
		"--format", "json", "--fail-on", "warning",
	)
	if code != 1 {
		t.Fatalf("exit = %d, stderr = %s, stdout = %s", code, stderr, stdout)
	}
	var response struct {
		Status   string `json:"status"`
		Findings []any  `json:"findings"`
	}
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		t.Fatalf("stdout is not json: %v", err)
	}
	if response.Status != "findings" || len(response.Findings) != 1 {
		t.Fatalf("response = %+v", response)
	}
	if len(captured) != 1 {
		t.Fatalf("requests = %d", len(captured))
	}
	request := captured[0]
	if request["repoUrl"] != "https://github.com/BrokkAi/slopcop" {
		t.Errorf("repoUrl = %v", request["repoUrl"])
	}
	if request["baseCommit"] != wantBase {
		t.Errorf("baseCommit = %v, want %s", request["baseCommit"], wantBase)
	}
	patch, err := base64.StdEncoding.DecodeString(request["change"].(string))
	if err != nil {
		t.Fatalf("change is not base64: %v", err)
	}
	if !strings.HasPrefix(string(patch), "diff --git ") {
		t.Fatalf("change is not a git patch:\n%s", patch)
	}
	for _, key := range []string{"format", "headCommit", "branch"} {
		if _, ok := request[key]; ok {
			t.Errorf("request carries %q, which the service rejects", key)
		}
	}
	selection, _ := request["selection"].(map[string]any)
	files, _ := selection["files"].([]any)
	if len(files) != 1 || files[0] != ".slopcop/policies/local.rqlp" {
		t.Fatalf("selection = %v", request["selection"])
	}

	code, _, stderr = runCLI(t,
		"check", "--root", repo, "--service", server.URL,
		"--format", "json", "--fail-on", "error",
	)
	if code != 0 {
		t.Fatalf("exit with fail-on error = %d, stderr = %s", code, stderr)
	}
}

func TestCheckPrintsTheServiceRemedy(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{
			"code": "auth_required",
			"message": "This check needs a SlopCop login.",
			"remedy": "Sign in, then retry.",
			"retryable": false
		}`))
	}))
	defer server.Close()

	code, stdout, stderr := runCLI(t,
		"check", "--root", repo, "--service", server.URL,
	)
	if code != 2 {
		t.Fatalf("exit = %d", code)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q", stdout)
	}
	for _, want := range []string{
		"This check needs a SlopCop login.",
		"Sign in, then retry.",
		"Sign in with `slopcop login`.",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	}
}

// A repo-state failure must not carry the login hint: the user is signed in,
// and the service's remedy already names the real next step.
func TestCheckPrintsRepoStateRemediesWithoutLoginHint(t *testing.T) {
	isolateUserConfig(t)
	for _, test := range []struct {
		name    string
		status  int
		code    string
		message string
		remedy  string
	}{
		{
			name:    "repo not indexed",
			status:  http.StatusConflict,
			code:    "repo_not_indexed",
			message: "SlopCop has not indexed this repository for checks.",
			remedy:  "Analyze the repository on SlopCop first. For a private repository, install the SlopCop GitHub App, then sign in again.",
		},
		{
			name:    "no access",
			status:  http.StatusForbidden,
			code:    "no_access",
			message: "Your account does not have access to this repository.",
			remedy:  "Ask a repository administrator for access, then sign in again.",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
			writeFile(t, repo, "demo.go", "package demo\n")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code":      test.code,
					"message":   test.message,
					"remedy":    test.remedy,
					"retryable": false,
				})
			}))
			defer server.Close()

			code, stdout, stderr := runCLI(t,
				"check", "--root", repo, "--service", server.URL,
			)
			if code != 2 {
				t.Fatalf("exit = %d", code)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q", stdout)
			}
			for _, want := range []string{test.message, test.remedy} {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr = %q, want %q", stderr, want)
				}
			}
			if strings.Contains(stderr, "slopcop login") {
				t.Errorf("stderr = %q, must not tell a signed-in user to log in again", stderr)
			}
		})
	}
}

// While SlopCop is still indexing the repository the service answers at
// once; the CLI prints that and exits instead of waiting or asking again.
func TestIndexingInProgressExitsWithoutWaiting(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{
			"code": "indexing_in_progress",
			"message": "Indexing in progress: SlopCop is still indexing this repository.",
			"remedy": "Run the check again once SlopCop has finished indexing the repository.",
			"retryable": false
		}`))
	}))
	defer server.Close()

	code, stdout, stderr := runCLI(t,
		"check", "--root", repo, "--service", server.URL,
	)
	if code != 2 {
		t.Fatalf("exit = %d", code)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want no report", stdout)
	}
	for _, want := range []string{
		"Indexing in progress: SlopCop is still indexing this repository.",
		"Run the check again once SlopCop has finished indexing the repository.",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	}
	if strings.Contains(stderr, "slopcop login") {
		t.Errorf("stderr = %q, must not tell a signed-in user to log in again", stderr)
	}
}

func TestLoginNamesTheTokenLoginWhenBrowserLoginIsOff(t *testing.T) {
	isolateUserConfig(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{
			"code": "device_flow_disabled",
			"message": "This service does not support browser login.",
			"remedy": "Use a token login instead.",
			"retryable": false
		}`))
	}))
	defer server.Close()

	code, _, stderr := runCLI(t, "login", "--service", server.URL)
	if code != 2 {
		t.Fatalf("exit = %d", code)
	}
	for _, want := range []string{
		"This service does not support browser login.",
		"Use a token login instead.",
		"Sign in with `slopcop login --token <token>`.",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	}
}

func TestIncompleteCheckExitsTwo(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": "incomplete",
			"baseCommit": "0123456789abcdef0123456789abcdef01234567",
			"applied": true,
			"summary": {"files": 1, "findings": 0, "incompletePolicies": 1, "incompletePolicyIds": [], "omittedFindings": 0, "failOn": "finding"},
			"findings": [],
			"incomplete": [],
			"failure": "The analysis could not be completed."
		}`))
	}))
	defer server.Close()

	code, stdout, stderr := runCLI(t,
		"check", "--root", repo, "--service", server.URL,
	)
	if code != 2 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if stdout != "0 findings | Analysis did not complete: The analysis could not be completed.\n" {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestServiceURLPrecedence(t *testing.T) {
	isolateUserConfig(t)
	stored := config.Config{ServiceURL: "https://stored.example"}
	if got := resolveServiceURL(stored, "https://flag.example"); got != "https://flag.example" {
		t.Errorf("flag URL = %q", got)
	}
	t.Setenv("SLOPCOP_SERVICE", "https://env.example")
	if got := resolveServiceURL(stored, ""); got != "https://env.example" {
		t.Errorf("env URL = %q", got)
	}
	t.Setenv("SLOPCOP_SERVICE", "")
	if got := resolveServiceURL(stored, ""); got != "https://stored.example" {
		t.Errorf("stored URL = %q", got)
	}
	if got := resolveServiceURL(config.Config{}, ""); got != config.DefaultServiceURL {
		t.Errorf("default URL = %q", got)
	}
}

func TestUnknownFormatFailsBeforeGitWork(t *testing.T) {
	isolateUserConfig(t)
	code, _, stderr := runCLI(t, "check", "--root", t.TempDir(), "--format", "xml")
	if code != 2 || !strings.Contains(stderr, "unknown format") {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
}

func TestTokenLoginAndLogout(t *testing.T) {
	isolateUserConfig(t)
	code, stdout, stderr := runCLI(t,
		"login", "--service", "http://127.0.0.1:9999", "--token", "secret",
	)
	if code != 0 {
		t.Fatalf("login exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "logged in") {
		t.Fatalf("login stdout = %q", stdout)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Token != "secret" || cfg.ServiceURL != "http://127.0.0.1:9999" {
		t.Fatalf("config = %+v", cfg)
	}

	code, stdout, stderr = runCLI(t, "logout")
	if code != 0 {
		t.Fatalf("logout exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "logged out") {
		t.Fatalf("logout stdout = %q", stdout)
	}
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Token != "" {
		t.Fatalf("token was not cleared: %+v", cfg)
	}
}

func TestBrowserLoginStoresTheToken(t *testing.T) {
	isolateUserConfig(t)
	var deviceHits, tokenHits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/device":
			deviceHits++
			_, _ = w.Write([]byte(`{
				"deviceCode": "device-1",
				"userCode": "WXYZ-1234",
				"verificationUri": "https://example.test/activate",
				"expiresIn": 600,
				"interval": 1
			}`))
		case "/api/token":
			tokenHits++
			_, _ = w.Write([]byte(`{"status": "authorized", "token": "browser-token"}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	code, stdout, stderr := runCLI(t, "login", "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	for _, want := range []string{"https://example.test/activate", "WXYZ-1234"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	}
	if !strings.Contains(stdout, "logged in") {
		t.Fatalf("stdout = %q", stdout)
	}
	if deviceHits != 1 || tokenHits != 1 {
		t.Fatalf("device hits = %d, token hits = %d", deviceHits, tokenHits)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Token != "browser-token" || cfg.ServiceURL != server.URL {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestExitCodeFollowsTheServiceStatus(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	// An unrated finding gates only on fail-on finding; the service decides.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": "clean",
			"baseCommit": "0123456789abcdef0123456789abcdef01234567",
			"applied": true,
			"summary": {"files": 1, "findings": 1, "incompletePolicies": 0, "failOn": "warning"},
			"findings": [{"policyId": "p", "severity": "unrated", "certainty": "definite", "path": "demo.go", "startLine": 1, "endLine": 1, "title": "t"}],
			"incomplete": []
		}`))
	}))
	defer server.Close()

	code, stdout, stderr := runCLI(t,
		"check", "--root", repo, "--service", server.URL, "--fail-on", "warning",
	)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "1 finding across 1 file(s) | Analysis complete") {
		t.Fatalf("stdout = %q", stdout)
	}
}
