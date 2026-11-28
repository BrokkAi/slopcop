package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
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
	// A script on disk in the starting directory.
	if err := os.MkdirAll(filepath.Join(a, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a, "scripts", "check.sh"), []byte("true\n"), 0o755); err != nil {
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
		// A heredoc's text is data: a cd in it moves nothing, and a quote
		// in it ends nothing.
		{"cat <<'EOF'\ncd " + b + "\nEOF\ngit push origin HEAD", []string{a}},
		{"cat <<'EOF'\nIt's docs\nEOF\ngit push; git -C " + b + " push", []string{a, b}},
		{"cat > notes.md <<'EOF'\ngit push origin HEAD\nEOF\ngit push", []string{a}},
		// Blocks, if, !, command prefixes and substitutions.
		{"{ cd " + b + "; }; git push", []string{b}},
		{"if true; then cd " + b + "; else cd " + b + "; fi; git push", []string{b}},
		{"! cd " + b + "; git push", []string{b}},
		{"! cd " + b + " && git push", []string{a, b}},
		{"cd " + a + "; command -- cd " + b + "; git push", []string{b}},
		{"cd " + a + "; command -p cd " + b + "; git push", []string{b}},
		// command -- -p runs a program named -p, which moves nothing.
		{"cd " + a + "; command -- -p cd " + b + "; git push", []string{a}},
		// builtin has no -p: builtin -p cd fails and moves nothing.
		{"cd " + a + "; builtin -p cd " + b + "; git push", []string{a}},
		{`echo "$(git -C ` + b + ` push origin HEAD)"`, []string{b}},
		// Loops and case that do not move the shell, and code that names
		// no push.
		{"for x in 1 2; do echo $x; done; git push", []string{a}},
		// A loop body is read as a list: after its cd, a push is there.
		{"for i in 1; do \"cd\" " + b + "; git push; done", []string{b}},
		{"case x in x) echo x ;; esac; git push", []string{a}},
		{strings.Repeat("for i in 1; do ", 25) + ":" + strings.Repeat("; done", 25) + "; git push", []string{a}},
		// A script on disk runs in a process of its own and cannot move
		// the shell.
		{"python3 tools/check.py && git push", []string{a}},
		{"bash scripts/check.sh && git push", []string{a}},
		{"bash scripts/check.sh -s && git push", []string{a}},
		{"bash +s scripts/check.sh && git push", []string{a}},
		{"bash -s +s scripts/check.sh && git push", []string{a}},
		{"cd " + b + "; CDPATH=; cd sub; git push", []string{filepath.Join(b, "sub")}},
		{"cd " + b + "; CDPATH=''; cd sub; git push", []string{filepath.Join(b, "sub")}},
		{"cd " + b + "; export CDPATH=\"\"; cd sub; git push", []string{filepath.Join(b, "sub")}},
		{`bash scripts/check.sh "$file" && git push`, []string{a}},
		{`bash scripts/check.sh "$(printf arg)" && git push`, []string{a}},
		{"bash scripts/check.sh <(printf arg) && git push", []string{a}},
		{"bash -e scripts/check.sh <(printf arg) && git push", []string{a}},
		{"bash -- scripts/check.sh <(printf arg) && git push", []string{a}},
		{"bash - scripts/check.sh && git push", []string{a}},
		{"grep -F ${pattern:-'<(git push)'} README.md; git push", []string{a}},
		{`grep -F "${pattern:-<(git push)}" README.md; git push`, []string{a}},
		{`python3 tools/check.py "$file" && git push`, []string{a}},
		{"cd " + b + "; CDPATH= cd sub && git push", []string{filepath.Join(b, "sub")}},
		// CDPATH only when the line sets it.
		{"cd " + b + "; echo CDPATH; cd sub; git push", []string{filepath.Join(b, "sub")}},
		{"cd " + b + "; echo CDPATH=/tmp; cd sub; git push", []string{filepath.Join(b, "sub")}},
		{"cd " + b + "; cd sub; git push; CDPATH=/tmp", []string{filepath.Join(b, "sub")}},
		{"cd " + b + "; (CDPATH=/tmp); cd sub; git push", []string{filepath.Join(b, "sub")}},
		{"cd " + b + "; CDPATH=/tmp | cat; cd sub; git push", []string{filepath.Join(b, "sub")}},
		{"cd " + b + "; CDPATH=/tmp & wait; cd sub; git push", []string{filepath.Join(b, "sub")}},
		{"cd " + b + "; CDPATH=/tmp printf ok; cd sub; git push", []string{filepath.Join(b, "sub")}},
		{"cd " + b + "; coproc { CDPATH=/tmp; }; wait; cd sub; git push", []string{filepath.Join(b, "sub")}},
		{"{ git push; } 3<<< ':' >/dev/null", []string{a}},
		{"git push > /dev/null", []string{a}},
		{"cat <<< \"$(git -C " + b + " push origin HEAD)\" \"$(printf /dev/null)\"", []string{b}},
		{"cat <<EOF; :\n$(git -C " + b + " push origin HEAD)\nEOF", []string{b}},
		{"for i in x; do cd " + b + " | cat; git push; done", []string{a}},
		{"echo sed; bash scripts/check.sh && git push", []string{a}},
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
		// The condition may fail, and the if then ends where it began.
		{"if true; then cd " + b + "; fi && git push", []string{a, b}},
		{"if true; then cd " + b + "; else cd " + b + "; fi && git push", []string{b}},
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
		// Code the gate does not follow: shell code that names a push,
		// eval, source, trap, alias, functions, pipes into a shell, scripts
		// the line writes, loops that move the shell, CDPATH, $cmd.
		`bash -c "git -C ` + b + ` push"`,
		`time -p bash -c "git -C ` + b + ` \$'push'"`,
		`sh -c 'cd ` + b + ` && git push'`,
		`sudo -u me sh -c 'git -C ` + b + ` push'`,
		`env -u git bash -c 'git -C ` + b + ` push'`,
		`bash <<< 'git -C ` + b + ` push'`,
		"sh <<'EOF'\ngit -C " + b + " push\nEOF",
		"cat <<'EOF' | sh\ngit push origin HEAD\nEOF",
		"printf 'git push origin HEAD' | bash",
		"(cd " + b + "; bash) <<< 'git push origin HEAD'",
		"{ cd " + b + "; python3 -; } <<< 'import os; os.system(\"git push origin HEAD\")'",
		`eval "git -C ` + b + ` push"`,
		`eval "cd ` + b + `" && git push`,
		"cd " + b + "; source /tmp/setup.sh; git push",
		"cd " + b + "; command source /tmp/setup.sh; git push",
		"cd " + b + "; builtin . /tmp/setup.sh; git push",
		"trap 'cd " + b + "' DEBUG; git push",
		"alias x='cd " + b + "'; x; git push",
		"f() { cd " + b + "; }; f; git push",
		"f() { git -C " + b + " push; }; trap f EXIT",
		"cat > /tmp/p.sh <<'EOF'\ngit -C " + b + " push origin HEAD\nEOF\nbash /tmp/p.sh",
		"printf 'git push\\n' > ./x; chmod +x ./x; ./x",
		"for x in 1; do cd " + b + "; done; git push",
		"case x in x) cd " + b + " ;& y) git push ;; esac",
		"CDPATH=/tmp; cd sub; git push",
		"cd " + b + "; CDPATH=/tmp cd sub; git push",
		// Once set, CDPATH stays set: a clear may not run.
		"cd " + b + "; CDPATH=/tmp; false && CDPATH=; cd sub; git push",
		"p='git -C " + b + " push'; $p",
		"echo " + b + " | xargs -I{} git -C {} push",
		"find " + b + " -prune -exec git -C " + b + " \"push\" \\;",
		"env -C " + b + " git \"push\"",
		// A wrapper may run git somewhere else.
		"sudo git -C " + b + " push",
		"sudo -u build -i git \"push\"",
		"env GIT_TRACE=1 git -C " + b + " push",
		"env -u git -u push git -C " + b + " \"push\"",
		"env -u git --chdir " + b + " git \"push\"",
		"awk '{system($0)}' <<'EOF'; :\ngit -C " + b + " push origin HEAD\nEOF",
		`python3 -c "import subprocess; subprocess.run(['git', '-C', '` + b + `', 'push'])"`,
		`python3 - <<< 'import subprocess; subprocess.run(["git", "-C", "` + b + `", "push"])'`,
		// Any shell or interpreter code but a script on disk.
		"bash -c 'cd " + b + "; g\"it\" push'",
		"bash -c 'cd " + b + "; \"git\" push'",
		"bash -c cat <<< 'hello'; git push",
		`python3 -c "print(1)" && git push`,
		"{ bash -c 'source /dev/stdin'; } <<'EOF'\ncd " + b + "\ngit push origin HEAD\nEOF",
		"env -u bash bash -c 'git -C " + b + " push'",
		"bash -s arg <<'EOF'\ngit -C " + b + " push origin HEAD\nEOF",
		"bash -o errexit -s arg <<'EOF'\ncd " + b + "; git push\nEOF",
		"bash +o errexit -s arg <<'EOF'\ncd " + b + "; git push\nEOF",
		"bash <(cat <<'EOF'\ngit -C " + b + " push\nEOF\n)",
		"bash -o errexit <(cat <<'EOF'\ngit -C " + b + " push\nEOF\n)",
		"{ cat; } <<'EOF' | bash /dev/stdin\ngit -C " + b + " push origin HEAD\nEOF",
		"cat <<'EOF' | awk '{system($0)}'\ngit -C " + b + " push origin HEAD\nEOF",
		"zsh --emulate sh <(cat <<'EOF'\ngit -C " + b + " push origin HEAD\nEOF\n)",
		// After a long option, a process substitution after a word may be
		// the script, as an option's value cannot be told from it.
		"bash --norc scripts/check.sh <(printf arg) && git push",
		// A process substitution >(...) reads what the statement writes.
		"cat > >(awk '{system($0)}') <<'EOF'\ngit -C " + b + " push origin HEAD\nEOF",
		// A process substitution in a default is run, though parsed as text.
		"awk '{print}' ${x:-<(cd " + b + "; git push origin HEAD)}",
		// >&file writes a file; git apply writes one from its patch.
		"cat >&cmds.sh <<'EOF'\ngit -C " + b + " push origin HEAD\nEOF\nbash cmds.sh",
		"git apply <<'EOF'\n--- /dev/null\n+++ b/p.sh\n@@ -0,0 +1 @@\n+git -C " + b + " push origin HEAD\nEOF\nbash p.sh",
		// A startup file a variable names runs before the script.
		"BASH_ENV=/dev/fd/3 bash scripts/check.sh 3< <(cat <<'EOF'\ngit -C " + b + " push origin HEAD\nEOF\n)",
		"export BASH_ENV=/dev/fd/3; bash scripts/check.sh 3< <(cat <<'EOF'\ngit -C " + b + " push origin HEAD\nEOF\n)",
		"sort -o cmds.sh <<'EOF'\ngit -C " + b + " push origin HEAD\nEOF\nbash cmds.sh",
		"bash --rcfile /dev/fd/3 -i scripts/check.sh 3< <(cat <<'EOF'\ngit -C " + b + " push origin HEAD\nEOF\n)",
		"bash --rcfile=/dev/fd/3 -i scripts/check.sh 3< <(cat <<'EOF'\ngit -C " + b + " push origin HEAD\nEOF\n)",
		"bash scripts/check.sh 3< <(cat <<'EOF'\ngit -C " + b + " push origin HEAD\nEOF\n)",
		"exec 3< <(cat <<'EOF'\ngit -C " + b + " push origin HEAD\nEOF\n)\nbash scripts/check.sh",
		"cat <<'EOF' | less -F -Ocmds.sh\ngit -C " + b + " push origin HEAD\nEOF\nbash cmds.sh",
		"bat --paging=always --pager 'tee cmds.sh' <<'EOF'\ngit -C " + b + " push origin HEAD\nEOF\nbash cmds.sh",
		"git config alias.save '!tee cmds.sh'; git save <<'EOF'\ngit -C " + b + " push origin HEAD\nEOF\nbash cmds.sh",
		"gh alias set --shell save 'tee cmds.sh'; gh save <<'EOF'\ngit -C " + b + " push origin HEAD\nEOF\nbash cmds.sh",
		"git rebase --exec 'cat > cmds.sh' HEAD~1 <<'EOF'\ngit -C " + b + " push origin HEAD\nEOF\nbash cmds.sh",
		"fish -C true <(cat <<'EOF'\ngit -C " + b + " push origin HEAD\nEOF\n)",
		"git commit --allow-empty -F - <<'EOF'\ngit -C " + b + " push origin HEAD\nEOF\nbash .git/COMMIT_EDITMSG",
		"git commit --allow-empty -m 'git -C " + b + " push origin HEAD' && bash .git/COMMIT_EDITMSG",
		// A file the line writes from a heredoc may be run by any command.
		"cat > cmds <<'EOF'\ngit -C " + b + " push origin HEAD\nEOF\nawk '{system($0)}' cmds",
		// A process substitution's output on stdin is fed as by a pipe.
		"awk '{system($0)}' < <(cat <<'EOF'\ngit -C " + b + " push origin HEAD\nEOF\n)",
		"awk '{system($0)}' <(cat <<'EOF'\ngit -C " + b + " push origin HEAD\nEOF\n)",
		// An option's value may be code the shell runs.
		"bash --rcfile <(cat <<'EOF'\ngit -C " + b + " push origin HEAD\nEOF\n) -i scripts/check.sh",
		"cat <<'EOF' | make -f -\nall:\n\tgit -C " + b + " push origin HEAD\nEOF",
		"{ cat | awk '{system($0)}'; } <<'EOF'\ngit -C " + b + " push origin HEAD\nEOF",
		"printf x | command tee /tmp/p.sh; bash /tmp/p.sh",
		// Quoted names are names.
		"printf 'git push' | \"tee\" /tmp/p.sh; bash /tmp/p.sh",
		// A line the parser cannot read, which names a push.
		"cat <<'EOF'\ncd " + b + "\nEOF\ngit push\nif",
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
		// Code the gate does not follow asks for a push it can read, even
		// to a checked repository.
		{unguarded, `bash -c "git -C ` + checked + ` push"`, true},
		{unguarded, `time -p bash -c "git -C ` + unchecked + ` \$'push'"`, true},
		// Code the gate does not follow is still read for the repositories
		// it names, from wherever the line goes.
		{unguarded, "bash -c 'cd " + filepath.Dir(unchecked) + "; cd " + filepath.Base(unchecked) + "; git push'", true},
		{unguarded, "cd " + filepath.Dir(unchecked) + "; bash -c 'cd " + filepath.Base(unchecked) + "; git push'", true},
		{unguarded, "bash -c 'git --git-dir=" + filepath.Join(unchecked, ".git") + " push origin HEAD'", true},
		{unguarded, "{ bash; } <<'EOF'\ncd " + unchecked + "\ngit push origin HEAD\nEOF", true},
		{unguarded, "eval 'cd " + filepath.Dir(unchecked) + "; cd " + filepath.Base(unchecked) + "; git push'", true},
		{unguarded, "trap 'cd " + unchecked + "; git push' EXIT", true},
		{unguarded, "f() { cd " + filepath.Dir(unchecked) + "; cd " + filepath.Base(unchecked) + "; git push; }; f", true},
		{unguarded, "for i in 1; do cd " + filepath.Dir(unchecked) + "; cd " + filepath.Base(unchecked) + "; git push; done", true},
		{unguarded, "cd " + filepath.Dir(unchecked) + "; cd " + filepath.Base(unchecked) + "; git push\nif", true},
		{unguarded, "env git -C " + unchecked + " \"push\"", true},
		{unguarded, "xargs -n4 env <<'EOF'\ngit -C " + unchecked + " push\nEOF", true},
		{unguarded, "bash -c 'f() { cd " + filepath.Dir(unchecked) + "; cd " + filepath.Base(unchecked) + "; git push; }; f'", true},
		{unguarded, "bash -c $'cd " + filepath.Dir(unchecked) + "; cd " + filepath.Base(unchecked) + "; git push\\nif'", true},
		{unguarded, "case x in x) cd " + filepath.Dir(unchecked) + " ;& y) git -C " + filepath.Base(unchecked) + " push ;; esac", true},
		{unguarded, "env -u git git -C " + unchecked + " \"push\"", true},
		{unguarded, "case x in \"$(git -C " + unchecked + " push)\") :;; esac", true},
		{unguarded, "f() { cd " + filepath.Dir(unchecked) + "; }; f; cd " + filepath.Base(unchecked) + "; git push", true},
		{unguarded, "f() { cd " + filepath.Dir(filepath.Dir(unchecked)) + "; }; g() { cd " + filepath.Base(filepath.Dir(unchecked)) + "; }; f; g; cd " + filepath.Base(unchecked) + "; git push", true},
		{unguarded, "f() { cd " + filepath.Dir(filepath.Dir(unchecked)) + "; }; for i in 1; do f; done; cd " + filepath.Base(filepath.Dir(unchecked)) + "; cd " + filepath.Base(unchecked) + "; git push", true},
		{unguarded, "f() { cd " + filepath.Dir(unchecked) + "; cd " + filepath.Base(unchecked) + "; git push; }; trap -- f EXIT", true},
		{unguarded, "f() { cd " + filepath.Dir(unchecked) + "; cd " + filepath.Base(unchecked) + "; git push; }; cmd=f; $cmd", true},
		{unguarded, "awk '{system($0)}' <<'EOF'\ncd " + filepath.Dir(unchecked) + "; cd " + filepath.Base(unchecked) + "; git push origin HEAD\nEOF", true},
		{unguarded, "awk '{system($0)}' < <(cat <<'EOF'\ngit -C " + unchecked + " push origin HEAD\nEOF\n)", true},
		{unguarded, "cat > >(awk '{system($0)}') <<'EOF'\ngit -C " + unchecked + " push origin HEAD\nEOF", true},
		{unguarded, "cat > cmds <<'EOF'\ngit -C " + unchecked + " push origin HEAD\nEOF\nawk '{system($0)}' cmds", true},
		{unguarded, "awk '{print}' ${x:-<(cd " + filepath.Dir(unchecked) + "; cd " + filepath.Base(unchecked) + "; git push origin HEAD)}", true},
		{unguarded, "command() { cd " + filepath.Dir(filepath.Dir(unchecked)) + "; }; command; cd " + filepath.Base(filepath.Dir(unchecked)) + "; cd " + filepath.Base(unchecked) + "; git push", true},
		{unguarded, "g() { :; }; f() { cd " + filepath.Dir(filepath.Dir(unchecked)) + "; }; " + strings.Repeat("g; ", 70) + "f; cd " + filepath.Base(filepath.Dir(unchecked)) + "; cd " + filepath.Base(unchecked) + "; git push", true},
		{unguarded, "source /dev/stdin <<'EOF'\ncd " + filepath.Dir(filepath.Dir(unchecked)) + "\nEOF\ncd " + filepath.Base(filepath.Dir(unchecked)) + "; cd " + filepath.Base(unchecked) + "; git push", true},
		{unguarded, "source /dev/stdin <<'EOF'\ncd /\nEOF\n" + deepCds(unchecked) + "git push", true},
		{unguarded, "g() { :; }; f() { cd /; }; " + strings.Repeat("g; ", 70) + "f; " + deepCds(unchecked) + "git push", true},
		{unguarded, "g() { :; }; f() { cd /; }; " + strings.Repeat("g; ", 70) + "cmd=f; $cmd; " + deepCds(unchecked) + "git push", true},
		// A line past the reader's bounds is blocked wherever it runs.
		{unguarded, "g() { :; }; h() { cd /; }; f() { h; }; " + strings.Repeat("g; ", 70) + "f; " + deepCds(unchecked) + "git push", true},
		{unguarded, "g() { :; }; " + strings.Repeat("g; ", 70) + "git push", true},
		{unguarded, "echo " + strings.Repeat("word ", 300) + "; find " + unchecked + " -prune -exec git -C " + unchecked + " \"push\" \\;", true},
		{unguarded, "f() { cd " + filepath.Dir(unchecked) + "; cd " + filepath.Base(unchecked) + "; git push; }; export -f f; find . -prune -exec bash -c f \\;", true},
		{unguarded, "source /dev/stdin <<-'OUT'; :\n\tcat <<X\n\tdata\n\tX\n\tcd " + filepath.Dir(unchecked) + "\n\tcd " + filepath.Base(unchecked) + "\n\tgit push\n\tOUT", true},
		{unguarded, "for i in 1; do cd " + filepath.Dir(unchecked) + "; done; cd " + filepath.Base(unchecked) + "; git push", true},
		{unguarded, "bash -c \"cat > /tmp/p.sh <<'EOF'\ncd " + filepath.Dir(unchecked) + "; cd " + filepath.Base(unchecked) + "; git push\nEOF\nbash /tmp/p.sh\"", true},
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

// deepCds is a cd into each part of path in turn, relative after the first.
func deepCds(path string) string {
	var b strings.Builder
	for _, part := range strings.Split(strings.TrimPrefix(filepath.ToSlash(path), "/"), "/") {
		b.WriteString("cd " + part + "; ")
	}
	return b.String()
}

// TestPushTargetsStayFastOnNestedHeredocs covers a short line that would
// read the same heredocs again and again: it ends quickly, past the reader's
// bounds.
func TestPushTargetsStayFastOnNestedHeredocs(t *testing.T) {
	line := "git push"
	for range 7 {
		line = "cat <<'EOF'\n" + line + "\nEOF\n" + strings.Repeat("bash -c :\n", 20)
	}
	line += "git push"
	done := make(chan string)
	go func() {
		_, unsure := pushTargets(t.TempDir(), line)
		done <- unsure
	}()
	select {
	case unsure := <-done:
		if unsure == "" {
			t.Errorf("read as sure")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("pushTargets did not finish")
	}
}

// TestPushTargetsLimitDeepFunctionBodies covers a function defined deeper
// than the reader reads: its body may push anywhere, so the line is too
// complex to follow.
func TestPushTargetsLimitDeepFunctionBodies(t *testing.T) {
	line := "f() { cd; git push; }; find . -prune -exec bash -c f \\;"
	for range 8 {
		line = "bash -c '" + strings.ReplaceAll(line, "'", `'\''`) + "'"
	}
	if _, unsure := pushTargets(t.TempDir(), line); unsure != unsureLimit {
		t.Errorf("unsure %q, want %q", unsure, unsureLimit)
	}
}

// TestNamedDirsStaysFastOnNestedHeredocs covers fifty nested heredocs with
// command substitutions: each is parsed once, so namedDirs ends quickly.
func TestNamedDirsStaysFastOnNestedHeredocs(t *testing.T) {
	line := "git push"
	for i := range 50 {
		tag := fmt.Sprintf("E%d", i)
		line = "cat <<" + tag + "\n$(" + line + ")\n" + tag
	}
	done := make(chan struct{})
	go func() {
		namedDirs(t.TempDir(), line)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("namedDirs did not finish")
	}
}

// TestPushTargetsInheritedStartupFile covers BASH_ENV in the gate's own
// environment: a startup file may run code before a script on disk.
func TestPushTargetsInheritedStartupFile(t *testing.T) {
	dir := t.TempDir()
	if _, unsure := pushTargets(dir, "bash scripts/check.sh && git push"); unsure != "" {
		t.Fatalf("unsure %q without BASH_ENV", unsure)
	}
	t.Setenv("BASH_ENV", "/dev/fd/3")
	if _, unsure := pushTargets(dir, "bash scripts/check.sh && git push"); unsure == "" {
		t.Error("read as sure with BASH_ENV set")
	}
}

// TestPushTargetsLimitBranchingCds covers branches that each move the shell
// one of two ways, twenty in a row: past the reader's bounds, not a million
// directories.
func TestPushTargetsLimitBranchingCds(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a", "b"} {
		if err := os.Symlink(".", filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	line := strings.Repeat("if :; then cd a; else cd b; fi; ", 20) + "git push"
	done := make(chan string)
	go func() {
		_, unsure := pushTargets(dir, line)
		done <- unsure
	}()
	select {
	case unsure := <-done:
		if unsure != unsureLimit {
			t.Errorf("unsure %q, want %q", unsure, unsureLimit)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("pushTargets did not finish")
	}
}

// TestNamedDirsStaysFastOnManyCds covers two thousand cds to distinct
// directories: namedDirs stops at its bounds.
func TestNamedDirsStaysFastOnManyCds(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := range 2000 {
		dir := filepath.Join(root, fmt.Sprint(i))
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		b.WriteString("cd " + dir + "; ")
	}
	b.WriteString(`cd "$dir"; git push`)
	done := make(chan []string)
	go func() {
		named, _ := namedDirs(root, b.String())
		done <- named
	}()
	select {
	case named := <-done:
		if len(named) < maxNamedDirs {
			t.Errorf("named %d directories, want at least %d", len(named), maxNamedDirs)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("namedDirs did not finish")
	}
}

// TestPushTargetsStayFastOnNestedLoops covers thousands of nested loops:
// each is judged once.
func TestPushTargetsStayFastOnNestedLoops(t *testing.T) {
	line := strings.Repeat("for i in 1; do ", 3000) + ":" + strings.Repeat("; done", 3000) + "; git push"
	done := make(chan struct{})
	go func() {
		pushTargets(t.TempDir(), line)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("pushTargets did not finish")
	}
}

// TestPushTargetsStayFastOnNestedFunctions covers thousands of nested
// function definitions: each body is read once.
func TestPushTargetsStayFastOnNestedFunctions(t *testing.T) {
	line := strings.Repeat("f() { ", 3000) + ":" + strings.Repeat("; }", 3000) + "; git push"
	done := make(chan struct{})
	go func() {
		pushTargets(t.TempDir(), line)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("pushTargets did not finish")
	}
}

// TestPushTargetsStayFastOnLongFunctions covers a line that would read a
// long function body thousands of times: it ends quickly, past the reader's
// bounds.
func TestPushTargetsStayFastOnLongFunctions(t *testing.T) {
	line := "f() { " + strings.Repeat(":; ", 20000) + "}; " + strings.Repeat("f; ", 20000) + "git push"
	done := make(chan string)
	go func() {
		_, unsure := pushTargets(t.TempDir(), line)
		done <- unsure
	}()
	select {
	case unsure := <-done:
		if unsure != unsureLimit {
			t.Errorf("unsure %q, want %q", unsure, unsureLimit)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("pushTargets did not finish")
	}
}
