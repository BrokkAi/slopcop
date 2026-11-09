package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	var out, errOut bytes.Buffer
	cmd := newGateCommand()
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
