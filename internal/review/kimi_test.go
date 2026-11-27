package review

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/gitx"
)

const kimiTestKey = "test-key-not-a-secret"

func kimiSettings(mode, trace string) Settings {
	return Settings{Reviewers: []Family{Kimi}, Adapters: map[string][]string{kimiAdapter: fakeAdapter(mode, kimiModel, trace)}}
}

func TestKimiIsOptIn(t *testing.T) {
	if slices.Contains(Families, Kimi) {
		t.Fatal("slopcop init would pick kimi by itself")
	}
	if f, err := ParseFamily("kimi"); err != nil || f != Kimi {
		t.Fatalf("ParseFamily: %q %v", f, err)
	}
	// Copilot must not take over a family whose restrictions it does not apply.
	if got := AdapterFor(Kimi, Settings{Provider: ProviderCopilot}); got != kimiAdapter {
		t.Fatalf("adapter %q", got)
	}
}

func TestFamilyOfKimi(t *testing.T) {
	for model, want := range map[string]bool{
		"kimi-k3":                   true,
		"slopcop/kimi-k3":           true,
		"kimi-k4-preview":           true,
		"kimi-for-coding":           false,
		"kimi-latest":               false,
		"moonshot-v1-128k":          false,
		"kimi-code/kimi-for-coding": false,
	} {
		f, ok := FamilyOf(model)
		if got := ok && f == Kimi; got != want {
			t.Errorf("%s: family %q, ok %v", model, f, ok)
		}
	}
}

func TestKimiConfigAllowsOnlyReadInsideTheWorkspace(t *testing.T) {
	config := kimiConfig("https://api.example.test/v1", "/tmp/slopcop-kimi-1/workspace")
	for _, want := range []string{
		"[tools]\nenabled = [\"Read\"]\n",
		`pattern = "Read(!/tmp/slopcop-kimi-1/workspace/*(@(*|.*)/)@(*|.*))"`,
		"telemetry = false\n",
		"require_trust = true\n",
		"dangerous_command_guard = true\n",
		"api_key_env = \"KIMI_API_KEY\"\n",
		"base_url = \"https://api.example.test/v1\"\n",
		"default_model = \"kimi-k3\"\n",
	} {
		if !strings.Contains(config, want) {
			t.Errorf("config lacks %q:\n%s", want, config)
		}
	}
	for _, tool := range kimiDeniedTools {
		if !strings.Contains(config, fmt.Sprintf("decision = \"deny\"\npattern = %q\n", tool)) {
			t.Errorf("config does not deny %s", tool)
		}
	}
	if strings.Contains(config, "api_key =") || strings.Contains(config, "[[hooks]]") {
		t.Fatalf("config holds a key or a hook:\n%s", config)
	}
}

func TestKimiReadFenceEscapesTheWorkspacePath(t *testing.T) {
	got := kimiReadFence(`/tmp/a(b)[c]{d}!+@|*?e/workspace`)
	want := `Read(!/tmp/a\(b\)\[c\]\{d\}\!\+\@\|\*\?e/workspace` + kimiPathTail + ")"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestTomlStringEscapes(t *testing.T) {
	if got, want := tomlString("a\"b\\c\nd"), `"a\"b\\c\nd"`; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestKimiBaseURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"":                             true,
		"https://api.moonshot.cn/v1":   true,
		"http://127.0.0.1:8080/v1":     true,
		"http://[::1]:8080/v1":         true,
		"http://localhost:8080/v1":     true,
		"http://api.example.test/v1":   false,
		"http://127.0.0.1.evil.test/":  false,
		"https://user:pw@example.test": false,
		"file:///etc/passwd":           false,
		"not a url":                    false,
	} {
		if _, err := kimiBaseURL(raw); (err == nil) != ok {
			t.Errorf("%q: err = %v, want ok %v", raw, err, ok)
		}
	}
}

func TestKimiEnvironment(t *testing.T) {
	got := kimiEnvironment([]string{
		"HOME=/home/me",
		"PATH=/bin",
		"KIMI_API_KEY=k",
		"KIMI_BASE_URL=https://elsewhere.test",
		"KIMI_CODE_TRUST_WORKSPACE=1",
		"KIMI_MODEL_NAME=other",
		"KIMI_WEB_FETCH_BASE_URL=https://elsewhere.test",
		"KIMI_CODE_HOME=/home/me/.kimi-code",
		"NPM_CONFIG_IGNORE_SCRIPTS=false",
		"UserProfile=/home/me",
		"kimi_code_trust_workspace=1",
	}, "/t/home", "/t/kimi", "/home/me")
	for _, want := range []string{"PATH=/bin", "KIMI_API_KEY=k", "HOME=/t/home", "USERPROFILE=/t/home", "KIMI_CODE_HOME=/t/kimi", "KIMI_DISABLE_TELEMETRY=1", "KIMI_CODE_NO_AUTO_UPDATE=1", "npm_config_ignore_scripts=true", "NODE_DISABLE_COMPILE_CACHE=1", "npm_config_cache=" + filepath.Join("/home/me", ".npm"), "npm_config_userconfig=" + filepath.Join("/home/me", ".npmrc")} {
		if !slices.Contains(got, want) {
			t.Errorf("environment lacks %q: %v", want, got)
		}
	}
	for _, v := range got {
		if v == "HOME=/home/me" || v == "UserProfile=/home/me" || v == "kimi_code_trust_workspace=1" || v == "KIMI_CODE_HOME=/home/me/.kimi-code" || strings.HasPrefix(v, "KIMI_CODE_TRUST_WORKSPACE") || strings.HasPrefix(v, "KIMI_MODEL_") || strings.HasPrefix(v, "KIMI_WEB_") || strings.HasPrefix(v, "KIMI_BASE_URL") || v == "NPM_CONFIG_IGNORE_SCRIPTS=false" {
			t.Errorf("environment keeps %q", v)
		}
	}
	kept := kimiEnvironment([]string{"NPM_CONFIG_CACHE=/cache", "npm_config_userconfig=/rc"}, "/t/home", "/t/kimi", "/home/me")
	if slices.Contains(kept, "npm_config_cache="+filepath.Join("/home/me", ".npm")) || slices.Contains(kept, "npm_config_userconfig="+filepath.Join("/home/me", ".npmrc")) {
		t.Fatalf("overrode the user's npm settings: %v", kept)
	}
}

func kimiLine(update, id, kind, title, status string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":%q,"toolCallId":%q,"kind":%q,"title":%q,"status":%q}}}`+"\n", update, id, kind, title, status)
}

func TestKimiToolGuard(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines []string
		want  string
	}{
		{"read", []string{kimiLine("tool_call", "1", "read", "Reading repo/a.go", "in_progress"), kimiLine("tool_call_update", "1", "", "", "completed")}, ""},
		{"refused read", []string{kimiLine("tool_call", "1", "read", "Reading /etc/passwd", "in_progress"), kimiLine("tool_call_update", "1", "", "", "failed")}, ""},
		{"name streamed late", []string{kimiLine("tool_call", "1", "other", "tool", "pending"), kimiLine("tool_call_update", "1", "read", "Reading repo/a.go", "in_progress"), kimiLine("tool_call_update", "1", "", "", "completed")}, ""},
		{"status text", []string{kimiLine("tool_call", "1", "read", "Reading repo/a.go", "in_progress"), kimiLine("tool_call_update", "1", "", "reading", "")}, ""},
		{"name streamed late is a fetch", []string{kimiLine("tool_call", "1", "other", "tool", "pending"), kimiLine("tool_call_update", "1", "other", "FetchURL", "in_progress")}, "FetchURL"},
		{"never named", []string{kimiLine("tool_call", "1", "other", "tool", "pending"), kimiLine("tool_call_update", "1", "", "", "completed")}, "never named"},
		{"web search", []string{kimiLine("tool_call", "1", "fetch", "WebSearch", "in_progress")}, "WebSearch"},
		{"fetch", []string{kimiLine("tool_call", "1", "other", "Fetching https://example.test", "in_progress")}, "Fetching"},
		{"subagent", []string{kimiLine("tool_call", "1", "other", "Agent", "in_progress")}, "Agent"},
		{"command", []string{kimiLine("tool_call", "1", "execute", "Bash", "pending")}, "Bash"},
		{"edit", []string{kimiLine("tool_call", "1", "edit", "Write", "in_progress")}, "Write"},
		{"no kind", []string{kimiLine("tool_call", "1", "", "mystery", "in_progress")}, "mystery"},
		{"not a tool update", []string{`{"jsonrpc":"2.0","id":1,"result":{"kind":"execute"}}` + "\n", "not json\n"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got error
			check := newKimiToolCheck("/w", nil)
			g := &toolGuard{check: mayHaveRun(check.check), trip: func(err error) { got = err }}
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
		})
	}
}

func TestKimiPromptNamesTheExportedRepository(t *testing.T) {
	p := reviewPrompt("/home/someone/private-repo", Capture{BaseTree: "base", Tree: "snap"}, intent{}, "", "diff", AdapterFor(Kimi, Settings{}))
	if strings.Contains(p, "/home/someone/private-repo") || !strings.Contains(p, "Repository root: repo\n") || !strings.Contains(p, "Read tool") || strings.Contains(p, "git -C") {
		t.Fatal(p)
	}
}

func TestKimiReviewerRunsIsolated(t *testing.T) {
	t.Setenv(kimiKeyEnv, kimiTestKey)
	t.Setenv("KIMI_CODE_TRUST_WORKSPACE", "1")
	t.Setenv("KIMI_MODEL_NAME", "attacker-model")
	root, c, store := reviewRepo(t)
	if err := os.MkdirAll(filepath.Join(root, ".kimi-code"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".kimi-code", "mcp.json"), []byte(`{"mcpServers":{"x":{"command":"false"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	c = hostileCapture(t, root, c)
	trace := filepath.Join(t.TempDir(), "trace")
	v := Run(context.Background(), store, c, Options{Root: root, Settings: kimiSettings("kimi-env", trace)})
	if v.Status != StatusClean || v.Reviewers[0].Adapter != kimiAdapter || v.Reviewers[0].Model != kimiModel || v.Reviewers[0].Effort != "high" {
		t.Fatalf("%+v", v)
	}
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if strings.Contains(got, kimiTestKey) {
		t.Fatal("the key reached the args or the config")
	}
	for _, want := range []string{
		"key-set=true",
		"KIMI_DISABLE_TELEMETRY=1",
		"KIMI_CODE_NO_AUTO_UPDATE=1",
		"KIMI_CODE_TRUST_WORKSPACE=\n",
		"KIMI_MODEL_NAME=\n",
		"exists repo/change.txt=true",
		"exists repo/.kimi-code/mcp.json=true",
		"exists repo/link=false",
		"exists .kimi-code=false",
		"exists .agents=false",
		"enabled = [\"Read\"]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("trace lacks %q:\n%s", want, got)
		}
	}
	value := func(key string) string {
		for _, line := range strings.Split(got, "\n") {
			if v, ok := strings.CutPrefix(line, key+"="); ok {
				return v
			}
		}
		return ""
	}
	workspace, home, data2 := value("session-cwd"), value("HOME"), value("KIMI_CODE_HOME")
	if value("USERPROFILE") != home {
		t.Fatalf("USERPROFILE %q, home %q", value("USERPROFILE"), home)
	}
	tmp := filepath.Dir(workspace)
	if workspace == "" || filepath.Dir(home) != tmp || filepath.Dir(data2) != tmp {
		t.Fatalf("workspace %q, home %q, data %q", workspace, home, data2)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	adapters, err := filepath.EvalSymlinks(filepath.Join(cache, "slopcop", "adapters"))
	if err != nil {
		t.Fatal(err)
	}
	if process := value("process-cwd"); filepath.Dir(process) != adapters {
		t.Fatalf("npx would start in %q, not under %s", process, adapters)
	}
	if !strings.Contains(got, tomlString(kimiReadFence(workspace))) {
		t.Fatalf("config does not fence reads to %s:\n%s", workspace, got)
	}
	if insideGitRepository(workspace) {
		t.Fatalf("workspace %s is inside a repository", workspace)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("temporary directory left behind: %v", err)
	}
}

func TestKimiReadDoesNotStopTheReviewer(t *testing.T) {
	t.Setenv(kimiKeyEnv, kimiTestKey)
	root, c, store := reviewRepo(t)
	v := Run(context.Background(), store, c, Options{Root: root, Settings: kimiSettings("kimi-read", filepath.Join(t.TempDir(), "trace"))})
	if v.Status != StatusClean {
		t.Fatalf("%+v", v)
	}
}

func kimiReadLine(update, id, path, status string) string {
	input := ""
	if path != "" {
		input = fmt.Sprintf(`,"rawInput":{"path":%q}`, path)
	}
	kind := ""
	if update == "tool_call" || path != "" {
		kind = `,"kind":"read"`
	}
	return fmt.Sprintf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":%q,"toolCallId":%q,"title":"Reading"%s,"status":%q%s}}}`, update, id, kind, status, input)
}

func TestKimiTracksReadsOfChangedFiles(t *testing.T) {
	changed := map[string]bool{"repo/a.go": true, "repo/b.go": true}
	workspace := t.TempDir()
	for _, tc := range []struct {
		name  string
		lines []string
		want  string
	}{
		{"read", []string{kimiReadLine("tool_call", "1", "repo/a.go", "in_progress"), kimiReadLine("tool_call_update", "1", "", "completed")}, ""},
		{"failed", []string{kimiReadLine("tool_call", "1", "repo/a.go", "in_progress"), kimiReadLine("tool_call_update", "1", "", "failed")}, "a.go"},
		{"absolute path", []string{kimiReadLine("tool_call", "1", filepath.ToSlash(workspace)+"/repo/./a.go", "in_progress"), kimiReadLine("tool_call_update", "1", "", "failed")}, "a.go"},
		{"failed then read", []string{kimiReadLine("tool_call", "1", "repo/a.go", "in_progress"), kimiReadLine("tool_call_update", "1", "", "failed"), kimiReadLine("tool_call", "2", "repo/a.go", "in_progress"), kimiReadLine("tool_call_update", "2", "", "completed")}, ""},
		{"path named late", []string{`{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"tool_call","toolCallId":"1","kind":"other","title":"tool","status":"pending"}}}`, kimiReadLine("tool_call_update", "1", "repo/b.go", "in_progress"), kimiReadLine("tool_call_update", "1", "", "failed")}, "b.go"},
		{"path in an update without a kind", []string{kimiReadLine("tool_call", "1", "", "in_progress"), `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"tool_call_update","toolCallId":"1","status":"in_progress","rawInput":{"path":"repo/a.go"}}}}`, kimiReadLine("tool_call_update", "1", "", "failed")}, "a.go"},
		{"path corrected to a file outside the change", []string{kimiReadLine("tool_call", "1", "repo/a.go", "in_progress"), `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"tool_call_update","toolCallId":"1","rawInput":{"path":"repo/guess.go"}}}}`, kimiReadLine("tool_call_update", "1", "", "failed")}, ""},
		{"file outside the change", []string{kimiReadLine("tool_call", "1", "repo/guess.go", "in_progress"), kimiReadLine("tool_call_update", "1", "", "failed")}, ""},
		{"outside the workspace", []string{kimiReadLine("tool_call", "1", "/etc/passwd", "in_progress"), kimiReadLine("tool_call_update", "1", "", "failed")}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newKimiToolCheck(workspace, changed)
			for _, line := range tc.lines {
				if what := c.check([]byte(line)); what != "" {
					t.Fatalf("stopped: %s", what)
				}
			}
			err := c.unreadChange()
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestKimiGuardStillStopsAToolWithUnexpectedInput(t *testing.T) {
	c := newKimiToolCheck("/w", nil)
	line := `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"tool_call","toolCallId":"1","kind":"execute","title":"Bash","status":"in_progress","rawInput":"not an object"}}}`
	if what := c.check([]byte(line)); !strings.Contains(what, "Bash") {
		t.Fatalf("got %q", what)
	}
}

func TestKimiBinaryFile(t *testing.T) {
	dir := t.TempDir()
	for name, tc := range map[string]struct {
		data   []byte
		binary bool
	}{
		"empty":     {nil, false},
		"text":      {[]byte("package a\n"), false},
		"binary":    {[]byte("PNG\x00data"), true},
		"late null": {append(make([]byte, 0, 8002), append([]byte(strings.Repeat("a", 8000)), 0)...), false},
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, tc.data, 0600); err != nil {
			t.Fatal(err)
		}
		if got, err := kimiBinaryFile(path); err != nil || got != tc.binary {
			t.Errorf("%s: %v, %v", name, got, err)
		}
	}
}

func TestKimiDoesNotApproveAChangedFileItFailedToRead(t *testing.T) {
	t.Setenv(kimiKeyEnv, kimiTestKey)
	root, c, store := reviewRepo(t)
	trace := filepath.Join(t.TempDir(), "trace")
	v := Run(context.Background(), store, c, Options{Root: root, Settings: kimiSettings("kimi-read-fails", trace)})
	if v.Status != StatusFailed || !strings.Contains(v.Reviewers[0].Error, "could not read changed file") || !strings.Contains(v.Reviewers[0].Error, "change.txt") {
		t.Fatalf("%+v", v)
	}
	data, _ := os.ReadFile(trace)
	if n := strings.Count(string(data), "prompt\n"); n != 2 {
		t.Fatalf("%d prompts, want one retry:\n%s", n, data)
	}
}

func TestKimiOverrideCannotApproveAChangedFileItFailedToRead(t *testing.T) {
	t.Setenv(kimiKeyEnv, kimiTestKey)
	root, c, store := reviewRepo(t)
	v := Run(context.Background(), store, c, Options{Root: root, Settings: kimiSettings("kimi-read-fails-findings", filepath.Join(t.TempDir(), "trace"))})
	if v.Status != StatusFindings || len(v.Findings()) != 1 {
		t.Fatalf("the reviewer's finding must stand: %+v", v)
	}
	if !strings.Contains(v.Reviewers[0].UnreadChange, "change.txt") || !strings.Contains(v.Reviewers[0].Warning, "could not read changed file") {
		t.Fatalf("%+v", v.Reviewers[0])
	}
	if v.Overridable() {
		t.Fatal("a whole-review override could approve a change the reviewer did not read")
	}
	f := v.Findings()[0]
	v.applyOverrides([]FindingOverride{{ID: f.ID, Finding: f}}, nil)
	if v.Status != StatusFailed || !strings.Contains(v.Error, "could not read changed file") {
		t.Fatalf("overriding the finding approved an unread change: %+v", v)
	}
}

func TestKimiFailedReadOutsideTheChangeDoesNotFailTheReview(t *testing.T) {
	t.Setenv(kimiKeyEnv, kimiTestKey)
	root, c, store := reviewRepo(t)
	v := Run(context.Background(), store, c, Options{Root: root, Settings: kimiSettings("kimi-read-missing", filepath.Join(t.TempDir(), "trace"))})
	if v.Status != StatusClean {
		t.Fatalf("%+v", v)
	}
}

func TestKimiToolOutsideTheAllowlistStopsTheReviewer(t *testing.T) {
	t.Setenv(kimiKeyEnv, kimiTestKey)
	root, c, store := reviewRepo(t)
	trace := filepath.Join(t.TempDir(), "trace")
	v := Run(context.Background(), store, c, Options{Root: root, Settings: kimiSettings("kimi-fetch", trace)})
	if v.Status != StatusFailed || len(v.Reviewers) != 1 {
		t.Fatalf("%+v", v)
	}
	r := v.Reviewers[0]
	if !strings.Contains(r.Error, errForbiddenTool.Error()) || !strings.Contains(r.Error, "may have left this machine") || r.FirstError != "" {
		t.Fatalf("%+v", r)
	}
	data, _ := os.ReadFile(trace)
	if n := strings.Count(string(data), "prompt\n"); n != 1 {
		t.Fatalf("%d prompts, want no retry:\n%s", n, data)
	}
}

func TestKimiRefusesEveryPermissionRequest(t *testing.T) {
	t.Setenv(kimiKeyEnv, kimiTestKey)
	root, c, store := reviewRepo(t)
	trace := filepath.Join(t.TempDir(), "trace")
	v := Run(context.Background(), store, c, Options{Root: root, Settings: kimiSettings("outside-read", trace)})
	if v.Status != StatusFailed || !strings.Contains(v.Reviewers[0].Error, "/outside/secret") {
		t.Fatalf("%+v", v)
	}
	data, _ := os.ReadFile(trace)
	if strings.Contains(string(data), "permission=allow") {
		t.Fatalf("a read was allowed:\n%s", data)
	}
}

func TestKimiWithoutAKeyFailsBeforeStarting(t *testing.T) {
	t.Setenv(kimiKeyEnv, "")
	root, c, store := reviewRepo(t)
	trace := filepath.Join(t.TempDir(), "trace")
	v := Run(context.Background(), store, c, Options{Root: root, Settings: kimiSettings("clean", trace)})
	if v.Status != StatusFailed || !strings.Contains(v.Reviewers[0].Error, "KIMI_API_KEY is not set") || v.Reviewers[0].FirstError != "" {
		t.Fatalf("%+v", v)
	}
	if _, err := os.Stat(trace); !os.IsNotExist(err) {
		t.Fatal("the adapter started without a key")
	}
}

func TestKimiMissingKeyIsAMissingPrerequisite(t *testing.T) {
	t.Setenv(kimiKeyEnv, "")
	if got := MissingPrerequisite(Kimi, t.TempDir(), Settings{}); !strings.Contains(got, kimiKeyEnv) {
		t.Fatalf("got %q", got)
	}
	t.Setenv(kimiKeyEnv, kimiTestKey)
	if got := MissingPrerequisite(Kimi, t.TempDir(), Settings{}); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestKimiRefusesACommandThatWidensItsAccess(t *testing.T) {
	t.Setenv(kimiKeyEnv, kimiTestKey)
	for _, flag := range []string{"--yolo", "--auto", "--auto-approve", "--add-dir=/", "--skills-dir=/x", "--agent-file=/x"} {
		root, c, store := reviewRepo(t)
		trace := filepath.Join(t.TempDir(), "trace")
		s := kimiSettings("clean", trace)
		s.Adapters[kimiAdapter] = append(s.Adapters[kimiAdapter], flag)
		v := Run(context.Background(), store, c, Options{Root: root, Settings: s})
		if v.Status != StatusFailed || !strings.Contains(v.Reviewers[0].Error, "cannot be restricted") {
			t.Fatalf("%s: %+v", flag, v)
		}
		if _, err := os.Stat(trace); !os.IsNotExist(err) {
			t.Fatalf("%s: the adapter started", flag)
		}
	}
}

func TestKimiDoesNotApproveADiffItCannotSee(t *testing.T) {
	t.Setenv(kimiKeyEnv, kimiTestKey)
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
	v := Run(context.Background(), store, c, Options{Root: root, Settings: kimiSettings("clean", trace)})
	if v.Status != StatusClean || len(v.Reviewers) != 1 {
		t.Fatalf("on-demand diff review: %+v", v)
	}
	if _, err := os.Stat(trace); err != nil {
		t.Fatal("adapter did not start", err)
	}
}

func TestKimiDoesNotApproveAChangeToAFileItCannotRead(t *testing.T) {
	t.Setenv(kimiKeyEnv, kimiTestKey)
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
	v := Run(context.Background(), store, c, Options{Root: root, Settings: kimiSettings("clean", trace)})
	if v.Status != StatusFailed || !strings.Contains(v.Reviewers[0].Error, "large.py") || !strings.Contains(v.Reviewers[0].Error, "over 1 MiB") {
		t.Fatalf("%+v", v)
	}
	if _, err := os.Stat(trace); !os.IsNotExist(err) {
		t.Fatal("the Kimi adapter started on a change it cannot read")
	}
}
