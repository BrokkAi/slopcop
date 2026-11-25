package chatagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/BrokkAi/slopcop/internal/gitx"
)

const testKey = "sk-test-not-a-real-key"

// model is a fake chat service: each request gets the next scripted reply.
type model struct {
	mu       sync.Mutex
	replies  []string
	requests []map[string]any
	headers  []http.Header
}

func (m *model) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(data, &body)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, body)
	m.headers = append(m.headers, r.Header.Clone())
	reply := stop("No findings.")
	if len(m.replies) > 0 {
		reply, m.replies = m.replies[0], m.replies[1:]
	}
	_, _ = io.WriteString(w, reply)
}

// got returns the requests so far. The handler runs on another goroutine.
func (m *model) got() []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.requests)
}

func (m *model) authorization() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.headers[0].Get("Authorization")
}

func stop(text string) string {
	data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}}})
	return string(data)
}

func call(name string, args map[string]any) string {
	encoded, _ := json.Marshal(args)
	data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"finish_reason": "tool_calls", "message": map[string]any{
		"role": "assistant", "content": nil, "reasoning_details": []any{map[string]any{"type": "reasoning.text", "text": "kept"}},
		"tool_calls": []any{map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": name, "arguments": string(encoded)}}},
	}}}})
	return string(data)
}

// client records what the agent reports and refuses nothing it is not asked.
type client struct {
	acp.Client
	mu    sync.Mutex
	text  strings.Builder
	kinds []string
}

func (c *client) SessionUpdate(_ context.Context, n acp.SessionNotification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n.Update.AgentMessageChunk != nil && n.Update.AgentMessageChunk.Content.Text != nil {
		c.text.WriteString(n.Update.AgentMessageChunk.Content.Text.Text)
	}
	if n.Update.ToolCall != nil {
		c.kinds = append(c.kinds, string(n.Update.ToolCall.Kind))
	}
	return nil
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// repoTree makes a repository whose tree holds a symlink to a file outside it
// and a textconv filter that would leave a canary if anything ran it.
func repoTree(t *testing.T) (root, tree, secret, canary string) {
	t.Helper()
	root = t.TempDir()
	outside := t.TempDir()
	secret = filepath.Join(outside, "secret.txt")
	canary = filepath.Join(outside, "canary")
	if err := os.WriteFile(secret, []byte("TOP-SECRET-VALUE\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, root, "init", "-q")
	git(t, root, "config", "diff.evil.textconv", "sh -c 'touch "+canary+"'")
	for name, content := range map[string]string{"a.txt": "alpha\nneedle one\n", "dir/b.txt": "needle two\n", ".gitattributes": "*.txt diff=evil\nhidden.src binary\n", "opencode.json": `{"mcp":{}}`, "bin.dat": "\x00\x01", "hidden.src": "needle three\n"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(secret, filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "-A")
	return root, git(t, root, "write-tree"), secret, canary
}

// review runs one prompt through a connected agent.
func review(t *testing.T, root, tree string, m *model, mutate func(*Config)) (*client, string, error) {
	t.Helper()
	server := httptest.NewServer(m)
	t.Cleanup(server.Close)
	p, _ := Lookup("openrouter")
	p.BaseURL = server.URL
	cfg := Config{Provider: p, Model: "vendor/model", Key: testKey, Tree: tree}
	if mutate != nil {
		mutate(&cfg)
	}
	agentIn, clientOut := io.Pipe()
	clientIn, agentOut := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	go func() { _ = Serve(ctx, cfg, agentIn, agentOut) }()
	c := &client{}
	conn := acp.NewClientSideConnection(c, clientOut, clientIn)
	if _, err := conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber}); err != nil {
		t.Fatal(err)
	}
	session, err := conn.NewSession(ctx, acp.NewSessionRequest{Cwd: root, McpServers: []acp.McpServer{}})
	if err != nil {
		return c, "", err
	}
	_, err = conn.Prompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("review")}})
	c.mu.Lock()
	defer c.mu.Unlock()
	return c, c.text.String(), err
}

func toolResult(t *testing.T, body map[string]any) string {
	t.Helper()
	messages := body["messages"].([]any)
	last := messages[len(messages)-1].(map[string]any)
	if last["role"] != "tool" {
		t.Fatalf("last message is %v", last["role"])
	}
	return last["content"].(string)
}

func TestRequestCarriesPrivacyControlsAndOnlyReadTools(t *testing.T) {
	root, tree, _, _ := repoTree(t)
	m := &model{}
	_, reply, err := review(t, root, tree, m, nil)
	if err != nil || reply != "No findings." {
		t.Fatalf("%q %v", reply, err)
	}
	body := m.got()[0]
	if m.authorization() != "Bearer "+testKey {
		t.Fatal("key was not sent as a bearer token")
	}
	data, _ := json.Marshal(body)
	if strings.Contains(string(data), testKey) {
		t.Fatal("the key is in the request body")
	}
	provider := body["provider"].(map[string]any)
	if provider["data_collection"] != "deny" || provider["require_parameters"] != true {
		t.Fatalf("provider = %v", provider)
	}
	plugin := body["plugins"].([]any)[0].(map[string]any)
	if plugin["id"] != "web" || plugin["enabled"] != false {
		t.Fatalf("plugins = %v", body["plugins"])
	}
	var names []string
	for _, tool := range body["tools"].([]any) {
		names = append(names, tool.(map[string]any)["function"].(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "list_changes,read_diff,read_file,list_files,search_files" {
		t.Fatalf("tools = %v", names)
	}
}

func TestProbeOffersNoTools(t *testing.T) {
	m := &model{}
	if _, _, err := review(t, t.TempDir(), "", m, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.got()[0]["tools"]; ok {
		t.Fatal("a probe was offered tools")
	}
}

func TestToolsReadOnlyTheTree(t *testing.T) {
	root, tree, secret, canary := repoTree(t)
	for name, tc := range map[string]struct {
		tool   string
		args   map[string]any
		want   string
		absent string
	}{
		"file":          {"read_file", map[string]any{"path": "a.txt"}, "2\tneedle one", ""},
		"page":          {"read_file", map[string]any{"path": "a.txt", "offset": 2, "limit": 1}, "needle one", "alpha"},
		"list":          {"list_files", map[string]any{"prefix": "dir/"}, "dir/b.txt", "a.txt"},
		"search":        {"search_files", map[string]any{"pattern": "needle"}, "dir/b.txt:1:needle two", tree},
		"under prefix":  {"search_files", map[string]any{"pattern": "needle", "prefix": "dir"}, "needle two", "needle one"},
		"absolute":      {"read_file", map[string]any{"path": secret}, "not a readable file", "TOP-SECRET"},
		"traversal":     {"read_file", map[string]any{"path": "../" + filepath.Base(filepath.Dir(secret)) + "/secret.txt"}, "not a readable file", "TOP-SECRET"},
		"symlink":       {"read_file", map[string]any{"path": "link.txt"}, "not a readable file", "TOP-SECRET"},
		"git dir":       {"read_file", map[string]any{"path": ".git/config"}, "not a readable file", "textconv"},
		"no shell":      {"bash", map[string]any{"command": "touch " + canary}, "no tool named", ""},
		"bad pattern":   {"search_files", map[string]any{"pattern": "("}, "Error:", ""},
		"marked binary": {"search_files", map[string]any{"pattern": "needle three"}, "hidden.src:1:needle three", ""},
	} {
		t.Run(name, func(t *testing.T) {
			m := &model{replies: []string{call(tc.tool, tc.args), stop("No findings.")}}
			c, reply, err := review(t, root, tree, m, nil)
			if err != nil || reply != "No findings." {
				t.Fatalf("%q %v", reply, err)
			}
			got := toolResult(t, m.got()[1])
			if !strings.Contains(got, tc.want) || tc.absent != "" && strings.Contains(got, tc.absent) {
				t.Fatalf("result %q, want %q without %q", got, tc.want, tc.absent)
			}
			want := []string{string(acp.ToolKindRead), string(acp.ToolKindSearch)}
			if tc.tool == "bash" {
				want = []string{string(acp.ToolKindOther)}
			}
			for _, kind := range c.kinds {
				if !slices.Contains(want, kind) {
					t.Fatalf("reported kind %q, want one of %v", kind, want)
				}
			}
		})
	}
	if _, err := os.Stat(canary); err == nil {
		t.Fatal("a repository filter or command ran")
	}
	if changed := git(t, root, "diff", "--name-only"); changed != "" {
		t.Fatalf("the checkout changed: %s", changed)
	}
}

func TestToolCallKeepsTheModelsReasoningFields(t *testing.T) {
	root, tree, _, _ := repoTree(t)
	m := &model{replies: []string{call("list_files", nil), stop("No findings.")}}
	if _, _, err := review(t, root, tree, m, nil); err != nil {
		t.Fatal(err)
	}
	assistant := m.got()[1]["messages"].([]any)[1].(map[string]any)
	if _, ok := assistant["reasoning_details"]; !ok {
		t.Fatalf("assistant message lost its reasoning fields: %v", assistant)
	}
}

func TestAnAnswerThatIsNotAVerdictFails(t *testing.T) {
	root, tree, _, _ := repoTree(t)
	for name, reply := range map[string]string{
		"cut off":        `{"choices":[{"message":{"role":"assistant","content":"No findings."},"finish_reason":"length"}]}`,
		"filtered":       `{"choices":[{"message":{"role":"assistant","content":"No findings."},"finish_reason":"content_filter"}]}`,
		"finish error":   `{"choices":[{"message":{"role":"assistant","content":"No findings."},"finish_reason":"error"}]}`,
		"error in 200":   `{"error":{"code":502,"message":"upstream failed"}}`,
		"choice error":   `{"choices":[{"message":{"role":"assistant","content":"No findings."},"finish_reason":"stop","error":{"code":500,"message":"died"}}]}`,
		"empty":          `{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`,
		"no choices":     `{"choices":[]}`,
		"not json":       `<html>gateway</html>`,
		"unknown format": `{"choices":[{"message":{"role":"assistant","content":{"x":1}},"finish_reason":"stop"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, text, err := review(t, root, tree, &model{replies: []string{reply}}, nil)
			if err == nil || text != "" {
				t.Fatalf("approved: %q %v", text, err)
			}
		})
	}
}

func TestServerSideToolStopsTheReview(t *testing.T) {
	root, tree, _, _ := repoTree(t)
	for name, reply := range map[string]string{
		"citation":  `{"choices":[{"message":{"role":"assistant","content":"No findings.","annotations":[{"type":"url_citation","url_citation":{"url":"https://example.com"}}]},"finish_reason":"stop"}]}`,
		"usage":     `{"choices":[{"message":{"role":"assistant","content":"No findings."},"finish_reason":"stop"}],"usage":{"server_tool_use_details":{"web_search_requests":1}}}`,
		"tool type": `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"s","type":"openrouter:web_search","function":{"name":"x","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			c, text, err := review(t, root, tree, &model{replies: []string{reply}}, nil)
			if err == nil || text != "" {
				t.Fatalf("approved: %q %v", text, err)
			}
			if len(c.kinds) != 1 || c.kinds[0] != string(acp.ToolKindFetch) {
				t.Fatalf("kinds = %v, want one fetch for the guard to stop", c.kinds)
			}
		})
	}
	if _, _, err := review(t, root, tree, &model{replies: []string{`{"choices":[{"message":{"role":"assistant","content":"No findings."},"finish_reason":"stop"}],"usage":{"server_tool_use_details":{"web_search_requests":0}}}`}}, nil); err != nil {
		t.Fatalf("a zero count is not a tool run: %v", err)
	}
}

func TestReviewEndsWithoutAVerdictAtTheRoundLimit(t *testing.T) {
	root, tree, _, _ := repoTree(t)
	replies := make([]string, maxRounds+1)
	for i := range replies {
		replies[i] = call("list_files", nil)
	}
	m := &model{replies: replies}
	if _, text, err := review(t, root, tree, m, nil); err == nil || text != "" {
		t.Fatalf("%q %v", text, err)
	}
	if len(m.got()) != maxRounds {
		t.Fatalf("%d requests", len(m.got()))
	}
}

func TestReadBudgetEndsTheReviewWithoutAVerdict(t *testing.T) {
	root, tree, _, _ := repoTree(t)
	m := &model{replies: []string{call("read_file", map[string]any{"path": "a.txt"}), stop("No findings.")}}
	_, text, err := review(t, root, tree, m, func(c *Config) { c.ReadBudget = 4 })
	if err == nil || !IsLimitError(err.Error()) || text != "" {
		t.Fatalf("%q %v", text, err)
	}
	if len(m.got()) != 1 {
		t.Fatalf("%d requests: the model was asked for a verdict after the limit", len(m.got()))
	}
}

// emptyTree is git's empty tree, so every file of a tree compared with it is
// changed.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

func TestAVerdictAfterAFailedReadOfAChangedFileFails(t *testing.T) {
	root, tree, _, _ := repoTree(t)
	withBase := func(c *Config) { c.Base = emptyTree }
	m := &model{replies: []string{call("read_file", map[string]any{"path": "bin.dat"}), stop("No findings.")}}
	if _, text, err := review(t, root, tree, m, withBase); err == nil || !strings.Contains(err.Error(), "changed file") || !strings.Contains(err.Error(), "bin.dat") || text != "" {
		t.Fatalf("%q %v", text, err)
	}
	// Reading past the end of a file is not a failure to read it.
	m = &model{replies: []string{call("read_file", map[string]any{"path": "a.txt", "offset": 99}), stop("No findings.")}}
	if _, text, err := review(t, root, tree, m, withBase); err != nil || text != "No findings." {
		t.Fatalf("%q %v", text, err)
	}
	// Without a base, as for an unchanged file, a failed read is the model's to handle.
	m = &model{replies: []string{call("read_file", map[string]any{"path": "bin.dat"}), stop("No findings.")}}
	if _, text, err := review(t, root, tree, m, nil); err != nil || text != "No findings." {
		t.Fatalf("%q %v", text, err)
	}
}

func TestReadKeepsWhitespaceInPaths(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init", "-q")
	// The tree is built with plumbing: Windows cannot hold a file name that
	// ends in a space, but a tree can.
	blob := func(name, content string) string {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		// No line-ending conversion: its warning would join the object id.
		return git(t, root, "-c", "core.autocrlf=false", "-c", "core.safecrlf=false", "hash-object", "-w", name)
	}
	git(t, root, "update-index", "--add", "--cacheinfo", "100644,"+blob("plain.txt", "plain\n")+",x.go")
	// Git for Windows refuses such a path in its index unless protectNTFS is off.
	git(t, root, "-c", "core.protectNTFS=false", "update-index", "--add", "--cacheinfo", "100644,"+blob("spaced.txt", "with a space\n")+",x.go ")
	s, err := loadSnapshot(context.Background(), root, git(t, root, "write-tree"), "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.call(context.Background(), "read_file", `{"path":"x.go "}`)
	if strings.Contains(got, "plain") || err == nil && !strings.Contains(got, "with a space") {
		t.Fatalf("%q %v: read another file's contents", got, err)
	}
	if err != nil {
		t.Fatalf("%v", err)
	}
}

func TestSendBudgetBoundsTheResentConversation(t *testing.T) {
	root, tree, _, _ := repoTree(t)
	replies := make([]string, 10)
	for i := range replies {
		replies[i] = call("list_files", nil)
	}
	m := &model{replies: replies}
	// Room for a few requests, each resending the listings before it.
	_, text, err := review(t, root, tree, m, func(c *Config) { c.SendBudget = 8 << 10 })
	if err == nil || !IsLimitError(err.Error()) || !strings.Contains(err.Error(), "sent to the model") || text != "" {
		t.Fatalf("%q %v", text, err)
	}
	if n := len(m.got()); n == 0 || n >= len(replies) {
		t.Fatalf("%d requests sent", n)
	}
}

func TestFailedToolRepliesCountAgainstTheReadBudget(t *testing.T) {
	root, tree, _, _ := repoTree(t)
	m := &model{replies: []string{call("read_file", map[string]any{"path": strings.Repeat("x", 4096)}), stop("No findings.")}}
	_, text, err := review(t, root, tree, m, func(c *Config) { c.ReadBudget = 1024 })
	if err == nil || !IsLimitError(err.Error()) || text != "" {
		t.Fatalf("%q %v", text, err)
	}
}

func TestALongLineCanBeReadToItsEnd(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init", "-q")
	// Multi-byte runes, so a piece that split one would corrupt the text.
	long := strings.Repeat("é", maxReadLine*2) + "END-OF-LINE"
	if err := os.WriteFile(filepath.Join(root, "long.txt"), []byte("first\n"+long+"\nlast\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "-A")
	s, err := loadSnapshot(context.Background(), root, git(t, root, "write-tree"), "")
	if err != nil {
		t.Fatal(err)
	}
	var got strings.Builder
	column := 1
	for range 20 {
		out, err := s.read(context.Background(), "long.txt", 2, 1, column)
		if err != nil {
			t.Fatal(err)
		}
		text, more, found := strings.Cut(out, "\n[line 2 continues; read it with offset 2 and column ")
		got.WriteString(strings.TrimSuffix(strings.TrimPrefix(text, "2\t"), "\n"))
		if !found {
			break
		}
		digits, _, _ := strings.Cut(more, "]")
		column, err = strconv.Atoi(digits)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !strings.HasPrefix(got.String(), long) {
		t.Fatalf("pages do not rebuild the line (%d bytes, want %d)", got.Len(), len(long))
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	var hit sync.Mutex
	followed := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hit.Lock()
		followed = true
		hit.Unlock()
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	root, tree, _, _ := repoTree(t)
	_, _, err := review(t, root, tree, &model{}, func(c *Config) { c.Provider.BaseURL = redirect.URL })
	hit.Lock()
	defer hit.Unlock()
	if err == nil || followed {
		t.Fatalf("err=%v followed=%v", err, followed)
	}
}

func TestErrorsAreClassifiedAndNeverCarryTheKey(t *testing.T) {
	root, tree, _, _ := repoTree(t)
	for status, want := range map[int]string{401: "authentication required", 402: "payment required", 429: "rate limit", 503: "service unavailable"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"error":{"code":1,"message":"bad key `+testKey+`"}}`)
		}))
		_, _, err := review(t, root, tree, &model{}, func(c *Config) { c.Provider.BaseURL = server.URL })
		server.Close()
		if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), testKey) {
			t.Fatalf("status %d: %v", status, err)
		}
	}
}

func TestAKeyCutByTheErrorLimitIsStillRemoved(t *testing.T) {
	root, tree, _, _ := repoTree(t)
	// The key straddles the point where error text is shortened.
	// The key is written plainly, then with a JSON escape that decoding turns
	// back into the key.
	for _, key := range []string{testKey, strings.Replace(testKey, "sk-t", `sk-\u0074`, 1)} {
		message := strings.Repeat("x", maxErrorText-len(testKey)/2) + key
		for name, status := range map[string]int{"failed status": http.StatusBadRequest, "error in 200": http.StatusOK} {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":{"code":400,"message":"`+message+`"}}`)
			}))
			_, _, err := review(t, root, tree, &model{}, func(c *Config) { c.Provider.BaseURL = server.URL })
			server.Close()
			if err == nil || strings.Contains(err.Error(), testKey[:len(testKey)/2]) {
				t.Fatalf("%s, %s: %v", key, name, err)
			}
		}
	}
}

func TestAnEscapedKeyIsRemovedFromEveryErrorShape(t *testing.T) {
	root, tree, _, _ := repoTree(t)
	escaped := strings.Replace(testKey, "sk-t", `sk-\u0074`, 1)
	for name, body := range map[string]string{
		"string error":      `{"error":"bad key ` + escaped + `"}`,
		"error without msg": `{"error":{"code":400,"metadata":{"key":"` + escaped + `"}}}`,
		"no error field":    `{"detail":"bad key ` + escaped + `"}`,
	} {
		for _, status := range []int{http.StatusBadRequest, http.StatusOK} {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, body)
			}))
			_, _, err := review(t, root, tree, &model{}, func(c *Config) { c.Provider.BaseURL = server.URL })
			server.Close()
			if err == nil || strings.Contains(err.Error(), testKey[:len(testKey)/2]) || strings.Contains(err.Error(), escaped[:len(escaped)/2]) {
				t.Fatalf("%s, status %d: %v", name, status, err)
			}
		}
	}
}

func TestAReplyNeverCarriesTheKey(t *testing.T) {
	root, tree, _, _ := repoTree(t)
	escaped := strings.Replace(testKey, "sk-t", `sk-\u0074`, 1)
	m := &model{replies: []string{`{"choices":[{"message":{"role":"assistant","content":"- [P2] the key is ` + escaped + `"},"finish_reason":"stop"}]}`}}
	_, text, err := review(t, root, tree, m, nil)
	if err != nil || strings.Contains(text, testKey) || !strings.Contains(text, "[api key]") {
		t.Fatalf("%q %v", text, err)
	}
}

func TestForbiddenIsNotALoginFailure(t *testing.T) {
	root, tree, _, _ := repoTree(t)
	for detail, want := range map[string]string{
		"Workspace budget exceeded":   "usage limit",
		"Key limit exceeded":          "usage limit",
		"Input flagged by moderation": "refused the request",
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":{"code":403,"message":"`+detail+`"}}`)
		}))
		_, _, err := review(t, root, tree, &model{}, func(c *Config) { c.Provider.BaseURL = server.URL })
		server.Close()
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), detail) || strings.Contains(err.Error(), "authentication required") {
			t.Fatalf("%s: %v", detail, err)
		}
	}
}

func TestMissingKeyFailsAtSessionStart(t *testing.T) {
	root, tree, _, _ := repoTree(t)
	m := &model{}
	_, _, err := review(t, root, tree, m, func(c *Config) { c.Key = "" })
	if err == nil || !strings.Contains(err.Error(), "OPENROUTER_API_KEY") {
		t.Fatalf("%v", err)
	}
	if len(m.got()) != 0 {
		t.Fatal("sent a request with no key")
	}
}

func TestCheckOpenRouterModel(t *testing.T) {
	for model, ok := range map[string]bool{
		"anthropic/claude-sonnet-4.5": true,
		"deepseek/deepseek-r1:free":   true,
		"vendor/model:nitro":          true,
		"vendor/model:free:floor":     true,
		"vendor/model:online":         false,
		"vendor/model:batch":          false,
		"~vendor/model-latest":        false,
		"vendor/model:free:online":    false,
		"openrouter/auto":             false,
		"openrouter/free":             false,
		"@preset/mine":                false,
		"model-without-vendor":        false,
		"vendor/model --flag":         false,
		"":                            false,
	} {
		if err := checkOpenRouterModel(model); (err == nil) != ok {
			t.Errorf("%q: %v", model, err)
		}
	}
}

func TestMainRemovesTheKeyFromTheEnvironment(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", testKey)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Main(ctx, "openrouter", "vendor/model", "", "", strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, ok := os.LookupEnv("OPENROUTER_API_KEY"); ok {
		t.Fatal("child git commands would inherit the key")
	}
	for _, bad := range [][2]string{{"nope", "vendor/model"}, {"openrouter", "openrouter/auto"}} {
		if err := Main(ctx, bad[0], bad[1], "", "", strings.NewReader(""), io.Discard); err == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
	for _, ids := range [][2]string{{"--output=/tmp/x", ""}, {"", "--output=/tmp/x"}} {
		if err := Main(ctx, "openrouter", "vendor/model", ids[0], ids[1], strings.NewReader(""), io.Discard); err == nil {
			t.Fatalf("%v: a tree that is not an object id was accepted", ids)
		}
	}
}

func TestReadDiffModifiedDeletedAndBinary(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = root
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git: %s: %v", b, e)
		}
		return strings.TrimSpace(string(b))
	}
	git("init")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.com")
	write := func(p, s string) {
		t.Helper()
		if e := os.WriteFile(filepath.Join(root, p), []byte(s), 0600); e != nil {
			t.Fatal(e)
		}
	}
	write("edit", "before\n")
	write("delete", "removed\n")
	write("binary", "\x00before readable text\n")
	git("add", ".")
	git("commit", "-m", "base")
	base := git("rev-parse", "HEAD^{tree}")
	write("edit", "after\n")
	os.Remove(filepath.Join(root, "delete"))
	write("binary", "\x00after readable text\n")
	git("add", "-A")
	tree := git("write-tree")
	s, e := loadSnapshot(context.Background(), root, tree, base)
	if e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{"edit", "delete", "binary"} {
		args, _ := json.Marshal(map[string]any{"path": path})
		out, e := s.call(context.Background(), "read_diff", string(args))
		if e != nil || !strings.Contains(out, "diff --git") {
			t.Fatalf("%s: %s %v", path, out, e)
		}
	}
	out, e := s.call(context.Background(), "read_diff", `{"path":"edit","offset":2,"limit":1}`)
	if e != nil || !strings.HasPrefix(out, "2\t") || !strings.Contains(out, "continue with offset 3") {
		t.Fatalf("pagination: %s %v", out, e)
	}
}

func TestReadDiffRecoversFailedBinaryRead(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init")
	git(t, root, "commit", "--allow-empty", "-m", "base")
	base := git(t, root, "rev-parse", "HEAD^{tree}")
	if err := os.WriteFile(filepath.Join(root, "binary"), []byte("\x00readable script\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", ".")
	tree := git(t, root, "write-tree")
	m := &model{replies: []string{
		call("read_file", map[string]any{"path": "binary"}),
		call("read_diff", map[string]any{"path": "./binary"}),
		stop("No findings."),
	}}
	_, text, err := review(t, root, tree, m, func(c *Config) { c.Base = base })
	if err != nil || !strings.Contains(text, "No findings.") {
		t.Fatalf("verdict: %s %v", text, err)
	}
}

func TestListChanges(t *testing.T) {
	root, base, _, _ := repoTree(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("replacement\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "dir/b.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dir/new.txt"), []byte("added\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "-A")
	s, err := loadSnapshot(context.Background(), root, git(t, root, "write-tree"), base)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args, want string
		absent     string
	}{
		{`{}`, "1\t1\t2\ta.txt\n2\t0\t1\tdir/b.txt\n3\t1\t0\tdir/new.txt\n", "bin.dat"},
		{`{"prefix":"./dir/"}`, "1\t0\t1\tdir/b.txt\n2\t1\t0\tdir/new.txt\n", "a.txt"},
		{`{"offset":2,"limit":1}`, "2\t0\t1\tdir/b.txt\n[2 of 3 lines shown; continue with offset 3]\n", "dir/new.txt"},
		{`{"prefix":"dir/","offset":2,"limit":1}`, "2\t1\t0\tdir/new.txt\n", "dir/b.txt"},
		{`{"prefix":"missing"}`, "(no matching changed files)", "a.txt"},
	} {
		out, err := s.call(context.Background(), "list_changes", tc.args)
		if err != nil || out != tc.want || strings.Contains(out, tc.absent) {
			t.Errorf("%s: %q, %v; want %q", tc.args, out, err, tc.want)
		}
	}
	if _, err := s.call(context.Background(), "list_changes", `{"offset":4}`); !errors.Is(err, errPastEnd) {
		t.Fatalf("past end: %v", err)
	}
	s.base = ""
	if out, err := s.call(context.Background(), "list_changes", `{}`); err != nil || out != "(no base tree recorded)" {
		t.Fatalf("%q: %v", out, err)
	}
}

func TestOversizedDeletedFileDiffFailureRejectsVerdict(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.name", "t"}, {"config", "user.email", "t@t"}} {
		if _, err := gitx.Run(root, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "huge.txt"), []byte(strings.Repeat("deleted line of code\n", 60000)), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", "base"}} {
		if _, err := gitx.Run(root, args...); err != nil {
			t.Fatal(err)
		}
	}
	base, err := gitx.Run(root, "rev-parse", "HEAD^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "huge.txt")); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", "delete huge"}} {
		if _, err := gitx.Run(root, args...); err != nil {
			t.Fatal(err)
		}
	}
	tree, err := gitx.Run(root, "rev-parse", "HEAD^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	s, err := loadSnapshot(context.Background(), root, tree, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.call(context.Background(), "read_diff", `{"path":"huge.txt"}`); err == nil {
		t.Fatal("expected oversized deleted file diff to fail")
	}
	if !s.unread["huge.txt"] {
		t.Fatalf("expected huge.txt to be marked unread after read_diff failure: %+v", s.unread)
	}
}
