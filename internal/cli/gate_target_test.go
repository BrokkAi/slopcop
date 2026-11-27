package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestPushTargetsFollowCdAndGitC(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(b, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
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
		{`git -C ` + b + ` "push"`, []string{b}},
		{`git -C ` + b + ` $'push'`, []string{b}},
		{"cd " + b + " && git $'\\x70ush'", []string{b}},
		{"git -C " + b + " pu\\\nsh", []string{b}},
		// Pushes in shell code run where the code says.
		{`bash -c "git -C ` + b + ` push"`, []string{a, b}},
		{`bash -c "git -C ` + b + ` \$'push'"`, []string{a, b}},
		{`sh -c 'cd ` + b + ` && git push'`, []string{a, b}},
		{`eval "git -C ` + b + ` push"`, []string{a, b}},
		{`bash <<< $'git -C ` + filepath.ToSlash(b) + ` pu\x73h'`, []string{a, b}},
		{"sh <<'EOF'\ngit -C " + b + " push\nEOF", []string{b}},
		{"bash <<< 'cd " + b + "' <<< 'git push origin HEAD'", []string{a}},
		{`if true; then bash -c "git -C ` + b + ` \$'push'"; fi`, []string{a, b}},
		{`while true; do sudo sh -c 'git -C ` + b + ` push'; done`, []string{a, b}},
		{`sudo -u me sh -c 'git -C ` + b + ` push'`, []string{a, b}},
		{`cd ` + b + ` && bash -c 'cd sub && git push'`, []string{b, filepath.Join(b, "sub")}},
		// The reader may misread shell code, so the shell's own directory
		// counts too.
		{"bash -c 'cat <<EOF\ncd " + b + "\nEOF\ngit push origin HEAD'", []string{a, b}},
		{`bash -c 'git -C ` + b + ` push; source /dev/stdin' <<< 'git push origin HEAD'`, []string{a, b}},
		// A word after a wrapper may be a branch, not a shell: the direct
		// push still counts.
		{"env git -C " + b + " push origin sh <<< 'git push'", []string{a, b}},
		// A cd in a heredoc's text may be read as one, so on a line with a
		// heredoc a push counts in every directory the line has been in.
		{"cat <<'EOF'\ncd " + b + "\nEOF\ngit push origin HEAD", []string{a, b}},
		{"cat <<'EOF'\ncd " + b + "\nEOF\nbash -c 'git \"push\" origin HEAD'", []string{a, b}},
		{"cat <<'EOF'\ncd " + b + "\nEOF\nbash -c 'git \"push\" origin HEAD'\nif", []string{a, b}},
		{"cat <<'EOF'\nIt's docs\nEOF\ngit push; git -C " + b + " push", []string{a, b}},
		{"builtin cd -P " + b + " && git push", []string{b}},
		{"cd " + b + " && cd sub && git push", []string{filepath.Join(b, "sub")}},
		// The push runs only if cd succeeded, so it would run there.
		// A directory missing now may be made by an earlier command on the
		// line, so after ; both count.
		// The push runs in the old directory when the cd does not carry over.
		// After ;, the next list runs wherever the last one stopped.
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
		{"cd " + b + " | git push", []string{a}},
		{"echo x | cd " + b + " && git push", []string{a}},
		{"echo x |& cd " + b + " && git push", []string{a}},
		// A pipeline after a cd is one command of the cd's list, so pushes
		// after it still run where the cd went.
		{"cd " + b + " && git push 2>&1 | tail -2 && gh pr create --fill", []string{b}},
		{"cd " + b + " && make | tee log && git push", []string{b}},
		{"cd " + b + " && make |& tee log && git push", []string{b}},
		{"cd " + b + " && make | tee log; git push", []string{a, b}},
		{"cd " + b + " && make | tee log || git push", []string{a, b}},
		{"cd " + b + " & git push", []string{a}},
		{"(cd " + b + ") && git push", []string{a}},
		// After ||, the push runs where the list began when the cd failed,
		// and where the cd moved it when a later command failed.
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
		// A command substitution inside double quotes runs its commands.
		{`echo "$(git -C ` + b + ` push)"`, []string{b}},
		{`echo "$(cd ` + b + ` && git push)" && git push`, []string{b, a}},
		{"echo \"`git -C " + b + " push`\"", []string{b}},
		{`echo "a; $(echo b) (c)" && git push`, []string{a}},
	} {
		got, unsure := pushTargets(a, tc.command)
		if !reflect.DeepEqual(got, tc.want) || unsure != "" {
			t.Errorf("%q: targets %v (unsure %q), want %v", tc.command, got, unsure, tc.want)
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
		// A directory missing now may be one the line makes, which the
		// gate cannot judge before it exists.
		"cd " + filepath.Join(b, "missing") + " && git push",
		"cd " + filepath.Join(b, "missing") + "; git push",
		"git worktree add ../wt -b x && cd ../wt && git push",
		"cp -r " + b + " " + filepath.Join(b, "copy") + "; cd " + filepath.Join(b, "copy") + "; git push",
		"git -C " + filepath.Join(b, "missing") + " push",
		"make || cd nosuch; cd sub; git push",
		// A push the parser finds where these rules place none.
		`time -p bash -c "git -C ` + b + ` \$'push'"`,
	} {
		if _, unsure := pushTargets(a, command); unsure == "" {
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
		{unguarded, `echo "$(git -C ` + unchecked + ` push)"`, true},
		// Pushes in shell code, followed or not, are judged where they run.
		{unguarded, `bash -c "git -C ` + unchecked + ` push"`, true},
		{unguarded, `bash -c "git -C ` + checked + ` push"`, false},
		{unguarded, `time -p bash -c "git -C ` + unchecked + ` \$'push'"`, true},
		{unguarded, "cat <<EOF\n$(git -C " + unchecked + " push origin HEAD)\nEOF", true},
		{unchecked, "cat <<'EOF'\ncd " + checked + "\nEOF\nbash -c 'git \"push\" origin HEAD'", true},
		{unchecked, "cat <<'EOF'\nIt's docs\nEOF\ngit push; git -C " + checked + " push", true},
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

func TestShellWordsKeepWindowsPathSeparators(t *testing.T) {
	for _, tc := range []struct {
		separates bool
		text      string
		want      []string
	}{
		{true, `cd C:\Users\me\repo`, []string{"cd", `C:\Users\me\repo`}},
		{true, `cd C:\Users\my\ repo`, []string{"cd", `C:\Users\my repo`}},
		{true, `cd "C:\Users\me\repo"`, []string{"cd", `C:\Users\me\repo`}},
		{true, `echo a\;b`, []string{"echo", "a;b"}},
		{false, `cd C:\Users\me`, []string{"cd", "C:Usersme"}},
		{false, `cd my\ repo`, []string{"cd", "my repo"}},
	} {
		backslashSeparates = tc.separates
		if got := shellWords(tc.text); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("separates=%v %q: words %q, want %q", tc.separates, tc.text, got, tc.want)
		}
	}
	backslashSeparates = runtime.GOOS == "windows"
}

func TestPushTargetsReadGluedRedirectionsAndPartialCds(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	for _, dir := range []string{"sub", "repo2"} {
		if err := os.Mkdir(filepath.Join(b, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		cwd, command string
		want         []string
	}{
		{a, "cd " + b + ">/dev/null && git push", []string{b}},
		{a, "cd " + b + " 2>/dev/null>&2 && git push", []string{b}},
		{a, "git -C " + b + ">/dev/null push", []string{b}},
		// The 2 of repo2>&1 is the directory's, not a descriptor.
		{a, "cd " + filepath.Join(b, "repo2") + ">&1 && git push", []string{filepath.Join(b, "repo2")}},
		{a, "cd " + b + " >| out && git push", []string{b}},
		// A cd that succeeds from one candidate and fails from another
		// moves the first and leaves the second.
	} {
		got, unsure := pushTargets(tc.cwd, tc.command)
		if !reflect.DeepEqual(got, tc.want) || unsure != "" {
			t.Errorf("%q: targets %v (unsure %q), want %v", tc.command, got, unsure, tc.want)
		}
	}
	if got := shellWords("cd dir>/dev/null 2>&1 <in"); !reflect.DeepEqual(got, []string{"cd", "dir"}) {
		t.Errorf("words %q", got)
	}
}

func TestGateHookSaysWhatItBlockedAndWhy(t *testing.T) {
	t.Setenv("GROK_HOOK_EVENT", "")
	unchecked := armedRepo(t, false)
	unguarded := testInitRepo(t)
	copyDir := filepath.Join(t.TempDir(), "copy")
	// git names the checkout by its real path (/private/var on macOS).
	real, err := filepath.EvalSymlinks(unchecked)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		cwd, command string
		want         string
	}{
		// The block names the repository, so the check runs there.
		{unguarded, "git -C " + unchecked + " push", "in " + filepath.ToSlash(real)},
		{unchecked, "git worktree add ../wt -b x && cd ../wt && git push", "does not exist yet"},
		{unchecked, "cd $REPO && git push", "depends on the shell"},
		// A copy of a guarded repository is guarded, though the session is
		// not and the copy does not exist yet.
		{unguarded, "cp -r " + unchecked + " " + copyDir + " && cd " + copyDir + " && git push", "does not exist yet"},
		// The source named bare, from the directory that holds it.
		{filepath.Dir(unchecked), "cp -r " + filepath.Base(unchecked) + " copy && cd copy && git push", "does not exist yet"},
	} {
		payload, _ := json.Marshal(map[string]any{
			"cwd": tc.cwd, "tool_name": "Bash", "tool_input": map[string]string{"command": tc.command},
		})
		cmd := newGateCommand()
		cmd.SetIn(bytes.NewReader(payload))
		cmd.SetArgs([]string{"--hook", "claude"})
		var errOut bytes.Buffer
		cmd.SetErr(&errOut)
		if err := cmd.Execute(); err == nil || !strings.Contains(errOut.String(), tc.want) {
			t.Errorf("%q: err %v, stderr %q, want it to say %q", tc.command, err, errOut.String(), tc.want)
		}
	}
}
