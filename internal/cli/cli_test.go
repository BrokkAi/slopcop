package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/gitx"
	"github.com/BrokkAi/slopcop/internal/review/reviewtest"
	"github.com/BrokkAi/slopcop/internal/service"
	"github.com/BrokkAi/slopcop/internal/update"
)

// TestMain makes a check in the tests that do not exercise the repository
// gate see an active repository, as the service would for a repository the
// workspace already checks. Tests about the gate stand the lookup in
// themselves.
func TestMain(m *testing.M) {
	reviewtest.Main()
	checkRepository = func(context.Context, *service.Client, string) (statusRepository, error) {
		return statusRepository{State: statusActive, Enabled: true, Active: true}, nil
	}
	os.Exit(m.Run())
}

// realRepositoryCheck makes a check use the real repository gate, for tests
// about whether a check is sent.
func realRepositoryCheck(t *testing.T) {
	t.Helper()
	original := checkRepository
	checkRepository = lookupRepositoryStatus
	t.Cleanup(func() { checkRepository = original })
}

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
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	// os.UserConfigDir and os.UserCacheDir read these on Windows.
	t.Setenv("AppData", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LocalAppData", filepath.Join(home, "AppData", "Local"))
	t.Setenv("SLOPCOP_TOKEN", "")
	t.Setenv("SLOPCOP_SERVICE", "")
	t.Setenv("SLOPCOP_WORKSPACE", "")
	t.Setenv("CODEX_HOME", "")
	// A test run inside an agent must not attribute its checks to that
	// agent's session.
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("AI_AGENT", "")
	t.Setenv("CODEX_THREAD_ID", "")
	// Fail rather than let a test touch the real credential or skill install
	// if either location ever depends on something not isolated above.
	configPath, err := config.Path()
	requireUnder(t, home, configPath, err)
	if err == nil && !strings.HasPrefix(t.Name(), "TestConfig") {
		if e := os.MkdirAll(filepath.Dir(configPath), 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(configPath, []byte(`{"mode":"static"}`), 0600); e != nil {
			t.Fatal(e)
		}
	}
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
		Status      string `json:"status"`
		BaseCommit  string `json:"baseCommit"`
		EmptyChange bool   `json:"emptyChange"`
	}
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		t.Fatalf("stdout is not json: %v\n%s", err, stdout)
	}
	if response.Status != "clean" || response.BaseCommit != wantBase || !response.EmptyChange {
		t.Fatalf("response = %+v", response)
	}
	if !strings.Contains(stderr, "no change was checked") || !strings.Contains(stderr, "--base") {
		t.Errorf("stderr does not say the patch was empty: %q", stderr)
	}
}

func TestVerboseNamesInconclusiveChecks(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	base := runGit(t, repo, "rev-parse", "HEAD")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": "clean",
			"baseCommit": "` + base + `",
			"applied": true,
			"summary": {"files": 1, "findings": 0, "incompletePolicies": 0,
				"inconclusive": 2, "inconclusiveElsewhere": 1, "failOn": "finding"},
			"findings": [],
			"incomplete": [],
			"inconclusive": [{"policyId": "acme.a", "reason": "inconclusive"},
				{"policyId": "acme.b", "reason": "inconclusive"}],
			"inconclusiveElsewhere": [{"policyId": "acme.b", "reason": "inconclusive"}]
		}`))
	}))
	defer server.Close()

	code, stdout, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "Inconclusive: 2 check(s), 1 only about files the change does not write") ||
		strings.Contains(stdout, "acme.a") {
		t.Errorf("stdout = %q", stdout)
	}
	code, stdout, stderr = runCLI(t, "check", "--root", repo, "--service", server.URL, "--verbose")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "  acme.a\n  acme.b (only files the change does not write)\n") {
		t.Errorf("verbose stdout = %q", stdout)
	}
}

func TestPushedBranchFallsBackToTheDefaultBranch(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	mainCommit := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "update-ref", "refs/remotes/origin/main", mainCommit)
	runGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	runGit(t, repo, "checkout", "-qb", "feature")
	writeFile(t, repo, "feature.txt", "pushed work\n")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-qm", "feature")
	// The feature branch is pushed: its upstream already has HEAD.
	runGit(t, repo, "update-ref", "refs/remotes/origin/feature", "HEAD")
	runGit(t, repo, "branch", "--set-upstream-to=origin/feature")

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": "clean",
			"baseCommit": "` + mainCommit + `",
			"applied": true,
			"summary": {"files": 1, "findings": 0, "incompletePolicies": 0, "failOn": "finding"},
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
	if captured["baseCommit"] != mainCommit {
		t.Errorf("baseCommit = %v, want the origin/HEAD merge base %s", captured["baseCommit"], mainCommit)
	}
	patch, err := base64.StdEncoding.DecodeString(captured["change"].(string))
	if err != nil {
		t.Fatalf("decode change: %v", err)
	}
	if !strings.Contains(string(patch), "b/feature.txt") {
		t.Errorf("patch does not carry the pushed commit:\n%s", patch)
	}
	if strings.Contains(stdout, "emptyChange") || strings.Contains(stderr, "no change was checked") {
		t.Errorf("a non-empty change was reported as empty: stdout %q, stderr %q", stdout, stderr)
	}
}

func TestCheckResendsWithoutAttributionAServiceRejects(t *testing.T) {
	isolateUserConfig(t)
	// An agent session with no transcript still attributes the harness.
	t.Setenv("CLAUDE_CODE_SESSION_ID", "ad904a2f-b013-4c97-8bdb-3ccd08e38b31")
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	base := runGit(t, repo, "rev-parse", "HEAD")
	var attributed []bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, ok := body["attribution"]
		attributed = append(attributed, ok)
		w.Header().Set("Content-Type", "application/json")
		// A service from before attribution denies unknown fields.
		if ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code": "invalid_change", "message": "The change could not be read.", "remedy": "Send one git patch."}`))
			return
		}
		_, _ = w.Write([]byte(`{"status": "clean", "baseCommit": "` + base + `", "applied": true,
			"summary": {"files": 0, "findings": 0, "incompletePolicies": 0, "failOn": "finding"},
			"findings": [], "incomplete": []}`))
	}))
	defer server.Close()

	code, _, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if !reflect.DeepEqual(attributed, []bool{true, false}) {
		t.Fatalf("requests carried attribution = %v, want [true false]", attributed)
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
	if _, ok := request["attribution"]; ok {
		t.Errorf("a check outside any agent session carries attribution: %v", request["attribution"])
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
	if code != service.ExitNeedsPerson {
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

// A disabled repository names whoami: the workspace that disabled it is the
// login's, which may not be the one the person expects.
func TestCheckOfADisabledRepositoryPointsAtWhoami(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{
			"code": "repository_disabled",
			"message": "This repository is disabled in your SlopCop workspace, so it isn't scanned.",
			"remedy": "Enable it on the Repositories page in SlopCop, then retry.",
			"retryable": false
		}`))
	}))
	defer server.Close()

	code, stdout, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL)
	if code != service.ExitNeedsPerson || stdout != "" {
		t.Fatalf("exit = %d, stdout = %q", code, stdout)
	}
	want := "slopcop: repository_disabled: This repository is disabled in your SlopCop workspace, so it isn't scanned.\n" +
		"Enable it on the Repositories page in SlopCop, then retry.\n" +
		"`slopcop whoami` shows which workspace this login checks in.\n"
	if stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
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
			if code != service.ExitNeedsPerson {
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

// A refusal the service marks not retryable is printed once; the CLI exits
// instead of waiting or asking again.
func TestNonRetryableRefusalExitsWithoutWaiting(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{
			"code": "repo_renamed",
			"message": "This repository was renamed on GitHub.",
			"remedy": "Point the git remote at the repository's new URL (git remote set-url origin <new url>), then retry.",
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
		"This repository was renamed on GitHub.",
		"Point the git remote at the repository's new URL (git remote set-url origin <new url>), then retry.",
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

// --show-existing still parses, so scripts that pass it keep working, but the
// service no longer lists findings outside the change: the flag is hidden and
// says so on stderr, never on stdout, where it would break the JSON report.
func TestShowExistingIsDeprecatedOnStderr(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": "clean",
			"baseCommit": "0123456789abcdef0123456789abcdef01234567",
			"applied": true,
			"summary": {"files": 1, "findings": 0, "newFindings": 0, "existingFindings": 4, "incompletePolicies": 0, "failOn": "finding"},
			"findings": [],
			"incomplete": []
		}`))
	}))
	defer server.Close()

	code, stdout, stderr := runCLI(t,
		"check", "--root", repo, "--service", server.URL, "--format", "json", "--show-existing",
	)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("stdout is not the JSON report: %q: %v", stdout, err)
	}
	if !strings.Contains(stderr, "--show-existing is deprecated") {
		t.Errorf("stderr = %q", stderr)
	}
	if _, help, _ := runCLI(t, "check", "--help"); strings.Contains(help, "show-existing") {
		t.Errorf("--help still lists --show-existing:\n%s", help)
	}
}

// A status this client does not know may mean anything but clean, so it
// fails the check instead of passing it.
func TestAnUnknownStatusNeverPassesTheCheck(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": "abandoned",
			"baseCommit": "0123456789abcdef0123456789abcdef01234567",
			"applied": true,
			"summary": {"files": 1, "findings": 0, "newFindings": 0, "existingFindings": 0, "incompletePolicies": 0, "failOn": "finding"},
			"findings": [],
			"incomplete": []
		}`))
	}))
	defer server.Close()

	code, _, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL)
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stderr = %s", code, stderr)
	}
	// The report reads like a pass, so stderr says why the check failed.
	if !strings.Contains(stderr, `the service answered status "abandoned"`) {
		t.Errorf("stderr = %q", stderr)
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
	if stdout != "No new findings; 2 already in the code | Analysis complete\n" {
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

// An interrupt is not a timeout: nothing was waited for, so it prints no
// pending report and exits 130, the shell's code for Ctrl-C, not 3.
func TestAnInterruptedCheckIsNotPending(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cancel() // the signal arrives while the service is still working
		checkRunning(w, "5")
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := execute(ctx, []string{"check", "--root", repo, "--service", server.URL, "--format", "json"},
		"test", &stdout, &stderr)
	if code != 130 {
		t.Fatalf("exit = %d, want 130, stderr = %s", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want no report", stdout.String())
	}
	if !strings.Contains(stderr.String(), "interrupted before the check answered") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

// The exit code names the signal, the shell's way: SIGTERM, as a CI runner
// sends on cancel, exits 143.
func TestASignalNamesItsExitCode(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cancel(interrupt{syscall.SIGTERM})
		checkRunning(w, "5")
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := execute(ctx, []string{"check", "--root", repo, "--service", server.URL}, "test", &stdout, &stderr)
	if code != 143 {
		t.Fatalf("exit = %d, want 143, stderr = %s", code, stderr.String())
	}
}

// An interrupt before the request goes out says nothing was sent, and sends
// nothing.
func TestAnInterruptBeforeSendingSendsNothing(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		checkRunning(w, "5")
	}))
	defer server.Close()
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(interrupt{os.Interrupt})

	var stdout, stderr bytes.Buffer
	code := execute(ctx, []string{"check", "--root", repo, "--service", server.URL}, "test", &stdout, &stderr)
	if code != 130 || requests != 0 {
		t.Fatalf("exit = %d (want 130), requests = %d (want 0), stderr = %s", code, requests, stderr.String())
	}
	if !strings.Contains(stderr.String(), "interrupted before the check was sent") {
		t.Errorf("stderr = %q", stderr.String())
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

// enableService answers the account routes and a check, and records what a
// call asked for. It starts with the repositories named in the test.
type enableService struct {
	repositories []service.Repository
	lists        int
	checks       []map[string]any
	states       []map[string]any
	workspaces   []string
	base         string
	added        bool
}

func (s *enableService) start(t *testing.T, repo string) *httptest.Server {
	t.Helper()
	s.base = runGit(t, repo, "rev-parse", "HEAD")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/whoami":
			_, _ = w.Write([]byte(whoamiBody))
		case "/v1/repositories":
			s.lists++
			s.workspaces = append(s.workspaces, r.Header.Get("SlopCop-Workspace"))
			_ = json.NewEncoder(w).Encode(service.RepositoryList{Repositories: s.repositories})
		case "/v1/check":
			s.workspaces = append(s.workspaces, r.Header.Get("SlopCop-Workspace"))
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode request: %v", err)
			}
			s.checks = append(s.checks, body)
			if body["addRepository"] == true {
				s.added = true
			}
			_ = json.NewEncoder(w).Encode(service.Response{
				Status:          service.StatusClean,
				BaseCommit:      s.base,
				Applied:         true,
				Findings:        []service.Finding{},
				Incomplete:      []service.IncompletePolicy{},
				RepositoryAdded: s.added,
			})
		case "/v1/repositories/state":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode request: %v", err)
			}
			s.states = append(s.states, body)
			id, _ := body["repositoryId"].(string)
			enabled, _ := body["enabled"].(bool)
			state := service.RepositoryState{RepositoryID: id, Enabled: enabled, Active: enabled}
			for index := range s.repositories {
				if s.repositories[index].ID == id {
					s.repositories[index].Enabled = enabled
					s.repositories[index].Active = enabled
					state.FullName = s.repositories[index].FullName
				}
			}
			_ = json.NewEncoder(w).Encode(state)
		default:
			t.Errorf("path = %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestCheckNeverAddsARepository(t *testing.T) {
	isolateUserConfig(t)
	realRepositoryCheck(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")

	t.Run("a plain check refuses without sending one", func(t *testing.T) {
		svc := &enableService{}
		server := svc.start(t, repo)
		pipedStdin(t, "")
		code, stdout, stderr := runCLI(t, "check", "--root", repo, "--format", "json", "--service", server.URL)
		var document map[string]any
		if err := json.Unmarshal([]byte(stdout), &document); err != nil {
			t.Fatalf("stdout is not JSON: %v (%q), stderr = %q", err, stdout, stderr)
		}
		if code != service.ExitNeedsPerson || document["status"] != "failed" ||
			document["code"] != repositoryNotEnabledCode || document["retryable"] != false {
			t.Errorf("exit = %d, stdout = %v, stderr = %q", code, document, stderr)
		}
		if len(svc.checks) != 0 || svc.lists != 1 {
			t.Errorf("checks = %d, lists = %d, want 0 and 1", len(svc.checks), svc.lists)
		}
	})

	t.Run("--if-enabled skips without sending a check", func(t *testing.T) {
		svc := &enableService{}
		server := svc.start(t, repo)
		pipedStdin(t, "")
		code, stdout, stderr := runCLI(t, "check", "--root", repo, "--if-enabled", "--format", "json", "--service", server.URL)
		var document map[string]any
		if err := json.Unmarshal([]byte(stdout), &document); err != nil {
			t.Fatalf("stdout is not JSON: %v (%q), stderr = %q", err, stdout, stderr)
		}
		if code != 0 || document["status"] != "skipped" || document["code"] != repositoryNotEnabledCode ||
			document["state"] != statusNotAdded || !strings.Contains(stderr, "is not enabled in your SlopCop workspace, so nothing was checked") {
			t.Errorf("exit = %d, stdout = %v, stderr = %q", code, document, stderr)
		}
		if len(svc.checks) != 0 {
			t.Errorf("checks = %v, want none", svc.checks)
		}
	})

	t.Run("--if-enabled checks an active repository", func(t *testing.T) {
		svc := &enableService{repositories: []service.Repository{{
			ID: widgetsID, Provider: "github", FullName: "BrokkAi/slopcop", Enabled: true, Active: true,
		}}}
		server := svc.start(t, repo)
		pipedStdin(t, "")
		code, _, stderr := runCLI(t, "check", "--root", repo, "--if-enabled", "--format", "json", "--service", server.URL)
		if code != 0 || len(svc.checks) != 1 {
			t.Errorf("exit = %d, checks = %d, stderr = %q", code, len(svc.checks), stderr)
		}
	})

	t.Run("--add-repository sends one confirmed check", func(t *testing.T) {
		svc := &enableService{}
		server := svc.start(t, repo)
		pipedStdin(t, "")
		code, _, stderr := runCLI(t, "check", "--root", repo, "--add-repository", "-w", "Acme team", "--service", server.URL)
		if code != 0 {
			t.Fatalf("exit = %d, stderr = %s", code, stderr)
		}
		if strings.Contains(stderr, "[y/N]") || strings.Count(stderr, "slopcop: added BrokkAi/slopcop to \"Acme team\"\n") != 1 {
			t.Errorf("stderr = %q", stderr)
		}
		if len(svc.checks) != 1 || svc.checks[0]["addRepository"] != true {
			t.Errorf("checks = %v, want one confirmed check", svc.checks)
		}
		if len(svc.workspaces) != 1 || svc.workspaces[0] != teamID {
			t.Errorf("workspaces = %v, want the named workspace", svc.workspaces)
		}
	})

	t.Run("--add-repository without --workspace sends nothing", func(t *testing.T) {
		svc := &enableService{}
		server := svc.start(t, repo)
		t.Setenv("SLOPCOP_WORKSPACE", "Acme team")
		code, stdout, stderr := runCLI(t, "check", "--root", repo, "--add-repository", "--format", "json", "--service", server.URL)
		if code != 2 || stdout != "" || !strings.HasPrefix(stderr, "slopcop: --add-repository needs --workspace <name|id>: ") {
			t.Errorf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
		}
		if len(svc.checks) != 0 {
			t.Errorf("checks = %v, want none", svc.checks)
		}
	})
}

func TestEnableAddsARepositoryAfterConfirmation(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	svc := &enableService{}
	server := svc.start(t, repo)
	typed(t, "BrokkAi/slopcop\n")

	code, stdout, stderr := runCLI(t, "enable", "--root", repo, "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	for _, want := range []string{
		"lets that workspace check and bill it",
		"Type BrokkAi/slopcop to add it:",
		"slopcop: added BrokkAi/slopcop to \"Dave\"",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	}
	if !strings.Contains(stdout, "No new findings") {
		t.Errorf("stdout = %q, want the first check's report", stdout)
	}
	if len(svc.checks) != 1 || svc.checks[0]["addRepository"] != true {
		t.Errorf("checks = %v, want one confirmed check", svc.checks)
	}
}

func TestEnableRefusesWithoutATerminal(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	svc := &enableService{}
	server := svc.start(t, repo)
	pipedStdin(t, "BrokkAi/slopcop\n")

	code, _, stderr := runCLI(t, "enable", "--root", repo, "--service", server.URL)
	if code != 2 || !strings.Contains(stderr, "must be confirmed at a terminal") {
		t.Errorf("exit = %d, stderr = %q", code, stderr)
	}
	if len(svc.checks) != 0 || len(svc.states) != 0 || svc.lists != 1 {
		t.Errorf("checks = %d, states = %d, lists = %d, want none but the listing",
			len(svc.checks), len(svc.states), svc.lists)
	}
}

func TestEnableNamesAnAlreadyEnabledRepository(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	svc := &enableService{repositories: []service.Repository{{
		ID: widgetsID, Provider: "github", FullName: "BrokkAi/slopcop", Enabled: true, Active: true,
	}}}
	server := svc.start(t, repo)

	code, stdout, stderr := runCLI(t, "enable", "--root", repo, "--service", server.URL)
	if code != 0 || !strings.Contains(stdout, "slopcop: BrokkAi/slopcop is already enabled in \"Dave\"") {
		t.Errorf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if len(svc.checks) != 0 || len(svc.states) != 0 {
		t.Errorf("checks = %d, states = %d, want none", len(svc.checks), len(svc.states))
	}
}

func TestEnableTurnsADisabledRepositoryBackOn(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	svc := &enableService{repositories: []service.Repository{{
		ID: widgetsID, Provider: "github", FullName: "BrokkAi/slopcop", Enabled: false, Active: false,
	}}}
	server := svc.start(t, repo)
	typed(t, "BrokkAi/slopcop\n")

	code, stdout, stderr := runCLI(t, "enable", "--root", repo, "--service", server.URL)
	if code != 0 || !strings.Contains(stdout, "slopcop: enabled BrokkAi/slopcop in \"Dave\"") {
		t.Errorf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if len(svc.states) != 1 || svc.states[0]["enabled"] != true {
		t.Errorf("states = %v, want the repository enabled", svc.states)
	}
}

func TestEnableUsesTheWorkspaceFlag(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	svc := &enableService{}
	server := svc.start(t, repo)
	typed(t, "BrokkAi/slopcop\n")

	code, _, stderr := runCLI(t, "enable", "--root", repo, "-w", "Acme team", "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if len(svc.workspaces) != 2 || svc.workspaces[0] != teamID || svc.workspaces[1] != teamID {
		t.Errorf("workspaces = %v, want the listing and the check in Acme team", svc.workspaces)
	}
	if !strings.Contains(stderr, `slopcop: added BrokkAi/slopcop to "Acme team"`) {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestCheckSaysWhenItAddedARepositoryThroughTheFlag(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	svc := &enableService{}
	server := svc.start(t, repo)
	pipedStdin(t, "")
	code, stdout, stderr := runCLI(t, "check", "--root", repo, "--add-repository", "-w", "Acme team", "--format", "json", "--service", server.URL)
	var document map[string]any
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q), stderr = %q", err, stdout, stderr)
	}
	if code != 0 || strings.Count(stderr, "slopcop: added BrokkAi/slopcop to \"Acme team\"\n") != 1 ||
		document["repositoryAdded"] != true {
		t.Errorf("exit = %d, stdout = %v, stderr = %q", code, document, stderr)
	}
}

func TestAnUpgradeNoticeGoesToStderrOnceADay(t *testing.T) {
	isolateUserConfig(t)
	t.Setenv(update.DisableEnv, "")
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	base := runGit(t, repo, "rev-parse", "HEAD")
	body := `{
		"status": "clean",
		"baseCommit": "` + base + `",
		"applied": true,
		"summary": {"files": 0, "findings": 0, "incompletePolicies": 0, "failOn": "finding"},
		"findings": [],
		"incomplete": []
	}`
	latest := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if latest != "" {
			w.Header().Set("SlopCop-Latest-Version", latest)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	run := func() (string, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		args := []string{"check", "--root", repo, "--service", server.URL, "--format", "json"}
		if code := execute(context.Background(), args, "v0.0.7", &stdout, &stderr); code != 0 {
			t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
		}
		return stdout.String(), stderr.String()
	}
	plain, _ := run()

	latest = "v0.0.9"
	stdout, stderr := run()
	if stdout != plain {
		t.Errorf("stdout changed with a newer release:\n%s\nwant:\n%s", stdout, plain)
	}
	if !strings.Contains(stderr, "slopcop v0.0.9 is available (you have v0.0.7). Upgrade: ") {
		t.Errorf("stderr = %q, want the notice", stderr)
	}
	if _, stderr := run(); strings.Contains(stderr, "is available") {
		t.Errorf("a second run the same day printed the notice again: %q", stderr)
	}
}

func TestBaseUsesTheMergeBaseWithTheNamedBranch(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	branchPoint := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "checkout", "-q", "-b", "feature")
	writeFile(t, repo, "feature.txt", "feature\n")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-qm", "feature")
	runGit(t, repo, "checkout", "-q", "main")
	writeFile(t, repo, "later-on-main.txt", "main moved on\n")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-qm", "main moved on")
	mainTip := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "checkout", "-q", "feature")

	var captured []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		captured = append(captured, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": "clean",
			"baseCommit": "` + body["baseCommit"].(string) + `",
			"applied": true,
			"summary": {"files": 1, "findings": 0, "incompletePolicies": 0, "failOn": "finding"},
			"findings": [],
			"incomplete": []
		}`))
	}))
	defer server.Close()

	patchFor := func(request map[string]any) string {
		t.Helper()
		patch, err := base64.StdEncoding.DecodeString(request["change"].(string))
		if err != nil {
			t.Fatalf("change is not base64: %v", err)
		}
		return string(patch)
	}

	code, _, stderr := runCLI(t,
		"check", "--root", repo, "--service", server.URL, "--format", "json", "--base", "main",
	)
	if code != 0 {
		t.Fatalf("--base exit = %d, stderr = %s", code, stderr)
	}
	if captured[0]["baseCommit"] != branchPoint {
		t.Errorf("--base baseCommit = %v, want the merge base %s", captured[0]["baseCommit"], branchPoint)
	}
	if patch := patchFor(captured[0]); !strings.Contains(patch, "feature.txt") ||
		strings.Contains(patch, "later-on-main.txt") {
		t.Errorf("--base patch should carry only the branch's change:\n%s", patch)
	}

	code, _, stderr = runCLI(t,
		"check", "--root", repo, "--service", server.URL, "--format", "json", "--base-exact", "main",
	)
	if code != 0 {
		t.Fatalf("--base-exact exit = %d, stderr = %s", code, stderr)
	}
	if captured[1]["baseCommit"] != mainTip {
		t.Errorf("--base-exact baseCommit = %v, want the tip %s", captured[1]["baseCommit"], mainTip)
	}
	if patch := patchFor(captured[1]); !strings.Contains(patch, "later-on-main.txt") {
		t.Errorf("--base-exact patch should reverse main's newer commit:\n%s", patch)
	}

	code, _, stderr = runCLI(t,
		"check", "--root", repo, "--service", server.URL, "--base", "main", "--base-exact", "main",
	)
	if code != 2 || !strings.Contains(stderr, "cannot be used together") {
		t.Fatalf("both flags: exit = %d, stderr = %q", code, stderr)
	}
	if len(captured) != 2 {
		t.Fatalf("requests = %d, want 2", len(captured))
	}
}

// A base the service does not have often comes from a clone that missed a
// rename of origin's default branch, so origin/HEAD, and the default base,
// still point at the old one.
func TestBaseNotFoundHintNamesOriginsDefaultBranch(t *testing.T) {
	origin := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, t.TempDir(), "init", "-q", "--bare", "-b", "main", origin)
	work := t.TempDir()
	runGit(t, work, "clone", "-q", origin, ".")
	runGit(t, work, "config", "user.email", "test@example.com")
	runGit(t, work, "config", "user.name", "Test")
	writeFile(t, work, "a.txt", "a\n")
	runGit(t, work, "add", "-A")
	runGit(t, work, "commit", "-qm", "a")
	runGit(t, work, "push", "-q", "origin", "main")
	runGit(t, work, "remote", "set-head", "origin", "--auto")
	pushed := runGit(t, work, "rev-parse", "HEAD")

	runGit(t, origin, "branch", "-m", "main", "master")
	runGit(t, work, "fetch", "-q", "--prune", "origin")
	hint := describeBase(work, pushed, "master")
	for _, want := range []string{
		"origin's default branch is master",
		"origin/HEAD is origin/main",
		"git remote set-head origin --auto",
		"--base origin/master",
	} {
		if !strings.Contains(hint, want) {
			t.Errorf("after a rename, hint = %q, want %q", hint, want)
		}
	}

	runGit(t, work, "remote", "set-head", "origin", "--auto")
	if hint := describeBase(work, pushed, "master"); hint != "" {
		t.Errorf("a base on origin's default branch got hint %q", hint)
	}

	writeFile(t, work, "b.txt", "b\n")
	runGit(t, work, "add", "-A")
	runGit(t, work, "commit", "-qm", "b")
	local := runGit(t, work, "rev-parse", "HEAD")
	hint = describeBase(work, local, "master")
	if !strings.Contains(hint, "is not on origin/master") || !strings.Contains(hint, "--base origin/master") {
		t.Errorf("an unpushed base got hint %q", hint)
	}
}

func TestAILocalReviewAndOverride(t *testing.T) {
	for _, tc := range []struct {
		mode   string
		want   int
		status string
	}{{"clean", 0, "clean"}, {"findings", 1, "findings"}, {"empty", 2, "incomplete"}} {
		t.Run(tc.mode, func(t *testing.T) {
			isolateUserConfig(t)
			repo := newRepo(t, "")
			writeFile(t, repo, "README.md", "changed\n")
			t.Setenv("SLOPCOP_FAKE_ACP", "1")
			cfg := config.Config{Mode: "ai", Review: &config.Review{Count: 1, Adapters: map[string]config.Adapter{"terra": {Command: []string{os.Args[0], "-test.run=^TestFakeAgent$", "--fake-mode=" + tc.mode}}}}}
			if _, err := config.Save(cfg); err != nil {
				t.Fatal(err)
			}
			code, out, stderr := runCLI(t, "check", "--root", repo, "--format", "json")
			if code != tc.want {
				t.Fatalf("exit %d want %d, stdout %s stderr %s", code, tc.want, out, stderr)
			}
			var doc struct {
				Status string `json:"status"`
				Review struct {
					Status string `json:"status"`
				} `json:"review"`
			}
			if err := json.Unmarshal([]byte(out), &doc); err != nil {
				t.Fatal(err)
			}
			if doc.Status != tc.status {
				t.Fatalf("status %q want %q", doc.Status, tc.status)
			}
			snapshot, err := gitx.CaptureWorkingTree(repo, nil)
			if err != nil {
				t.Fatal(err)
			}
			ref := "refs/slopcop/reviewed/" + snapshot.Tree
			_, err = gitx.Run(repo, "show-ref", "--verify", "--quiet", ref)
			if (err == nil) != (tc.mode == "clean") {
				t.Fatalf("approval before override: %v", err)
			}
			if tc.mode == "clean" {
				code, _, _ = runCLI(t, "override", "--root", repo, "--reason", "false positive")
				if code != 2 {
					t.Fatalf("clean review override exit %d", code)
				}
			}
			if tc.mode == "findings" {
				code, out, stderr = runCLI(t, "override", "--root", repo, "--reason", "false positive")
				if code != 0 {
					t.Fatalf("override %d %s %s", code, out, stderr)
				}
			}
		})
	}
}

func TestBothReviewRunsWhenStaticCannotStart(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "")
	writeFile(t, repo, "README.md", "changed\n")
	t.Setenv("SLOPCOP_FAKE_ACP", "1")
	cfg := config.Config{Mode: "both", Review: &config.Review{Count: 1, Adapters: map[string]config.Adapter{"terra": {Command: []string{os.Args[0], "-test.run=^TestFakeAgent$"}}}}}
	if _, err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runCLI(t, "check", "--root", repo, "--format", "json")
	if code != 2 {
		t.Fatalf("exit %d out %s stderr %s", code, out, stderr)
	}
	var doc struct {
		Status       string `json:"status"`
		StaticStatus string `json:"staticStatus"`
		Review       struct {
			Status string `json:"status"`
		} `json:"review"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Status != "failed" || doc.Review.Status != "clean" {
		t.Fatalf("%+v", doc)
	}
}

func TestCombinedExitAndStatus(t *testing.T) {
	cases := []struct {
		name, static, agent, failOn, wantStatus, wantStatic string
		wantExit                                            int
	}{
		{"static findings review clean", "findings", "clean", "finding", "findings", "", 1},
		{"static clean review findings", "clean", "findings", "finding", "findings", "clean", 1},
		{"person review findings", "person", "findings", "finding", "findings", "failed", 1},
		{"person review clean", "person", "clean", "finding", "failed", "", 4},
		{"static clean review failed", "clean", "empty", "finding", "incomplete", "clean", 2},
		{"static pending review clean", "pending", "clean", "finding", "pending", "", 3},
		{"fail never review findings", "clean", "findings", "never", "clean", "", 0},
		{"static incomplete review clean", "incomplete", "clean", "finding", "incomplete", "", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateUserConfig(t)
			repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
			writeFile(t, repo, "README.md", "changed\n")
			t.Setenv("SLOPCOP_FAKE_ACP", "1")
			cfg := config.Config{Mode: "both", Review: &config.Review{Count: 1, Adapters: map[string]config.Adapter{"terra": {Command: []string{os.Args[0], "-test.run=^TestFakeAgent$", "--fake-mode=" + tc.agent}}}}}
			if _, err := config.Save(cfg); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch tc.static {
				case "person":
					w.WriteHeader(http.StatusConflict)
					_, _ = w.Write([]byte(`{"code":"repository_not_enabled","message":"not enabled","retryable":false}`))
				case "pending":
					checkRunning(w, "5")
				default:
					_, _ = fmt.Fprintf(w, `{"status":%q,"baseCommit":"0123456789abcdef0123456789abcdef01234567","applied":true,"summary":{"files":1,"newFindings":0,"incompletePolicies":0,"failOn":%q},"findings":[],"incomplete":[]}`, tc.static, tc.failOn)
				}
			}))
			defer server.Close()
			args := []string{"check", "--root", repo, "--service", server.URL, "--format", "json", "--timeout", "50ms", "--fail-on", tc.failOn}
			code, out, stderr := runCLI(t, args...)
			if code != tc.wantExit {
				t.Fatalf("exit %d want %d: stdout %s stderr %s", code, tc.wantExit, out, stderr)
			}
			var doc struct {
				Status       string `json:"status"`
				StaticStatus string `json:"staticStatus"`
				Review       struct {
					Status string `json:"status"`
				} `json:"review"`
			}
			if err := json.Unmarshal([]byte(out), &doc); err != nil {
				t.Fatal(err)
			}
			if doc.Status != tc.wantStatus || doc.StaticStatus != tc.wantStatic {
				t.Fatalf("status %+v want %q/%q", doc, tc.wantStatus, tc.wantStatic)
			}
			if tc.name == "static incomplete review clean" || tc.name == "static pending review clean" {
				snapshot, err := gitx.CaptureWorkingTree(repo, nil)
				if err != nil {
					t.Fatal(err)
				}
				_, err = gitx.Run(repo, "show-ref", "--verify", "--quiet", "refs/slopcop/reviewed/"+snapshot.Tree)
				want := tc.name == "static incomplete review clean"
				if (err == nil) != want {
					t.Fatalf("approval ref exists=%v want %v: %v", err == nil, want, err)
				}
			}
		})
	}
}

func TestReviewInterruptStopsAdapter(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "")
	writeFile(t, repo, "README.md", "changed\n")
	trace := filepath.Join(t.TempDir(), "agent.trace")
	t.Setenv("SLOPCOP_FAKE_ACP", "1")
	cfg := config.Config{Mode: "ai", Review: &config.Review{Count: 1, Adapters: map[string]config.Adapter{"terra": {Command: []string{os.Args[0], "-test.run=^TestFakeAgent$", "--fake-mode=hang", "--fake-trace=" + trace}}}}}
	if _, err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		deadline := time.After(10 * time.Second)
		for {
			data, _ := os.ReadFile(trace)
			if strings.Contains(string(data), "prompt\n") {
				cancel()
				return
			}
			select {
			case <-deadline:
				cancel()
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
	var stdout, stderr bytes.Buffer
	code := execute(ctx, []string{"check", "--root", repo, "--format", "json"}, "test", &stdout, &stderr)
	<-done
	if code != 130 || !strings.Contains(stderr.String(), "reviewers were stopped") {
		t.Fatalf("exit %d stderr %q", code, stderr.String())
	}
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	var pid string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "pid=") {
			pid = strings.TrimPrefix(line, "pid=")
			break
		}
	}
	if pid == "" {
		t.Fatalf("missing agent pid: %s", data)
	}
	if runtime.GOOS != "windows" {
		for i := 0; i < 20; i++ {
			if exec.Command("kill", "-0", pid).Run() != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("agent %s still running", pid)
	}
}

func TestStaticIncompleteApprovesNonemptyTree(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "README.md", "changed\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"incomplete","baseCommit":"0123456789abcdef0123456789abcdef01234567","applied":true,"summary":{"files":1,"newFindings":0,"incompletePolicies":1},"findings":[],"incomplete":[]}`))
	}))
	defer server.Close()
	code, out, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL, "--mode", "static", "--format", "json")
	if code != 2 {
		t.Fatalf("exit %d out %s err %s", code, out, stderr)
	}
	snapshot, err := gitx.CaptureWorkingTree(repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.Run(repo, "show-ref", "--verify", "--quiet", "refs/slopcop/reviewed/"+snapshot.Tree); err != nil {
		t.Fatalf("missing approval: %v", err)
	}
}

func TestOverrideAfterStaticIncomplete(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "README.md", "changed\n")
	t.Setenv("SLOPCOP_FAKE_ACP", "1")
	cfg := config.Config{Mode: "both", Review: &config.Review{Count: 1, Adapters: map[string]config.Adapter{"terra": {Command: []string{os.Args[0], "-test.run=^TestFakeAgent$", "--fake-mode=findings"}}}}}
	if _, err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"incomplete","baseCommit":"0123456789abcdef0123456789abcdef01234567","applied":true,"summary":{"files":1,"newFindings":0,"incompletePolicies":1},"findings":[],"incomplete":[]}`))
	}))
	defer server.Close()
	code, out, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL, "--format", "json")
	if code != 1 {
		t.Fatalf("exit %d out %s err %s", code, out, stderr)
	}
	code, out, stderr = runCLI(t, "override", "--root", repo, "--reason", "false positive")
	if code != 0 {
		t.Fatalf("override exit %d out %s err %s", code, out, stderr)
	}
	snapshot, err := gitx.CaptureWorkingTree(repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.Run(repo, "show-ref", "--verify", "--quiet", "refs/slopcop/reviewed/"+snapshot.Tree); err != nil {
		t.Fatalf("missing approval: %v", err)
	}
}
