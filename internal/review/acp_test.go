package review

import (
	"context"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

// TestReviewClientKeepsLastMessage checks that a reviewer's text is its last
// message after its last tool call, so a progress note before a final
// "No findings." does not turn a clean review into findings.
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
		{"tool call", []acp.SessionUpdate{msg("a", "Reading x.go.\n"), tool, msg("a", "No findings.")}, CleanReply},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &reviewClient{}
			for _, u := range tc.updates {
				if err := c.SessionUpdate(context.Background(), acp.SessionNotification{SessionId: "s", Update: u}); err != nil {
					t.Fatal(err)
				}
			}
			if got := c.text.String(); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
