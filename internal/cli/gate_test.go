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
		{`git -C my\ dir push`, true},
		{`git -C ../"my dir"/sub push`, true},
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
		// Text the shell does not run as a command.
		{"python3 - <<'EOF'\np = 'internal/cli/gate_test.go'\ns = open(p).read().replace('\"git \" + \"push origin HEAD\"', '\"git push origin HEAD\"')\nopen(p, 'w').write(s)\nEOF", false},
		{`sed -i '' 's/"git " + "push origin HEAD"/"git push origin HEAD"/' file.go`, false},
		{`git commit -m "make git push retry"`, false},
		{`gh issue comment 1 --body "run git push after the check"`, false},
		{"cat > notes.md <<'EOF'\ngit push origin HEAD\nEOF", false},
		{"git status # then git push", false},
		{`python3 -c "print('git push')"`, false},
		// Code the line runs.
		{`bash -c "git push"`, true},
		{`sh -c 'cd x && git push origin HEAD'`, true},
		{`eval "git push"`, true},
		{`sudo -u me sh -c 'git push'`, true},
		{`echo "git push" | sh`, true},
		{"sh <<'EOF'\ngit push\nEOF", true},
		{"cat <<EOF\n$(git push)\nEOF", true},
		{`echo "$(git push)"`, true},
		{`python3 -c "import os; os.system('git push')"`, true},
		{"python3 - <<'EOF'\nimport subprocess\nsubprocess.run(['git', 'push', 'origin', 'HEAD'])\nEOF", true},
		{`node -e "require('child_process').execSync('gh pr create --fill')"`, true},
		{`python3 -c "import os; os.system('git -C . push origin HEAD')"`, true},
		{`python3 -c "import subprocess; subprocess.run(['git', '-c', 'k=v', 'push'])"`, true},
		{`bash <<< "git push"`, true},
		{`ruby -e 'system "git push origin HEAD"'`, true},
		{"source /dev/stdin <<< 'git push origin HEAD'", true},
		{`git -c alias.publish='!git push origin HEAD' publish`, true},
		{`git -c core.editor='git push origin HEAD; true' commit --allow-empty`, true},
		{`printf 'git push origin HEAD\n' | sed -n e`, true},
		{`printf 'git push origin HEAD\n' | (cd .; sh)`, true},
		{`printf 'git push origin HEAD\n' | grep push`, false},
		{`bash -c 'source /dev/stdin' <<< 'git push origin HEAD'`, true},
		{`git "push" origin HEAD`, true},
		{`git -C . 'push'`, true},
		{`gh "pr" "create" --fill`, true},
		{"source /dev/stdin <<'EOF'\ngit push origin HEAD\nEOF", true},
		{"printf 'git push origin HEAD\\n' > /tmp/push.sh; sh /tmp/push.sh", true},
		{"printf 'git push origin HEAD\\n' > /tmp/push.sh; source /tmp/push.sh", true},
		{"printf 'git push origin HEAD\\n' > /tmp/push.sh; . /tmp/push.sh", true},
		{`bash -c "$(printf 'git push origin HEAD')"`, true},
		{`eval "$(echo 'git push origin HEAD')"`, true},
		{"printf 'import os; os.system(\"git push\")' > /tmp/p.py && python3 /tmp/p.py", true},
		{"cat > script.sh <<EOF\necho '$(git push origin HEAD)'\nEOF", true},
		{`GIT_EDITOR='git push origin HEAD #' git commit --allow-empty`, true},
		{`printf 'git push origin HEAD' | python3 -c 'import os,sys; os.system(sys.stdin.read())'`, true},
		{"sed -n 'e git push origin HEAD' /etc/hosts", true},
		{`make deploy ARGS="git push origin HEAD"`, true},
		{`python3 -c"import os; os.system('git push origin HEAD')"`, true},
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

// TestForgedApproval covers which commands that name the approval refs or
// the state directory the gate blocks: those that could write, remove or
// read them. Commands that only carry the name as text pass.
func TestForgedApproval(t *testing.T) {
	blocked := []string{
		// git's ref plumbing, porcelain and reads.
		"git update-ref refs/slopcop/reviewed/abc abc",
		"git update-ref -d refs/slopcop/reviewed/abc",
		"git -C ../other update-ref refs/slopcop/reviewed/abc abc",
		"git update-ref --stdin <<EOF\nupdate refs/slopcop/reviewed/abc abc\nEOF",
		"printf 'update refs/slopcop/reviewed/abc abc\\n' | git update-ref --stdin",
		"echo refs/slopcop/reviewed/abc | xargs -I{} git update-ref {} abc",
		"T=refs/slopcop/reviewed/abc; git update-ref $T abc",
		"git branch -f refs/slopcop/reviewed/abc abc",
		"git tag refs/slopcop/reviewed/abc abc",
		"git symbolic-ref refs/slopcop/reviewed/abc refs/heads/main",
		"git for-each-ref refs/slopcop",
		"git show-ref refs/slopcop/reviewed/abc",
		"git log refs/slopcop/reviewed/abc",
		"git grep pattern refs/slopcop/reviewed/abc",
		"git fetch . +HEAD:refs/slopcop/reviewed/abc",
		// Files under .git.
		"echo abc > .git/refs/slopcop/reviewed/abc",
		"echo abc >.git/refs/slopcop/reviewed/abc",
		`echo "abc refs/slopcop/reviewed/abc" >> .git/packed-refs`,
		"echo abc | tee .git/refs/slopcop/reviewed/abc",
		"echo 'abc refs/slopcop/reviewed/abc' | tee -a .git/packed-refs",
		"cp /tmp/x .git/refs/slopcop/reviewed/abc",
		"mv /tmp/x .git/refs/slopcop/reviewed/abc",
		"mkdir -p .git/refs/slopcop/reviewed",
		"ln -s ../heads/main .git/refs/slopcop/reviewed/abc",
		"find .git/refs/slopcop -delete",
		"sed -i 's/x/refs\\/slopcop\\/reviewed\\/abc/' .git/packed-refs",
		"cat > .git/refs/slopcop/reviewed/abc <<EOF\nabc\nEOF",
		"cat <<EOF > .git/refs/slopcop/reviewed/abc\nabc\nEOF",
		// Removing the state that arms the gate.
		"rm -rf .git/slopcop",
		"rm -rf .git/refs/slopcop",
		"mv .git/slopcop /tmp/off",
		`rm -rf .git\slopcop`,
		// Reads of the state.
		"cat .git/slopcop/state.json",
		"ls .git/refs/slopcop/reviewed",
		"grep -r abc .git/slopcop",
		"head < .git/slopcop/state.json",
		// Code that runs one of these.
		"bash -c 'git update-ref refs/slopcop/reviewed/abc abc'",
		"bash -lc \"rm -rf .git/slopcop\"",
		"sh <<'EOF'\ngit update-ref refs/slopcop/reviewed/abc abc\nEOF",
		"echo 'git update-ref refs/slopcop/reviewed/abc abc' | sh",
		`eval "git update-ref refs/slopcop/reviewed/abc abc"`,
		"python3 - <<'EOF'\nopen('.git/refs/slopcop/reviewed/abc', 'w').write('abc')\nEOF",
		"python3 - <<'EOF'\nimport subprocess\nsubprocess.run(['git', 'update-ref', 'refs/slopcop/reviewed/abc', 'abc'])\nEOF",
		`python3 -c "import shutil; shutil.rmtree('.git/slopcop')"`,
		"python3 write.py refs/slopcop/reviewed/abc",
		"echo refs/slopcop/reviewed/abc | python3 write.py",
		`node -e "require('fs').writeFileSync('.git/refs/slopcop/reviewed/abc', 'abc')"`,
		// A variable or substitution that carries the path.
		`state=.git/slopcop; rm -rf "$state"`,
		"T=refs/slopcop/reviewed/abc; cat > .git/$T",
		"cat <<EOF\n$(rm -rf .git/slopcop)\nEOF",
		"cat <<EOF\n`git update-ref refs/slopcop/reviewed/abc abc`\nEOF",
		`ref=.git/refs/slopcop/reviewed/abc; echo abc > "$ref"`,
		`STATE=.git/slopcop python3 -c 'import os, shutil; shutil.rmtree(os.environ["STATE"])'`,
		"cat <<E'OF'\nrefs/slopcop\nEOF\nrm -rf .git/slopcop",
		"cat <<\\EOF\nrefs/slopcop\nEOF\nrm -rf .git/slopcop",
		"gh release download v1 --pattern x -Ointernal/../.git/refs/slopcop/reviewed/abc",
		`ref=.git/refs/slopcop/reviewed/abc; printf '%s\n' abc | sed -n "w $ref"`,
		"echo refs/slopcop\\ #; rm -rf .git/slopcop",
		"printf 'rm -rf .git/slopcop\\n' | env sh",
		"grep -l refs/slopcop docs | sudo tee .git/slopcop/x",
		`python3 -c 'import subprocess; subprocess.run(["git", "fetch", ".", "HEAD:refs/slopcop/reviewed/abc"])'`,
		"echo $[1 << true ]\nrm -rf .git/slopcop\ntrue",
		"echo $((1 << 2)) refs/slopcop\nrm -rf .git/slopcop",
		"echo refs/slopcop \\\n# <<EOF\nrm -rf .git/slopcop\nEOF",
		"echo refs/slopcop\\\n#text; rm -rf .git/slopcop",
		"echo refs/slopcop <<EOF \"a\nb\"; rm -rf .git/slopcop\nEOF",
		"echo $((1\n<< 1))\nrm -rf .git/slopcop refs/slopcop",
		"python3 /tmp/approve.py <<EOF\nrefs/slopcop/reviewed/abc abc\nEOF",
		"bash /tmp/approve.sh <<EOF\nrefs/slopcop/reviewed/abc abc\nEOF",
		"printf 'HEAD:refs/slopcop/reviewed/abc\\n' | git fetch --stdin .",
		"rg --files -uuu .git/refs/slopcop/reviewed",
		"printf %s .git/slopcop | python3 -c 'import shutil,sys; shutil.rmtree(sys.stdin.read())'",
		`rg -c refs/slopcop | python3 -c "import sys; print(len(sys.stdin.readlines()))"`,
		"printf 'rm -rf .git/slopcop\\n' | sed e",
		"printf 'rm -rf .git/slopcop\\n' | sed 's/^//e'",
		"printf 'rm -rf .git/slopcop\\n' | sed 's|^||e'",
		"printf 'rm -rf .git/slopcop\\n' | sed 's|.*|&|e'",
		"printf 'rm -rf .git/slopcop\\n' | sed 'sX^XXe'",
		"printf 'rm -rf .git/slopcop\\n' | sed ' e'",
		"printf 'rm -rf .git/slopcop\\n' | sed 's/^//ep'",
		"printf %s .git/slopcop | (cd .; python3 -c 'import shutil,sys; shutil.rmtree(sys.stdin.read())')",
		`printf '%s\n' .git/slopcop | awk 'BEGIN {getline p; system("rm -rf " p)}'`,
		"grep --exclude-from .git/slopcop/config pattern docs/",
		"printf 'rm -rf .git/slopcop\\n' | sed 's|^|| e'",
		`awk 'BEGIN {ARGV[1]=".git/slopcop/last.json"; ARGC=2} {print}'`,
		`printf %s .git/slopcop | bash -c 'cd .; python3 -c "import shutil,sys; shutil.rmtree(sys.stdin.read())"'`,
		"printf %s .git/slopcop | (python3 -c 'import shutil,sys; shutil.rmtree(sys.stdin.read())')",
		"printf 'e rm -rf .git/slopcop\\n' | sed -f /dev/stdin /etc/hosts",
		`printf %s .git/slopcop | bash -c 'python3 -c "import shutil,sys; shutil.rmtree(sys.stdin.read())"'`,
		"python3 -c 'import shutil,sys; shutil.rmtree(sys.stdin.read().strip())' <<< .git/slopcop",
		`bash <<< "rm -rf .git/slopcop"`,
		"rg --ignore-file .git/slopcop/config.json needle docs/",
		"printf 'rm -rf .git/slopcop\\n' | . /dev/stdin",
		"printf 'rm -rf .git/slopcop\\n' | source /dev/stdin",
		"echo refs/slopcop/reviewed/abc | python3 -E approve.py",
		// Heredocs whose end the gate cannot be sure of.
		"sh <<'A' <<'B'\necho '\nA\nrm -rf .git/slopcop\nB",
		"cat <<$'EOF'\nEOF\nrm -rf .git/slopcop refs/slopcop",
		"cat <<EOF\nrefs/slopcop\nEO\\\nF\nrm -rf .git/slopcop\nEOF",
		"echo $((1 + ((1)) << 2))\nrm -rf .git/slopcop refs/slopcop\n2",
		"cat <<EOF\nrefs/slopcop\nrm -rf .git/slopcop",
		// A comment hides nothing from the shell.
		"echo refs/slopcop$(true)#text; rm -rf .git/slopcop",
		"echo refs/slopcop # don't touch state\nrm -rf .git/slopcop",
		"echo refs/slopcop # cat <<EOF\nrm -rf .git/slopcop\nEOF",
		// A glued value is not a cluster of text options.
		"gh release download v1 -O.git/refs/slopcop/reviewed/abc",
		"grep -f.git/slopcop/state.json file",
		// gh options that read or write files.
		"gh release download v1 --pattern x --output .git/refs/slopcop/reviewed/abc",
		"gh pr create --title x --body-file .git/slopcop/state.json",
		// A text tool in a line that also writes.
		"grep -rn refs/slopcop docs && git update-ref refs/slopcop/reviewed/abc abc",
		`gh pr create --body "refs/slopcop" ; echo abc > .git/refs/slopcop/reviewed/abc`,
	}
	allowed := []string{
		// Search patterns.
		`grep -rn "refs/slopcop" docs/`,
		"rg refs/slopcop internal",
		"rg -n -e 'refs/slopcop/review-only' -e '.git/slopcop' docs skills",
		`ag "refs/slopcop" .`,
		"rg -g '*.go' 'refs/slopcop' internal/",
		"grep -rn --include '*.md' -C 2 refs/slopcop docs",
		"echo refs/slopcop # it's only text",
		"git grep -n refs/slopcop -- docs",
		`git log --grep "refs/slopcop" --oneline`,
		"git log -S'refs/slopcop/review-only' -p",
		// Text sent elsewhere.
		`gh pr create --title "Gate" --body "Adds refs/slopcop/review-only/<tree> beside .git/slopcop"`,
		"gh pr create --title Gate --body \"$(cat <<'EOF'\nThe gate blocks `git update-ref refs/slopcop/...` and `rm -rf .git/slopcop`.\nDon't worry: it's fine.\nEOF\n)\"",
		`gh issue comment 239 --body "see refs/slopcop/review-only"`,
		`gh pr edit 244 -b "see .git/slopcop" -t "Gate: refs/slopcop"`,
		"gh pr create --title Gate --body \"$(cat <<EOF\nAdds refs/slopcop/review-only, no substitutions here.\nEOF\n)\"",
		`git commit -m "Gate: mark refs/slopcop/review-only trees"`,
		`git commit -am "drop .git/slopcop mentions"`,
		"git commit -F- <<'EOF'\nKeep refs/slopcop/reviewed when rm -rf .git/slopcop runs\nEOF",
		`echo "the approval lives at refs/slopcop/reviewed/<tree>"`,
		`printf '%s\n' refs/slopcop/review-only`,
		"echo refs/slopcop/x 2>&1 | head -1",
		// Reading and editing ordinary files that mention them.
		"sed -n '/refs\\/slopcop/p' docs/src/content/docs/reference/cli.md",
		"grep -rn refs/slopcop docs | sed 's/^docs\\///'",
		"grep -rn refs/slopcop docs | awk -F: '{print $1}'",
		"sed -i '' 's/refs\\/slopcop\\/old/refs\\/slopcop\\/new/' docs/cli.md",
		"cat > docs/notes.md <<'EOF'\nThe gate reads refs/slopcop/reviewed/<tree> and .git/slopcop.\nEOF",
		// Scripts whose code only carries the name.
		"python3 - <<'EOF'\nimport re\np = 'internal/cli/gate.go'\ns = open(p).read()\ns = s.replace('refs/slopcop/reviewed/', 'refs/slopcop/review-only/')\nopen(p, 'w').write(s)\nEOF",
		`python3 -c "print('refs/slopcop')"`,
		`bash -c "grep -rn refs/slopcop docs"`,
		"bash -o pipefail -c 'echo refs/slopcop'",
		`python3 -W ignore -c "print('refs/slopcop')"`,
		"python3 -W ignore - <<'EOF'\nprint('refs/slopcop')\nEOF",
		`python3 -E -c "print('refs/slopcop')"`,
		// Neither named.
		"git log refs/heads/slopcop-fix",
		"slopcop check --format json",
		"git update-ref refs/heads/topic HEAD",
		"rm -rf .git/info/exclude.bak",
		"cat .git/HEAD",
		"echo '<<EOF' && echo refs/slopcop",
	}
	for _, command := range blocked {
		if !forgedApproval(command) {
			t.Errorf("allowed, want blocked: %q", command)
		}
	}
	for _, command := range allowed {
		if forgedApproval(command) {
			t.Errorf("blocked, want allowed: %q", command)
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
		if got := advisoryFindings(response, level, pullRequestFailOn); got != want {
			t.Errorf("advisoryFindings at %s = %d, want %d", level, got, want)
		}
	}
	// A repository's [check] fail_on is the pull request check's threshold:
	// only the findings between it and the push level are advisory.
	for prLevel, want := range map[string]int{"warning": 3, "error": 2, "never": 0} {
		if got := advisoryFindings(response, "never", prLevel); got != want {
			t.Errorf("advisoryFindings at never under pull request level %s = %d, want %d", prLevel, got, want)
		}
	}
	// Findings the service left out of the list block no push and still fail
	// the pull request check, even when nothing listed is advisory.
	response = service.Response{Findings: []service.Finding{{New: true, Severity: "warning", Certainty: "possible"}}}
	response.Summary.OmittedFindings = 4
	if got := advisoryFindings(response, "error", pullRequestFailOn); got != 4 {
		t.Errorf("advisoryFindings with 4 omitted = %d, want 4", got)
	}
	// When the push level is the pull request's, nothing is advisory, not
	// even the omitted findings.
	if got := advisoryFindings(response, "warning", "warning"); got != 0 {
		t.Errorf("advisoryFindings at equal levels with 4 omitted = %d, want 0", got)
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
