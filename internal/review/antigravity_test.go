package review

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/gitx"
)

func TestGeminiIsOptIn(t *testing.T) {
	if slices.Contains(Families, Gemini) {
		t.Fatal("slopcop init would pick gemini by itself")
	}
	if f, err := ParseFamily("gemini"); err != nil || f != Gemini {
		t.Fatalf("ParseFamily: %q %v", f, err)
	}
	// Copilot must not take over a family whose restrictions it does not apply.
	if got := AdapterFor(Gemini, Settings{Provider: ProviderCopilot}); got != antigravityAdapter {
		t.Fatalf("adapter %q", got)
	}
}

func TestFamilyOfGemini(t *testing.T) {
	for model, want := range map[string]bool{
		"gemini-3.8-flash":       true,
		"google/gemini-4-pro":    true,
		"gemini-3.8-flash-lite":  false,
		"gemini-3.8-flash-image": false,
		"gemini-embedding-001":   false,
		"claude-sonnet-4-6":      false,
		"gpt-oss-120b-medium":    false,
	} {
		f, ok := FamilyOf(model)
		if got := ok && f == Gemini; got != want {
			t.Errorf("%s: family %q, ok %v", model, f, ok)
		}
	}
}

func toolLine(update, id, name, status string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":%q,"toolCallId":%q,"name":%q,"status":%q}}}`+"\n", update, id, name, status)
}

func TestToolGuard(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines []string
		want  string
	}{
		{"read", []string{toolLine("tool_call", "1", "read_file", "in_progress"), toolLine("tool_call_update", "1", "", "completed")}, ""},
		{"refused command", []string{toolLine("tool_call", "1", "run_command", "in_progress"), toolLine("tool_call_update", "1", "", "failed")}, ""},
		{"command ran without approval", []string{toolLine("tool_call", "1", "run_command", "in_progress"), toolLine("tool_call_update", "1", "", "completed")}, "not approved"},
		{"edit reported as done", []string{toolLine("tool_call", "1", "edit_file", "completed")}, "edit_file"},
		{"web search", []string{toolLine("tool_call", "1", "search_web", "in_progress")}, "search_web"},
		{"subagent", []string{toolLine("tool_call", "1", "invoke_subagent", "in_progress")}, "invoke_subagent"},
		{"unknown tool", []string{toolLine("tool_call", "1", "new_tool", "pending")}, "new_tool"},
		{"unnamed tool", []string{toolLine("tool_call", "1", "", "pending")}, "unnamed"},
		{"not a tool update", []string{`{"jsonrpc":"2.0","id":1,"result":{"name":"search_web"}}` + "\n", "not json\n"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got error
			g := &toolGuard{trip: func(err error) { got = err }}
			// Feed it in small pieces: a read need not end at a line break.
			stream := []byte(strings.Join(tc.lines, ""))
			for len(stream) > 0 {
				n := min(7, len(stream))
				if w, err := g.Write(stream[:n]); w != n || err != nil {
					t.Fatalf("Write = %d, %v", w, err)
				}
				stream = stream[n:]
			}
			if tc.want == "" {
				if got != nil {
					t.Fatalf("tripped: %v", got)
				}
				return
			}
			if !errors.Is(got, errForbiddenTool) || !strings.Contains(got.Error(), tc.want) {
				t.Fatalf("got %v, want %q", got, tc.want)
			}
			// Nothing Antigravity reports is a request SlopCop refused itself.
			if text := explainFailure(Reviewer{Family: Gemini, Error: got.Error()}, got); !strings.Contains(text, "may have left this machine") {
				t.Fatalf("no warning: %s", text)
			}
		})
	}
}

func TestGeminiPromptNamesTheExportedRepository(t *testing.T) {
	p := reviewPrompt("/home/someone/private-repo", Capture{BaseTree: "base", Tree: "snap"}, intent{}, "", "diff", AdapterFor(Gemini, Settings{}))
	if strings.Contains(p, "/home/someone/private-repo") || !strings.Contains(p, "Repository root: repo\n") || !strings.Contains(p, "read_file") || strings.Contains(p, "git -C") {
		t.Fatal(p)
	}
}

// hostileCapture adds what a repository could use against a reviewer: hooks
// in the folder Antigravity loads them from, and a symlink out of the tree.
func hostileCapture(t *testing.T, root string, c Capture) Capture {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".agents"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".agents", "hooks.json"), []byte(`{"h":{"SessionStart":[{"type":"command","command":"false"}]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hosts", filepath.Join(root, "link")); err != nil {
		t.Skip(err)
	}
	if _, err := gitx.Run(root, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	tree, err := gitx.Run(root, "write-tree")
	if err != nil {
		t.Fatal(err)
	}
	out, err := NewCapture(root, c.BaseCommit, tree)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestGeminiReviewerRunsInAnIsolatedWorkspace(t *testing.T) {
	root, c, store := reviewRepo(t)
	c = hostileCapture(t, root, c)
	trace := filepath.Join(t.TempDir(), "trace")
	v := Run(context.Background(), store, c, Options{Root: root, Settings: Settings{Reviewers: []Family{Gemini}, Adapters: map[string][]string{antigravityAdapter: fakeAdapter("workspace", "gemini-3.8-flash", trace)}}})
	if v.Status != StatusClean || v.Reviewers[0].Adapter != antigravityAdapter || v.Reviewers[0].Model != "gemini-3.8-flash" {
		t.Fatalf("%+v", v)
	}
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{
		"exists repo/change.txt=true",
		"exists repo/.agents/hooks.json=true",
		"exists repo/link=false",
		"exists .agents=false",
		"index=repo/.agents/hooks.json,repo/change.txt",
		"--disable-telemetry",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("trace lacks %q:\n%s", want, got)
		}
	}
	var workspace, configDir string
	for _, line := range strings.Split(got, "\n") {
		if v, ok := strings.CutPrefix(line, "session-cwd="); ok {
			workspace = v
		}
		if _, after, ok := strings.Cut(line, "--gemini-dir="); ok {
			configDir, _, _ = strings.Cut(after, " ")
		}
	}
	if workspace == "" || configDir == "" || filepath.Dir(workspace) != filepath.Dir(configDir) {
		t.Fatalf("workspace %q, config %q", workspace, configDir)
	}
	if insideGitRepository(workspace) {
		t.Fatalf("workspace %s is inside a repository", workspace)
	}
	if _, err := os.Stat(filepath.Dir(workspace)); !os.IsNotExist(err) {
		t.Fatalf("temporary directory left behind: %v", err)
	}
}

func TestGeminiToolThatCannotBeRefusedStopsTheReviewer(t *testing.T) {
	root, c, store := reviewRepo(t)
	trace := filepath.Join(t.TempDir(), "trace")
	v := Run(context.Background(), store, c, Options{Root: root, Settings: Settings{Reviewers: []Family{Gemini}, Adapters: map[string][]string{antigravityAdapter: fakeAdapter("ungated-tool", "gemini-3.8-flash", trace)}}})
	if v.Status != StatusFailed || len(v.Reviewers) != 1 {
		t.Fatalf("%+v", v)
	}
	r := v.Reviewers[0]
	if !strings.Contains(r.Error, errForbiddenTool.Error()) || !strings.Contains(r.Error, "may have left this machine") || !strings.Contains(r.Error, "text aimed at reviewers") || r.FirstError != "" {
		t.Fatalf("%+v", r)
	}
	data, _ := os.ReadFile(trace)
	if n := strings.Count(string(data), "prompt\n"); n != 1 {
		t.Fatalf("%d prompts, want no retry:\n%s", n, data)
	}
}

func TestGeminiReadOutsideTheWorkspaceIsRefused(t *testing.T) {
	root, c, store := reviewRepo(t)
	trace := filepath.Join(t.TempDir(), "trace")
	v := Run(context.Background(), store, c, Options{Root: root, Settings: Settings{Reviewers: []Family{Gemini}, Adapters: map[string][]string{antigravityAdapter: fakeAdapter("outside-read", "gemini-3.8-flash", trace)}}})
	if v.Status != StatusFailed || !strings.Contains(v.Reviewers[0].Error, "/outside/secret") {
		t.Fatalf("%+v", v)
	}
	data, _ := os.ReadFile(trace)
	if strings.Contains(string(data), "permission=allow") || strings.Count(string(data), "permission=deny") != 2 {
		t.Fatalf("want a refusal on the attempt and on its retry:\n%s", data)
	}
}

func TestGeminiDoesNotApproveADiffItCannotSee(t *testing.T) {
	root, c, store := reviewRepo(t)
	if err := os.WriteFile(filepath.Join(root, "large.py"), []byte(strings.Repeat("value = 1\n", (256*1024)/5)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.Run(root, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	tree, err := gitx.Run(root, "write-tree")
	if err != nil {
		t.Fatal(err)
	}
	c, err = NewCapture(root, c.BaseCommit, tree)
	if err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(t.TempDir(), "trace")
	fallback := filepath.Join(t.TempDir(), "fallback")
	v := Run(context.Background(), store, c, Options{Root: root, Settings: Settings{Reviewers: []Family{Gemini}, Fallbacks: []Family{Haiku}, MinCompleted: 1, Adapters: map[string][]string{
		antigravityAdapter: fakeAdapter("clean", "gemini-3.8-flash", trace),
		"claude":           fakeAdapter("clean", "claude-haiku-5-5", fallback),
	}}})
	if v.Status != StatusClean || len(v.Reviewers) != 1 {
		t.Fatalf("on-demand diff review: %+v", v)
	}
	if _, err := os.Stat(trace); err != nil {
		t.Fatal("adapter did not start", err)
	}
}

func TestGeminiDoesNotApproveAChangeToAFileItCannotRead(t *testing.T) {
	root, c, store := reviewRepo(t)
	git := func(args ...string) string {
		t.Helper()
		out, err := gitx.Run(root, args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	large := strings.Repeat("value = 1\n", antigravityMaxFile/5)
	if err := os.WriteFile(filepath.Join(root, "large.py"), []byte(large), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "large file")
	base := git("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(root, "large.py"), []byte(large+"value = 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	c, err := NewCapture(root, base, git("write-tree"))
	if err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(t.TempDir(), "trace")
	v := Run(context.Background(), store, c, Options{Root: root, Settings: Settings{Reviewers: []Family{Gemini}, Fallbacks: []Family{Haiku}, MinCompleted: 1, Adapters: map[string][]string{
		antigravityAdapter: fakeAdapter("clean", "gemini-3.8-flash", trace),
		"claude":           fakeAdapter("clean", "claude-haiku-5-5", filepath.Join(t.TempDir(), "fallback")),
	}}})
	if v.Status != StatusClean || len(v.Reviewers) != 2 {
		t.Fatalf("want the fallback to review it: %+v", v)
	}
	gemini := v.Reviewers[0]
	if gemini.Status != StatusFailed || !strings.Contains(gemini.Error, "large.py") || !strings.Contains(gemini.Error, "over 1 MiB") || gemini.FirstError != "" {
		t.Fatalf("%+v", gemini)
	}
	if _, err := os.Stat(trace); !os.IsNotExist(err) {
		t.Fatal("the Gemini adapter started on a change it cannot read")
	}
}

func TestAntigravityRefusesACommandThatSkipsPermissions(t *testing.T) {
	root, c, store := reviewRepo(t)
	trace := filepath.Join(t.TempDir(), "trace")
	command := append(fakeAdapter("clean", "gemini-3.8-flash", trace), "--dangerously-skip-permissions")
	v := Run(context.Background(), store, c, Options{Root: root, Settings: Settings{Reviewers: []Family{Gemini}, Adapters: map[string][]string{antigravityAdapter: command}}})
	if v.Status != StatusFailed || !strings.Contains(v.Reviewers[0].Error, "cannot be restricted") {
		t.Fatalf("%+v", v)
	}
	if _, err := os.Stat(trace); !os.IsNotExist(err) {
		t.Fatal("the adapter started")
	}
}

func TestAntigravityWorkspaceRefusesATempDirInsideARepository(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("TMPDIR")
	}
	root, c, _ := reviewRepo(t)
	t.Setenv("TMPDIR", root)
	if _, _, err := antigravityWorkspace(context.Background(), root, reviewTrees{base: c.BaseTree, checked: c.CheckedTree()}); err == nil || !strings.Contains(err.Error(), "inside a git repository") {
		t.Fatalf("err = %v", err)
	}
	entries, err := filepath.Glob(filepath.Join(root, "slopcop-antigravity-*"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("left behind: %v %v", entries, err)
	}
}
