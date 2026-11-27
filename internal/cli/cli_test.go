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
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/gitx"
	"github.com/BrokkAi/slopcop/internal/review"
	"github.com/BrokkAi/slopcop/internal/review/reviewtest"
	"github.com/spf13/cobra"

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
	unmappedNotice = func(*cobra.Command, *service.Client, workspaceChoice) {}
	ignoreElsewhereNotice = func(*cobra.Command, *service.Client, string) {}
	// Tests print the same remedies on every OS; the Windows shell note has
	// its own test.
	separateShellLogins = func() bool { return false }
	os.Exit(m.Run())
}

// realUnmappedNotice makes a check consider the note about an unmapped
// checkout, for tests about that note.
func realUnmappedNotice(t *testing.T) {
	t.Helper()
	original := unmappedNotice
	unmappedNotice = noticeUnmapped
	t.Cleanup(func() { unmappedNotice = original })
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

func installUserGate(t *testing.T) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"slopcop gate --hook claude"}]}]}}`), 0o600); err != nil {
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

// privateTestHome makes a home directory below the real user cache. Reviewers
// refuse a cache below a directory other users can write to, which the default
// temporary directory is on Linux.
func privateTestHome(t *testing.T) string {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(cache, 0700); err != nil {
		t.Fatal(err)
	}
	home, err := os.MkdirTemp(cache, "slopcop-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	return home
}

func isolateUserConfig(t *testing.T) {
	t.Helper()
	home := privateTestHome(t)
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
	t.Setenv("GROK_SESSION_ID", "")
	t.Setenv("GROK_HOME", "")
	t.Setenv("GROK_HOOK_EVENT", "")
	t.Setenv("KIMI_CODE_HOME", "")
	// A check with no mode set runs only the static half in CI; tests run
	// the same everywhere.
	t.Setenv("CI", "")
	t.Setenv("TF_BUILD", "")
	t.Setenv("JENKINS_URL", "")
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

func TestCheckPolicySelectors(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	var selection map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		selection, _ = request["selection"].(map[string]any)
		_, _ = w.Write([]byte(`{"status":"clean","summary":{"failOn":"finding"},"findings":[],"incomplete":[]}`))
	}))
	defer server.Close()
	code, _, stderr := runCLI(t, "check", "--mode", "static", "--root", repo, "--service", server.URL,
		"--no-policy-discovery", "--policy-id", "slopcop.similarity", "--policy-id", "slopcop.similarity",
		"--policy-pack", "built-in", "--policy-pack", "built-in",
		"--policy-category", "duplication", "--policy-category", "duplication")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	for key, want := range map[string]string{"policyIds": "slopcop.similarity", "packs": "built-in", "categories": "duplication"} {
		values, _ := selection[key].([]any)
		if len(values) != 1 || values[0] != want {
			t.Errorf("selection[%s]=%v, want [%s]", key, values, want)
		}
	}
}

func TestSimilarityFindingIsShownRegardlessOfCertaintyAndNeverGates(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"clean","summary":{"newFindings":1,"failOn":"finding"},"findings":[{"policyId":"slopcop.similarity","severity":"info","certainty":"heuristic","path":"new.go","startLine":3,"title":"Similar function","new":true,"relatedLocations":[{"path":"old.go","startLine":8,"endLine":10,"message":"P(combine) = 0.87"}]}],"incomplete":[],"checks":[{"policyId":"slopcop.similarity","status":"future_status","message":"Readiness detail."}]}`))
	}))
	defer server.Close()
	code, stdout, stderr := runCLI(t, "check", "--mode", "static", "--root", repo, "--service", server.URL, "--certainty", "definite")
	if code != 0 || !strings.Contains(stdout, "[info] slopcop.similarity") || !strings.Contains(stdout, "Candidate: old.go:8 — P(combine) = 0.87") || !strings.Contains(stdout, "Note: slopcop.similarity (future_status): Readiness detail.") {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
}

// TestCheckStatusNamesFindingsWhateverFailOn covers BrokkAi/slopcop#120: the
// service's status follows failOn, but the report's status names the result,
// and only the exit follows --fail-on.
func TestCheckStatusNamesFindingsWhateverFailOn(t *testing.T) {
	cases := []struct {
		failOn, service string
		incomplete      bool
		wantStatus      string
		wantExit        int
	}{
		{"finding", "findings", false, "findings", 1},
		{"note", "findings", false, "findings", 1},
		{"warning", "findings", false, "findings", 1},
		{"error", "clean", false, "findings", 0},
		{"never", "clean", false, "findings", 0},
		{"never", "incomplete", true, "findings", 2},
	}
	for _, tc := range cases {
		name := tc.failOn
		if tc.incomplete {
			name += " incomplete"
		}
		t.Run(name, func(t *testing.T) {
			isolateUserConfig(t)
			repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
			writeFile(t, repo, "demo.go", "package demo\n")
			incomplete, incompleteCount := `[]`, 0
			if tc.incomplete {
				incomplete, incompleteCount = `[{"policyId": "slow", "reason": "timed_out"}]`, 1
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{
					"status": %q,
					"baseCommit": "0123456789abcdef0123456789abcdef01234567",
					"applied": true,
					"summary": {"files": 1, "findings": 1, "newFindings": 1, "incompletePolicies": %d, "failOn": %q},
					"findings": [{"id": "f1", "policyId": "local", "severity": "warning", "certainty": "possible", "path": "demo.go", "startLine": 1, "endLine": 1, "title": "example", "new": true}],
					"incomplete": %s
				}`, tc.service, incompleteCount, tc.failOn, incomplete)
			}))
			defer server.Close()
			for _, format := range []string{"json", "sarif"} {
				code, stdout, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL, "--format", format, "--fail-on", tc.failOn)
				if code != tc.wantExit {
					t.Fatalf("%s exit = %d want %d, stderr = %s", format, code, tc.wantExit, stderr)
				}
				var got string
				if format == "json" {
					var doc struct {
						Status string `json:"status"`
					}
					if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
						t.Fatalf("stdout is not json: %v", err)
					}
					got = doc.Status
				} else {
					var doc struct {
						Runs []struct {
							Properties map[string]any `json:"properties"`
						} `json:"runs"`
					}
					if err := json.Unmarshal([]byte(stdout), &doc); err != nil || len(doc.Runs) == 0 {
						t.Fatalf("stdout is not sarif: %v\n%s", err, stdout)
					}
					got, _ = doc.Runs[0].Properties["slopcop.status"].(string)
				}
				if got != tc.wantStatus {
					t.Fatalf("%s status = %q want %q:\n%s", format, got, tc.wantStatus, stdout)
				}
			}
		})
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
	want := "slopcop: using service " + server.URL + "\n" + "slopcop: repository_disabled: This repository is disabled in your SlopCop workspace, so it isn't scanned.\n" +
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
	if cfg.TokenFor("http://127.0.0.1:9999") != "secret" {
		t.Fatalf("config = %+v", cfg)
	}
	// The first login on a machine makes its service the default, and says so.
	if cfg.DefaultService() != "http://127.0.0.1:9999" || !strings.Contains(stderr, "http://127.0.0.1:9999 is now the default service") {
		t.Fatalf("default = %q, stderr = %q", cfg.ServiceURL, stderr)
	}

	// Without --service, logout is about the default service.
	code, stdout, stderr = runCLI(t, "logout", "--service", config.DefaultServiceURL)
	if code != 0 || !strings.Contains(stdout, "no stored credential for https://slopcop.com") {
		t.Fatalf("logout exit = %d, stdout = %q, stderr = %s", code, stdout, stderr)
	}
	code, stdout, stderr = runCLI(t, "logout", "--service", "http://127.0.0.1:9999/")
	if code != 0 {
		t.Fatalf("logout exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "logged out of http://127.0.0.1:9999") {
		t.Fatalf("logout stdout = %q", stdout)
	}
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Logins) != 0 {
		t.Fatalf("token was not cleared: %+v", cfg)
	}
}

// TestFirstLoginMovesTheDefault covers the install command from a service
// other than slopcop.com: the default has no login, so the new service
// becomes the default, while a default that is signed in stays.
func TestFirstLoginMovesTheDefault(t *testing.T) {
	const local = "http://127.0.0.1:3610"
	const staging = "https://staging.example.test"
	t.Run("first login", func(t *testing.T) {
		isolateUserConfig(t)
		code, _, stderr := runCLI(t, "--service", local, "login", "--token", "local-token")
		if code != 0 {
			t.Fatalf("login: exit = %d, stderr = %s", code, stderr)
		}
		want := "slopcop: " + local + " is now the default service, as https://slopcop.com has no login here; `slopcop service use https://slopcop.com` moves it back\n"
		if stderr != want {
			t.Fatalf("stderr = %q", stderr)
		}
		cfg, err := config.Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.DefaultService() != local || resolveToken(cfg, resolveServiceURL(cfg, "")) != "local-token" {
			t.Fatalf("config = %+v", cfg)
		}
		// Running the install command again stores the new token and says
		// nothing more about the default.
		code, _, stderr = runCLI(t, "--service", local, "login", "--token", "local-2")
		if code != 0 || stderr != "" {
			t.Fatalf("second login: exit = %d, stderr = %q", code, stderr)
		}
		if cfg, err = config.Load(); err != nil || cfg.DefaultService() != local || cfg.TokenFor(local) != "local-2" {
			t.Fatalf("config = %+v, err = %v", cfg, err)
		}
	})
	t.Run("first login to the hosted service", func(t *testing.T) {
		isolateUserConfig(t)
		code, _, stderr := runCLI(t, "login", "--token", "prod-token")
		if code != 0 || stderr != "" {
			t.Fatalf("login: exit = %d, stderr = %q", code, stderr)
		}
		if cfg, err := config.Load(); err != nil || cfg.ServiceURL != "" {
			t.Fatalf("config = %+v, err = %v", cfg, err)
		}
	})
	t.Run("default signed out, another service signed in", func(t *testing.T) {
		isolateUserConfig(t)
		for _, args := range [][]string{
			{"login", "--token", "prod-token"},
			{"--service", staging, "login", "--token", "staging-token"},
			{"logout"},
		} {
			if code, _, stderr := runCLI(t, args...); code != 0 {
				t.Fatalf("%v: exit = %d, stderr = %s", args, code, stderr)
			}
		}
		code, _, stderr := runCLI(t, "--service", local, "login", "--token", "local-token")
		if code != 0 || !strings.Contains(stderr, local+" is now the default service") {
			t.Fatalf("login: exit = %d, stderr = %q", code, stderr)
		}
		if cfg, err := config.Load(); err != nil || cfg.DefaultService() != local || cfg.TokenFor(staging) != "staging-token" {
			t.Fatalf("config = %+v, err = %v", cfg, err)
		}
	})
	t.Run("default signed in", func(t *testing.T) {
		for name, first := range map[string][]string{
			"to the default": {"login", "--token", "prod-token"},
			"to another":     {"--service", staging, "login", "--token", "staging-token"},
		} {
			t.Run(name, func(t *testing.T) {
				isolateUserConfig(t)
				if code, _, stderr := runCLI(t, first...); code != 0 {
					t.Fatalf("%v: exit = %d, stderr = %s", first, code, stderr)
				}
				before, err := config.Load()
				if err != nil {
					t.Fatal(err)
				}
				code, _, stderr := runCLI(t, "--service", local, "login", "--token", "local-token")
				if code != 0 || !strings.Contains(stderr, "the default service is still "+config.Origin(before.DefaultService())) {
					t.Fatalf("login: exit = %d, stderr = %q", code, stderr)
				}
				after, err := config.Load()
				if err != nil {
					t.Fatal(err)
				}
				if after.DefaultService() != before.DefaultService() {
					t.Fatalf("default moved from %q to %q", before.DefaultService(), after.DefaultService())
				}
			})
		}
	})
}

func TestLoginsAreKeptPerService(t *testing.T) {
	isolateUserConfig(t)
	const staging = "https://staging.example.test"
	for _, args := range [][]string{
		{"login", "--token", "prod-token"},
		{"login", "--service", staging, "--token", "staging-token"},
	} {
		if code, _, stderr := runCLI(t, args...); code != 0 {
			t.Fatalf("%v: exit = %d, stderr = %s", args, code, stderr)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TokenFor(config.DefaultServiceURL) != "prod-token" || cfg.TokenFor(staging) != "staging-token" {
		t.Fatalf("logins = %+v", cfg.Logins)
	}
	// A credential goes only to the service that issued it.
	if got := resolveToken(cfg, staging+"/"); got != "staging-token" {
		t.Errorf("staging token = %q", got)
	}
	if got := resolveToken(cfg, "https://other.example.test"); got != "" {
		t.Errorf("a service with no login was handed %q", got)
	}
	if got := resolveServiceURL(cfg, ""); got != config.DefaultServiceURL {
		t.Errorf("default = %q", got)
	}

	code, stdout, _ := runCLI(t, "service", "list")
	want := "SERVICE                       LOGIN      DEFAULT\n" +
		"https://slopcop.com           signed in  *\n" +
		"https://staging.example.test  signed in  \n"
	if code != 0 || stdout != want {
		t.Fatalf("service list: exit = %d, stdout = %q", code, stdout)
	}

	// login --default moves the default, which only a person at a terminal
	// may do; logout --all forgets every login and keeps it.
	if code, _, stderr := runCLI(t, "login", "--service", staging, "--token", "staging-2", "--default"); code != 2 || !strings.Contains(stderr, "login --default moves every agent") {
		t.Fatalf("login --default off a terminal: exit = %d, stderr = %q", code, stderr)
	}
	if cfg, err = config.Load(); err != nil || cfg.TokenFor(staging) != "staging-token" || cfg.DefaultService() != config.DefaultServiceURL {
		t.Fatalf("a refused login --default changed the config: %+v, err = %v", cfg, err)
	}
	typed(t, "y\n")
	if code, _, stderr := runCLI(t, "login", "--service", staging, "--token", "staging-2", "--default"); code != 0 || strings.Contains(stderr, "still") {
		t.Fatalf("login --default: exit = %d, stderr = %q", code, stderr)
	}
	code, stdout, _ = runCLI(t, "logout", "--all")
	if code != 0 || !strings.Contains(stdout, "logged out of https://slopcop.com, "+staging) {
		t.Fatalf("logout --all: exit = %d, stdout = %q", code, stdout)
	}
	cfg, err = config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Logins) != 0 || cfg.DefaultService() != staging {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestServiceUseAsksAtATerminal(t *testing.T) {
	isolateUserConfig(t)
	const staging = "https://staging.example.test"
	code, _, stderr := runCLI(t, "service", "use", staging)
	if code != 2 || !strings.Contains(stderr, "must be confirmed at a terminal") {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	if code, _, stderr = runCLI(t, "service", "use", "http://staging.example.test"); code != 2 || !strings.Contains(stderr, "https") {
		t.Fatalf("plain http: exit = %d, stderr = %q", code, stderr)
	}

	typed(t, "n\n")
	if code, _, stderr = runCLI(t, "service", "use", staging); code != 2 || !strings.Contains(stderr, "still https://slopcop.com") {
		t.Fatalf("declined: exit = %d, stderr = %q", code, stderr)
	}
	typed(t, "y\n")
	code, stdout, stderr := runCLI(t, "service", "use", staging+"/")
	if code != 0 || !strings.Contains(stdout, "the default service is now "+staging) || !strings.Contains(stderr, "not signed in to "+staging) {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	cfg, err := config.Load()
	if err != nil || cfg.DefaultService() != staging {
		t.Fatalf("config = %+v, err = %v", cfg, err)
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
	if cfg.TokenFor(server.URL) != "browser-token" || !strings.Contains(stderr, "slopcop: signing in to "+server.URL) {
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

// The service decides which findings a check answers: the CLI sends no
// certainty by default, so an older service still accepts the request, and
// sends certainty all, on the request and when it asks again by id, for
// --certainty all. It prints the service's count of the findings it left out.
func TestCertaintyIsTheServicesToApply(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	var sent []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		certainty := r.URL.Query().Get("certainty")
		if r.Method == http.MethodPost {
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode request: %v", err)
			}
			value, present := request["certainty"]
			if !present {
				value = "<none>"
			}
			certainty = fmt.Sprint(value)
			sent = append(sent, "post "+certainty)
			// The first answer is still running, so the CLI asks by id.
			checkRunningWithID(w, "6f1c2a7e-3b4d-4e5f-8a9b-0c1d2e3f4a5b")
			return
		}
		sent = append(sent, "get "+r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		if certainty == "all" {
			_, _ = w.Write([]byte(`{"status": "findings", "baseCommit": "0123456789abcdef0123456789abcdef01234567", "applied": true,
				"summary": {"files": 1, "findings": 1, "newFindings": 1, "lessCertainFindings": 0, "incompletePolicies": 0, "failOn": "finding"},
				"findings": [{"policyId": "p", "severity": "error", "certainty": "possible", "path": "demo.go", "startLine": 1, "endLine": 1, "title": "maybe", "new": true}],
				"incomplete": []}`))
			return
		}
		_, _ = w.Write([]byte(`{"status": "clean", "baseCommit": "0123456789abcdef0123456789abcdef01234567", "applied": true,
			"summary": {"files": 1, "findings": 0, "newFindings": 0, "lessCertainFindings": 1, "incompletePolicies": 0, "failOn": "finding"},
			"findings": [], "incomplete": []}`))
	}))
	defer server.Close()

	code, stdout, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL)
	if code != 0 || stdout != "No new findings; 1 less certain not shown (--certainty all) | Analysis complete\n" {
		t.Fatalf("exit = %d, stdout = %q, stderr = %s", code, stdout, stderr)
	}
	code, stdout, _ = runCLI(t, "check", "--root", repo, "--service", server.URL, "--certainty", "ALL")
	if code != 1 || !strings.Contains(stdout, "[error] p: maybe") {
		t.Fatalf("exit = %d, stdout = %q", code, stdout)
	}
	// Under --certainty all the count is 0, and the JSON still carries it.
	code, stdout, _ = runCLI(t, "check", "--root", repo, "--service", server.URL, "--certainty", "all", "--format", "json")
	if code != 1 || !strings.Contains(stdout, `"lessCertainFindings": 0`) {
		t.Fatalf("exit = %d, stdout = %s", code, stdout)
	}
	want := []string{"post <none>", "get failOn=finding", "post all", "get certainty=all&failOn=finding",
		"post all", "get certainty=all&failOn=finding"}
	if fmt.Sprint(sent) != fmt.Sprint(want) {
		t.Fatalf("sent %q, want %q", sent, want)
	}

	if code, _, stderr = runCLI(t, "check", "--root", repo, "--certainty", "likely"); code == 0 ||
		!strings.Contains(stderr, `unknown certainty "likely"`) {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
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

// checkRunningWithID answers check_running for the check id, with no wait,
// so the client asks again by that id.
func checkRunningWithID(w http.ResponseWriter, checkID string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "0")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = fmt.Fprintf(w, `{"code": "check_running", "message": "The check is still running.", "remedy": "Wait.", "retryable": true, "checkId": %q}`, checkID)
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
	// inTeam is what the Acme team workspace has, when a test gives it its
	// own; otherwise every workspace answers repositories.
	inTeam []service.Repository
	// teamFails makes the Acme team workspace refuse to list its repositories.
	teamFails bool
	// alone makes the account's personal workspace its only one.
	alone      bool
	lists      int
	checks     []map[string]any
	states     []map[string]any
	workspaces []string
	base       string
	added      bool
}

func (s *enableService) start(t *testing.T, repo string) *httptest.Server {
	t.Helper()
	s.base = runGit(t, repo, "rev-parse", "HEAD")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/whoami":
			if s.alone {
				_, _ = w.Write([]byte(aloneWhoamiBody))
				return
			}
			_, _ = w.Write([]byte(whoamiBody))
		case "/v1/repositories":
			s.lists++
			s.workspaces = append(s.workspaces, r.Header.Get("SlopCop-Workspace"))
			if s.teamFails && r.Header.Get("SlopCop-Workspace") == teamID {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"code": "unavailable", "message": "Unavailable.", "retryable": true}`))
				return
			}
			listed := s.repositories
			if s.inTeam != nil && r.Header.Get("SlopCop-Workspace") == teamID {
				listed = s.inTeam
			}
			_ = json.NewEncoder(w).Encode(service.RepositoryList{Repositories: listed})
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

func TestCheckAddsARepositoryTheWorkspaceNeverAdded(t *testing.T) {
	isolateUserConfig(t)
	realRepositoryCheck(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")

	t.Run("a plain check adds it and checks it", func(t *testing.T) {
		svc := &enableService{}
		server := svc.start(t, repo)
		pipedStdin(t, "")
		code, _, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL)
		if code != 0 || strings.Contains(stderr, "[y/N]") || strings.Count(stderr, "slopcop: added BrokkAi/slopcop to \"Dave\"\n") != 1 {
			t.Errorf("exit = %d, stderr = %q", code, stderr)
		}
		if len(svc.checks) != 1 || svc.checks[0]["addRepository"] != true {
			t.Errorf("checks = %v, want one check that adds the repository", svc.checks)
		}
	})

	t.Run("--workspace picks the workspace it is added to", func(t *testing.T) {
		svc := &enableService{}
		server := svc.start(t, repo)
		pipedStdin(t, "")
		code, _, stderr := runCLI(t, "check", "--root", repo, "-w", "Acme team", "--service", server.URL)
		if code != 0 || !strings.Contains(stderr, "slopcop: added BrokkAi/slopcop to \"Acme team\"") {
			t.Errorf("exit = %d, stderr = %q", code, stderr)
		}
		if len(svc.workspaces) == 0 {
			t.Error("no request named a workspace")
		}
		for _, workspace := range svc.workspaces {
			if workspace != teamID {
				t.Errorf("workspaces = %v, want only the named workspace", svc.workspaces)
				break
			}
		}
	})

	t.Run("--if-enabled adds it too", func(t *testing.T) {
		svc := &enableService{}
		server := svc.start(t, repo)
		pipedStdin(t, "")
		code, _, stderr := runCLI(t, "check", "--root", repo, "--if-enabled", "--format", "json", "--service", server.URL)
		if code != 0 || len(svc.checks) != 1 || svc.checks[0]["addRepository"] != true {
			t.Errorf("exit = %d, checks = %v, stderr = %q", code, svc.checks, stderr)
		}
	})

	t.Run("an active repository is checked without adding it", func(t *testing.T) {
		svc := &enableService{repositories: []service.Repository{{
			ID: widgetsID, Provider: "github", FullName: "BrokkAi/slopcop", Enabled: true, Active: true,
		}}}
		server := svc.start(t, repo)
		pipedStdin(t, "")
		code, _, stderr := runCLI(t, "check", "--root", repo, "--if-enabled", "--format", "json", "--service", server.URL)
		if code != 0 || len(svc.checks) != 1 || svc.checks[0]["addRepository"] == true {
			t.Errorf("exit = %d, checks = %v, stderr = %q", code, svc.checks, stderr)
		}
	})

	t.Run("the retired --add-repository flag is still accepted", func(t *testing.T) {
		svc := &enableService{}
		server := svc.start(t, repo)
		pipedStdin(t, "")
		code, _, stderr := runCLI(t, "check", "--root", repo, "--add-repository", "--service", server.URL)
		if code != 0 || len(svc.checks) != 1 {
			t.Errorf("exit = %d, checks = %v, stderr = %q", code, svc.checks, stderr)
		}
	})
}

func TestCheckDoesNotTurnAnInactiveRepositoryBackOn(t *testing.T) {
	isolateUserConfig(t)
	realRepositoryCheck(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")

	t.Run("a disabled repository is refused without a command to re-enable it", func(t *testing.T) {
		svc := &enableService{repositories: []service.Repository{{
			ID: widgetsID, Provider: "github", FullName: "BrokkAi/slopcop", Enabled: false, Active: false,
		}}}
		server := svc.start(t, repo)
		pipedStdin(t, "")
		code, stdout, stderr := runCLI(t, "check", "--root", repo, "--format", "json", "--service", server.URL)
		var document map[string]any
		if err := json.Unmarshal([]byte(stdout), &document); err != nil {
			t.Fatalf("stdout is not JSON: %v (%q), stderr = %q", err, stdout, stderr)
		}
		if code != service.ExitNeedsPerson || document["status"] != "failed" || document["code"] != repositoryDisabledCode {
			t.Errorf("exit = %d, stdout = %v, stderr = %q", code, document, stderr)
		}
		remedy, _ := document["remedy"].(string)
		if strings.Contains(remedy, "slopcop enable") || strings.Contains(remedy, "--add-repository") {
			t.Errorf("remedy = %q, want no command that re-enables the repository", remedy)
		}
		if len(svc.checks) != 0 {
			t.Errorf("checks = %v, want none", svc.checks)
		}
	})

	t.Run("--if-enabled skips a disabled repository", func(t *testing.T) {
		svc := &enableService{repositories: []service.Repository{{
			ID: widgetsID, Provider: "github", FullName: "BrokkAi/slopcop", Enabled: false, Active: false,
		}}}
		server := svc.start(t, repo)
		pipedStdin(t, "")
		code, stdout, stderr := runCLI(t, "check", "--root", repo, "--if-enabled", "--format", "json", "--service", server.URL)
		var document map[string]any
		if err := json.Unmarshal([]byte(stdout), &document); err != nil {
			t.Fatalf("stdout is not JSON: %v (%q), stderr = %q", err, stdout, stderr)
		}
		if code != 0 || document["status"] != "skipped" || document["code"] != repositoryNotEnabledCode ||
			document["state"] != statusDisabled || !strings.Contains(stderr, "was disabled in your SlopCop workspace, so nothing was checked") {
			t.Errorf("exit = %d, stdout = %v, stderr = %q", code, document, stderr)
		}
		if len(svc.checks) != 0 {
			t.Errorf("checks = %v, want none", svc.checks)
		}
	})

	t.Run("a repository past the active limit is refused without sending one", func(t *testing.T) {
		svc := &enableService{repositories: []service.Repository{{
			ID: widgetsID, Provider: "github", FullName: "BrokkAi/slopcop", Enabled: true, Active: false,
		}}}
		server := svc.start(t, repo)
		pipedStdin(t, "")
		code, _, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL)
		if code != service.ExitNeedsPerson || !strings.Contains(stderr, activeLimitCode) || len(svc.checks) != 0 {
			t.Errorf("exit = %d, checks = %v, stderr = %q", code, svc.checks, stderr)
		}
	})

	t.Run("--add-repository does not re-enable a disabled repository", func(t *testing.T) {
		svc := &enableService{repositories: []service.Repository{{
			ID: widgetsID, Provider: "github", FullName: "BrokkAi/slopcop", Enabled: false, Active: false,
		}}}
		server := svc.start(t, repo)
		pipedStdin(t, "")
		code, _, stderr := runCLI(t, "check", "--root", repo, "--add-repository", "-w", "Acme team", "--service", server.URL)
		if code != service.ExitNeedsPerson || !strings.Contains(stderr, repositoryDisabledCode) || len(svc.checks) != 0 {
			t.Errorf("exit = %d, checks = %v, stderr = %q", code, svc.checks, stderr)
		}
	})
}

func TestEnableAddsARepositoryWithoutAsking(t *testing.T) {
	isolateUserConfig(t)
	realRepositoryCheck(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	svc := &enableService{}
	server := svc.start(t, repo)
	// Not a terminal: a check would add the repository anyway, so 'enable'
	// does not need one either.
	pipedStdin(t, "")

	code, stdout, stderr := runCLI(t, "enable", "--root", repo, "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	for _, want := range []string{
		"slopcop: added BrokkAi/slopcop to \"Dave\"; your agent's changes here are checked there from now on.\n",
		"slopcop: wrong workspace? Run `slopcop workspace map <name>` here to check it in another (`slopcop workspace list` names them), then `slopcop repo disable BrokkAi/slopcop --workspace \"Dave\"` to remove it from \"Dave\".\n",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	}
	// Nothing is asked, the added line already names where checks here run,
	// and an empty first check prints nothing more.
	for _, unwanted := range []string{"Which one should check", "Type ", "[y/N]", "chosen by", "now check in", "patch is empty", "may not be pushed"} {
		if strings.Contains(stderr, unwanted) {
			t.Errorf("stderr = %q, want no %q", stderr, unwanted)
		}
	}
	if strings.Contains(stdout, "No new findings") {
		t.Errorf("stdout = %q, want no report of an empty first check", stdout)
	}
	if len(svc.checks) != 1 || svc.checks[0]["addRepository"] != true {
		t.Errorf("checks = %v, want one check that adds the repository", svc.checks)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if mapped, ok := cfg.WorkspaceFor(server.URL, "https://github.com/BrokkAi/slopcop"); !ok || mapped.ID != personalID {
		t.Errorf("mapping = %v, %v, want the checkout mapped to the workspace it was added to", mapped, ok)
	}
}

const aloneWhoamiBody = `{
	"account": {"id": "a", "username": "dave", "displayName": "Dave", "email": "dave@example.com"},
	"workspace": {"id": "` + personalID + `", "name": "Dave", "kind": "personal", "role": "owner", "current": true},
	"plan": "free",
	"workspaces": [
		{"id": "` + personalID + `", "name": "Dave", "kind": "personal", "role": "owner", "current": true}
	]
}`

func TestEnableWithOneWorkspaceSaysOneLine(t *testing.T) {
	isolateUserConfig(t)
	realRepositoryCheck(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	svc := &enableService{alone: true}
	server := svc.start(t, repo)
	pipedStdin(t, "")

	code, stdout, stderr := runCLI(t, "enable", "--root", repo, "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	// The test's own service is named first, as any other service would be,
	// by 'enable' and again by its first check.
	want := strings.Repeat("slopcop: using service "+server.URL+"\n", 2) +
		"slopcop: added BrokkAi/slopcop to \"Dave\"; your agent's changes here are checked there from now on. `slopcop repo disable BrokkAi/slopcop` undoes that.\n"
	if stderr != want || stdout != "" {
		t.Errorf("stderr = %q, stdout = %q, want only %q", stderr, stdout, want)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if mapped, ok := cfg.WorkspaceFor(server.URL, "https://github.com/BrokkAi/slopcop"); !ok || mapped.ID != personalID {
		t.Errorf("mapping = %v, %v, want the checkout mapped to the only workspace", mapped, ok)
	}
}

func TestEnableReportsAFirstCheckThatHasAChange(t *testing.T) {
	isolateUserConfig(t)
	realRepositoryCheck(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	if err := os.WriteFile(filepath.Join(repo, "change.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := &enableService{alone: true}
	server := svc.start(t, repo)
	pipedStdin(t, "")

	code, stdout, stderr := runCLI(t, "enable", "--root", repo, "--service", server.URL)
	if code != 0 || !strings.Contains(stdout, "No new findings") {
		t.Errorf("exit = %d, stdout = %q, stderr = %q, want the first check's report", code, stdout, stderr)
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
	// A fresh list for each service, which enabling changes.
	disabled := func() []service.Repository {
		return []service.Repository{{
			ID: widgetsID, Provider: "github", FullName: "BrokkAi/slopcop", Enabled: false, Active: false,
		}}
	}

	t.Run("at a terminal, without typing its name", func(t *testing.T) {
		svc := &enableService{repositories: disabled()}
		server := svc.start(t, repo)
		typed(t, "")
		code, stdout, stderr := runCLI(t, "enable", "--root", repo, "--service", server.URL)
		if code != 0 || !strings.Contains(stdout, "slopcop: enabled BrokkAi/slopcop in \"Dave\"") || strings.Contains(stderr, "Type ") {
			t.Errorf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
		}
		if len(svc.states) != 1 || svc.states[0]["enabled"] != true {
			t.Errorf("states = %v, want the repository enabled", svc.states)
		}
	})

	t.Run("not without a terminal", func(t *testing.T) {
		svc := &enableService{repositories: disabled()}
		server := svc.start(t, repo)
		pipedStdin(t, "")
		code, _, stderr := runCLI(t, "enable", "--root", repo, "--service", server.URL)
		if code != 2 || !strings.Contains(stderr, "BrokkAi/slopcop was disabled in your SlopCop workspace; turning it back on is a step a person takes") {
			t.Errorf("exit = %d, stderr = %q", code, stderr)
		}
		if len(svc.checks) != 0 || len(svc.states) != 0 {
			t.Errorf("checks = %d, states = %d, want none", len(svc.checks), len(svc.states))
		}
	})
}

func TestEnableUsesTheWorkspaceFlag(t *testing.T) {
	isolateUserConfig(t)
	realRepositoryCheck(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	svc := &enableService{}
	server := svc.start(t, repo)
	pipedStdin(t, "")

	code, _, stderr := runCLI(t, "enable", "--root", repo, "-w", "Acme team", "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	// The listing in Acme team, a look at whether the personal workspace
	// already has the repository, then the check's requests in Acme team.
	if len(svc.workspaces) < 3 || !slices.Equal(svc.workspaces[:2], []string{teamID, personalID}) ||
		slices.ContainsFunc(svc.workspaces[2:], func(workspace string) bool { return workspace != teamID }) {
		t.Errorf("workspaces = %v, want Acme team, the personal one, then Acme team alone", svc.workspaces)
	}
	if !strings.Contains(stderr, `slopcop: added BrokkAi/slopcop to "Acme team"; your agent's changes here are checked there from now on.`) {
		t.Errorf("stderr = %q", stderr)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if mapped, ok := cfg.WorkspaceFor(server.URL, "https://github.com/BrokkAi/slopcop"); !ok || mapped.ID != teamID {
		t.Errorf("mapping = %v, %v, want the checkout mapped to Acme team", mapped, ok)
	}
}

func TestEnableSaysWhereTheRepositoryAlreadyIs(t *testing.T) {
	isolateUserConfig(t)
	realRepositoryCheck(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")

	t.Run("adding it a second time is said first", func(t *testing.T) {
		svc := &enableService{inTeam: []service.Repository{{
			ID: widgetsID, Provider: "github", FullName: "BrokkAi/slopcop", Enabled: true, Active: true,
		}}}
		server := svc.start(t, repo)
		pipedStdin(t, "")
		code, _, stderr := runCLI(t, "enable", "--root", repo, "--service", server.URL)
		warning := `slopcop: BrokkAi/slopcop is also in "Acme team"; adding it to "Dave" too checks and bills it in each.`
		added := `slopcop: added BrokkAi/slopcop to "Dave"`
		if code != 0 || !strings.Contains(stderr, warning) || strings.Index(stderr, warning) > strings.Index(stderr, added) {
			t.Errorf("exit = %d, stderr = %q, want the warning before the add", code, stderr)
		}
		if len(svc.checks) != 1 {
			t.Errorf("checks = %v, want the first check", svc.checks)
		}
	})

	t.Run("a workspace that does not answer is named", func(t *testing.T) {
		svc := &enableService{teamFails: true}
		server := svc.start(t, repo)
		pipedStdin(t, "")
		_, _, stderr := runCLI(t, "enable", "--root", repo, "--service", server.URL)
		unknown := `slopcop: couldn't tell whether "Acme team" already has BrokkAi/slopcop.`
		if !strings.Contains(stderr, unknown) || strings.Index(stderr, unknown) > strings.Index(stderr, "slopcop: added") {
			t.Errorf("stderr = %q, want %q before the add", stderr, unknown)
		}
	})
}

func TestWorkspaceSelectorUsesTheIDForASharedName(t *testing.T) {
	workspaces := []service.Workspace{{ID: personalID, Name: "Dave"}, {ID: teamID, Name: "Acme"}, {ID: widgetsID, Name: "acme"}}
	if got := workspaceSelector(workspaces, workspaces[1]); got != teamID {
		t.Errorf("selector = %q, want the id of a workspace whose name another shares", got)
	}
	if got := workspaceSelector(workspaces, workspaces[0]); got != "Dave" {
		t.Errorf("selector = %q, want the name", got)
	}
}

func TestCheckSaysWhenItAddedARepository(t *testing.T) {
	isolateUserConfig(t)
	realRepositoryCheck(t)
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

// TestIncompleteReviewFailsUnderFailOnNever checks that findings from one
// reviewer, while fewer than review.minCompleted reviewers finished, still
// exit 2 when --fail-on never lets the findings themselves exit 0.
func TestIncompleteReviewFailsUnderFailOnNever(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "")
	writeFile(t, repo, "README.md", "changed\n")
	t.Setenv("SLOPCOP_FAKE_ACP", "1")
	fake := func(mode string) config.Adapter {
		return config.Adapter{Command: []string{os.Args[0], "-test.run=^TestFakeAgent$", "--fake-mode=" + mode}}
	}
	cfg := config.Config{Mode: "ai", Review: &config.Review{Reviewers: []string{"terra", "flash"}, Adapters: map[string]config.Adapter{"codex": fake("findings"), "dsh": fake("empty")}}}
	if _, err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runCLI(t, "check", "--root", repo, "--format", "json", "--fail-on", "never")
	if code != 2 || !strings.Contains(out, `"incomplete": true`) {
		t.Fatalf("exit %d, stdout %s stderr %s", code, out, stderr)
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
			cfg := config.Config{Mode: "ai", Review: &config.Review{Reviewers: []string{"terra"}, Adapters: map[string]config.Adapter{"codex": {Command: []string{os.Args[0], "-test.run=^TestFakeAgent$", "--fake-mode=" + tc.mode}}}}}
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
				// -w is reused from check and ignored: an override is local.
				code, out, stderr = runCLI(t, "override", "--root", repo, "-w", "Acme team", "--reason", "false positive")
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
	cfg := config.Config{Mode: "both", Review: &config.Review{Reviewers: []string{"terra"}, Adapters: map[string]config.Adapter{"codex": {Command: []string{os.Args[0], "-test.run=^TestFakeAgent$"}}}}}
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

func TestBothReviewDoesNotRunForADisabledRepository(t *testing.T) {
	isolateUserConfig(t)
	realRepositoryCheck(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "README.md", "changed\n")
	t.Setenv("SLOPCOP_FAKE_ACP", "1")
	cfg := config.Config{Mode: "both", Review: &config.Review{Reviewers: []string{"terra"}, Adapters: map[string]config.Adapter{"codex": {Command: []string{os.Args[0], "-test.run=^TestFakeAgent$"}}}}}
	if _, err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	svc := &enableService{repositories: []service.Repository{{
		ID: widgetsID, Provider: "github", FullName: "BrokkAi/slopcop", Enabled: false, Active: false,
	}}}
	server := svc.start(t, repo)
	pipedStdin(t, "")
	code, out, stderr := runCLI(t, "check", "--root", repo, "--format", "json", "--service", server.URL)
	if code != service.ExitNeedsPerson {
		t.Fatalf("exit %d out %s stderr %s", code, out, stderr)
	}
	if strings.Contains(stderr, "reviewing with") {
		t.Errorf("reviewers started for a refused check: %s", stderr)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(out), &document); err != nil {
		t.Fatal(err)
	}
	if _, has := document["review"]; has || document["status"] != "failed" ||
		document["code"] != repositoryDisabledCode || document["reviewSkipped"] != "repositoryInactive" {
		t.Errorf("document = %v", document)
	}
	if len(svc.checks) != 0 {
		t.Errorf("checks = %v, want none", svc.checks)
	}

	// --if-enabled skips only the static half: the local review needs no
	// active repository, so an automatic check still gets it.
	code, out, stderr = runCLI(t, "check", "--root", repo, "--if-enabled", "--format", "json", "--service", server.URL)
	if code != 0 || !strings.Contains(stderr, "reviewing with") {
		t.Fatalf("exit %d out %s stderr %s", code, out, stderr)
	}
	document = nil
	if err := json.Unmarshal([]byte(out), &document); err != nil {
		t.Fatal(err)
	}
	if _, has := document["review"]; !has || document["status"] != "skipped" {
		t.Errorf("document = %v", document)
	}
}

func TestARefusedCheckWithdrawsTheStaticPassFromOverride(t *testing.T) {
	isolateUserConfig(t)
	realRepositoryCheck(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "README.md", "changed\n")
	t.Setenv("SLOPCOP_FAKE_ACP", "1")
	cfg := config.Config{Mode: "both", Review: &config.Review{Reviewers: []string{"terra"}, Adapters: map[string]config.Adapter{"codex": {Command: []string{os.Args[0], "-test.run=^TestFakeAgent$", "--fake-mode=findings"}}}}}
	if _, err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	svc := &enableService{repositories: []service.Repository{{
		ID: widgetsID, Provider: "github", FullName: "BrokkAi/slopcop", Enabled: true, Active: true,
	}}}
	server := svc.start(t, repo)
	pipedStdin(t, "")
	if code, out, stderr := runCLI(t, "check", "--root", repo, "--format", "json", "--service", server.URL); code != service.ExitFindings {
		t.Fatalf("first check exit %d out %s stderr %s", code, out, stderr)
	}
	svc.repositories[0].Enabled, svc.repositories[0].Active = false, false
	if code, out, stderr := runCLI(t, "check", "--root", repo, "--format", "json", "--service", server.URL); code != service.ExitNeedsPerson {
		t.Fatalf("refused check exit %d out %s stderr %s", code, out, stderr)
	}
	if code, out, stderr := runCLI(t, "override", "--root", repo, "--reason", "false positive"); code == 0 {
		t.Fatalf("override after a refused check succeeded: %s %s", out, stderr)
	}
}

func TestCombinedExitAndStatus(t *testing.T) {
	cases := []struct {
		name, static, agent, failOn, wantStatus, wantStatic string
		wantExit                                            int
		// wantReason is the JSON exitReason, set only for exit 1.
		wantReason string
	}{
		{"static findings review clean", "findings", "clean", "finding", "findings", "", 1, "static_findings"},
		{"static clean review findings", "clean", "findings", "finding", "findings", "clean", 1, "review_findings"},
		{"static findings review findings", "findings", "findings", "finding", "findings", "", 1, "static_and_review_findings"},
		{"person review findings", "person", "findings", "finding", "findings", "failed", 1, "review_findings"},
		{"person review clean", "person", "clean", "finding", "failed", "", 4, ""},
		{"static clean review failed", "clean", "empty", "finding", "incomplete", "clean", 2, ""},
		{"static pending review clean", "pending", "clean", "finding", "pending", "", 3, ""},
		{"static pending review failed", "pending", "empty", "finding", "pending", "", 3, ""},
		{"fail never review findings", "clean", "findings", "never", "findings", "clean", 0, ""},
		{"static incomplete review clean", "incomplete", "clean", "finding", "incomplete", "", 2, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateUserConfig(t)
			repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
			writeFile(t, repo, "README.md", "changed\n")
			t.Setenv("SLOPCOP_FAKE_ACP", "1")
			cfg := config.Config{Mode: "both", Review: &config.Review{Reviewers: []string{"terra"}, Adapters: map[string]config.Adapter{"codex": {Command: []string{os.Args[0], "-test.run=^TestFakeAgent$", "--fake-mode=" + tc.agent}}}}}
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
				ExitReason   string `json:"exitReason"`
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
			if doc.ExitReason != tc.wantReason {
				t.Fatalf("exitReason %q want %q", doc.ExitReason, tc.wantReason)
			}
			// A failure the review caused is said on stderr, since the
			// summary's static counts do not show it.
			explained := strings.Contains(stderr, "slopcop: the check failed (exit 1)") && strings.Contains(stderr, "review.findings")
			if explained != strings.Contains(tc.wantReason, "review") {
				t.Fatalf("review failure explained = %v for reason %q: stderr %s", explained, tc.wantReason, stderr)
			}
			if tc.agent == "empty" && doc.Review.Status != "failed" {
				t.Fatalf("failed review hidden: %+v", doc)
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
	cfg := config.Config{Mode: "ai", Review: &config.Review{Reviewers: []string{"terra"}, Adapters: map[string]config.Adapter{"codex": {Command: []string{os.Args[0], "-test.run=^TestFakeAgent$", "--fake-mode=hang", "--fake-trace=" + trace}}}}}
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
	cfg := config.Config{Mode: "both", Review: &config.Review{Reviewers: []string{"terra"}, Adapters: map[string]config.Adapter{"codex": {Command: []string{os.Args[0], "-test.run=^TestFakeAgent$", "--fake-mode=findings"}}}}}
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

func TestPushApprovalLevel(t *testing.T) {
	for _, tc := range []struct {
		name, severity, level, failOn string
		approved                      bool
		exit                          int
	}{
		{"warning advisory", "warning", "", "finding", true, 1},
		{"info advisory default error level", "info", "", "finding", true, 1},
		{"info advisory finding level", "info", "finding", "finding", true, 1},
		{"info advisory note level", "info", "note", "finding", true, 1},
		{"info advisory warning level", "info", "warning", "finding", true, 1},
		{"info advisory error level", "info", "error", "finding", true, 1},
		{"info advisory never level", "info", "never", "finding", true, 1},
		{"error blocks", "error", "", "finding", false, 1},
		{"unknown severity blocks", "mystery", "", "finding", false, 1},
		{"warning threshold", "warning", "warning", "finding", false, 1},
		{"never threshold", "error", "never", "finding", true, 1},
		{"finding threshold", "note", "finding", "finding", false, 1},
		{"fail never does not approve", "error", "", "never", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateUserConfig(t)
			repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
			writeFile(t, repo, "README.md", "changed\n")
			t.Setenv("SLOPCOP_FAKE_ACP", "1")
			cfg := config.Config{Mode: "both", Review: &config.Review{Reviewers: []string{"terra"}, Adapters: map[string]config.Adapter{"codex": {Command: []string{os.Args[0], "-test.run=^TestFakeAgent$", "--fake-mode=clean"}}}}}
			if tc.level != "" {
				cfg.Gate = &config.Gate{BlockOn: tc.level}
			}
			if _, err := config.Save(cfg); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				status := "findings"
				if tc.failOn == "never" {
					status = "clean"
				}
				_, _ = fmt.Fprintf(w, `{"status":%q,"baseCommit":"0123456789abcdef0123456789abcdef01234567","applied":true,"summary":{"files":1,"newFindings":1,"failOn":%q},"findings":[{"policyId":"p","severity":%q,"new":true,"path":"README.md","title":"issue"}],"incomplete":[]}`, status, tc.failOn, tc.severity)
			}))
			defer server.Close()
			code, out, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL, "--format", "json", "--timeout", "50ms", "--fail-on", tc.failOn)
			if code != tc.exit {
				t.Fatalf("exit %d want %d: %s %s", code, tc.exit, out, stderr)
			}
			var doc struct {
				Approved bool `json:"approved"`
			}
			if err := json.Unmarshal([]byte(out), &doc); err != nil {
				t.Fatal(err)
			}
			if doc.Approved != tc.approved {
				t.Fatalf("approved=%v want %v: %s", doc.Approved, tc.approved, out)
			}
			snapshot, err := gitx.CaptureWorkingTree(repo, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = gitx.Run(repo, "show-ref", "--verify", "--quiet", "refs/slopcop/reviewed/"+snapshot.Tree)
			if (err == nil) != tc.approved {
				t.Fatalf("approval ref=%v want %v", err == nil, tc.approved)
			}
			if tc.name == "warning advisory" || tc.name == "error blocks" || strings.Contains(tc.name, "info advisory") {
				// The push line is printed only where init installed the
				// user-level gate.
				installUserGate(t)
				_, _, humanErr := runCLI(t, "check", "--root", repo, "--service", server.URL, "--format", "human", "--timeout", "50ms")
				want := "may not be pushed yet: 1 static finding(s) at or above error."
				if tc.approved {
					want = "may be pushed: it passed the check."
				}
				if !strings.Contains(humanErr, want) {
					t.Fatalf("human stderr %q missing %q", humanErr, want)
				}
			}
		})
	}
}

func TestInvalidPushApprovalConfig(t *testing.T) {
	isolateUserConfig(t)
	cfg := config.Config{Gate: &config.Gate{BlockOn: "bogus"}}
	path, err := config.Save(cfg)
	if err != nil {
		t.Fatal(err)
	}
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	code, _, stderr := runCLI(t, "check", "--root", repo, "--static")
	if code == 0 || !strings.Contains(stderr, path) {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	code, _, stderr = runCLI(t, "config", "set", "gate.blockOn", "error")
	if code == 0 || !strings.Contains(stderr, path) || !strings.Contains(stderr, "edited by hand") {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
}

func TestOverrideStaticPushLevel(t *testing.T) {
	for _, tc := range []struct {
		severity string
		allowed  bool
	}{{"warning", true}, {"error", false}} {
		t.Run(tc.severity, func(t *testing.T) {
			isolateUserConfig(t)
			repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
			writeFile(t, repo, "README.md", "changed\n")
			t.Setenv("SLOPCOP_FAKE_ACP", "1")
			cfg := config.Config{Mode: "both", Review: &config.Review{Reviewers: []string{"terra"}, Adapters: map[string]config.Adapter{"codex": {Command: []string{os.Args[0], "-test.run=^TestFakeAgent$", "--fake-mode=findings"}}}}}
			if _, err := config.Save(cfg); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"status":"findings","baseCommit":"0123456789abcdef0123456789abcdef01234567","applied":true,"summary":{"files":1,"newFindings":1},"findings":[{"policyId":"p","severity":%q,"new":true,"path":"README.md","title":"issue"}],"incomplete":[]}`, tc.severity)
			}))
			defer server.Close()
			code, out, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL, "--format", "json")
			if code != 1 {
				t.Fatalf("check exit %d: %s %s", code, out, stderr)
			}
			code, out, stderr = runCLI(t, "override", "--root", repo, "--reason", "review finding is wrong")
			if (code == 0) != tc.allowed {
				t.Fatalf("override exit %d: %s %s", code, out, stderr)
			}
			if !tc.allowed && !strings.Contains(stderr, "static finding") {
				t.Fatalf("reason: %s", stderr)
			}
		})
	}
}

func TestLaterReviewWithdrawsApproval(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "")
	writeFile(t, repo, "README.md", "changed\n")
	t.Setenv("SLOPCOP_FAKE_ACP", "1")
	adapter := config.Adapter{Command: []string{os.Args[0], "-test.run=^TestFakeAgent$"}}
	cfg := config.Config{Mode: "ai", Review: &config.Review{Reviewers: []string{"terra"}, Adapters: map[string]config.Adapter{"codex": adapter}}}
	if _, err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runCLI(t, "check", "--root", repo, "--format", "json")
	if code != 0 {
		t.Fatalf("approval check: %d %s %s", code, out, stderr)
	}
	snapshot, err := gitx.CaptureWorkingTree(repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	ref := "refs/slopcop/reviewed/" + snapshot.Tree
	if _, err = gitx.Run(repo, "show-ref", "--verify", "--quiet", ref); err != nil {
		t.Fatal(err)
	}
	cfg.Review.Adapters["codex"] = config.Adapter{Command: []string{os.Args[0], "-test.run=^TestFakeAgent$", "--fake-mode=findings"}}
	if _, err = config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	code, out, stderr = runCLI(t, "check", "--root", repo, "--format", "json")
	if code != 1 {
		t.Fatalf("findings check: %d %s %s", code, out, stderr)
	}
	if _, err = gitx.Run(repo, "show-ref", "--verify", "--quiet", ref); err == nil {
		t.Fatal("stale approval ref")
	}
}

func TestEmptyCheckPreservesApproval(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "")
	t.Setenv("SLOPCOP_FAKE_ACP", "1")
	cfg := config.Config{Mode: "ai", Review: &config.Review{Reviewers: []string{"terra"}, Adapters: map[string]config.Adapter{"codex": {Command: []string{os.Args[0], "-test.run=^TestFakeAgent$"}}}}}
	if _, err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	snapshot, err := gitx.CaptureWorkingTree(repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	ref := "refs/slopcop/reviewed/" + snapshot.Tree
	gitAt(t, repo, "update-ref", ref, snapshot.Tree)
	_, _, _ = runCLI(t, "check", "--root", repo, "--format", "json")
	if _, err = gitx.Run(repo, "show-ref", "--verify", "--quiet", ref); err != nil {
		t.Fatalf("empty check removed approval: %v", err)
	}
}

func TestStaticApprovalWithdrawnByBothReview(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "README.md", "changed\n")
	t.Setenv("SLOPCOP_FAKE_ACP", "1")
	cfg := config.Config{Mode: "both", Review: &config.Review{Reviewers: []string{"terra"}, Adapters: map[string]config.Adapter{"codex": {Command: []string{os.Args[0], "-test.run=^TestFakeAgent$", "--fake-mode=findings"}}}}}
	if _, err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"clean","baseCommit":"0123456789abcdef0123456789abcdef01234567","applied":true,"summary":{"files":1,"newFindings":0},"findings":[]}`))
	}))
	defer server.Close()
	code, out, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL, "--mode", "static", "--format", "json")
	if code != 0 {
		t.Fatalf("static: %d %s %s", code, out, stderr)
	}
	snapshot, err := gitx.CaptureWorkingTree(repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	ref := "refs/slopcop/reviewed/" + snapshot.Tree
	if _, err = gitx.Run(repo, "show-ref", "--verify", "--quiet", ref); err != nil {
		t.Fatal(err)
	}
	code, out, stderr = runCLI(t, "check", "--root", repo, "--service", server.URL, "--mode", "both", "--format", "json")
	if code != 1 {
		t.Fatalf("both: %d %s %s", code, out, stderr)
	}
	if _, err = gitx.Run(repo, "show-ref", "--verify", "--quiet", ref); err == nil {
		t.Fatal("stale static approval")
	}
}

// TestPushApprovalWithIncompleteRules covers an incomplete static result with
// no findings and a clean review: a rule that could not answer for a cause of
// its own does not block the push, but a run failure, which left the whole
// analyzer run without an answer, does, and the push line says why.
func TestPushApprovalWithIncompleteRules(t *testing.T) {
	for _, tc := range []struct {
		name, incomplete, runFailures string
		approved                      bool
	}{
		{"own cause approves", `[{"policyId":"p","reason":"unsupported"}]`, `[]`, true},
		{"run failure blocks", `[{"policyId":"p","reason":"unreliable","detail":"type=incomplete reasons=analyzer_process_failed exit_code=137","scope":"run"}]`,
			`[{"reason":"unreliable","detail":"type=incomplete reasons=analyzer_process_failed exit_code=137","policies":1}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateUserConfig(t)
			repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
			writeFile(t, repo, "README.md", "changed\n")
			t.Setenv("SLOPCOP_FAKE_ACP", "1")
			cfg := config.Config{Mode: "both", Review: &config.Review{Reviewers: []string{"terra"}, Adapters: map[string]config.Adapter{"codex": {Command: []string{os.Args[0], "-test.run=^TestFakeAgent$", "--fake-mode=clean"}}}}}
			if _, err := config.Save(cfg); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"status":"incomplete","baseCommit":"0123456789abcdef0123456789abcdef01234567","applied":true,"summary":{"files":1,"incompletePolicies":1,"failOn":"finding"},"findings":[],"incomplete":%s,"runFailures":%s}`, tc.incomplete, tc.runFailures)
			}))
			defer server.Close()
			_, out, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL, "--format", "json", "--timeout", "50ms")
			var doc struct {
				Approved bool `json:"approved"`
			}
			if err := json.Unmarshal([]byte(out), &doc); err != nil {
				t.Fatalf("%v: %s %s", err, out, stderr)
			}
			if doc.Approved != tc.approved {
				t.Fatalf("approved=%v want %v: %s", doc.Approved, tc.approved, out)
			}
			// json too says how to get past a run that keeps failing.
			if notice := "`slopcop override --reason"; strings.Contains(stderr, notice) == tc.approved {
				t.Fatalf("run failure notice in stderr = %v, want %v: %s", !tc.approved, !tc.approved, stderr)
			}
			installUserGate(t)
			_, _, humanErr := runCLI(t, "check", "--root", repo, "--service", server.URL, "--format", "human", "--timeout", "50ms")
			want := "may be pushed: it passed the check."
			if !tc.approved {
				want = "may not be pushed yet: the static analyzer run failed (unreliable), so it checked nothing; run the check again, and if it keeps failing, accept the tree with slopcop override --reason."
			}
			if !strings.Contains(humanErr, want) {
				t.Fatalf("push line: want %q in %s", want, humanErr)
			}
			if tc.approved {
				return
			}
			// BrokkAi/slopcop#236: a run that keeps failing can be accepted
			// for this tree, with a reason, and then the tree is approved.
			code, overrideOut, overrideErr := runCLI(t, "override", "--root", repo, "--reason", "the analyzer run fails on every check")
			if code != 0 || !strings.Contains(overrideOut, "accepted the static check whose analyzer run failed for tree") {
				t.Fatalf("override: exit = %d, stdout = %q, stderr = %q", code, overrideOut, overrideErr)
			}
			tree, err := gitx.WorkingTreeID(repo)
			if err != nil {
				t.Fatal(err)
			}
			if ok, err := review.Reviewed(repo, tree); err != nil || !ok {
				t.Fatalf("tree not approved after override: %v %v", ok, err)
			}
		})
	}
}

// TestHumanQuotesTheSpanFromTheCheckedTree covers
// BrokkAi/bifrost-service#1143: a finding's span is quoted from the files
// the check uploaded, so an edit made while the check runs does not move it.
func TestHumanQuotesTheSpanFromTheCheckedTree(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "lib/feed.ts", "const x = segmnet.length;\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeFile(t, repo, "lib/feed.ts", "// edited while the check ran\n")
		_, _ = w.Write([]byte(`{"status":"findings","summary":{"newFindings":1,"failOn":"finding"},"findings":[{"policyId":"acme.misspelled-identifier","severity":"warning","certainty":"definite","path":"lib/feed.ts","startLine":1,"endLine":1,"startColumn":11,"endColumn":18,"title":"an identifier may be misspelled","message":"Seen once in this repository.","new":true}],"incomplete":[]}`))
	}))
	defer server.Close()
	code, stdout, stderr := runCLI(t, "check", "--mode", "static", "--root", repo, "--service", server.URL)
	want := "    Message: Seen once in this repository.\n    Location: lib/feed.ts:1:11\n    Code: `segmnet`\n"
	if code != 1 || !strings.Contains(stdout, want) {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
}
