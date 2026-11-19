package review

import (
	"context"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

// TestReviewClientKeepsLastMessage checks that a reviewer's text is its last
// message after its last tool call, so a progress note before a final
// "No findings." does not turn a clean review into findings, and that a tool
// update inside the final message does not cut it to its tail.
func TestReviewClientKeepsLastMessage(t *testing.T) {
	msg := func(id, text string) acp.SessionUpdate {
		u := acp.UpdateAgentMessageText(text)
		if id != "" {
			u.AgentMessageChunk.MessageId = &id
		}
		return u
	}
	thought := acp.UpdateAgentThoughtText("Review is complete")
	tool := acp.StartToolCall("call", "read")
	toolDone := acp.UpdateToolCall("call", acp.WithUpdateStatus(acp.ToolCallStatusCompleted))
	for _, tc := range []struct {
		name    string
		updates []acp.SessionUpdate
		want    string
	}{
		{"named messages", []acp.SessionUpdate{msg("a", "I'm checking the markers.\n"), thought, msg("b", "No "), msg("b", "findings.")}, CleanReply},
		{"named message across a thought", []acp.SessionUpdate{msg("a", "- [P2] Bug "), thought, msg("a", "in x.go")}, "- [P2] Bug in x.go"},
		// Without ids there is no telling a new message from a thought inside
		// one reply, so the text stays whole and the review is not clean.
		{"unnamed chunks across a thought", []acp.SessionUpdate{msg("", "- [P2] Bug in x.go\n"), thought, msg("", "No findings.")}, "- [P2] Bug in x.go\nNo findings."},
		{"unnamed chunk inside a named message", []acp.SessionUpdate{msg("a", "- [P2] Bug "), msg("", "in "), msg("a", "x.go")}, "- [P2] Bug in x.go"},
		{"tool call", []acp.SessionUpdate{msg("a", "Reading x.go.\n"), tool, msg("b", "No findings.")}, CleanReply},
		{"unnamed chunks across a tool call", []acp.SessionUpdate{msg("", "Reading x.go.\n"), tool, msg("", "No findings.")}, CleanReply},
		// Codex streams updates for earlier calls into its final reply; the
		// reply goes on under its own id and must stay whole (issue 177).
		{"named message across a tool update", []acp.SessionUpdate{msg("a", "Reading.\n"), tool, msg("b", "- [P2] Bug `x.go:3` breaks the"), toolDone, msg("b", " cancellation contract.")}, "- [P2] Bug `x.go:3` breaks the cancellation contract."},
		{"tool update after the reply", []acp.SessionUpdate{msg("a", "No findings."), toolDone}, CleanReply},
		// A tool call with no message after it leaves the turn without a
		// final reply, so the text before it is not the verdict.
		{"tool call after the text", []acp.SessionUpdate{msg("a", "No findings."), tool}, ""},
		{"tool call after the text, then its update", []acp.SessionUpdate{msg("a", "No findings."), tool, toolDone}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &reviewClient{}
			for _, u := range tc.updates {
				if err := c.SessionUpdate(context.Background(), acp.SessionNotification{SessionId: "s", Update: u}); err != nil {
					t.Fatal(err)
				}
			}
			if got := c.reply(); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
