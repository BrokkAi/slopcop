package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
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
	// os.UserConfigDir reads this on Windows.
	t.Setenv("AppData", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("SLOPCOP_TOKEN", "")
	t.Setenv("SLOPCOP_SERVICE", "")
	t.Setenv("CODEX_HOME", "")
	// Fail rather than let a test touch the real credential or skill install
	// if either location ever depends on something not isolated above.
	configPath, err := config.Path()
	requireUnder(t, home, configPath, err)
	userHome, err := os.UserHomeDir()
	requireUnder(t, home, userHome, err)
}

func requireUnder(t *testing.T, root, path string, err error) {
	t.Helper()
	if err == nil {
		var rel string
		rel, err = filepath.Rel(root, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return
		}
	}
	t.Fatalf("%q (%v) escapes the isolated home %q", path, err, root)
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
			message: "SlopCop cannot check this repository with this login.",
			remedy:  "If the repository is private, install the SlopCop GitHub App on its owner with access to it, then retry; if the App is already installed there, run slopcop login again so this login can use that installation. If the remote URL is wrong, fix it first.",
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
			// The service's remedy alone: the CLI adds no sign-in command of
			// its own, though repo_not_indexed's remedy names one.
			if want := test.message + "\n" + test.remedy + "\n"; stderr != want {
				t.Errorf("stderr = %q, want %q", stderr, want)
			}
			if strings.Contains(stderr, "Sign in with `slopcop login`") {
				t.Errorf("stderr = %q, must not tell a signed-in user to log in again", stderr)
			}
		})
	}
}

// A service from before checks stopped waiting on indexing answers
// indexing_in_progress at once; the CLI prints that and exits instead of
// waiting or asking again.
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
	if stdout != "No new findings | Analysis did not complete: The analysis could not be completed.\n" {
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
	// A service from before new findings were marked gates on every finding,
	// so each one is listed as new.
	if !strings.Contains(stdout, "1 new finding across 1 file(s) | Analysis complete") {
		t.Fatalf("stdout = %q", stdout)
	}
}

// checkRunning answers the service's check_running, as control does while the
// analysis is still going.
func checkRunning(w http.ResponseWriter, retryAfter string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", retryAfter)
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`{
		"code": "check_running",
		"message": "The check is still running.",
		"remedy": "Run the same check again to wait for its result.",
		"retryable": true
	}`))
}

const onlyPreExisting = `{
	"status": "clean",
	"baseCommit": "0123456789abcdef0123456789abcdef01234567",
	"applied": true,
	"summary": {"files": 1, "findings": 2, "newFindings": 0, "existingFindings": 2, "incompletePolicies": 0, "failOn": "finding"},
	"findings": [
		{"policyId": "p", "severity": "error", "certainty": "definite", "path": "old.go", "startLine": 3, "endLine": 3, "title": "t", "new": false},
		{"policyId": "q", "severity": "warning", "certainty": "definite", "path": "old.go", "startLine": 9, "endLine": 9, "title": "u", "new": false}
	],
	"incomplete": []
}`

func TestACheckWithNoNewFindingsExitsClean(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(onlyPreExisting))
	}))
	defer server.Close()

	code, stdout, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if stdout != "No new findings; 2 already in the code (--show-existing lists them) | Analysis complete\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	code, stdout, _ = runCLI(t, "check", "--root", repo, "--service", server.URL, "--show-existing")
	if code != 0 || !strings.Contains(stdout, "Already in the code (never fail the check)\n  [error] p: t") {
		t.Fatalf("exit = %d, stdout = %q", code, stdout)
	}
}

func TestCheckWaitsWhileTheServiceIsStillRunning(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		if len(bodies) < 3 {
			checkRunning(w, "0")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(onlyPreExisting))
	}))
	defer server.Close()

	code, stdout, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL, "--format", "json")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if len(bodies) != 3 || bodies[0] != bodies[1] || bodies[1] != bodies[2] {
		t.Fatalf("the same check is asked again until it answers: %d requests", len(bodies))
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(stdout), &report); err != nil || report["status"] != "clean" {
		t.Fatalf("stdout = %q (%v)", stdout, err)
	}
}

func TestACheckStillRunningAtTheTimeoutIsPending(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		checkRunning(w, "5")
	}))
	defer server.Close()

	for _, format := range []string{"human", "json", "sarif"} {
		code, stdout, stderr := runCLI(t,
			"check", "--root", repo, "--service", server.URL, "--format", format, "--timeout", "2s",
		)
		if code != 3 {
			t.Fatalf("%s: exit = %d, want 3 (pending), stderr = %s", format, code, stderr)
		}
		if !strings.Contains(stderr, "The check is still running.\nRun the same check again to wait for its result.") {
			t.Errorf("%s: stderr = %q", format, stderr)
		}
		switch format {
		case "human":
			if stdout != "" {
				t.Errorf("human stdout = %q", stdout)
			}
		case "json":
			var report map[string]any
			if err := json.Unmarshal([]byte(stdout), &report); err != nil {
				t.Fatalf("json stdout = %q: %v", stdout, err)
			}
			if report["status"] != "pending" || report["retryAfter"] != float64(5) {
				t.Errorf("json = %v", report)
			}
		case "sarif":
			var log map[string]any
			if err := json.Unmarshal([]byte(stdout), &log); err != nil {
				t.Fatalf("sarif stdout = %q: %v", stdout, err)
			}
			properties := log["runs"].([]any)[0].(map[string]any)["properties"].(map[string]any)
			if properties["slopcop.status"] != "pending" {
				t.Errorf("sarif properties = %v", properties)
			}
		}
	}
}

func TestFormatAndListingAreNotPartOfTheRequest(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(onlyPreExisting))
	}))
	defer server.Close()

	for _, args := range [][]string{
		{"--format", "human"},
		{"--format", "json"},
		{"--format", "sarif", "--show-existing"},
	} {
		runCLI(t, append([]string{"check", "--root", repo, "--service", server.URL}, args...)...)
	}
	if len(bodies) != 3 || bodies[0] != bodies[1] || bodies[1] != bodies[2] {
		t.Fatalf("each format sends the same check, so the service reuses it: %q", bodies)
	}
}

// checkAnswer is a check response from a service that names checks and
// findings.
func checkAnswer(status string, summary string, findings ...string) string {
	return `{
		"status": "` + status + `",
		"checkId": "2b3c4d5e-6f70-4812-9a3b-4c5d6e7f8091",
		"baseCommit": "0123456789abcdef0123456789abcdef01234567",
		"applied": true,
		"summary": ` + summary + `,
		"findings": [` + strings.Join(findings, ",") + `],
		"incomplete": []
	}`
}

const (
	newFinding       = `{"id": "6f1c2a9e-3b4d-4e5f-8a7b-0c1d2e3f4a5b", "policyId": "p", "severity": "warning", "certainty": "definite", "path": "demo.go", "startLine": 1, "endLine": 1, "title": "t", "new": true, "dismissed": false}`
	dismissedFinding = `{"id": "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d", "policyId": "q", "severity": "error", "certainty": "definite", "path": "demo.go", "startLine": 3, "endLine": 3, "title": "u", "new": false, "dismissed": true}`
)

func TestCheckExitCodesWithFindingIDsAndDismissals(t *testing.T) {
	cases := []struct {
		name   string
		answer string
		args   []string
		exit   int
		want   string
	}{
		{
			name: "a new finding fails and says how to report it",
			answer: checkAnswer("findings",
				`{"files": 1, "findings": 1, "newFindings": 1, "existingFindings": 0, "dismissedFindings": 0, "incompletePolicies": 0, "failOn": "finding"}`,
				newFinding),
			exit: 1,
			want: "New findings\n" +
				"  [warning] p: t\n" +
				"    Location: demo.go:1\n" +
				"    Certainty: definite\n" +
				"    Id: 6f1c2a9e-3b4d-4e5f-8a7b-0c1d2e3f4a5b\n" +
				"\n" +
				"False positive? slopcop report <id> --reason \"why\"\n" +
				"\n" +
				"1 new finding across 1 file(s) | Analysis complete\n",
		},
		{
			name: "a dismissed finding never fails the check",
			answer: checkAnswer("clean",
				`{"files": 1, "findings": 1, "newFindings": 0, "existingFindings": 0, "dismissedFindings": 1, "incompletePolicies": 0, "failOn": "finding"}`,
				dismissedFinding),
			exit: 0,
			want: "No new findings; 1 reported as false positive (--show-existing lists them) | Analysis complete\n",
		},
		{
			name: "a dismissed finding is listed with --show-existing",
			answer: checkAnswer("clean",
				`{"files": 1, "findings": 1, "newFindings": 0, "existingFindings": 0, "dismissedFindings": 1, "incompletePolicies": 0, "failOn": "finding"}`,
				dismissedFinding),
			args: []string{"--show-existing"},
			exit: 0,
			want: "Reported as false positive (never fail the check)\n" +
				"  [error] q: u\n" +
				"    Location: demo.go:3\n" +
				"    Certainty: definite\n" +
				"    Id: 0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d\n" +
				"\n" +
				"No new findings; 1 reported as false positive | Analysis complete\n",
		},
		{
			name: "a new finding next to a dismissed one still fails",
			answer: checkAnswer("findings",
				`{"files": 1, "findings": 2, "newFindings": 1, "existingFindings": 0, "dismissedFindings": 1, "incompletePolicies": 0, "failOn": "finding"}`,
				newFinding, dismissedFinding),
			args: []string{"--format", "json"},
			exit: 1,
			want: `"checkId": "2b3c4d5e-6f70-4812-9a3b-4c5d6e7f8091"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateUserConfig(t)
			repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
			writeFile(t, repo, "demo.go", "package demo\n")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.answer))
			}))
			defer server.Close()

			code, stdout, stderr := runCLI(t, append([]string{"check", "--root", repo, "--service", server.URL}, tc.args...)...)
			if code != tc.exit {
				t.Fatalf("exit = %d, want %d, stderr = %s", code, tc.exit, stderr)
			}
			if strings.HasPrefix(tc.want, `"`) {
				if !strings.Contains(stdout, tc.want) {
					t.Errorf("stdout = %q, want %q", stdout, tc.want)
				}
				return
			}
			if stdout != tc.want {
				t.Errorf("stdout = %q, want %q", stdout, tc.want)
			}
		})
	}
}
