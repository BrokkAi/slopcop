package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPushTargetsFollowCdAndGitC(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(b, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(b, "missing")
	spaced := filepath.Join(b, "my repo")
	if err := os.Mkdir(spaced, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		command string
		want    []string
	}{
		{"git push", []string{a}},
		{"cd " + b + " && git push", []string{b}},
		{"cd " + b + "; git push", []string{b}},
		{"cd " + b + "\ngit push", []string{b}},
		{`cd "` + b + `" && git push origin HEAD`, []string{b}},
		{"builtin cd -P " + b + " && git push", []string{b}},
		{"cd " + b + " && cd sub && git push", []string{filepath.Join(b, "sub")}},
		// The push runs only if cd succeeded, so it would run there.
		{"cd " + missing + " && git push", []string{missing}},
		// The push runs in the old directory when the cd fails or does not
		// carry over.
		{"cd " + missing + "; git push", []string{a}},
		// After ;, the next list runs wherever the last one stopped.
		{"cd " + missing + " && true; git push", []string{a, missing}},
		{"make && cd " + b + "; git push", []string{a, b}},
		{"(make) && cd " + b + "; git push", []string{a, b}},
		{"cd " + b + " && cd sub; git push", []string{a, b, filepath.Join(b, "sub")}},
		{"cd " + b + "; cd sub; git push", []string{filepath.Join(b, "sub")}},
		// Around ||, a cd may or may not have run, so both count.
		{"cd " + b + " || git push", []string{a, b}},
		{"cd " + b + " || exit 1 && git push", []string{a, b}},
		{"cd " + b + " || true; git push", []string{a, b}},
		{"make || cd " + b + " && git push", []string{a, b}},
		// If make succeeded, cd sub runs from a.
		{"make || cd " + b + " && cd sub && git push", []string{a, b, filepath.Join(a, "sub"), filepath.Join(b, "sub")}},
		{"cd " + b + " | git push", []string{a}},
		{"echo x | cd " + b + " && git push", []string{a}},
		{"echo x |& cd " + b + " && git push", []string{a}},
		{"cd " + b + " & git push", []string{a}},
		{"(cd " + b + ") && git push", []string{a}},
		// After ||, the push runs where the list began when the cd failed,
		// and where the cd moved it when a later command failed.
		{"cd " + missing + " && true || git push", []string{a, missing}},
		{"cd " + b + " && make || git push", []string{a, b}},
		{"cd " + b + " && git push || git push", []string{b, a}},
		{"cd " + b + " && make || git push; git push", []string{a, b}},
		{"(cd " + b + " && git push)", []string{b}},
		{"echo $(cd " + b + ") && git push", []string{a}},
		{"echo `cd " + b + "` && git push", []string{a}},
		{"cd $HOME && cd " + b + " && git push", []string{b}},
		// Escaped spaces and compound commands.
		{`cd ` + strings.ReplaceAll(spaced, " ", `\ `) + ` && git push`, []string{spaced}},
		{`git -C ` + strings.ReplaceAll(spaced, " ", `\ `) + ` push`, []string{spaced}},
		{"{ cd " + b + "; } && git push", []string{b}},
		{"if true; then cd " + b + "; fi && git push", []string{b}},
		{"command cd " + b + " && git push", []string{b}},
		{"git -C " + b + " push", []string{b}},
		{"git -C " + b + " -C sub push --force", []string{filepath.Join(b, "sub")}},
		{"git -c core.x=y -C " + b + " --no-pager push", []string{b}},
		{"cd " + b + " && git -C " + a + " push", []string{a}},
		{"cd $REPO && git -C " + b + " push", []string{b}},
		// --git-dir names the repository whose refs are pushed; --work-tree
		// does not.
		{"git --git-dir " + filepath.Join(b, ".git") + " push", []string{b}},
		{"GIT_DIR=" + filepath.Join(b, ".git") + " git push", []string{b}},
		{"git -C " + b + " --git-dir sub/.git push", []string{filepath.Join(b, "sub")}},
		{"git --work-tree=" + b + " push", []string{a}},
		{"git --work-tree " + b + " push", []string{a}},
		{"git --git-dir=" + filepath.Join(b, ".git") + " --work-tree=" + a + " push", []string{b}},
		// Quoted operators and line continuations do not end a command.
		{"cd " + b + ` && git commit -m "fix: a; b && c | d" && git push`, []string{b}},
		{"cd " + b + " && git commit -m 'x; (y) `z`' && git push", []string{b}},
		{"cd " + b + " && \\\n  git push", []string{b}},
		{"cd " + b + ` && git commit -m "say \"hi\"; bye" && git push`, []string{b}},
		// Redirections are neither background jobs nor arguments.
		{"cd " + b + " >&/dev/null && git push", []string{b}},
		{"cd " + b + " >& /dev/null && git push", []string{b}},
		{"cd " + b + " 2>&1 && git push", []string{b}},
		{"cd " + b + " &>/dev/null && git push", []string{b}},
		{"cd " + b + " >/dev/null && git push", []string{b}},
		{"cd " + b + " > /dev/null 2>&1 && git push", []string{b}},
		{"cd " + b + " && gh pr create --fill", []string{b}},
		{"git push && cd " + b + " && git push", []string{a, b}},
		{"cd " + b + " && git status", nil},
	} {
		got, unsure := pushTargets(a, tc.command)
		if !reflect.DeepEqual(got, tc.want) || unsure {
			t.Errorf("%q: targets %v (unsure %v), want %v", tc.command, got, unsure, tc.want)
		}
	}
}

func TestPushTargetsCannotReadEveryDirectory(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	for _, command := range []string{
		"cd $REPO && git push",
		"cd $REPO; git push",
		"cd - && git push",
		"popd && git push",
		"cd a b && git push",
		"git -C $DIR push",
		"git --git-dir $GD push",
		"cd $REPO && git -C sub push",
		"cd $REPO && gh pr create",
		"make || cd $REPO && git push",
		"cd " + b + " && cd $(pwd)/x && git push",
	} {
		if _, unsure := pushTargets(a, command); !unsure {
			t.Errorf("%q: read as sure", command)
		}
	}
}

// armedRepo is a repository with one commit and the review store that arms
// the gate; approved also records a passing check of its HEAD tree.
func armedRepo(t *testing.T, approved bool) string {
	t.Helper()
	repo := testInitRepo(t)
	gitAt(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--allow-empty", "-q", "-m", "init")
	if err := os.MkdirAll(filepath.Join(repo, ".git", "slopcop"), 0o700); err != nil {
		t.Fatal(err)
	}
	if approved {
		tree := gitAt(t, repo, "rev-parse", "HEAD^{tree}")
		gitAt(t, repo, "update-ref", "refs/slopcop/reviewed/"+tree, tree)
	}
	return repo
}

func TestGateHookJudgesTheRepositoryThePushTargets(t *testing.T) {
	t.Setenv("GROK_HOOK_EVENT", "")
	unchecked, checked := armedRepo(t, false), armedRepo(t, true)
	unguarded := testInitRepo(t)
	for _, tc := range []struct {
		cwd, command string
		blocked      bool
	}{
		{unchecked, "git push", true},
		{checked, "git push", false},
		// The session sits in one checkout and pushes another.
		{unchecked, "git -C " + checked + " push", false},
		{unchecked, "cd " + checked + " && git push", false},
		{unchecked, "cd " + checked + " && gh pr create --fill", false},
		// A push from elsewhere is judged there, even from a checked session.
		{checked, "git -C " + unchecked + " push", true},
		{checked, "cd " + unchecked + " && git push", true},
		// A cd the push does not follow changes nothing.
		{unchecked, "cd " + checked + " || git push", true},
		{unchecked, "(cd " + checked + ") && git push", true},
		{unchecked, "cd " + checked + " && true || git push", true},
		{unchecked, "cd /no/such/dir && true || git push", true},
		{unchecked, "make || cd " + checked + " && git push", true},
		{checked, "cd " + unchecked + " || exit 1 && git push", true},
		{checked, "git --git-dir " + filepath.Join(unchecked, ".git") + " push", true},
		{checked, "cd " + unchecked + " 2>&1 && git push", true},
		{unchecked, "git --work-tree=" + checked + " push", true},
		{unchecked, "echo x | cd " + checked + " && git push", true},
		{unchecked, "cd /no/such/dir && true; git push", true},
		{checked, "cd " + unchecked + " >&/dev/null && git push", true},
		// A push the gate cannot read is blocked where SlopCop guards a
		// repository in sight, and left alone where it guards none.
		{checked, "cd $REPO && git push", true},
		{unguarded, "cd $REPO && git push", false},
		{unguarded, "cd $REPO && git -C " + unchecked + " push", true},
		{unchecked, "cd " + checked + ` && git commit -m "a; b" && git push`, false},
		// Every push on the line must pass.
		{unchecked, "git -C " + checked + " push && git push", true},
	} {
		payload, _ := json.Marshal(map[string]any{
			"cwd": tc.cwd, "tool_name": "Bash", "tool_input": map[string]string{"command": tc.command},
		})
		cmd := newGateCommand()
		cmd.SetIn(bytes.NewReader(payload))
		cmd.SetArgs([]string{"--hook", "claude"})
		var errOut bytes.Buffer
		cmd.SetErr(&errOut)
		err := cmd.Execute()
		if blocked := err != nil; blocked != tc.blocked {
			name := "checked"
			if tc.cwd == unchecked {
				name = "unchecked"
			}
			t.Errorf("from %s, %q: blocked %v, want %v (%s)", name, strings.NewReplacer(checked, "<checked>", unchecked, "<unchecked>").Replace(tc.command), blocked, tc.blocked, errOut.String())
		}
	}
}
