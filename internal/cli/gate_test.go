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
	}{{"git push", true}, {"git -C . push origin main", true}, {"gh pr create", true}, {"echo hello", false}, {"git status; echo push", false}, {"echo git push", true}} {
		if got := pushCommand.MatchString(tc.cmd); got != tc.want {
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
