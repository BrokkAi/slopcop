package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/gitx"
	acp "github.com/coder/acp-go-sdk"
)

func grokSettings(mode, trace string) Settings {
	return Settings{Reviewers: []Family{Grok}, Adapters: map[string][]string{grokAdapter: fakeAdapter(mode, "grok-4.6", trace)}}
}

func TestGrokIsOptIn(t *testing.T) {
	if slices.Contains(Families, Grok) {
		t.Fatal("slopcop init would pick grok by itself")
	}
	if f, err := ParseFamily("grok"); err != nil || f != Grok {
		t.Fatalf("ParseFamily: %q %v", f, err)
	}
	if got := AdapterFor(Grok, Settings{Provider: ProviderCopilot}); got != grokAdapter {
		t.Fatalf("adapter %q", got)
	}
}

func TestFamilyOfGrok(t *testing.T) {
	for model, want := range map[string]bool{
		"grok-4.7":                     true,
		"xai/grok-5":                   true,
		"grok-build":                   true,
		"grok-4.20-0309-reasoning":     false,
		"grok-4.20-0309-non-reasoning": false,
		"grok-4.20-multi-agent-0309":   false,
		"grok-4.6-mini":                false,
		"grok-4-fast":                  false,
		"grok-code-fast-1":             false,
		"grok-imagine-image-2.0":       false,
		"grok-imagine-video-1.5":       false,
		"gemini-3.8-flash":             false,
	} {
		f, ok := FamilyOf(model)
		if got := ok && f == Grok; got != want {
			t.Errorf("%s: family %q, ok %v", model, f, ok)
		}
	}
}

// The catalog Grok Build 1.0.92 offered an API key.
func TestBestGrokModelSkipsDatedVariants(t *testing.T) {
	var options []acp.SessionConfigSelectOption
	for _, m := range []string{"grok-4.20-0309-non-reasoning", "grok-4.20-0309-reasoning", "grok-4.20-multi-agent-0309", "grok-4.3", "grok-4.5", "grok-4.6", "grok-4.7", "grok-imagine-image", "grok-imagine-image-2.0", "grok-imagine-video", "grok-imagine-video-1.5", "grok-imagine-video-1.5-lite"} {
		options = append(options, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(m), Name: m})
	}
	if got, ok := bestModel(Grok, options); !ok || got != "grok-4.7" {
		t.Fatalf("best = %q, %v", got, ok)
	}
}

func grokFsLine(path string) string {
	data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "method": "fs/read_text_file", "params": map[string]any{"sessionId": "s", "path": path}})
	return string(data) + "\n"
}

func grokToolLine(t *testing.T, kind, id, name, status string, input map[string]any, raw map[string]any) string {
	t.Helper()
	update := map[string]any{"sessionUpdate": kind, "toolCallId": id, "status": status}
	if name != "" || input != nil {
		tool := map[string]any{}
		if name != "" {
			tool["name"] = name
		}
		if input != nil {
			tool["input"] = input
		}
		update["_meta"] = map[string]any{"x.ai/tool": tool}
	}
	if raw != nil {
		update["rawInput"] = raw
	}
	data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "s", "update": update}})
	if err != nil {
		t.Fatal(err)
	}
	return string(data) + "\n"
}

// feedGrokGuard writes lines to the guard and, as the client would, serves
// each read request the guard let through.
func feedGrokGuard(t *testing.T, c *grokToolCheck, g *toolGuard, lines []string) {
	t.Helper()
	for _, line := range lines {
		if _, err := g.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
		var m struct {
			Method string `json:"method"`
			Params struct {
				Path string `json:"path"`
			} `json:"params"`
		}
		if json.Unmarshal([]byte(line), &m) == nil && m.Method == "fs/read_text_file" && !g.tripped {
			c.served(m.Params.Path)
		}
	}
}

func TestGrokToolGuard(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Grok reviewers do not run on Windows, and these are Unix paths")
	}
	const ws = "/tmp/slopcop-x/workspace"
	path := func(p string) map[string]any { return map[string]any{"path": p} }
	for _, tc := range []struct {
		name  string
		lines []string
		want  string
	}{
		{"read inside", []string{grokToolLine(t, "tool_call", "1", "read_file", "in_progress", nil, nil), grokToolLine(t, "tool_call_update", "1", "", "in_progress", path(ws+"/repo/a.go"), nil), grokFsLine(ws + "/repo/a.go"), grokToolLine(t, "tool_call_update", "1", "", "completed", nil, nil)}, ""},
		{"relative read", []string{grokFsLine(ws + "/repo/a.go"), grokToolLine(t, "tool_call", "1", "read_file", "completed", path("repo/a.go"), nil)}, ""},
		{"failed read serves nothing", []string{grokToolLine(t, "tool_call", "1", "read_file", "in_progress", path("repo/a.go"), nil), grokToolLine(t, "tool_call_update", "1", "", "failed", nil, nil)}, ""},
		{"not a tool update", []string{`{"jsonrpc":"2.0","id":1,"result":{"name":"bash"}}` + "\n", "not json\n"}, ""},
		{"failed then read from disk", []string{grokFsLine(ws + "/repo/a.go"), grokToolLine(t, "tool_call", "1", "read_file", "failed", path("repo/a.go"), nil), grokToolLine(t, "tool_call", "2", "read_file", "completed", path("repo/a.go"), nil)}, "did not go through"},
		{"failed read credits no other file", []string{grokFsLine(ws + "/repo/a.go"), grokToolLine(t, "tool_call", "1", "read_file", "failed", path("repo/a.go"), nil), grokToolLine(t, "tool_call", "2", "read_file", "completed", path("repo/b.go"), nil)}, "did not go through"},
		{"served read of another file", []string{grokFsLine(ws + "/repo/b.go"), grokToolLine(t, "tool_call", "1", "read_file", "completed", path("repo/a.go"), nil)}, "did not go through"},
		{"two reads of one file", []string{grokFsLine(ws + "/repo/a.go"), grokFsLine(ws + "/repo/a.go"), grokToolLine(t, "tool_call", "1", "read_file", "completed", path("repo/a.go"), nil), grokToolLine(t, "tool_call", "2", "read_file", "completed", path(ws+"/repo/./a.go"), nil)}, ""},
		{"two paths on one call", []string{grokFsLine(ws + "/repo/a.go"), grokToolLine(t, "tool_call", "1", "read_file", "completed", path("repo/a.go"), map[string]any{"target_file": "repo/b.go"})}, "two different paths"},
		{"read from disk", []string{grokToolLine(t, "tool_call", "1", "read_file", "completed", path("repo/a.go"), nil)}, "did not go through"},
		{"second read from disk", []string{grokFsLine(ws + "/repo/a.go"), grokToolLine(t, "tool_call", "1", "read_file", "completed", path("repo/a.go"), nil), grokToolLine(t, "tool_call", "2", "read_file", "completed", path("repo/b.go"), nil)}, "did not go through"},
		{"client read outside", []string{grokFsLine("/etc/passwd")}, "outside"},
		{"client read traversal", []string{grokFsLine(ws + "/../secret")}, "outside"},
		{"client write", []string{`{"jsonrpc":"2.0","id":8,"method":"fs/write_text_file","params":{"path":"` + ws + `/repo/a.go","content":"x"}}` + "\n"}, "fs/write_text_file"},
		{"client terminal", []string{`{"jsonrpc":"2.0","id":9,"method":"terminal/create","params":{"command":"sh"}}` + "\n"}, "terminal/create"},
		{"absolute read outside", []string{grokToolLine(t, "tool_call", "1", "read_file", "in_progress", path("/etc/hosts"), nil)}, "outside"},
		{"parent traversal", []string{grokToolLine(t, "tool_call", "1", "read_file", "in_progress", path("repo/../../secret"), nil)}, "outside"},
		{"sibling prefix", []string{grokToolLine(t, "tool_call", "1", "read_file", "in_progress", path(ws+"-other/a"), nil)}, "outside"},
		{"home path", []string{grokToolLine(t, "tool_call", "1", "read_file", "in_progress", path("~/.ssh/id_rsa"), nil)}, "outside"},
		{"outside in raw input", []string{grokToolLine(t, "tool_call", "1", "read_file", "in_progress", nil, map[string]any{"target_file": "/etc/passwd"})}, "outside"},
		{"read with no path", []string{grokFsLine(ws + "/repo/a.go"), grokToolLine(t, "tool_call", "1", "read_file", "completed", nil, nil)}, "without a path"},
		{"grep", []string{grokToolLine(t, "tool_call", "1", "grep", "in_progress", path("repo"), nil)}, "grep"},
		{"list_dir", []string{grokToolLine(t, "tool_call", "1", "list_dir", "in_progress", map[string]any{"directory": "repo"}, nil)}, "list_dir"},
		{"shell", []string{grokToolLine(t, "tool_call", "1", "run_terminal_command", "pending", nil, nil)}, "run_terminal_command"},
		{"web search", []string{grokToolLine(t, "tool_call", "1", "web_search", "in_progress", nil, nil)}, "web_search"},
		{"subagent", []string{grokToolLine(t, "tool_call", "1", "task", "pending", nil, nil)}, "task"},
		{"mcp meta-tool", []string{grokToolLine(t, "tool_call", "1", "use_tool", "pending", nil, nil)}, "use_tool"},
		{"skill", []string{grokToolLine(t, "tool_call", "1", "skill", "pending", nil, nil)}, "skill"},
		{"unnamed", []string{grokToolLine(t, "tool_call", "1", "", "pending", nil, nil)}, "unnamed"},
		{"unannounced update", []string{grokToolLine(t, "tool_call_update", "9", "read_file", "completed", path("repo/a"), nil)}, "never announced"},
		{"renamed call", []string{grokToolLine(t, "tool_call", "1", "read_file", "pending", nil, nil), grokToolLine(t, "tool_call_update", "1", "run_terminal_command", "pending", nil, nil)}, "changed"},
		{"failed then completed", []string{grokToolLine(t, "tool_call", "1", "read_file", "failed", path("repo/a.go"), nil), grokToolLine(t, "tool_call_update", "1", "", "completed", nil, nil)}, "after it failed"},
		{"failed with no path after a served read", []string{grokToolLine(t, "tool_call", "1", "read_file", "in_progress", nil, nil), grokFsLine(ws + "/repo/a.go"), grokToolLine(t, "tool_call_update", "1", "", "failed", nil, nil)}, "failed without a path"},
		{"failed with no path and nothing served", []string{grokToolLine(t, "tool_call", "1", "read_file", "in_progress", nil, nil), grokToolLine(t, "tool_call_update", "1", "", "failed", nil, nil)}, ""},
		{"failed with no path after reads were accounted for", []string{grokFsLine(ws + "/repo/a.go"), grokToolLine(t, "tool_call", "1", "read_file", "completed", path("repo/a.go"), nil), grokToolLine(t, "tool_call", "2", "read_file", "failed", nil, nil)}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got error
			c := newGrokToolCheck(ws)
			g := &toolGuard{check: c.check, trip: func(err error) { got = err }}
			feedGrokGuard(t, c, g, tc.lines)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("tripped: %v", got)
				}
				return
			}
			if !errors.Is(got, errForbiddenTool) || !strings.Contains(got.Error(), tc.want) {
				t.Fatalf("got %v, want %q", got, tc.want)
			}
		})
	}
}

// TestGrokStopWording checks that only a request SlopCop did not serve, with
// no report that it finished, loses the warning that data may have left.
func TestGrokStopWording(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Grok reviewers do not run on Windows")
	}
	const ws = "/tmp/slopcop-x/workspace"
	path := func(p string) map[string]any { return map[string]any{"path": p} }
	for _, tc := range []struct {
		name      string
		lines     []string
		notServed bool
	}{
		{"client read outside", []string{grokFsLine("/etc/passwd")}, true},
		{"client write", []string{`{"jsonrpc":"2.0","id":8,"method":"fs/write_text_file","params":{"path":"` + ws + `/repo/a.go","content":"x"}}` + "\n"}, true},
		{"client terminal", []string{`{"jsonrpc":"2.0","id":9,"method":"terminal/create","params":{"command":"sh"}}` + "\n"}, true},
		{"read_file naming an outside path", []string{grokToolLine(t, "tool_call", "1", "read_file", "in_progress", path("/etc/hosts"), nil)}, true},
		{"failed read_file outside", []string{grokToolLine(t, "tool_call", "1", "read_file", "failed", path("/etc/hosts"), nil)}, true},
		{"read_file finished outside", []string{grokToolLine(t, "tool_call", "1", "read_file", "completed", path("/etc/hosts"), nil)}, false},
		{"outside path after it finished", []string{grokFsLine(ws + "/repo/a.go"), grokToolLine(t, "tool_call", "1", "read_file", "completed", path("repo/a.go"), nil), grokToolLine(t, "tool_call_update", "1", "", "", path("/etc/hosts"), nil)}, false},
		{"read_file outside with an unknown status", []string{grokToolLine(t, "tool_call", "1", "read_file", "done", path("/etc/hosts"), nil)}, false},
		{"read from disk", []string{grokToolLine(t, "tool_call", "1", "read_file", "completed", path("repo/a.go"), nil)}, false},
		{"read with no path", []string{grokFsLine(ws + "/repo/a.go"), grokToolLine(t, "tool_call", "1", "read_file", "completed", nil, nil)}, false},
		{"web search", []string{grokToolLine(t, "tool_call", "1", "web_search", "in_progress", nil, nil)}, false},
		{"subagent", []string{grokToolLine(t, "tool_call", "1", "task", "pending", nil, nil)}, false},
		{"unknown tool", []string{grokToolLine(t, "tool_call", "1", "new_tool", "pending", nil, nil)}, false},
		{"unnamed", []string{grokToolLine(t, "tool_call", "1", "", "pending", nil, nil)}, false},
		{"unannounced update", []string{grokToolLine(t, "tool_call_update", "9", "read_file", "completed", path("repo/a"), nil)}, false},
		{"renamed call", []string{grokToolLine(t, "tool_call", "1", "read_file", "pending", nil, nil), grokToolLine(t, "tool_call_update", "1", "run_terminal_command", "pending", nil, nil)}, false},
		// A tool's name is the agent's text, so it cannot buy the milder wording.
		{"tool named like the mild text", []string{grokToolLine(t, "tool_call", "1", "SlopCop did not serve this request", "pending", nil, nil)}, false},
		{"read that failed then completed", []string{grokToolLine(t, "tool_call", "1", "read_file", "failed", path("repo/a.go"), nil), grokToolLine(t, "tool_call_update", "1", "", "completed", nil, nil)}, false},
		{"pathless failure after a served read", []string{grokToolLine(t, "tool_call", "1", "read_file", "in_progress", nil, nil), grokFsLine(ws + "/repo/a.go"), grokToolLine(t, "tool_call_update", "1", "", "failed", nil, nil)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got error
			c := newGrokToolCheck(ws)
			g := &toolGuard{check: c.check, trip: func(err error) { got = err }}
			feedGrokGuard(t, c, g, tc.lines)
			if !errors.Is(got, errForbiddenTool) || retryable(context.Background(), got) {
				t.Fatalf("got %v", got)
			}
			text := explainFailure(Reviewer{Family: Grok, Error: got.Error()}, got)
			if strings.Contains(text, "may have left this machine") == tc.notServed || strings.Contains(text, "saw no sign") != tc.notServed || !strings.Contains(text, "text aimed at reviewers") {
				t.Fatalf("notServed %v: %s", tc.notServed, text)
			}
		})
	}
}

// TestGrokCreditsOnlyAReadItServed runs the guard with the real client: a
// request the client could not serve must not vouch for a finished read.
func TestGrokCreditsOnlyAReadItServed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Grok reviewers do not run on Windows")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a\n"), 0600); err != nil {
		t.Fatal(err)
	}
	check := newGrokToolCheck(root)
	client := &reviewClient{readRoot: root, onRead: check.served}
	var got error
	g := &toolGuard{check: check.check, trip: func(err error) { got = err }}
	read := func(id, file string, wantErr bool) {
		t.Helper()
		full := filepath.Join(root, file)
		if _, err := g.Write([]byte(grokFsLine(full))); err != nil {
			t.Fatal(err)
		}
		if _, err := client.ReadTextFile(context.Background(), acp.ReadTextFileRequest{Path: full}); (err != nil) != wantErr {
			t.Fatalf("%s: %v", file, err)
		}
		if _, err := g.Write([]byte(grokToolLine(t, "tool_call", id, "read_file", "completed", map[string]any{"path": file}, nil))); err != nil {
			t.Fatal(err)
		}
	}
	read("1", "a.txt", false)
	if got != nil {
		t.Fatalf("a served read tripped the guard: %v", got)
	}
	read("2", "missing.txt", true)
	if !errors.Is(got, errForbiddenTool) || !strings.Contains(got.Error(), "did not go through") {
		t.Fatalf("got %v", got)
	}
}

func TestGrokPromptNamesTheExportedRepository(t *testing.T) {
	p := reviewPrompt("/home/someone/private-repo", Capture{BaseTree: "base", Tree: "snap"}, intent{}, "", "diff", Grok)
	if strings.Contains(p, "/home/someone/private-repo") || !strings.Contains(p, "Repository root: repo\n") || !strings.Contains(p, "read_file") || strings.Contains(p, "git -C") {
		t.Fatal(p)
	}
}

func TestGrokEnvironmentDropsWhatCouldRedirectIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Grok reviewers do not run on Windows, and these are Unix paths")
	}
	got := grokEnvironment([]string{
		"PATH=/bin", "HOME=/Users/me", "XAI_API_KEY=k", "XAI_BASE_URL=https://evil.example",
		"GROK_SANDBOX=off", "GROK_MODELS_BASE_URL=https://evil.example", "GROK_AGENT=mine", "GROK_FOLDER_TRUST=0",
		"OTEL_EXPORTER_OTLP_ENDPOINT=https://evil.example", "XDG_CONFIG_HOME=/Users/me/.config",
	}, "/tmp/h", "/Users/me")
	joined := "\n" + strings.Join(got, "\n") + "\n"
	for _, gone := range []string{"evil.example", "GROK_SANDBOX=", "GROK_AGENT=", "GROK_FOLDER_TRUST=0", "HOME=/Users/me\n", "XDG_CONFIG_HOME=/Users/me"} {
		if strings.Contains(joined, gone) {
			t.Errorf("kept %q:\n%s", gone, joined)
		}
	}
	for _, want := range []string{"PATH=/bin", "XAI_API_KEY=k", "HOME=/tmp/h\n", "GROK_HOME=/tmp/h/.grok", "GROK_FOLDER_TRUST=1", "GROK_SUBAGENTS=0", "GROK_MEMORY=0", "GROK_WEB_FETCH=0", "GROK_RESPECT_GITIGNORE=0", "GROK_CLAUDE_HOOKS_ENABLED=0", "GROK_CLAUDE_MCPS_ENABLED=0", "GROK_CURSOR_HOOKS_ENABLED=0", "GROK_DISABLE_AUTOUPDATER=1", "npm_config_ignore_scripts=true", "npm_config_cache=/Users/me/.npm"} {
		if !strings.Contains(joined, want) {
			t.Errorf("lacks %q:\n%s", want, joined)
		}
	}
}

func TestGrokReviewerRunsContained(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Grok reviewers do not run on Windows")
	}
	t.Setenv("XAI_API_KEY", "test-value")
	t.Setenv("GROK_SANDBOX", "off")
	root, c, store := reviewRepo(t)
	if err := os.MkdirAll(filepath.Join(root, ".grok"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".grok", "hooks.json"), []byte(`{}`), 0600); err != nil {
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
	if c, err = NewCapture(root, c.BaseCommit, tree); err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(t.TempDir(), "trace")
	v := Run(context.Background(), store, c, Options{Root: root, Settings: grokSettings("grok-workspace", trace)})
	if v.Status != StatusClean || v.Reviewers[0].Adapter != grokAdapter || v.Reviewers[0].Model != "grok-4.6" {
		t.Fatalf("%+v", v)
	}
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if strings.Contains(got, "test-value") {
		t.Fatal("the key reached the trace")
	}
	for _, want := range []string{
		"exists repo/change.txt=true",
		"exists repo/.grok/hooks.json=true",
		"exists repo/link=false",
		"exists .grok=false",
		"key-set=true",
		"client-fs read=true write=true",
		"--sandbox " + grokSandboxProfile,
		"--disable-web-search",
		"--no-auto-update",
		"agent --agent-profile",
		"--no-leader stdio",
		`extends = "strict"`,
		fmt.Sprintf("restrict_network = %v", runtime.GOOS != "darwin"),
		"GROK_CLAUDE_HOOKS_ENABLED=0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("trace lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "GROK_SANDBOX=off") {
		t.Errorf("the user's sandbox setting reached the reviewer:\n%s", got)
	}
	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, realHome) {
		t.Errorf("the sandbox does not deny the real home %s:\n%s", realHome, got)
	}
	var workspace, processDir, grokHome string
	for _, line := range strings.Split(got, "\n") {
		if v, ok := strings.CutPrefix(line, "session-cwd="); ok {
			workspace = v
		}
		if v, ok := strings.CutPrefix(line, "process-cwd="); ok {
			processDir = v
		}
		if v, ok := strings.CutPrefix(line, "env GROK_HOME="); ok {
			grokHome = v
		}
	}
	if workspace == "" || processDir != workspace {
		t.Fatalf("process in %q, session in %q: the sandbox would root at the wrong folder", processDir, workspace)
	}
	if grokHome != filepath.Join(filepath.Dir(workspace), "home", ".grok") {
		t.Fatalf("GROK_HOME %q for workspace %q", grokHome, workspace)
	}
	if insideGitRepository(workspace) {
		t.Fatalf("workspace %s is inside a repository", workspace)
	}
	if _, err := os.Stat(filepath.Dir(workspace)); !os.IsNotExist(err) {
		t.Fatalf("temporary directory left behind: %v", err)
	}
}

func TestGrokForbiddenToolStopsTheReviewerWithoutARetry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Grok reviewers do not run on Windows")
	}
	t.Setenv("XAI_API_KEY", "test-value")
	for mode, warns := range map[string]bool{"grok-tool-web_search": true, "grok-tool-read_file:/etc/hosts": false, "grok-direct-read_file:repo/change.txt": true} {
		t.Run(mode, func(t *testing.T) {
			root, c, store := reviewRepo(t)
			trace := filepath.Join(t.TempDir(), "trace")
			v := Run(context.Background(), store, c, Options{Root: root, Settings: grokSettings(mode, trace)})
			if v.Status != StatusFailed || len(v.Reviewers) != 1 {
				t.Fatalf("%+v", v)
			}
			r := v.Reviewers[0]
			if !strings.Contains(r.Error, errForbiddenTool.Error()) || r.FirstError != "" {
				t.Fatalf("%+v", r)
			}
			if strings.Contains(r.Error, "may have left this machine") != warns {
				t.Fatalf("warns %v: %s", warns, r.Error)
			}
			data, _ := os.ReadFile(trace)
			if n := strings.Count(string(data), "prompt\n"); n != 1 {
				t.Fatalf("%d prompts, want no retry:\n%s", n, data)
			}
		})
	}
}

func TestGrokReadInsideTheWorkspaceFinishes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Grok reviewers do not run on Windows")
	}
	t.Setenv("XAI_API_KEY", "test-value")
	root, c, store := reviewRepo(t)
	trace := filepath.Join(t.TempDir(), "trace")
	v := Run(context.Background(), store, c, Options{Root: root, Settings: grokSettings("grok-tool-read_file:repo/change.txt", trace)})
	if v.Status != StatusClean {
		t.Fatalf("%+v", v)
	}
	if data, _ := os.ReadFile(trace); !strings.Contains(string(data), "read-bytes=") || strings.Contains(string(data), "read-bytes=0") {
		t.Fatalf("the read was not served:\n%s", data)
	}
}

func TestReadWorkspaceFileStaysInTheWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "workspace")
	if err = os.MkdirAll(filepath.Join(root, "repo"), 0700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(base, "secret")
	for file, content := range map[string]string{secret: "secret", filepath.Join(root, "repo", "a.txt"): "one\ntwo\nthree\n"} {
		if err = os.WriteFile(file, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.Symlink(secret, filepath.Join(root, "repo", "link")); err != nil {
		t.Fatal(err)
	}
	line, limit := 2, 1
	got, err := readWorkspaceFile(root, acp.ReadTextFileRequest{Path: filepath.Join(root, "repo", "a.txt"), Line: &line, Limit: &limit})
	if err != nil || got.Content != "two\n" {
		t.Fatalf("%q %v", got.Content, err)
	}
	line, limit = 2, math.MaxInt
	got, err = readWorkspaceFile(root, acp.ReadTextFileRequest{Path: filepath.Join(root, "repo", "a.txt"), Line: &line, Limit: &limit})
	if err != nil || got.Content != "two\nthree\n" {
		t.Fatalf("huge limit: %q %v", got.Content, err)
	}
	line, limit = math.MaxInt, 1
	got, err = readWorkspaceFile(root, acp.ReadTextFileRequest{Path: filepath.Join(root, "repo", "a.txt"), Line: &line, Limit: &limit})
	if err != nil || got.Content != "" {
		t.Fatalf("huge line: %q %v", got.Content, err)
	}
	for _, p := range []string{secret, filepath.Join(root, "repo", "link"), filepath.Join(root, "..", "secret"), "repo/a.txt", filepath.Join(root, "repo")} {
		if got, err := readWorkspaceFile(root, acp.ReadTextFileRequest{Path: p}); err == nil || strings.Contains(got.Content, "secret") {
			t.Errorf("%s: %q %v", p, got.Content, err)
		}
	}
}

func TestGrokUnreadableSnapshotFileFailsTheReview(t *testing.T) {
	root := t.TempDir()
	large := filepath.Join(root, "large.txt")
	if err := os.WriteFile(large, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(large, grokMaxRead+1); err != nil {
		t.Fatal(err)
	}
	c := &reviewClient{readRoot: root}
	if _, err := c.ReadTextFile(context.Background(), acp.ReadTextFileRequest{Path: filepath.Join(root, "missing.txt")}); err == nil || errors.Is(err, errSnapshotUnreadable) || c.permissionError != nil {
		t.Fatalf("a wrong path should stay a tool error: %v, %v", err, c.permissionError)
	}
	if _, err := c.ReadTextFile(context.Background(), acp.ReadTextFileRequest{Path: filepath.Join(root, "large.txt", "child")}); err == nil || c.permissionError != nil {
		t.Fatalf("a path below a file should stay a tool error: %v, %v", err, c.permissionError)
	}
	if _, err := c.ReadTextFile(context.Background(), acp.ReadTextFileRequest{Path: large}); !errors.Is(err, errSnapshotUnreadable) || !errors.Is(c.permissionError, errSnapshotUnreadable) {
		t.Fatalf("got %v, recorded %v", err, c.permissionError)
	}
}

func TestGrokNeedsAnAPIKey(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Grok reviewers do not run on Windows")
	}
	t.Setenv("XAI_API_KEY", "")
	root, c, store := reviewRepo(t)
	trace := filepath.Join(t.TempDir(), "trace")
	v := Run(context.Background(), store, c, Options{Root: root, Settings: grokSettings("clean", trace)})
	if v.Status != StatusFailed || !strings.Contains(v.Reviewers[0].Error, "XAI_API_KEY") || v.Reviewers[0].FirstError != "" {
		t.Fatalf("%+v", v)
	}
	if _, err := os.Stat(trace); !os.IsNotExist(err) {
		t.Fatal("the adapter started without a key")
	}
}

func TestGrokRefusesACommandThatWidensIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Grok reviewers do not run on Windows")
	}
	t.Setenv("XAI_API_KEY", "test-value")
	for _, arg := range []string{"--always-approve", "--yolo", "--sandbox=off", "--permission-mode=bypassPermissions", "--trust", "--plugin-dir=/x", "--xai-api-base-url=https://evil.example"} {
		t.Run(arg, func(t *testing.T) {
			root, c, store := reviewRepo(t)
			trace := filepath.Join(t.TempDir(), "trace")
			s := grokSettings("clean", trace)
			s.Adapters[grokAdapter] = append(s.Adapters[grokAdapter], arg)
			v := Run(context.Background(), store, c, Options{Root: root, Settings: s})
			if v.Status != StatusFailed || !strings.Contains(v.Reviewers[0].Error, "cannot be restricted") {
				t.Fatalf("%+v", v)
			}
			if _, err := os.Stat(trace); !os.IsNotExist(err) {
				t.Fatal("the adapter started")
			}
		})
	}
}

func TestGrokDoesNotApproveADiffItCannotSee(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Grok reviewers do not run on Windows")
	}
	t.Setenv("XAI_API_KEY", "test-value")
	root, c, store := reviewRepo(t)
	if err := os.WriteFile(filepath.Join(root, "large.py"), []byte(strings.Repeat("value = 1\n", promptDiffLimit/5)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.Run(root, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	tree, err := gitx.Run(root, "write-tree")
	if err != nil {
		t.Fatal(err)
	}
	if c, err = NewCapture(root, c.BaseCommit, tree); err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(t.TempDir(), "trace")
	v := Run(context.Background(), store, c, Options{Root: root, Settings: grokSettings("clean", trace)})
	if v.Status != StatusFailed || !strings.Contains(v.Reviewers[0].Error, "over the 256 KiB") || v.Reviewers[0].FirstError != "" {
		t.Fatalf("%+v", v)
	}
	if _, err := os.Stat(trace); !os.IsNotExist(err) {
		t.Fatal("the adapter started on a diff it cannot see")
	}
}

func TestGrokRefusesATempDirInsideHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Grok reviewers do not run on Windows")
	}
	home := t.TempDir()
	tmp := filepath.Join(home, "tmp", "slopcop")
	if _, err := writeGrokHome(filepath.Join(tmp, "home"), tmp, home); err == nil || !strings.Contains(err.Error(), "TMPDIR") {
		t.Fatalf("err = %v", err)
	}
}
