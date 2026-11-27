package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/review"
)

func TestOverrideBeforePushShapes(t *testing.T) {
	for _, tc := range []struct {
		cmd  string
		want overrideShape
	}{
		{"git push", noOverride},
		{`slopcop override --reason "wrong" && git push`, overrideThenPush},
		{`slopcop override --reason=wrong && git push origin HEAD`, overrideThenPush},
		{`slopcop override -w SlopCop --reason 'wrong' --format json && git push && gh pr create --fill`, overrideThenPush},
		{`/usr/local/bin/slopcop override --reason "a; b && git push" && git push`, overrideThenPush},
		{`slopcop override --reason x && git push 2>&1 | tail -5`, overrideThenPush},
		{`slopcop override --reason x && git push && git status`, overrideThenPush},
		{`slopcop override --reason "fine to git push"`, overrideOnly},
		{`slopcop override --reason x ; git push`, overrideUncredited},
		{"slopcop override --reason x\ngit push", overrideUncredited},
		{`git push && slopcop override --reason x`, overrideUncredited},
		{`git push ; slopcop override --reason x && git push`, overrideUncredited},
		{`slopcop override --reason x && git commit -qam y && git push`, overrideUncredited},
		{`slopcop override --reason x && cd ../other && git push`, overrideUncredited},
		{`slopcop override --reason x || git push`, overrideUncredited},
		{`slopcop override --reason x | tee log && git push`, overrideUncredited},
		{`slopcop override --reason x & git push`, overrideUncredited},
		{`slopcop override --reason "$(cat reason)" && git push`, overrideUncredited},
		{`(slopcop override --reason x) && git push`, overrideUncredited},
		{`slopcop override rv-1234abcd --reason x && git push`, overrideUncredited},
		{`slopcop override --root ../other --reason x && git push`, overrideUncredited},
		{`slopcop override --undo rv-1234abcd && git push`, overrideUncredited},
		{`slopcop override --reason "" && git push`, overrideUncredited},
		{`slopcop override --reason && git push`, overrideUncredited},
		{`FOO=1 slopcop override --reason x && git push`, overrideUncredited},
		{`slopcop override --reason $REASON && git push`, overrideUncredited},
		{"slopcop override --reason `cat r` && git push", overrideUncredited},
		{`slopcop override --reason x && git push origin $(git commit -qam y && git rev-parse HEAD)`, overrideUncredited},
		{"slopcop override --reason x && git push origin `git rev-parse HEAD`", overrideUncredited},
		{`slopcop override --reason x && sh -c 'git commit -qam y; git push origin HEAD'`, overrideUncredited},
		{`slopcop override --reason x && echo git push`, overrideUncredited},
		{`slopcop override --reason x && git -c alias.go='!git commit -am y && git push origin HEAD' go`, overrideUncredited},
		{`slopcop override --reason x && git merge -m "Merge git push fix" feature && git push`, overrideUncredited},
		{`slopcop override --reason x && git push && git merge --ff-only feature && command git push`, overrideUncredited},
		{`slopcop override --reason {accepted,--list} && git push`, overrideUncredited},
		{`slopcop override --reason ~ && git push`, overrideUncredited},
		{`slopcop override --reason "costs $5" && git push`, overrideUncredited},
		{`slopcop override --reason 'costs $5 {a,b} *?' && git push`, overrideThenPush},
		{`slopcop override --reason "a {b} *?" && git -C . --no-pager push`, overrideThenPush},
	} {
		if got, why := overrideBeforePush(tc.cmd); got != tc.want {
			t.Errorf("%q: got %v (%s), want %v", tc.cmd, got, why, tc.want)
		}
	}
}

// overrideGateRepo is an armed checkout with one commit and no approval.
func overrideGateRepo(t *testing.T) (repo, tree string) {
	t.Helper()
	repo = testInitRepo(t)
	gitAt(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--allow-empty", "-q", "-m", "init")
	if err := os.MkdirAll(filepath.Join(repo, ".git", "slopcop"), 0700); err != nil {
		t.Fatal(err)
	}
	return repo, gitAt(t, repo, "rev-parse", "HEAD^{tree}")
}

// writeLastReview records a latest review of tree in mode.
func writeLastReview(t *testing.T, repo, tree, mode string) {
	t.Helper()
	data, err := json.Marshal(review.Record{Mode: mode, Verdict: review.Verdict{Status: review.StatusFindings, Capture: review.Capture{Tree: tree}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "slopcop", "last.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

// runGateHook runs the Claude Code hook on command from cwd and returns
// whether it blocked and what it said.
func runGateHook(t *testing.T, cwd, command string) (bool, string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{
		"cwd": cwd, "tool_name": "Bash", "tool_input": map[string]string{"command": command},
	})
	cmd := newGateCommand()
	cmd.SetIn(bytes.NewReader(payload))
	cmd.SetArgs([]string{"--hook", "claude"})
	var errOut bytes.Buffer
	cmd.SetErr(&errOut)
	err := cmd.Execute()
	if err != nil {
		if _, ok := err.(silentExit); !ok {
			t.Fatalf("%q: %v", command, err)
		}
	}
	return err != nil, errOut.String()
}

// The hook judges a line before any of it runs, so it credits an override
// joined by && to the push when the latest review is of HEAD's tree (#249).
func TestGateLetsOverrideThenPushThrough(t *testing.T) {
	repo, tree := overrideGateRepo(t)
	writeLastReview(t, repo, tree, "both")
	other, _ := overrideGateRepo(t)

	for _, tc := range []struct {
		command string
		blocked bool
		says    []string
	}{
		{`git push`, true, []string{"push blocked", "joined to the push with &&"}},
		{`slopcop override --reason "the finding misreads the lock" && git push`, false, nil},
		{`slopcop override --reason x && git push origin HEAD && gh pr create --fill`, false, nil},
		{`slopcop override --reason "says git push in the reason"`, false, nil},
		{`slopcop override --reason x ; git push`, true, []string{"after ; or a newline the push runs even when the override fails", overrideOwnCommand}},
		{`git push && slopcop override --reason x`, true, []string{"the push runs before the override", overrideOwnCommand}},
		{`slopcop override --reason "$(cat reason.txt)" && git push`, true, []string{"does not count", overrideOwnCommand}},
		{`slopcop override --reason x | tee log && git push`, true, []string{"joined to the push by &&"}},
		{`slopcop override --reason x && git commit -qam y && git push`, true, []string{"could change what is pushed"}},
		{`slopcop override rv-1234abcd --reason x && git push`, true, []string{"without a finding id"}},
		{`slopcop override --reason x && git -C ` + other + ` push`, true, []string{"where the push runs"}},
	} {
		blocked, stderr := runGateHook(t, repo, tc.command)
		if blocked != tc.blocked {
			t.Errorf("%q: blocked %v, want %v: %s", tc.command, blocked, tc.blocked, stderr)
			continue
		}
		for _, want := range tc.says {
			if !strings.Contains(stderr, want) {
				t.Errorf("%q: stderr lacks %q: %s", tc.command, want, stderr)
			}
		}
	}

	// An approved tree passes whatever the line does, as before.
	gitAt(t, repo, "update-ref", "refs/slopcop/reviewed/"+tree, tree)
	if blocked, stderr := runGateHook(t, repo, `slopcop override --reason x ; git push`); blocked {
		t.Fatalf("an approved tree was blocked: %s", stderr)
	}
}

// The override approves only the tree the latest review saw. When that is
// not HEAD's, it would approve something other than what the push sends.
func TestGateRefusesOverrideThenPushOfAnotherTree(t *testing.T) {
	repo, _ := overrideGateRepo(t)
	command := `slopcop override --reason x && git push`
	if blocked, stderr := runGateHook(t, repo, command); !blocked || !strings.Contains(stderr, "no review has run") {
		t.Fatalf("no review: blocked %v: %s", blocked, stderr)
	}
	// A review of a dirty working tree: its tree is not HEAD's.
	writeFile(t, repo, "new.txt", "uncommitted\n")
	gitAt(t, repo, "add", "new.txt")
	dirty := gitAt(t, repo, "write-tree")
	writeLastReview(t, repo, dirty, "both")
	blocked, stderr := runGateHook(t, repo, command)
	if !blocked || !strings.Contains(stderr, "the latest review is of tree "+dirty) {
		t.Fatalf("dirty review: blocked %v: %s", blocked, stderr)
	}
}

// A review-only latest review cannot be credited where the static half can
// run: the approval it would give does not pass the gate either.
func TestGateRefusesOverrideThenPushOfAReviewOnlyReview(t *testing.T) {
	isolateUserConfig(t)
	t.Setenv("SLOPCOP_TOKEN", "test-token")
	withOriginBranches(t, true)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	if err := os.MkdirAll(filepath.Join(repo, ".git", "slopcop"), 0700); err != nil {
		t.Fatal(err)
	}
	writeLastReview(t, repo, runGit(t, repo, "rev-parse", "HEAD^{tree}"), "ai")
	blocked, stderr := runGateHook(t, repo, `slopcop override --reason x && git push`)
	if !blocked || !strings.Contains(stderr, "ran without the static check (--mode ai)") {
		t.Fatalf("blocked %v: %s", blocked, stderr)
	}
}
