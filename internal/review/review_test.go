package review

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
func TestConnectionReply(t *testing.T) {
	for text, want := range map[string]bool{
		"Codex can't reach chatgpt.com to look up your ChatGPT workspace. Check your network connection and any proxy, VPN, or firewall that blocks chatgpt.com, then try again.": true,
		"stream disconnected before completion: error sending request for url":                                                                                                    true,
		"- [P1] Retry loop ignores connection refused\nsrc/net.go:10-12\nThe dial error is dropped.":                                                                              false,
		"- [P2] Missing nil check": false,
		CleanReply:                 false,
		"":                         false,
	} {
		if got := connectionReply(text); got != want {
			t.Errorf("connectionReply(%q) = %v, want %v", text, got, want)
		}
	}
}
func TestPrompt(t *testing.T) {
	c := Capture{BaseTree: "base", Tree: "snap", Numstat: "2\t1\tx", ChangedLines: 3, MergedIn: []string{"other"}, Unattributed: "unknown"}
	p := reviewPrompt("/repo", c, intent{Source: intentNone}, "", "/patches", "codex")
	for _, want := range []string{`changed_lines="3"`, "Base tree: base", "other", "unknown", "--ignore-submodules=none", "User intent (unavailable)"} {
		if !strings.Contains(p, want) {
			t.Fatal(want)
		}
	}
	if strings.Contains(p, "Captured diff") {
		t.Fatal("inline diff")
	}
	p = reviewPrompt("/repo", c, intent{}, "", "/patches", "copilot")
	if strings.Contains(p, sandboxRules) || !strings.Contains(p, "/patches/patches.txt") {
		t.Fatal(p)
	}
}

func TestChronologicalIntent(t *testing.T) {
	messages := []string{"first request", strings.Repeat("middle", userMessagesLimit), "latest request"}
	packet, cut := userMessagesPacket(messages)
	if !cut || len(packet) > userMessagesLimit || !strings.Contains(packet, "first request") || !strings.Contains(packet, `index="3" current_outer_turn="true"`) || !strings.Contains(packet, "latest request") {
		t.Fatal("packet lost governing messages")
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
	if _, e = os.Stat(filepath.Join(s.dir, roundsFile)); e != nil {
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
	if _, e = os.Stat(filepath.Join(s.dir, roundsFile)); !os.IsNotExist(e) {
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
	for _, attribution := range []Capture{
		{MergedIn: []string{"first", "second"}},
		{MergedIn: []string{"firstsecond"}},
		{Unattributed: "unattributed commit"},
	} {
		before, err := s.Last()
		if err != nil {
			t.Fatal(err)
		}
		c.MergedIn, c.Unattributed = attribution.MergedIn, attribution.Unattributed
		v = Run(context.Background(), s, c, o)
		after, err := s.Last()
		if err != nil {
			t.Fatal(err)
		}
		if v.Reused || v.Status != StatusClean || before.ReuseKey == after.ReuseKey || before.ContentKey == after.ContentKey {
			t.Fatalf("attribution reused: %+v", v)
		}
	}
}

func TestCodexPrimaryDoesNotExportFallbackPatches(t *testing.T) {
	root, capture, store := reviewRepo(t)
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(strings.Repeat("large line\n", 900000)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.Run(root, "add", "large.txt"); err != nil {
		t.Fatal(err)
	}
	tree, err := gitx.Run(root, "write-tree")
	if err != nil {
		t.Fatal(err)
	}
	capture, err = NewCapture(root, capture.BaseTree, tree)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gitx.ExportPerFilePatches(context.Background(), root, capture.BaseTree, capture.CheckedTree(), t.TempDir(), 8<<20, 128<<20); err == nil {
		t.Fatal("fixture must exceed export limit")
	}
	settings := Settings{Reviewers: []Family{Terra}, Fallbacks: []Family{Opus}, Adapters: map[string][]string{"codex": fakeAdapter("capture-prompt", "gpt-6-terra", filepath.Join(t.TempDir(), "trace")), "claude": fakeAdapter("capture-prompt", "opus", "")}}
	v := Run(context.Background(), store, capture, Options{Root: root, Intent: "request", Settings: settings})
	if v.Status != StatusClean || len(v.Reviewers) != 1 {
		t.Fatalf("%+v", v)
	}
	// The first prompt's attempted export may fail too: start must report
	// that reviewer as failed and allow the git-capable fallback to finish.
	settings.Reviewers = []Family{Opus}
	settings.Fallbacks = []Family{Terra}
	v = Run(context.Background(), store, capture, Options{Root: root, Intent: "request", Settings: settings})
	if v.Status != StatusClean || len(v.Reviewers) != 2 || v.Reviewers[0].Status != StatusFailed {
		t.Fatalf("fallback after export failure: %+v", v)
	}
}

func TestBinaryPromptWithoutExtractedFile(t *testing.T) {
	p := reviewPrompt("/repo", Capture{Numstat: "-\t-\trun.sh\n"}, intent{}, "", "", "codex")
	if !strings.Contains(p, "diff --text") || !strings.Contains(p, "Binary files differ") {
		t.Fatal(p)
	}
}
