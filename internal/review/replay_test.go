package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BrokkAi/slopcop/internal/gitx"
)

// codexSplitReply is a Codex stream whose final reply has a tool update for
// an earlier call in its middle, as in issue 177, plus lines that are not
// session updates.
const codexSplitReply = `{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":1}}
{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","messageId":"m1","content":{"type":"text","text":"Reading x.go."}}}}
{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"tool_call","toolCallId":"c1","title":"read","status":"in_progress"}}}
{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","messageId":"m2","content":{"type":"text","text":"- [P2] Keep the cause ` + "`x.go:3-4`" + ` It breaks the"}}}}
{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"tool_call_update","toolCallId":"c1","status":"completed"}}}
{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","messageId":"m2","content":{"type":"text","text":" cancellation contract."}}}}
not json
`

// TestReplayRebuildsReplyFromLog checks that a replay reassembles a reply a
// tool update split, and re-derives the reviewer's and the verdict's status.
func TestReplayRebuildsReplyFromLog(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "2-sol.jsonl")
	if err := os.WriteFile(log, []byte(codexSplitReply), 0600); err != nil {
		t.Fatal(err)
	}
	v := Verdict{Status: StatusFindings, Reviewers: []Reviewer{
		{Name: "1-terra", Status: StatusClean, Text: CleanReply},
		{Name: "2-sol", Status: StatusFindings, Text: "cancellation contract.", Log: log},
		{Name: "3-flash", Status: StatusClean, Text: CleanReply, Log: filepath.Join(dir, "gone.jsonl")},
	}}
	got, errs := Replay(v)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "3-flash: kept the recorded reply") {
		t.Fatalf("errors: %v", errs)
	}
	want := "- [P2] Keep the cause `x.go:3-4` It breaks the cancellation contract."
	if got.Reviewers[1].Text != want || got.Status != StatusFindings || v.Reviewers[1].Text != "cancellation contract." {
		t.Fatalf("got %+v", got)
	}
	findings := got.Findings()
	if len(findings) != 1 || findings[0].Malformed || findings[0].Path != "x.go" || *findings[0].StartLine != 3 {
		t.Fatalf("findings: %+v", findings)
	}
}

// TestReplayStatus checks that a verdict whose only findings were a cut
// reply becomes clean, or failed when it was incomplete, and that a clean
// verdict whose reviewer has no reply in its log fails.
func TestReplayStatus(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "1-terra.jsonl")
	clean := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","messageId":"m","content":{"type":"text","text":"No findings."}}}}` + "\n"
	if err := os.WriteFile(log, []byte(clean), 0600); err != nil {
		t.Fatal(err)
	}
	for _, incomplete := range []bool{false, true} {
		v := Verdict{Status: StatusFindings, Incomplete: incomplete, Reviewers: []Reviewer{{Name: "1-terra", Status: StatusFindings, Text: "findings.", Log: log}}}
		got, errs := Replay(v)
		want := StatusClean
		if incomplete {
			want = StatusFailed
		}
		if len(errs) != 0 || got.Status != want || got.Reviewers[0].Status != StatusClean {
			t.Fatalf("incomplete=%v: %+v %v", incomplete, got, errs)
		}
	}
	empty := filepath.Join(dir, "2-sol.jsonl")
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		t.Fatal(err)
	}
	v := Verdict{Status: StatusClean, Reviewers: []Reviewer{{Name: "2-sol", Status: StatusClean, Text: CleanReply, Log: empty}}}
	if got, _ := Replay(v); got.Status != StatusFailed || !got.Incomplete || got.Reviewers[0].Status != StatusFailed {
		t.Fatalf("lost reply: %+v", got)
	}
}

// TestOverrideStateReadsAnEarlierReleasesOverride checks that an override
// logged by a release that did not mark the record is shown for that exact
// review, and for no other.
func TestOverrideStateReadsAnEarlierReleasesOverride(t *testing.T) {
	root, c, s := reviewRepo(t)
	snapshot, err := gitx.CaptureWorkingTree(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Tree = snapshot.Tree
	if err = s.Save(Record{Mode: "ai", Verdict: Verdict{Status: StatusFindings, Capture: c, Time: time.Now().UTC(),
		Reviewers: []Reviewer{{Name: "1-terra", Status: StatusFindings, Text: "- [P2] bug"}}}}); err != nil {
		t.Fatal(err)
	}
	saved, err := s.Last()
	if err != nil {
		t.Fatal(err)
	}
	appendLog := func(line []byte) {
		t.Helper()
		f, err := os.OpenFile(filepath.Join(s.dir, overrideLog), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err = f.Write(append(line, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	line, err := json.Marshal(overrideLogEntry{time.Now().UTC(), " an earlier reason ", saved})
	if err != nil {
		t.Fatal(err)
	}
	appendLog(line)
	if err = MarkReviewed(root, c.Tree); err != nil {
		t.Fatal(err)
	}
	stateOf := func() *OverrideState {
		t.Helper()
		last, err := s.Last()
		if err != nil {
			t.Fatal(err)
		}
		state, err := last.OverrideState(root)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	if state := stateOf(); state == nil || state.Status != OverrideAccepted || state.Reason != "an earlier reason" {
		t.Fatalf("earlier release's override: %+v", state)
	}

	// A later check saves another record, which the logged override is not of.
	later := *saved
	later.Reused = true
	if err = s.Save(later); err != nil {
		t.Fatal(err)
	}
	if state := stateOf(); state != nil {
		t.Fatalf("later record: %+v", state)
	}

	// A torn last line is an override that did not complete.
	if err = s.Save(*saved); err != nil {
		t.Fatal(err)
	}
	appendLog([]byte(`{"time":`))
	if state := stateOf(); state != nil {
		t.Fatalf("torn log line: %+v", state)
	}

	// This release's entry is marked, so a later unmarked record equal to it
	// in every other way does not inherit it.
	if err = s.Override("this release", c.Tree); err != nil {
		t.Fatal(err)
	}
	if state := stateOf(); state == nil || state.Status != OverrideAccepted || state.Reason != "this release" {
		t.Fatalf("this release's override: %+v", state)
	}
	if err = s.Save(*saved); err != nil {
		t.Fatal(err)
	}
	if state := stateOf(); state != nil {
		t.Fatalf("after this release's override: %+v", state)
	}
}
