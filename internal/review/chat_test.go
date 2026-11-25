package review

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/BrokkAi/slopcop/internal/gitx"
	"github.com/BrokkAi/slopcop/internal/review/chatagent"
)

const chatTestModel = "vendor/model-1"

func chatSettings(argv []string) Settings {
	return Settings{Reviewers: []Family{OpenRouter}, Models: map[Family]string{OpenRouter: chatTestModel}, Adapters: map[string][]string{string(OpenRouter): argv}}
}

// TestRealChatAgent is re-executed as the adapter, so a review runs the real
// agent against a local model server.
func TestRealChatAgent(t *testing.T) {
	url := os.Getenv("SLOPCOP_TEST_CHAT_URL")
	if url == "" {
		t.Skip("runs only as a re-executed adapter")
	}
	cfg := chatagent.Config{Key: os.Getenv("OPENROUTER_API_KEY")}
	cfg.Provider, _ = chatagent.Lookup(string(OpenRouter))
	cfg.Provider.BaseURL = url
	for _, arg := range os.Args {
		if v, ok := strings.CutPrefix(arg, "--model="); ok {
			cfg.Model = v
		}
		if v, ok := strings.CutPrefix(arg, "--tree="); ok {
			cfg.Tree = v
		}
		if v, ok := strings.CutPrefix(arg, "--base="); ok {
			cfg.Base = v
		}
	}
	_ = chatagent.Serve(context.Background(), cfg, os.Stdin, os.Stdout)
	os.Exit(0)
}

func TestOpenRouterIsOptIn(t *testing.T) {
	if slices.Contains(Families, OpenRouter) {
		t.Fatal("slopcop init would pick openrouter by itself")
	}
	if f, err := ParseFamily("openrouter"); err != nil || f != OpenRouter {
		t.Fatalf("ParseFamily: %q %v", f, err)
	}
	if got := AdapterFor(OpenRouter, Settings{Provider: ProviderCopilot}); got != "openrouter" {
		t.Fatalf("adapter %q", got)
	}
}

func TestChatCommandCarriesTheModelIntoTheReuseKey(t *testing.T) {
	a := CommandFor(OpenRouter, Settings{Models: map[Family]string{OpenRouter: "vendor/a"}})
	b := CommandFor(OpenRouter, Settings{Models: map[Family]string{OpenRouter: "vendor/b"}})
	if slices.Equal(a, b) || a[len(a)-1] != "--model=vendor/a" {
		t.Fatalf("%v %v", a, b)
	}
	if a[0] != chatSelfCommand || strings.Contains(strings.Join(a, " "), "sk-") {
		t.Fatalf("%v", a)
	}
}

func TestChatPrerequisites(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	if got := MissingPrerequisite(OpenRouter, t.TempDir(), Settings{}); !strings.Contains(got, "review.models.openrouter") {
		t.Fatalf("%q", got)
	}
	withModel := Settings{Models: map[Family]string{OpenRouter: chatTestModel}}
	if got := MissingPrerequisite(OpenRouter, t.TempDir(), withModel); got != "OPENROUTER_API_KEY is not set" {
		t.Fatalf("%q", got)
	}
	t.Setenv("OPENROUTER_API_KEY", "sk-test")
	if got := MissingPrerequisite(OpenRouter, t.TempDir(), withModel); got != "" {
		t.Fatalf("%q", got)
	}
}

func TestChatPromptHidesTheLocalPath(t *testing.T) {
	root, c, _ := reviewRepo(t)
	prompt := reviewPrompt(root, c, intent{Source: "test", Text: "x"}, "", "diff", AdapterFor(OpenRouter, Settings{}))
	if strings.Contains(prompt, root) {
		t.Fatal("the prompt names the repository's location on this machine")
	}
	if !strings.Contains(prompt, chatRules) || strings.Contains(prompt, sandboxRules) {
		t.Fatal("the prompt has the wrong tool rules")
	}
}

// chatServer answers with a read of change.txt, then the verdict.
type chatServer struct {
	mu     sync.Mutex
	bodies []string
	auth   []string
	reply  string
	status int
}

func (s *chatServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bodies = append(s.bodies, string(data))
	s.auth = append(s.auth, r.Header.Get("Authorization"))
	if s.status != 0 {
		w.WriteHeader(s.status)
	}
	if s.reply != "" {
		_, _ = io.WriteString(w, s.reply)
		return
	}
	if len(s.bodies) == 1 {
		args, _ := json.Marshal(map[string]string{"path": "change.txt"})
		out, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": "read_file", "arguments": string(args)}}}}}}})
		_, _ = w.Write(out)
		return
	}
	_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"No findings."},"finish_reason":"stop"}]}`)
}

// got returns the request bodies so far. The handler runs on another goroutine.
func (s *chatServer) got() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.bodies)
}

func (s *chatServer) authorization() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.auth[0]
}

func realChatAdapter(t *testing.T, s *chatServer) []string {
	t.Helper()
	server := httptest.NewServer(s)
	t.Cleanup(server.Close)
	t.Setenv("SLOPCOP_TEST_CHAT_URL", server.URL)
	return []string{os.Args[0], "-test.run=^TestRealChatAgent$", "--"}
}

func TestOpenRouterReviewsWithTheRealAgent(t *testing.T) {
	const key = "sk-test-not-a-real-key"
	t.Setenv("OPENROUTER_API_KEY", key)
	root, c, store := reviewRepo(t)
	canary := filepath.Join(t.TempDir(), "canary")
	// None of these may run or load: the agent reads no repository config.
	for name, content := range map[string]string{
		".opencode/plugin/canary.js": "require('fs').writeFileSync(" + fmt.Sprintf("%q", canary) + ",'1')\n",
		"opencode.json":              `{"mcp":{"x":{"type":"local","command":["touch",` + fmt.Sprintf("%q", canary) + `]}}}`,
		".agents/hooks.json":         `{"hooks":[{"command":"touch ` + canary + `"}]}`,
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
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
	server := &chatServer{}
	v := Run(context.Background(), store, c, Options{Root: root, Settings: chatSettings(realChatAdapter(t, server))})
	if v.Status != StatusClean || v.Reviewers[0].Model != chatTestModel || v.Reviewers[0].Adapter != "openrouter" {
		t.Fatalf("%+v", v)
	}
	if len(server.got()) != 2 || server.authorization() != "Bearer "+key {
		t.Fatalf("%d requests", len(server.got()))
	}
	if !strings.Contains(server.got()[1], `"role":"tool"`) {
		t.Fatalf("the tool result was not sent: %s", server.got()[1])
	}
	for _, body := range server.got() {
		if strings.Contains(body, root) {
			t.Fatal("a request names the repository's location on this machine")
		}
	}
	if _, err := os.Stat(canary); err == nil {
		t.Fatal("repository configuration ran")
	}
	err = filepath.WalkDir(v.LogDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), key) {
			t.Errorf("%s contains the API key", path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOpenRouterServerSearchFailsWithoutARetry(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-test")
	root, c, store := reviewRepo(t)
	server := &chatServer{reply: `{"choices":[{"message":{"role":"assistant","content":"No findings.","annotations":[{"type":"url_citation"}]},"finish_reason":"stop"}]}`}
	v := Run(context.Background(), store, c, Options{Root: root, Settings: chatSettings(realChatAdapter(t, server))})
	r := v.Reviewers[0]
	if v.Status != StatusFailed || !strings.Contains(r.Error, errForbiddenTool.Error()) || r.FirstError != "" {
		t.Fatalf("%+v", r)
	}
	if len(server.got()) != 1 {
		t.Fatalf("%d requests, want no retry", len(server.got()))
	}
}

func TestOpenRouterBudgetLimitIsReportedAsALimit(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-test")
	root, c, store := reviewRepo(t)
	server := &chatServer{status: http.StatusForbidden, reply: `{"error":{"code":403,"message":"Workspace budget exceeded"}}`}
	v := Run(context.Background(), store, c, Options{Root: root, Settings: chatSettings(realChatAdapter(t, server))})
	r := v.Reviewers[0]
	if v.Status != StatusFailed || !strings.Contains(r.Error, "over its usage limit") || strings.Contains(r.Error, "not signed in") || r.FirstError != "" {
		t.Fatalf("%+v", r)
	}
	if len(server.got()) != 1 {
		t.Fatalf("%d requests, want no retry", len(server.got()))
	}
}

func TestOpenRouterMissingKeyIsNotRetried(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	root, c, store := reviewRepo(t)
	server := &chatServer{}
	argv := realChatAdapter(t, server)
	v := Run(context.Background(), store, c, Options{Root: root, Settings: chatSettings(argv)})
	r := v.Reviewers[0]
	if v.Status != StatusFailed || !strings.Contains(r.Error, "set OPENROUTER_API_KEY to a valid OpenRouter key") || strings.Contains(r.Error, "not signed in") || r.FirstError != "" || len(server.got()) != 0 {
		t.Fatalf("%+v", r)
	}
}

// The fake agent stands in for an adapter override that misbehaves.

func TestChatGuardStopsAToolThatIsNotARead(t *testing.T) {
	root, c, store := reviewRepo(t)
	trace := filepath.Join(t.TempDir(), "trace")
	v := Run(context.Background(), store, c, Options{Root: root, Settings: chatSettings(fakeAdapter("ungated-tool", chatTestModel, trace))})
	r := v.Reviewers[0]
	if v.Status != StatusFailed || !strings.Contains(r.Error, errForbiddenTool.Error()) || r.FirstError != "" {
		t.Fatalf("%+v", r)
	}
	data, _ := os.ReadFile(trace)
	if n := strings.Count(string(data), "prompt\n"); n != 1 {
		t.Fatalf("%d prompts, want no retry", n)
	}
}

func TestChatClientRefusesEveryPermissionRequest(t *testing.T) {
	root, c, store := reviewRepo(t)
	trace := filepath.Join(t.TempDir(), "trace")
	v := Run(context.Background(), store, c, Options{Root: root, Settings: chatSettings(fakeAdapter("outside-read", chatTestModel, trace))})
	if v.Status != StatusFailed {
		t.Fatalf("%+v", v)
	}
	data, _ := os.ReadFile(trace)
	if strings.Contains(string(data), "permission=allow") || !strings.Contains(string(data), "permission=deny") {
		t.Fatalf("a read outside the snapshot was not refused:\n%s", data)
	}
}

func TestChatReviewerMustRunTheConfiguredModel(t *testing.T) {
	root, c, store := reviewRepo(t)
	trace := filepath.Join(t.TempDir(), "trace")
	v := Run(context.Background(), store, c, Options{Root: root, Settings: chatSettings(fakeAdapter("clean", "vendor/other", trace))})
	if v.Status != StatusFailed || !strings.Contains(v.Reviewers[0].Error, "did not confirm the configured model") {
		t.Fatalf("%+v", v)
	}
	data, _ := os.ReadFile(trace)
	if strings.Contains(string(data), "prompt\n") {
		t.Fatal("a reviewer on another model was prompted")
	}
}

func TestChatDoesNotApproveADiffItCannotSee(t *testing.T) {
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
	if c, err = NewCapture(root, c.BaseCommit, tree); err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(t.TempDir(), "trace")
	v := Run(context.Background(), store, c, Options{Root: root, Settings: chatSettings(fakeAdapter("clean", chatTestModel, trace))})
	if v.Status != StatusClean || len(v.Reviewers) != 1 {
		t.Fatalf("on-demand diff review: %+v", v)
	}
	if _, err := os.Stat(trace); err != nil {
		t.Fatal("adapter did not start", err)
	}
}

func TestChatDoesNotApproveAChangeToAFileItCannotRead(t *testing.T) {
	root, _, store := reviewRepo(t)
	// A small edit to a file over the limit: the diff fits, the file does not.
	large := strings.Repeat("line\n", chatagent.MaxFile/5+1)
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := gitx.Run(root, "add", "-A"); err != nil {
			t.Fatal(err)
		}
	}
	write(large)
	if _, err := gitx.Run(root, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "large"); err != nil {
		t.Fatal(err)
	}
	base, err := gitx.Run(root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	write(large + "one more\n")
	tree, err := gitx.Run(root, "write-tree")
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewCapture(root, strings.TrimSpace(base), strings.TrimSpace(tree))
	if err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(t.TempDir(), "trace")
	v := Run(context.Background(), store, c, Options{Root: root, Settings: chatSettings(fakeAdapter("clean", chatTestModel, trace))})
	r := v.Reviewers[0]
	if v.Status != StatusFailed || !strings.Contains(r.Error, errChangeNotExported.Error()) || r.FirstError != "" {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(trace); !os.IsNotExist(err) {
		t.Fatal("the adapter started on a change it cannot read")
	}
}

func TestChatDoesNotApproveAChangeTheDiffHides(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"utf-16 source":  {"source.txt": "\xff\xfei\x00f\x00 \x00x\x00\n\x00"},
		"latin-1 source": {"latin.txt": "caf\xe9\n"},
		"forced to text": {".gitattributes": "*.cs diff\n", "forced.cs": "\xff\xfei\x00f\x00 \x00x\x00\n\x00"},
	} {
		t.Run(name, func(t *testing.T) {
			root, c, store := reviewRepo(t)
			for path, content := range files {
				if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := gitx.Run(root, "add", "-A"); err != nil {
				t.Fatal(err)
			}
			tree, err := gitx.Run(root, "write-tree")
			if err != nil {
				t.Fatal(err)
			}
			if c, err = NewCapture(root, c.BaseCommit, strings.TrimSpace(tree)); err != nil {
				t.Fatal(err)
			}
			trace := filepath.Join(t.TempDir(), "trace")
			v := Run(context.Background(), store, c, Options{Root: root, Settings: chatSettings(fakeAdapter("clean", chatTestModel, trace))})
			r := v.Reviewers[0]
			if name != "latin-1 source" {
				if v.Status != StatusClean {
					t.Fatalf("binary diff accessible on demand: %+v", v)
				}
				return
			}
			if v.Status != StatusFailed || !strings.Contains(r.Error, errChangeBinary.Error()) || r.FirstError != "" {
				t.Fatalf("%+v", r)
			}
			if _, err := os.Stat(trace); !os.IsNotExist(err) {
				t.Fatal("the adapter started on a change it cannot see")
			}
		})
	}
}

func TestOpenRouterLimitIsNotRetried(t *testing.T) {
	err := fmt.Errorf("prompt: %s", "the reviewer reached its limit of 40 model requests before a verdict")
	if retryable(context.Background(), err) {
		t.Fatal("a reviewer at its spending limit would run again")
	}
}

func TestChatDoesNotApproveADeletedFileItCannotRead(t *testing.T) {
	root, _, store := reviewRepo(t)
	for path, content := range map[string]string{".gitattributes": "*.cs diff\n", "old.cs": "\xff\xfei\x00f\x00 \x00x\x00\n\x00"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := gitx.Run(root, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.Run(root, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "base"); err != nil {
		t.Fatal(err)
	}
	base, err := gitx.Run(root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = gitx.Run(root, "rm", "-q", "old.cs"); err != nil {
		t.Fatal(err)
	}
	tree, err := gitx.Run(root, "write-tree")
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewCapture(root, strings.TrimSpace(base), strings.TrimSpace(tree))
	if err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(t.TempDir(), "trace")
	v := Run(context.Background(), store, c, Options{Root: root, Settings: chatSettings(fakeAdapter("clean", chatTestModel, trace))})
	if v.Status != StatusClean {
		t.Fatalf("deleted binary available with read_diff: %+v", v)
	}
}

// The review diff ignores attributes, so text marked binary is shown in full.
func TestChatReviewsTextARepositoryMarksBinary(t *testing.T) {
	root, c, store := reviewRepo(t)
	for path, content := range map[string]string{".gitattributes": "hidden.py binary\n", "hidden.py": "print('shown in the diff')\n"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := gitx.Run(root, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	tree, err := gitx.Run(root, "write-tree")
	if err != nil {
		t.Fatal(err)
	}
	if c, err = NewCapture(root, c.BaseCommit, strings.TrimSpace(tree)); err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(t.TempDir(), "trace")
	v := Run(context.Background(), store, c, Options{Root: root, Settings: chatSettings(fakeAdapter("clean", chatTestModel, trace))})
	if v.Status != StatusClean {
		t.Fatalf("%+v", v.Reviewers)
	}
}

func TestChatDoesNotApproveAChangeToAPathJSONWouldAlter(t *testing.T) {
	root, c, store := reviewRepo(t)
	object, err := gitx.Run(root, "-c", "core.autocrlf=false", "hash-object", "-w", "change.txt")
	if err != nil {
		t.Fatal(err)
	}
	// Through stdin: Windows would turn the byte into U+FFFD in an argument.
	if _, err = gitx.RunInput(root, nil, []byte("100644 "+strings.TrimSpace(object)+"\tcaf\xe9.txt\n"), "update-index", "--index-info"); err != nil {
		t.Fatal(err)
	}
	tree, err := gitx.Run(root, "write-tree")
	if err != nil {
		t.Fatal(err)
	}
	if c, err = NewCapture(root, c.BaseCommit, strings.TrimSpace(tree)); err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(t.TempDir(), "trace")
	v := Run(context.Background(), store, c, Options{Root: root, Settings: chatSettings(fakeAdapter("clean", chatTestModel, trace))})
	if r := v.Reviewers[0]; v.Status != StatusFailed || !strings.Contains(r.Error, errChangeNotExported.Error()) {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(trace); !os.IsNotExist(err) {
		t.Fatal("the adapter started on a path it cannot address")
	}
}

func TestChatToolCheck(t *testing.T) {
	line := func(kind, status, title string) []byte {
		return []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"tool_call","toolCallId":"1","kind":"` + kind + `","status":"` + status + `","title":"` + title + `"}}}`)
	}
	for _, tc := range []struct {
		kind, status, title string
		stop                bool
	}{
		{"read", "completed", "read_file", false},
		{"search", "completed", "search_files", false},
		{"other", "failed", "bash", false},
		{"other", "completed", "bash", true},
		{"fetch", "completed", "OpenRouter web search", true},
		{"execute", "in_progress", "", true},
	} {
		if got := chatToolCheck(line(tc.kind, tc.status, tc.title)); (got != "") != tc.stop {
			t.Errorf("%+v: %q", tc, got)
		}
	}
}

func TestOnlyTheChatAgentGetsItsKey(t *testing.T) {
	env := []string{"OPENROUTER_API_KEY=sk-test", "PATH=/bin"}
	for _, f := range []Family{Terra, Haiku, Flash, Gemini, Kimi, Grok, Ollama, ""} {
		if got := reviewerEnvironment(f, "/repo", "/data", slices.Clone(env)); slices.Contains(got, env[0]) || !slices.Contains(got, env[1]) {
			t.Errorf("%q: %v", f, got)
		}
	}
	if got := reviewerEnvironment(OpenRouter, "/repo", "/data", slices.Clone(env)); !slices.Contains(got, env[0]) {
		t.Errorf("the OpenRouter agent lost its key: %v", got)
	}
}
