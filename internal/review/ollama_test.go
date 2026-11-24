package review

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	acp "github.com/coder/acp-go-sdk"

	"github.com/BrokkAi/slopcop/internal/gitx"
)

const ollamaTestModel = "qwen-test:7b"

// ollamaStub is the part of the Ollama API a reviewer's setup uses.
type ollamaStub struct {
	version      string
	missing      bool
	capabilities []string
	remoteHost   string
	context      int
	onLoad       func()

	mu    sync.Mutex
	paths []string
}

func (o *ollamaStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	o.paths = append(o.paths, r.URL.Path)
	o.mu.Unlock()
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	switch r.URL.Path {
	case "/api/version":
		reply(map[string]string{"version": o.version})
	case "/api/show":
		if o.missing {
			w.WriteHeader(http.StatusNotFound)
			reply(map[string]string{"error": "model not found"})
			return
		}
		reply(map[string]any{"capabilities": o.capabilities, "remote_host": o.remoteHost})
	case "/api/generate":
		if o.onLoad != nil {
			o.onLoad()
		}
		reply(map[string]any{"done": true, "done_reason": "load"})
	case "/api/ps":
		reply(map[string]any{"models": []map[string]any{{"name": ollamaTestModel, "model": ollamaTestModel, "context_length": o.context}}})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (o *ollamaStub) called(path string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Contains(o.paths, path)
}

func startOllamaStub(t *testing.T, change func(*ollamaStub)) *ollamaStub {
	t.Helper()
	stub := &ollamaStub{version: ollamaMinVersion, capabilities: []string{"completion", "tools"}, context: 64000}
	if change != nil {
		change(stub)
	}
	server := httptest.NewServer(stub)
	t.Cleanup(server.Close)
	t.Setenv("OLLAMA_HOST", server.URL)
	return stub
}

// fakeOpencode runs the fake agent through env, since a reviewer's environment
// does not carry the variable that turns the test binary into the agent.
func fakeOpencode(t *testing.T, mode, trace string) map[string][]string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses env to start the fake agent")
	}
	return map[string][]string{ollamaAdapter: append([]string{"env", "SLOPCOP_FAKE_ACP=1"}, fakeAdapter(mode, ollamaTestModel, trace)...)}
}

func ollamaSettings(t *testing.T, mode, trace string) Settings {
	return Settings{Reviewers: []Family{Ollama}, Models: map[Family]string{Ollama: ollamaTestModel}, Adapters: fakeOpencode(t, mode, trace)}
}

func traceValue(t *testing.T, trace, key string) string {
	t.Helper()
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			return v
		}
	}
	t.Fatalf("no %s in trace:\n%s", key, data)
	return ""
}

func TestOllamaIsOptIn(t *testing.T) {
	if slices.Contains(Families, Ollama) {
		t.Fatal("slopcop init would pick ollama by itself")
	}
	if f, err := ParseFamily("ollama"); err != nil || f != Ollama {
		t.Fatalf("ParseFamily: %q %v", f, err)
	}
	if got := AdapterFor(Ollama, Settings{Provider: ProviderCopilot}); got != ollamaAdapter {
		t.Fatalf("adapter %q", got)
	}
	if got := MissingPrerequisite(Ollama, t.TempDir(), Settings{}); !strings.Contains(got, "review.models.ollama") {
		t.Fatalf("prerequisite %q", got)
	}
}

func TestValidOllamaModel(t *testing.T) {
	for name, want := range map[string]bool{
		"llama3":                      true,
		"qwen3-coder:30b":             true,
		"hf.co/org/model-GGUF:Q4_K_M": true,
		"":                            false,
		"-flag":                       false,
		"a b":                         false,
		"../escape":                   false,
		"model:tag:other":             false,
		strings.Repeat("a", 257):      false,
		"model\n":                     false,
	} {
		if got := ValidOllamaModel(name); got != want {
			t.Errorf("%q: %v", name, got)
		}
	}
}

func TestSameOllamaModel(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"qwen3:8b", "qwen3:8b", true},
		{"qwen3:latest", "qwen3", true},
		{"qwen3:8b", "library/qwen3:8b", true},
		{"qwen3:8b", "registry.ollama.ai/library/qwen3:8b", true},
		{"qwen3:latest", "registry.ollama.ai/library/qwen3", true},
		{"qwen3:8b", "Qwen3:8B", true},
		{"hf.co/org/model:q4", "hf.co/org/model:q4", true},
		{"qwen3:8b", "qwen3:14b", false},
		{"qwen3:8b", "someone/qwen3:8b", false},
		{"hf.co/org/model:q4", "model:q4", false},
		{"", "qwen3", false},
	} {
		if got := sameOllamaModel(tc.a, tc.b); got != tc.want {
			t.Errorf("%q, %q: %v", tc.a, tc.b, got)
		}
	}
}

func TestOllamaServerFrom(t *testing.T) {
	for _, tc := range []struct {
		value, base string
		local       bool
		fails       bool
	}{
		{"", "http://127.0.0.1:11434", true, false},
		{":11500", "http://127.0.0.1:11500", true, false},
		{"0.0.0.0", "http://127.0.0.1:11434", true, false},
		{"[::]:11434", "http://[::1]:11434", true, false},
		{"localhost:8080", "http://localhost:8080", true, false},
		{"http://127.0.0.1", "http://127.0.0.1:80", true, false},
		{"gpu-box:11434", "http://gpu-box:11434", false, false},
		{"https://models.example.com/ollama/", "https://models.example.com:443/ollama", false, false},
		{"10.0.0.8", "http://10.0.0.8:11434", false, false},
		{"https://user:secret@example.com", "", false, true},
		{"ftp://example.com", "", false, true},
		{"example.com:notaport", "", false, true},
		{"ollama.com", "", false, true},
		{"https://api.ollama.com", "", false, true},
	} {
		got, err := ollamaServerFrom(tc.value)
		if tc.fails {
			if !errors.Is(err, errOllamaSetup) {
				t.Errorf("%q: err = %v", tc.value, err)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Errorf("%q: error shows the credential: %v", tc.value, err)
			}
			continue
		}
		if err != nil || got.base.String() != tc.base || got.local != tc.local {
			t.Errorf("%q: %v local=%v err=%v", tc.value, got.base, got.local, err)
		}
		if (got.warning() == "") != tc.local {
			t.Errorf("%q: warning %q", tc.value, got.warning())
		}
	}
}

func TestOllamaLimitsFinish(t *testing.T) {
	limits := newOllamaLimits(64000)
	if limits.output != ollamaMaxOutput {
		t.Fatalf("output %d", limits.output)
	}
	cached := func(n int) *int { return &n }
	for _, tc := range []struct {
		name  string
		usage *acp.Usage
		ok    bool
	}{
		{"fits", &acp.Usage{InputTokens: 1000, OutputTokens: 10}, true},
		{"exactly room for a full reply", &acp.Usage{InputTokens: 64000 - ollamaMaxOutput, OutputTokens: 10}, true},
		{"too full for a full reply", &acp.Usage{InputTokens: 64000 - ollamaMaxOutput + 1, OutputTokens: 10}, false},
		{"cached prompt counts", &acp.Usage{InputTokens: 100, CachedReadTokens: cached(60000), OutputTokens: 10}, false},
		{"reply cut off", &acp.Usage{InputTokens: 1000, OutputTokens: ollamaMaxOutput}, false},
		{"reasoning used up the cap", &acp.Usage{InputTokens: 1000, OutputTokens: 10, ThoughtTokens: cached(ollamaMaxOutput - 10)}, false},
		{"reasoning within the cap", &acp.Usage{InputTokens: 1000, OutputTokens: 10, ThoughtTokens: cached(500)}, true},
		{"no prompt tokens", &acp.Usage{OutputTokens: 10}, false},
		{"no usage", nil, false},
	} {
		err := limits.finish(nil, tc.usage)
		if tc.ok && err != nil || !tc.ok && !errors.Is(err, errOllamaCoverage) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	if err := limits.finish(errors.New("the prompt is longer than the context length"), nil); !errors.Is(err, errOllamaCoverage) {
		t.Fatalf("context error: %v", err)
	}
	other := errors.New("stream broke")
	if err := limits.finish(other, nil); !errors.Is(err, other) {
		t.Fatalf("other error: %v", err)
	}
}

func TestVerdictFormatError(t *testing.T) {
	for text, ok := range map[string]bool{
		"No findings.":                   true,
		"  No findings.\n":               true,
		"- [P2] a real problem":          true,
		"":                               true, // parseVerdict already fails it
		"No findings":                    false,
		"No findings. Looks good":        false,
		"LGTM":                           false,
		"I found no [P1] issues":         false,
		"[P1] not a list item":           false,
		"Review:\n- [P1] a real problem": true,
		"1. **[P0]** a numbered finding": true,
		"<think>x</think>No findings.":   false,
	} {
		if got := verdictFormatError(text) == nil; got != ok {
			t.Errorf("%q: %v", text, got)
		}
	}
}

func TestOllamaPromptNamesTheExportedRepository(t *testing.T) {
	p := reviewPrompt("/home/someone/private-repo", Capture{BaseTree: "base", Tree: "snap"}, intent{}, "", "diff", Ollama)
	if strings.Contains(p, "/home/someone/private-repo") || !strings.Contains(p, "Repository root: repo\n") || !strings.Contains(p, "read, glob and grep") || strings.Contains(p, "git -C") {
		t.Fatal(p)
	}
}

// TestOllamaReviewerIsContained checks what the reviewer is started with: a
// hostile tree only below the workspace, and a private home and environment.
func TestOllamaReviewerIsContained(t *testing.T) {
	root, c, store := reviewRepo(t)
	if err := os.MkdirAll(filepath.Join(root, ".opencode", "tools"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		".opencode/tools/evil.js": "evil",
		"opencode.json":           `{"permission":{"*":"allow"}}`,
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	c = hostileCapture(t, root, c)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("SLOPCOP_TEST_SECRET", "must-not-reach-the-reviewer")
	t.Setenv("OPENCODE_PERMISSION", `{"*":"allow"}`)
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:3128")
	t.Setenv("NO_PROXY", "corp.example")
	startOllamaStub(t, nil)
	trace := filepath.Join(t.TempDir(), "trace")
	v := Run(context.Background(), store, c, Options{Root: root, Settings: ollamaSettings(t, "opencode-read", trace)})
	if v.Status != StatusClean || v.Reviewers[0].Adapter != ollamaAdapter || v.Reviewers[0].Model != ollamaTestModel || v.Reviewers[0].Warning != "" {
		t.Fatalf("%+v", v)
	}
	workspace := traceValue(t, trace, "session-cwd")
	if got := traceValue(t, trace, "process-cwd"); got != workspace {
		t.Fatalf("process started in %s, session in %s", got, workspace)
	}
	if strings.HasPrefix(workspace, root) {
		t.Fatalf("workspace %s is inside the repository", workspace)
	}
	for key, want := range map[string]string{
		"exists .git/HEAD":                    "true",
		"exists package.json":                 "true",
		"exists repo/change.txt":              "true",
		"exists repo/.opencode/tools/evil.js": "true",
		"exists repo/link":                    "false",
		"exists .opencode":                    "false",
		"exists sdk-lock":                     "true",
		"env SLOPCOP_TEST_SECRET":             "",
		"env OPENCODE_PERMISSION":             "",
		"env OPENCODE_DISABLE_PROJECT_CONFIG": "1",
		"env OPENCODE_PURE":                   "1",
	} {
		if got := traceValue(t, trace, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	home := traceValue(t, trace, "env HOME")
	if filepath.Dir(home) != filepath.Dir(workspace) || !strings.HasPrefix(traceValue(t, trace, "env XDG_CONFIG_HOME"), home) {
		t.Errorf("home %s is not private to workspace %s", home, workspace)
	}
	noProxy := traceValue(t, trace, "env NO_PROXY")
	for _, host := range []string{"127.0.0.1", "localhost", "corp.example"} {
		if !strings.Contains(noProxy, host) {
			t.Errorf("NO_PROXY %q lacks %s", noProxy, host)
		}
	}
	var config struct {
		Permission map[string]string `json:"permission"`
		Compaction map[string]bool   `json:"compaction"`
		Providers  []string          `json:"enabled_providers"`
		Provider   map[string]struct {
			NPM     string            `json:"npm"`
			Options map[string]string `json:"options"`
			Models  map[string]struct {
				Limit map[string]int `json:"limit"`
			} `json:"models"`
		} `json:"provider"`
	}
	if err := json.Unmarshal([]byte(traceValue(t, trace, "env OPENCODE_CONFIG_CONTENT")), &config); err != nil {
		t.Fatal(err)
	}
	if config.Permission["*"] != "deny" || config.Permission["external_directory"] != "deny" || config.Permission["read"] != "allow" || len(config.Permission) != 5 {
		t.Errorf("permission %v", config.Permission)
	}
	if config.Compaction["auto"] || config.Compaction["prune"] {
		t.Errorf("compaction %v", config.Compaction)
	}
	provider := config.Provider[ollamaProviderID]
	if !slices.Equal(config.Providers, []string{ollamaProviderID}) || provider.NPM != "@ai-sdk/openai" || !strings.HasPrefix(provider.Options["baseURL"], os.Getenv("OLLAMA_HOST")) {
		t.Errorf("provider %+v", provider)
	}
	if limit := provider.Models[ollamaTestModel].Limit; limit["context"] != 64000 || limit["output"] != ollamaMaxOutput {
		t.Errorf("limit %v", limit)
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "slopcop-ollama-*")); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
}

func TestOllamaReviewerFailsRatherThanApproves(t *testing.T) {
	for _, tc := range []struct {
		mode, want string
		retried    bool
	}{
		{"opencode-context-error", "did not fit", false},
		{"opencode-no-usage", "did not report token usage", false},
		{"opencode-capped", "cut off", false},
		{"opencode-full", "too little", false},
		{"opencode-cached-full", "too little", false},
		{"opencode-prose", "not a verdict", true},
		{"opencode-ran-edit", "stopped the reviewer", false},
		{"opencode-wrong-model", "did not start on model", true},
		{"opencode-wrong-mode", "cannot be restricted", true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			root, c, store := reviewRepo(t)
			startOllamaStub(t, nil)
			trace := filepath.Join(t.TempDir(), "trace")
			v := Run(context.Background(), store, c, Options{Root: root, Settings: ollamaSettings(t, tc.mode, trace)})
			r := v.Reviewers[0]
			if v.Status != StatusFailed || r.Status != StatusFailed || !strings.Contains(r.Error, tc.want) {
				t.Fatalf("%+v", v)
			}
			if got := r.FirstError != ""; got != tc.retried {
				t.Fatalf("retried = %v: %+v", got, r)
			}
		})
	}
}

func TestOllamaReviewerRetriesAProseReply(t *testing.T) {
	root, c, store := reviewRepo(t)
	startOllamaStub(t, nil)
	trace := filepath.Join(t.TempDir(), "trace")
	v := Run(context.Background(), store, c, Options{Root: root, Settings: ollamaSettings(t, "opencode-prose-once", trace)})
	if v.Status != StatusClean || !strings.Contains(v.Reviewers[0].FirstError, "not a verdict") {
		t.Fatalf("%+v", v)
	}
}

func TestOllamaReviewerReportsFindings(t *testing.T) {
	root, c, store := reviewRepo(t)
	startOllamaStub(t, nil)
	trace := filepath.Join(t.TempDir(), "trace")
	v := Run(context.Background(), store, c, Options{Root: root, Settings: ollamaSettings(t, "opencode-finding", trace)})
	if v.Status != StatusFindings {
		t.Fatalf("%+v", v)
	}
}

func TestOllamaSetupFailsBeforeTheAdapterStarts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ollamaStub)
		want   string
	}{
		{"not installed", func(o *ollamaStub) { o.missing = true }, "ollama pull " + ollamaTestModel},
		{"old server", func(o *ollamaStub) { o.version = "0.40.15" }, "update to"},
		{"cloud model", func(o *ollamaStub) { o.remoteHost = "https://ollama.com:443" }, "cloud model"},
		{"no tool calling", func(o *ollamaStub) { o.capabilities = []string{"completion"} }, "tool calling"},
		{"default small context", func(o *ollamaStub) { o.context = 4096 }, "OLLAMA_CONTEXT_LENGTH"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, c, store := reviewRepo(t)
			stub := startOllamaStub(t, tc.change)
			trace := filepath.Join(t.TempDir(), "trace")
			v := Run(context.Background(), store, c, Options{Root: root, Settings: ollamaSettings(t, "opencode", trace)})
			r := v.Reviewers[0]
			if v.Status != StatusFailed || !strings.Contains(r.Error, tc.want) || r.FirstError != "" {
				t.Fatalf("%+v", v)
			}
			if _, err := os.Stat(trace); err == nil {
				t.Fatal("the adapter started")
			}
			if stub.called("/api/pull") {
				t.Fatal("setup pulled a model")
			}
		})
	}
}

func TestOllamaLoadsBeforeOtherReviewersStart(t *testing.T) {
	root, c, store := reviewRepo(t)
	dir := t.TempDir()
	terraTrace := filepath.Join(dir, "terra")
	var loads atomic.Int32
	var startedEarly atomic.Bool
	startOllamaStub(t, func(o *ollamaStub) {
		o.onLoad = func() {
			// The first load is the slow one; the reviewer's own check finds
			// the model loaded.
			if loads.Add(1) > 1 {
				return
			}
			if _, err := os.Stat(terraTrace); err == nil {
				startedEarly.Store(true)
			}
		}
	})
	settings := ollamaSettings(t, "opencode", filepath.Join(dir, "ollama"))
	settings.Reviewers = []Family{Terra, Ollama}
	settings.Adapters["codex"] = fakeAdapter("clean", "gpt-6-terra", terraTrace)
	v := Run(context.Background(), store, c, Options{Root: root, Settings: settings})
	if v.Status != StatusClean || v.Reviewers[0].Family != Terra || v.Reviewers[1].Family != Ollama {
		t.Fatalf("%+v", v)
	}
	if startedEarly.Load() || loads.Load() < 2 {
		t.Fatal("terra's timeout was running while Ollama loaded its model")
	}
}

func TestOllamaWithoutAModelFails(t *testing.T) {
	root, c, store := reviewRepo(t)
	stub := startOllamaStub(t, nil)
	settings := ollamaSettings(t, "opencode", filepath.Join(t.TempDir(), "trace"))
	delete(settings.Models, Ollama)
	v := Run(context.Background(), store, c, Options{Root: root, Settings: settings})
	if v.Status != StatusFailed || !strings.Contains(v.Reviewers[0].Error, "review.models.ollama") || stub.called("/api/show") {
		t.Fatalf("%+v", v)
	}
}

func TestOllamaUnreachableFails(t *testing.T) {
	root, c, store := reviewRepo(t)
	server := httptest.NewServer(http.NotFoundHandler())
	address := server.URL
	server.Close()
	t.Setenv("OLLAMA_HOST", address)
	v := Run(context.Background(), store, c, Options{Root: root, Settings: ollamaSettings(t, "opencode", filepath.Join(t.TempDir(), "trace"))})
	if v.Status != StatusFailed || !strings.Contains(v.Reviewers[0].Error, "ollama serve") {
		t.Fatalf("%+v", v)
	}
}

func TestOllamaRefusesADiffItCannotRead(t *testing.T) {
	root, c, store := reviewRepo(t)
	if err := os.WriteFile(filepath.Join(root, "change.txt"), []byte(strings.Repeat("line of changed code\n", 20000)), 0600); err != nil {
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
	startOllamaStub(t, nil)
	trace := filepath.Join(t.TempDir(), "trace")
	v := Run(context.Background(), store, c, Options{Root: root, Settings: ollamaSettings(t, "opencode", trace)})
	if v.Status != StatusFailed || !strings.Contains(v.Reviewers[0].Error, "over the 256 KiB") {
		t.Fatalf("%+v", v)
	}
	if _, err := os.Stat(trace); err == nil {
		t.Fatal("the adapter started")
	}
}

func TestOllamaVerdictIsNotReusedForAnotherModel(t *testing.T) {
	root, c, store := reviewRepo(t)
	startOllamaStub(t, nil)
	trace := filepath.Join(t.TempDir(), "trace")
	settings := ollamaSettings(t, "opencode", trace)
	if v := Run(context.Background(), store, c, Options{Root: root, Settings: settings}); v.Status != StatusClean {
		t.Fatalf("%+v", v)
	}
	if v := Run(context.Background(), store, c, Options{Root: root, Settings: settings}); !v.Reused {
		t.Fatalf("same model was not reused: %+v", v)
	}
	settings.Models = map[Family]string{Ollama: "another:model"}
	if v := Run(context.Background(), store, c, Options{Root: root, Settings: settings}); v.Reused {
		t.Fatalf("reused another model's verdict: %+v", v)
	}
}

func TestOllamaRemoteHostIsReported(t *testing.T) {
	server, err := ollamaServerFrom("gpu-box:11434")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(server.warning(), "gpu-box:11434") || !strings.Contains(server.warning(), "did not stay") {
		t.Fatal(server.warning())
	}
}

func TestOpencodeEnvironmentKeepsOnlyWhatItNeeds(t *testing.T) {
	got := opencodeEnvironment([]string{"PATH=/bin", "ANTHROPIC_API_KEY=k", "OPENAI_API_KEY=k", "GIT_DIR=/repo/.git", "OPENCODE_CONFIG=/evil.json", "XDG_CONFIG_HOME=/real", "HOME=/real", "https_proxy=http://p:1", "npm_config_registry=https://r.example"}, "/t", "{}", ollamaServer{base: mustURL(t, "http://gpu-box:11434")})
	joined := "\n" + strings.Join(got, "\n") + "\n"
	for _, gone := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GIT_DIR", "OPENCODE_CONFIG=", "=/real"} {
		if strings.Contains(joined, gone) {
			t.Errorf("kept %s: %s", gone, joined)
		}
	}
	for _, kept := range []string{"\nPATH=/bin\n", "\nhttps_proxy=http://p:1\n", "\nnpm_config_registry=https://r.example\n", "\nOPENCODE_CONFIG_CONTENT={}\n", "gpu-box"} {
		if !strings.Contains(joined, kept) {
			t.Errorf("lost %q: %s", kept, joined)
		}
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	s, err := ollamaServerFrom(raw)
	if err != nil {
		t.Fatal(err)
	}
	return s.base
}

func TestOpencodeToolGuard(t *testing.T) {
	line := func(update, id, kind, status string) string {
		return `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"` + update + `","toolCallId":"` + id + `","kind":"` + kind + `","status":"` + status + `"}}}` + "\n"
	}
	for _, tc := range []struct {
		name    string
		lines   []string
		tripped bool
	}{
		{"read", []string{line("tool_call", "1", "read", "pending"), line("tool_call_update", "1", "", "completed")}, false},
		{"search", []string{line("tool_call", "1", "search", "completed")}, false},
		{"refused edit", []string{line("tool_call", "1", "edit", "pending"), line("tool_call_update", "1", "", "failed")}, false},
		{"edit ran", []string{line("tool_call", "1", "edit", "pending"), line("tool_call_update", "1", "", "completed")}, true},
		{"command ran", []string{line("tool_call", "1", "execute", "completed")}, true},
		{"fetch ran", []string{line("tool_call", "1", "fetch", "completed")}, true},
		{"unknown call completed", []string{line("tool_call_update", "9", "", "completed")}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got error
			g := &toolGuard{check: (&opencodeTools{}).check, trip: func(err error) { got = err }}
			if _, err := g.Write([]byte(strings.Join(tc.lines, ""))); err != nil {
				t.Fatal(err)
			}
			if (got != nil) != tc.tripped || got != nil && !errors.Is(got, errForbiddenTool) {
				t.Fatalf("got %v", got)
			}
		})
	}
}

func TestOpencodeLaunch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("npx starts in the workspace on Windows")
	}
	workspace := "/tmp/slopcop-ollama-1/workspace"
	argv, dir, err := opencodeLaunch(DefaultCommand(Ollama), workspace)
	want := []string{"npx", "--yes", "-p", "opencode-ai@1.18.26", "-c", "cd '/tmp/slopcop-ollama-1/workspace' && exec opencode 'acp' '--pure'"}
	if err != nil || dir != "" || !slices.Equal(argv, want) {
		t.Fatalf("npx: %q %q %v", argv, dir, err)
	}
	argv, dir, err = opencodeLaunch([]string{"/opt/opencode"}, workspace)
	if err != nil || dir != workspace || !slices.Equal(argv, []string{"/opt/opencode", "acp", "--pure"}) {
		t.Fatalf("binary: %q %q %v", argv, dir, err)
	}
	for _, base := range [][]string{{"npm", "exec", "opencode-ai"}, {"npx", "opencode-ai@1"}, {"npx", "--yes", "--prefix=/tmp", "opencode-ai@1"}} {
		if _, _, err := opencodeLaunch(base, workspace); err == nil || !strings.Contains(err.Error(), "review.adapters.opencode.command") {
			t.Errorf("%q: %v", base, err)
		}
	}
}
