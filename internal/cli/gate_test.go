package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/service"
)

func TestGatePushCommands(t *testing.T) {
	for _, tc := range []struct {
		cmd  string
		want bool
	}{
		{"git push", true},
		{"git -C . push origin main", true},
		{"gh pr create", true},
		{"echo hello", false},
		{"git status; echo push", false},
		{"echo git push", true},
		{"git -c core.sshCommand=ssh push", true},
		{`git -C "my dir" -c user.name=x --no-pager push --force`, true},
		{"git --git-dir=.git --work-tree . push", true},
		{"/usr/bin/git push", true},
		{"git add . && git commit -m wip && git push", true},
		{"git fetch; git push origin HEAD", true},
		{"cd x || git push", true},
		{"git status\ngit push", true},
		{"(git push)", true},
		{"git push&& echo done", true},
		{"git status | gh pr create --fill", true},
		{`git commit -m "fix push retry"`, false},
		{"git stash push", false},
		{"git stash push -m wip", false},
		{"git add internal/push.go", false},
		{"git log --grep=push", false},
		{"git log --grep push", false},
		{"git -C push status", false},
		{"git config push.default simple", false},
		{"git help push", false},
		{"git pushx", false},
		{"git-lfs push", false},
		{"git status && echo push", false},
		{"git status | grep push", false},
	} {
		if got := pushCommand(tc.cmd); got != tc.want {
			t.Errorf("%q: got %v", tc.cmd, got)
		}
	}
}
func TestGateReviewedTree(t *testing.T) {
	repo := testInitRepo(t)
	gitAt(t, repo, "config", "user.name", "Test")
	gitAt(t, repo, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "a"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, repo, "add", "a")
	gitAt(t, repo, "commit", "-m", "a")
	cmd := newGateCommand()
	cmd.SetArgs([]string{"--root", repo})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("an unarmed repository was blocked: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".git", "slopcop"), 0700); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	cmd = newGateCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"--root", repo})
	err := cmd.Execute()
	if _, ok := err.(silentExit); !ok || !strings.Contains(errOut.String(), "push blocked") {
		t.Fatalf("err %v, stderr %s", err, errOut.String())
	}
	tree := gitAt(t, repo, "rev-parse", "HEAD^{tree}")
	gitAt(t, repo, "update-ref", "refs/slopcop/reviewed/"+tree, tree)
	errOut.Reset()
	cmd = newGateCommand()
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"--root", repo})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
}
func TestGateHookPayload(t *testing.T) {
	repo := testInitRepo(t)
	for _, tc := range []struct {
		payload string
		blocked bool
	}{{fmt.Sprintf(`{"cwd":%q,"tool_name":"Read","tool_input":{"command":"git push"}}`, repo), false}, {fmt.Sprintf(`{"cwd":%q,"tool_name":"Bash","tool_input":{"command":"echo hi"}}`, repo), false}, {`not-json`, true}, {`{"cwd":"/no/such/repo","tool_name":"Bash","tool_input":{"command":"git push"}}`, false}} {
		cmd := newGateCommand()
		cmd.SetIn(strings.NewReader(tc.payload))
		cmd.SetArgs([]string{"--hook", "claude"})
		var errOut bytes.Buffer
		cmd.SetErr(&errOut)
		err := cmd.Execute()
		if (err != nil) != tc.blocked {
			t.Errorf("payload %s: err %v", tc.payload, err)
		}
	}
}

func TestClaudeGateDefersToGrok(t *testing.T) {
	repo := testInitRepo(t)
	gitAt(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--allow-empty", "-q", "-m", "init")
	if err := os.MkdirAll(filepath.Join(repo, ".git", "slopcop"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROK_HOOK_EVENT", "PreToolUse")
	payload, _ := json.Marshal(map[string]any{
		"cwd": repo, "tool_name": "Bash", "tool_input": map[string]string{"command": "git push"},
	})
	cmd := newGateCommand()
	cmd.SetIn(bytes.NewReader(payload))
	cmd.SetArgs([]string{"--hook", "claude"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Claude compatibility gate did not defer to Grok: %v", err)
	}
}

// TestGateBlocksForgedApprovals covers commands that would write an approval
// the check did not give, in a repository where a push itself would pass.
func TestGateBlocksForgedApprovals(t *testing.T) {
	repo := testInitRepo(t)
	for _, tc := range []struct {
		command string
		blocked bool
	}{
		{"git update-ref refs/slopcop/reviewed/$(git rev-parse HEAD^{tree}) $(git rev-parse HEAD^{tree})", true},
		{"echo abc > .git/refs/slopcop/reviewed/abc", true},
		{`git update-ref refs\slopcop\reviewed\abc abc`, true},
		{"rm -rf .git/slopcop", true},
		{"git for-each-ref refs/slopcop", true},
		{"slopcop check --format json", false},
		{`slopcop override --reason "the finding is wrong"`, false},
		{"git log refs/heads/slopcop-fix", false},
	} {
		cmd := newGateCommand()
		payload, _ := json.Marshal(map[string]any{"cwd": repo, "tool_name": "Bash", "tool_input": map[string]string{"command": tc.command}})
		cmd.SetIn(bytes.NewReader(payload))
		cmd.SetArgs([]string{"--hook", "claude"})
		var errOut bytes.Buffer
		cmd.SetErr(&errOut)
		err := cmd.Execute()
		if (err != nil) != tc.blocked {
			t.Errorf("%s: err %v", tc.command, err)
		}
		if tc.blocked && !strings.Contains(errOut.String(), "slopcop override") {
			t.Errorf("%s: reason %q", tc.command, errOut.String())
		}
	}
}

// TestGateHookToolNames covers the tool names other than Claude Code's Bash
// that the gate guards: Codex sends Bash too, Grok sends its shell tool as
// run_terminal_command, and GitHub MCP tools push or open a pull request
// without a command. Each is blocked in a repository whose tree has no
// approval; other tools pass.
func TestGateHookToolNames(t *testing.T) {
	repo := testInitRepo(t)
	gitAt(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--allow-empty", "-q", "-m", "init")
	if err := os.MkdirAll(filepath.Join(repo, ".git", "slopcop"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		hook, tool, command string
		blocked             bool
	}{
		{"codex", "Bash", "git push origin HEAD", true},
		{"codex", "Bash", "git status", false},
		{"claude", "run_terminal_command", "git push", true},
		{"claude", "run_terminal_command", "git update-ref refs/slopcop/reviewed/x x", true},
		{"claude", "mcp__github__create_pull_request", "", true},
		{"claude", "mcp__plugin_gh_github__push_files", "", true},
		{"claude", "mcp__github__get_file_contents", "", false},
		{"claude", "BashOutput", "git push", false},
	} {
		cmd := newGateCommand()
		payload, _ := json.Marshal(map[string]any{"cwd": repo, "tool_name": tc.tool, "tool_input": map[string]string{"command": tc.command}})
		cmd.SetIn(bytes.NewReader(payload))
		cmd.SetArgs([]string{"--hook", tc.hook})
		var errOut bytes.Buffer
		cmd.SetErr(&errOut)
		err := cmd.Execute()
		if (err != nil) != tc.blocked {
			t.Errorf("%s %s %q: err %v, stderr %q", tc.hook, tc.tool, tc.command, err, errOut.String())
		}
		if tc.blocked && strings.Count(strings.TrimSpace(errOut.String()), "\n") != 0 {
			t.Errorf("%s: reason is not one line: %q", tc.tool, errOut.String())
		}
	}
}

// A pull request check fails on any new definite finding, and the push gate
// only on findings at or above gate.blockOn. advisoryFindings counts the
// findings on which the two disagree, so it must be zero exactly when the
// push level is the pull request check's.
func TestAdvisoryFindingsAreWhatOnlyThePullRequestCheckFailsOn(t *testing.T) {
	findings := []service.Finding{
		{New: true, Severity: "note", Certainty: "definite"},
		{New: true, Severity: "note"},
		{New: true, Severity: "warning", Certainty: "definite"},
		{New: true, Severity: "error", Certainty: "definite"},
		{New: true, Severity: "mystery", Certainty: "definite"},
		{New: true, Severity: "info", Certainty: "definite"},
		{New: true, Severity: "note", Certainty: "possible"},
		{New: false, Severity: "note", Certainty: "definite"},
	}
	response := service.Response{Findings: findings}
	for level, want := range map[string]int{"never": 5, "error": 3, "warning": 2, "note": 0, pullRequestFailOn: 0} {
		if got := advisoryFindings(response, level); got != want {
			t.Errorf("advisoryFindings at %s = %d, want %d", level, got, want)
		}
	}
	if pullRequestFailOn != "finding" {
		t.Fatalf("pullRequestFailOn = %q: the docs (checks/exit-codes.md, checks/pull-requests.md) and the skill say finding; change them together", pullRequestFailOn)
	}
}

// The fixture case of BrokkAi/slopcop#207: notes approve the tree at the
// default push level, and the check says a pull request check will fail.
func TestApprovedCheckWarnsOfFindingsThePullRequestCheckFailsOn(t *testing.T) {
	for _, tc := range []struct {
		name, severity, level string
		approved              bool
		advisory              int
	}{
		{"note at the default level", "note", "", true, 1},
		{"warning at the default level", "warning", "", true, 1},
		{"note at the pull request level", "note", "finding", false, 0},
		{"error", "error", "", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateUserConfig(t)
			installUserGate(t)
			repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
			writeFile(t, repo, "fixtures/policy/case.py", "def f(): pass\n")
			cfg := config.Config{Mode: "static"}
			if tc.level != "" {
				cfg.Gate = &config.Gate{BlockOn: tc.level}
			}
			if _, err := config.Save(cfg); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"status":"findings","baseCommit":"0123456789abcdef0123456789abcdef01234567","applied":true,"summary":{"files":1,"newFindings":1,"failOn":"finding"},"findings":[{"policyId":"missing-docstring","severity":%q,"certainty":"definite","new":true,"path":"fixtures/policy/case.py","title":"issue"}],"incomplete":[]}`, tc.severity)
			}))
			defer server.Close()
			for _, format := range []string{"json", "sarif", "human"} {
				code, out, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL, "--format", format)
				if code != 1 {
					t.Fatalf("%s: exit %d: %s %s", format, code, out, stderr)
				}
				warned := strings.Contains(stderr, "a SlopCop pull request check fails on any new finding")
				if warned != (tc.advisory > 0) {
					t.Fatalf("%s: warned=%v with %d advisory: %s", format, warned, tc.advisory, stderr)
				}
				if warned && !strings.Contains(stderr, "1 new static finding below the push level (error) does not block the push") {
					t.Fatalf("%s: stderr %q", format, stderr)
				}
				switch format {
				case "json":
					var doc struct {
						Approved         bool `json:"approved"`
						AdvisoryFindings int  `json:"advisoryFindings"`
					}
					if err := json.Unmarshal([]byte(out), &doc); err != nil {
						t.Fatal(err)
					}
					if doc.Approved != tc.approved || doc.AdvisoryFindings != tc.advisory {
						t.Fatalf("approved=%v advisoryFindings=%d, want %v and %d: %s", doc.Approved, doc.AdvisoryFindings, tc.approved, tc.advisory, out)
					}
				case "sarif":
					if got := strings.Contains(out, `"slopcop.advisoryFindings": 1`); got != (tc.advisory > 0) {
						t.Fatalf("sarif advisoryFindings present=%v: %s", got, out)
					}
				}
			}
		})
	}
}
