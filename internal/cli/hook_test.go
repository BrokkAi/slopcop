package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func runTestHook(t *testing.T, harness, serviceURL, payload string) (hookOutput, string) {
	t.Helper()
	command := &cobra.Command{}
	command.SetContext(context.Background())
	command.SetIn(strings.NewReader(payload))
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	if err := runHook(command, harness, serviceURL, "test"); err != nil {
		t.Fatalf("runHook: %v", err)
	}
	var output hookOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("stdout is not hook JSON: %v\n%s", err, stdout.String())
	}
	return output, stdout.String()
}

func hookPayload(repo, session string, active bool) string {
	return fmt.Sprintf(`{"hook_event_name":"Stop","cwd":%q,"session_id":%q,"stop_hook_active":%t}`, repo, session, active)
}

func serveCheck(t *testing.T, repo, status string, calls *int) *httptest.Server {
	t.Helper()
	base := runGit(t, repo, "rev-parse", "HEAD")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		if r.URL.Path != "/v1/check" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch status {
		case "findings":
			_, _ = fmt.Fprintf(w, `{"status":"findings","baseCommit":%q,"applied":true,"summary":{"files":1,"findings":1,"newFindings":1,"incompletePolicies":0,"failOn":"finding"},"findings":[{"id":"7d9f9584-b2db-4bd2-a9df-7982d40f2e11","policyId":"demo.policy","severity":"warning","certainty":"definite","path":"demo.go","startLine":1,"title":"Fix this finding","new":true}],"incomplete":[]}`, base)
		default:
			_, _ = fmt.Fprintf(w, `{"status":"clean","baseCommit":%q,"applied":true,"summary":{"files":0,"findings":0,"newFindings":0,"incompletePolicies":0,"failOn":"finding"},"findings":[],"incomplete":[]}`, base)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestHookChecksChangedTreesWithoutLooping(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	calls := 0
	server := serveCheck(t, repo, "findings", &calls)

	output, _ := runTestHook(t, "codex", server.URL, hookPayload(repo, "session-one", false))
	if output.Decision != "block" || !strings.Contains(output.Reason, "Fix this finding") {
		t.Fatalf("output = %+v", output)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}

	output, _ = runTestHook(t, "codex", server.URL, hookPayload(repo, "session-one", false))
	if output != (hookOutput{}) || calls != 1 {
		t.Fatalf("unchanged output = %+v, calls = %d", output, calls)
	}

	writeFile(t, repo, "demo.go", "package demo\n")
	output, _ = runTestHook(t, "codex", server.URL, hookPayload(repo, "session-one", true))
	if output.Decision != "" || !strings.Contains(output.SystemMessage, "will not request another continuation") || calls != 2 {
		t.Fatalf("changed output = %+v, calls = %d", output, calls)
	}
}

func TestManualCheckSuppressesTheSameHookCheck(t *testing.T) {
	isolateUserConfig(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "session-one")
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	calls := 0
	server := serveCheck(t, repo, "clean", &calls)

	code, _, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL)
	if code != 0 {
		t.Fatalf("check exit = %d, stderr = %s", code, stderr)
	}
	output, _ := runTestHook(t, "claude", server.URL, hookPayload(repo, "session-one", false))
	if output != (hookOutput{}) || calls != 1 {
		t.Fatalf("output = %+v, calls = %d", output, calls)
	}
}

func TestHookFailuresAreNonblockingAndNotCached(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"auth_required","message":"Sign in.","remedy":"Authenticate.","retryable":false}`))
	}))
	defer server.Close()

	for i := 0; i < 2; i++ {
		output, _ := runTestHook(t, "codex", server.URL, hookPayload(repo, "session-one", false))
		if output.Decision != "" || !strings.Contains(output.SystemMessage, "Authenticate.") {
			t.Fatalf("output = %+v", output)
		}
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}

	output, _ := runTestHook(t, "codex", server.URL, `{not json`)
	if output.Decision != "" || !strings.Contains(output.SystemMessage, "could not read") || calls != 2 {
		t.Fatalf("malformed output = %+v, calls = %d", output, calls)
	}
}

func TestHookCacheFailureDoesNotBlockARecheck(t *testing.T) {
	isolateUserConfig(t)
	// A file where the cache directory belongs makes every cache write fail.
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cache), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	calls := 0
	server := serveCheck(t, repo, "findings", &calls)

	for _, active := range []bool{false, true} {
		output, _ := runTestHook(t, "codex", server.URL, hookPayload(repo, "session-one", active))
		if active && output.Decision != "" {
			t.Fatalf("continued hook blocked again: %+v", output)
		}
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestBoundedHookText(t *testing.T) {
	got := boundedHookText(strings.Repeat("界", maxHookOutputRunes+1))
	if len([]rune(got)) != maxHookOutputRunes || !strings.HasSuffix(got, "…") {
		t.Fatalf("bounded length = %d", len([]rune(got)))
	}
}
