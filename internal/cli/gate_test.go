package cli

import (
	"bytes"
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
