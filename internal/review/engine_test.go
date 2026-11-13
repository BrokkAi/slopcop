package review

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/BrokkAi/slopcop/internal/gitx"
)

func reviewRepo(t *testing.T) (string, Capture, *Store) {
	t.Helper()
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		v, err := gitx.Run(root, args...)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	git("init")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "base")
	base := git("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(root, "change.txt"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "change.txt")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "change")
	capture, err := NewCapture(root, base, git("rev-parse", "HEAD^{tree}"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	t.Setenv("SLOPCOP_FAKE_ACP", "1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CODEX_THREAD_ID", "")
	return root, capture, store
}
func fakeAdapter(mode, model, trace string) []string {
	return []string{os.Args[0], "-test.run=^TestFakeAgent$", "--", "--fake-mode=" + mode, "--fake-model=" + model, "--fake-trace=" + trace}
}
func TestConfiguredReviewersNoFallback(t *testing.T) {
	root, c, store := reviewRepo(t)
	trace := filepath.Join(t.TempDir(), "trace")
	o := Options{Root: root, Settings: Settings{Reviewers: []Family{Terra, Flash}, Adapters: map[string][]string{"codex": fakeAdapter("clean", "gpt-6-terra", trace), "dsh": fakeAdapter("new-error", "deepseek-flash", trace)}}}
	v := Run(context.Background(), store, c, o)
	if v.Status != StatusFailed || len(v.Reviewers) != 2 || v.Reviewers[1].Status != StatusFailed {
		t.Fatalf("%+v", v)
	}
	data, _ := os.ReadFile(trace)
	if strings.Contains(string(data), "prompt") {
		t.Fatal(string(data))
	}
}
func TestAuthFailureKeepsPartial(t *testing.T) {
	root, c, store := reviewRepo(t)
	o := Options{Root: root, Settings: Settings{Reviewers: []Family{Terra, Flash}, Adapters: map[string][]string{"codex": fakeAdapter("findings", "gpt-6-terra", ""), "dsh": fakeAdapter("auth-required", "deepseek-flash", "")}}}
	v := Run(context.Background(), store, c, o)
	if v.Status != StatusFailed || len(v.Reviewers) != 2 || !strings.Contains(v.Reviewers[0].Text, "fake defect") || v.Reviewers[1].Status != StatusFailed {
		t.Fatalf("%+v", v)
	}
}
func TestEmptyReviewers(t *testing.T) {
	root, c, store := reviewRepo(t)
	v := Run(context.Background(), store, c, Options{Root: root})
	if v.Status != StatusFailed || !strings.Contains(v.Error, "slopcop init") {
		t.Fatalf("%+v", v)
	}
}
func TestNoEchoAndNoAllow(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want Status
	}{{"no-echo", StatusClean}, {"no-allow", StatusFailed}} {
		t.Run(tc.mode, func(t *testing.T) {
			root, c, s := reviewRepo(t)
			o := Options{Root: root, Intent: "request", Settings: Settings{Reviewers: []Family{Terra}, Adapters: map[string][]string{"codex": fakeAdapter(tc.mode, "gpt-6.10-terra", "")}}}
			v := Run(context.Background(), s, c, o)
			if v.Status != tc.want {
				t.Fatalf("%+v", v)
			}
		})
	}
}

// TestReviewerWithoutRestrictiveModeFails checks that a reviewer whose adapter
// offers no read-only mode fails rather than running in the adapter's default.
func TestReviewerWithoutRestrictiveModeFails(t *testing.T) {
	root, c, s := reviewRepo(t)
	trace := filepath.Join(t.TempDir(), "trace")
	o := Options{Root: root, Intent: "request", Settings: Settings{Reviewers: []Family{Terra}, Adapters: map[string][]string{"codex": fakeAdapter("no-mode", "gpt-6.10-terra", trace)}}}
	v := Run(context.Background(), s, c, o)
	if v.Status != StatusFailed || len(v.Reviewers) != 1 || v.Reviewers[0].Status != StatusFailed || !strings.Contains(v.Reviewers[0].Error, "mode=read-only") {
		t.Fatalf("%+v", v)
	}
	data, _ := os.ReadFile(trace)
	if strings.Contains(string(data), "prompt") {
		t.Fatalf("reviewer was prompted: %s", data)
	}
}

// TestAdapterStartsOutsideTheRepository checks that the adapter process runs
// in a directory the repository does not control, while its ACP session is
// still rooted in the repository.
func TestAdapterStartsOutsideTheRepository(t *testing.T) {
	root, c, s := reviewRepo(t)
	trace := filepath.Join(t.TempDir(), "trace")
	o := Options{Root: root, Intent: "request", Settings: Settings{Reviewers: []Family{Terra}, Adapters: map[string][]string{"codex": fakeAdapter("cwd", "gpt-6.10-terra", trace)}}}
	if v := Run(context.Background(), s, c, o); v.Status != StatusClean {
		t.Fatalf("%+v", v)
	}
	data, _ := os.ReadFile(trace)
	var process string
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "process-cwd="); ok {
			process = v
		}
	}
	// os.TempDir can be a symbolic link, as on macOS.
	want, _ := filepath.EvalSymlinks(os.TempDir())
	got, _ := filepath.EvalSymlinks(process)
	if process == "" || got != want || !strings.Contains(string(data), "session-cwd="+root+"\n") {
		t.Fatalf("trace: %s", data)
	}
}

// TestFailedReviewerNamesRefusedRequest checks that a failed reviewer's error
// carries the permission request slopcop refused during the turn.
func TestFailedReviewerNamesRefusedRequest(t *testing.T) {
	root, c, s := reviewRepo(t)
	o := Options{Root: root, Intent: "request", Settings: Settings{Reviewers: []Family{Terra}, Adapters: map[string][]string{"codex": fakeAdapter("denied-then-fail", "gpt-6.10-terra", "")}}}
	v := Run(context.Background(), s, c, o)
	if v.Status != StatusFailed || len(v.Reviewers) != 1 || !strings.Contains(v.Reviewers[0].Error, "reviewer requested execute permission: rm -rf .") {
		t.Fatalf("%+v", v)
	}
}
func TestReuseAndInvalidation(t *testing.T) {
	root, c, s := reviewRepo(t)
	trace := filepath.Join(t.TempDir(), "trace")
	o := Options{Root: root, Intent: "request", Settings: Settings{Reviewers: []Family{Terra}, Adapters: map[string][]string{"codex": fakeAdapter("findings", "gpt-6.10-terra", trace)}}}
	v := Run(context.Background(), s, c, o)
	if v.Status != StatusFindings {
		t.Fatal(v)
	}
	v = Run(context.Background(), s, c, o)
	if !v.Reused {
		t.Fatal("no reuse")
	}
	o.Intent = "different"
	v = Run(context.Background(), s, c, o)
	if v.Reused {
		t.Fatal("reused different intent")
	}
	o.Settings.Reviewers = []Family{Terra, Flash}
	o.Settings.Adapters["dsh"] = []string{"/missing/slopcop-fake"}
	o.Settings.Adapters["claude"] = []string{"/missing/slopcop-fake"}
	v = Run(context.Background(), s, c, o)
	if v.Status != StatusFailed || v.Reused {
		t.Fatal(v)
	}
	if err := s.Override("false positive", c.Tree); err == nil {
		t.Fatal("old findings override allowed")
	}
	b, err := os.ReadFile(filepath.Join(s.dir, "findings.json"))
	if err != nil || !strings.Contains(string(b), "fake defect") {
		t.Fatal(err)
	}
	o.Settings.Reviewers = []Family{Terra}
	o.Settings.Adapters["codex"] = fakeAdapter("clean", "gpt-6.10-terra", trace)
	v = Run(context.Background(), s, c, o)
	if v.Status != StatusClean {
		t.Fatal(v)
	}
	p, _ := os.ReadFile(filepath.Join(v.LogDir, "prompt.txt"))
	if !strings.Contains(string(p), "fake defect") {
		t.Fatal("previous findings omitted")
	}
	if _, err := os.Stat(filepath.Join(s.dir, "findings.json")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}
func TestOverrideRules(t *testing.T) {
	root, c, s := reviewRepo(t)
	r := Record{Verdict: Verdict{Status: StatusFindings, Capture: c, Reviewers: []Reviewer{{Text: "bug"}}}}
	if err := s.Save(r); err != nil {
		t.Fatal(err)
	}
	if err := s.Override(" ", c.Tree); err == nil {
		t.Fatal("empty reason")
	}
	if err := s.Override("reason", "wrong"); err == nil {
		t.Fatal("tree mismatch")
	}
	for _, tc := range []struct {
		exit    int
		status  string
		allowed bool
		blocks  bool
	}{
		{2, "incomplete", true, false},
		{0, "clean", true, false},
		{0, "skipped", true, false},
		{2, "failed", false, true},
		{3, "pending", false, true},
		{1, "findings", false, true},
		{1, "findings", true, false},
	} {
		if err := s.SetStatic(tc.exit, tc.status, tc.blocks); err != nil {
			t.Fatal(err)
		}
		err := s.Override("reason", c.Tree)
		if (err == nil) != tc.allowed {
			t.Errorf("override static %d/%s: %v", tc.exit, tc.status, err)
		}
		if !tc.allowed && (err == nil || !strings.Contains(err.Error(), "static")) {
			t.Errorf("override reason missing %s: %v", tc.status, err)
		}
	}
	if err := s.SetStatic(2, "incomplete", false); err != nil {
		t.Fatal(err)
	}
	if err := s.Override("reason", c.Tree); err != nil {
		t.Fatal(err)
	}
	ok, err := Reviewed(root, c.Tree)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	b, err := os.ReadFile(filepath.Join(s.dir, "overrides.jsonl"))
	if err != nil || !strings.Contains(string(b), "reason") || !strings.Contains(string(b), "bug") {
		t.Fatal(err)
	}
	r.Status = StatusClean
	if err := s.Save(r); err != nil {
		t.Fatal(err)
	}
	if err := s.Override("reason", c.Tree); err == nil {
		t.Fatal("clean override")
	}
	r.Status = StatusFindings
	r.Reviewers = append(r.Reviewers, Reviewer{Error: "timeout"})
	if err := s.Save(r); err != nil {
		t.Fatal(err)
	}
	if err := s.Override("reason", c.Tree); err == nil {
		t.Fatal("incomplete findings override")
	}
}
func TestBusyError(t *testing.T) {
	root, _, _ := reviewRepo(t)
	_, err := OpenStore(root)
	if !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
}
func TestTimeoutAndCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake grandchild uses sh; Windows process tree is covered by cross-OS vet")
	}
	for _, mode := range []string{"timeout", "hang"} {
		t.Run(mode, func(t *testing.T) {
			root, c, s := reviewRepo(t)
			trace := filepath.Join(t.TempDir(), "trace")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			limit := 500 * time.Millisecond
			if mode == "hang" {
				go func() { time.Sleep(150 * time.Millisecond); cancel() }()
				limit = 5 * time.Second
			}
			o := Options{Root: root, Intent: "request", Settings: Settings{Reviewers: []Family{Terra}, Timeout: limit, Adapters: map[string][]string{"codex": fakeAdapter(mode, "gpt-6.10-terra", trace)}}}
			start := time.Now()
			v := Run(ctx, s, c, o)
			if v.Status != StatusFailed || time.Since(start) > 3*time.Second {
				t.Fatalf("%+v", v)
			}
			if mode == "timeout" {
				traceData, _ := os.ReadFile(trace)
				if !strings.Contains(string(traceData), "grandchild-started") {
					t.Fatal("fake did not spawn grandchild")
				}
				time.Sleep(1100 * time.Millisecond)
				if _, err := os.Stat(trace + ".survived"); !os.IsNotExist(err) {
					t.Fatal("grandchild survived")
				}
			}
		})
	}
}

func TestCancelStopsAllAdapters(t *testing.T) {
	root, capture, store := reviewRepo(t)
	trace1 := filepath.Join(t.TempDir(), "terra")
	trace2 := filepath.Join(t.TempDir(), "flash")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts := Options{Root: root, Intent: "request", Settings: Settings{Reviewers: []Family{Terra, Flash}, Timeout: 5 * time.Second, Adapters: map[string][]string{"codex": fakeAdapter("hang", "gpt-6.10-terra", trace1), "dsh": fakeAdapter("hang", "deepseek-flash", trace2)}}}
	done := make(chan Verdict, 1)
	go func() { done <- Run(ctx, store, capture, opts) }()
	deadline := time.After(3 * time.Second)
	for {
		first, _ := os.ReadFile(trace1)
		second, _ := os.ReadFile(trace2)
		if strings.Contains(string(first), "prompt") && strings.Contains(string(second), "prompt") {
			break
		}
		select {
		case <-deadline:
			t.Fatal("both adapters did not receive prompts")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case v := <-done:
		if v.Status != StatusFailed || len(v.Reviewers) != 2 {
			t.Fatalf("%+v", v)
		}
		assertStopped(t, trace1)
		assertStopped(t, trace2)
		for _, r := range v.Reviewers {
			if !strings.Contains(r.Error, "canceled") {
				t.Fatal(r)
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop adapters promptly")
	}
}

func TestSerialInitializeParallelPrompts(t *testing.T) {
	root, capture, store := reviewRepo(t)
	trace := filepath.Join(t.TempDir(), "trace")
	opts := Options{Root: root, Intent: "request", Settings: Settings{Reviewers: []Family{Terra, Flash}, Timeout: 3 * time.Second, Adapters: map[string][]string{"codex": fakeAdapter("barrier", "gpt-6.10-terra", trace), "dsh": fakeAdapter("barrier", "deepseek-flash", trace)}}}
	v := Run(context.Background(), store, capture, opts)
	if v.Status != StatusClean {
		t.Fatalf("%+v", v)
	}
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	events := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(events) < 6 || events[1] != "initialize" || events[3] != "initialize" || events[4] != "prompt" || events[5] != "prompt" {
		t.Fatalf("initialize/prompt order: %q", events)
	}
}
