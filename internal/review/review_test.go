package review

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	acp "github.com/coder/acp-go-sdk"

	"github.com/BrokkAi/slopcop/internal/gitx"
	"github.com/BrokkAi/slopcop/internal/review/reviewtest"
)

func TestFakeAgent(t *testing.T) { reviewtest.Main() }
func TestVerdictAndSelection(t *testing.T) {
	if parseVerdict(Reviewer{Text: CleanReply}) != StatusClean || parseVerdict(Reviewer{Text: "bug"}) != StatusFindings || parseVerdict(Reviewer{}) != StatusFailed {
		t.Fatal("verdict")
	}
	m, ok := bestModel(Terra, []acp.SessionConfigSelectOption{{Value: "gpt-6.9-terra"}, {Value: "gpt-6.10-terra"}})
	if !ok || m != "gpt-6.10-terra" {
		t.Fatal(m)
	}
}
func TestPrompt(t *testing.T) {
	p := reviewPrompt("/repo", Capture{BaseTree: "base", Tree: "snap"}, intent{Source: "not available"}, nil, "+added line\n", Terra)
	for _, v := range []string{"snapshot of the working tree", "Base tree: base", "Snapshot tree: snap", "+added line", "git -C '/repo' diff --no-ext-diff base snap", "No findings."} {
		if !strings.Contains(p, v) {
			t.Fatal(v)
		}
	}
	for _, family := range []Family{Sol, Flash} {
		p = reviewPrompt("/repo", Capture{BaseTree: "base", Tree: "snap"}, intent{}, nil, "diff", family)
		if strings.Contains(p, "git -C") != (family == Sol) || strings.Contains(p, sandboxRules) != (family == Sol) {
			t.Fatal(family)
		}
	}
	p = reviewPrompt("/repo", Capture{}, intent{}, nil, strings.Repeat("a\n", 131073), Flash)
	if !strings.Contains(p, "Diff truncated at 256 KiB") || strings.Contains(p, strings.Repeat("a\n", 131073)) {
		t.Fatal("truncation")
	}
}

// TestIntentKeepsTheMostRecentMessages checks the intent cap and its marker.
func TestIntentKeepsTheMostRecentMessages(t *testing.T) {
	short := intent{Text: "first\n\nsecond", Source: "s"}
	if got := capIntent(short, 64); got != short {
		t.Fatalf("short intent changed: %+v", got)
	}
	if p := reviewPrompt("/repo", Capture{}, short, nil, "", Flash); strings.Contains(p, "omitted") {
		t.Fatal("untruncated intent marked")
	}
	old := strings.Repeat("o", 40)
	recent := strings.Repeat("r", 20)
	got := capIntent(intent{Text: old + "\n\n" + old + "\n\n" + recent, Source: "s"}, 50)
	if !got.Truncated || got.Text != recent {
		t.Fatalf("message boundary: %+v", got)
	}
	got = capIntent(intent{Text: old + "\n" + recent}, 30)
	if !got.Truncated || got.Text != recent {
		t.Fatalf("line boundary: %+v", got)
	}
	got = capIntent(intent{Text: strings.Repeat("é", 40)}, 25)
	if !got.Truncated || !utf8.ValidString(got.Text) || len(got.Text) > 25 {
		t.Fatalf("character boundary: %q", got.Text)
	}
	long := strings.Repeat("old message\n\n", 10000) + "the latest request"
	in := capIntent(intent{Text: long, Source: "s"}, intentLimit)
	if len(in.Text) > intentLimit || !strings.HasSuffix(in.Text, "the latest request") || !strings.HasPrefix(in.Text, "old message") {
		t.Fatalf("capped to %d bytes", len(in.Text))
	}
	p := reviewPrompt("/repo", Capture{}, in, nil, "", Flash)
	if !strings.Contains(p, "<user_intent>\n[Earlier user messages omitted; only the most recent 64 KiB are shown.]\nold message") {
		t.Fatal("truncation not marked")
	}
}

// TestTrimLogsPrunesReviewAndProbeDirectories checks that each kind of log
// directory keeps its newest twenty and that other entries are left alone.
func TestTrimLogsPrunesReviewAndProbeDirectories(t *testing.T) {
	dir := t.TempDir()
	for _, prefix := range []string{"review-", "probe-"} {
		for i := 0; i < 25; i++ {
			if err := os.Mkdir(filepath.Join(dir, fmt.Sprintf("%s20260101T%06d-x", prefix, i)), 0700); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "last.json"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	TrimLogs(dir)
	for _, prefix := range []string{"review-", "probe-"} {
		for i := 0; i < 25; i++ {
			_, err := os.Stat(filepath.Join(dir, fmt.Sprintf("%s20260101T%06d-x", prefix, i)))
			if kept := err == nil; kept != (i >= 5) {
				t.Errorf("%s%d kept=%v", prefix, i, kept)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "last.json")); err != nil {
		t.Fatal(err)
	}
}
func TestState(t *testing.T) {
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if _, e := gitx.Run(root, args...); e != nil {
			t.Fatal(e)
		}
	}
	run("init")
	run("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "init")
	s, e := OpenStore(root)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if _, e = OpenStore(root); e == nil {
		t.Fatal("lock not busy")
	}
	tree, e := gitx.Run(root, "rev-parse", "HEAD^{tree}")
	if e != nil {
		t.Fatal(e)
	}
	r := Record{Verdict: Verdict{Status: StatusFindings, Capture: Capture{Tree: tree}, Reviewers: []Reviewer{{Text: "bug"}}}}
	if e = s.Save(r); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(s.dir, "findings.json")); e != nil {
		t.Fatal(e)
	}
	if e = s.Override("reason", tree); e != nil {
		t.Fatal(e)
	}
	r.Status = StatusFailed
	if e = s.Save(r); e != nil {
		t.Fatal(e)
	}
	if e = s.Override("reason", tree); e == nil {
		t.Fatal("failed override accepted")
	}
	r.Status = StatusClean
	if e = s.Save(r); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(s.dir, "findings.json")); !os.IsNotExist(e) {
		t.Fatal(e)
	}
}
func TestRunFakeACP(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		v, e := gitx.Run(root, args...)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	git("init")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "base")
	base := git("rev-parse", "HEAD")
	if e := os.WriteFile(filepath.Join(root, "x.txt"), []byte("change"), 0600); e != nil {
		t.Fatal(e)
	}
	git("add", "x.txt")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "change")
	tree := git("rev-parse", "HEAD^{tree}")
	c, e := NewCapture(root, base, tree)
	if e != nil {
		t.Fatal(e)
	}
	s, e := OpenStore(root)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	t.Setenv("SLOPCOP_FAKE_ACP", "1")
	o := Options{Root: root, Intent: "test", Settings: Settings{Reviewers: []Family{Terra}, Adapters: map[string][]string{"codex": {os.Args[0], "-test.run=^TestFakeAgent$"}}}}
	v := Run(context.Background(), s, c, o)
	if v.Status != StatusClean {
		t.Fatalf("%+v", v)
	}
	v = Run(context.Background(), s, c, o)
	if !v.Reused || v.Status != StatusClean {
		t.Fatalf("reuse: %+v", v)
	}
}
