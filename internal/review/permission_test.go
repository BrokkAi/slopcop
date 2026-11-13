package review

import (
	"bytes"
	"context"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

func TestRequestPermission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kind   acp.ToolKind
		want   acp.PermissionOptionId
		denied bool
	}{
		{"edit", acp.ToolKindEdit, "reject", true},
		{"delete", acp.ToolKindDelete, "reject", true},
		{"move", acp.ToolKindMove, "reject", true},
		{"execute", acp.ToolKindExecute, "reject", true},
		{"fetch", acp.ToolKindFetch, "reject", true},
		{"other", acp.ToolKindOther, "reject", true},
		{"read", acp.ToolKindRead, "allow", false},
		{"search", acp.ToolKindSearch, "allow", false},
		{"think", acp.ToolKindThink, "allow", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var log bytes.Buffer
			c := &reviewClient{stderr: &log}
			title := "example tool"
			got, err := c.RequestPermission(context.Background(), acp.RequestPermissionRequest{ToolCall: acp.ToolCallUpdate{Kind: &tc.kind, Title: &title}, Options: []acp.PermissionOption{{Kind: acp.PermissionOptionKindAllowOnce, OptionId: "allow"}, {Kind: acp.PermissionOptionKindRejectOnce, OptionId: "reject"}}})
			if err != nil || got.Outcome.Selected == nil || got.Outcome.Selected.OptionId != tc.want {
				t.Fatalf("outcome=%+v err=%v", got, err)
			}
			if (c.deniedEdit != "") != tc.denied || (strings.Contains(log.String(), "requested")) != tc.denied {
				t.Fatalf("note=%q log=%q", c.deniedEdit, log.String())
			}
			if tc.denied && (!strings.Contains(log.String(), string(tc.kind)) || !strings.Contains(log.String(), title)) {
				t.Fatalf("refusal log missing kind or title: %q", log.String())
			}
		})
	}
	var log bytes.Buffer
	c := &reviewClient{stderr: &log}
	got, err := c.RequestPermission(context.Background(), acp.RequestPermissionRequest{ToolCall: acp.ToolCallUpdate{}, Options: []acp.PermissionOption{{Kind: acp.PermissionOptionKindAllowOnce, OptionId: "allow"}, {Kind: acp.PermissionOptionKindRejectOnce, OptionId: "reject"}}})
	if err != nil || got.Outcome.Selected == nil || got.Outcome.Selected.OptionId != "reject" || !strings.Contains(log.String(), "missing") {
		t.Fatalf("missing kind: %+v %v %q", got, err, log.String())
	}
	// Codex's "decline" refuses the command and lets the turn go on, where
	// its "cancel" ends the turn.
	execute := acp.ToolKindExecute
	title := "Run command"
	c = &reviewClient{}
	got, err = c.RequestPermission(context.Background(), acp.RequestPermissionRequest{ToolCall: acp.ToolCallUpdate{Kind: &execute, Title: &title, RawInput: map[string]any{"command": "go test ./..."}}, Options: []acp.PermissionOption{{Kind: acp.PermissionOptionKindAllowOnce, OptionId: "allow_once"}, {Kind: acp.PermissionOptionKindRejectOnce, OptionId: "cancel"}, {Kind: acp.PermissionOptionKindRejectOnce, OptionId: "decline"}}})
	if err != nil || got.Outcome.Selected == nil || got.Outcome.Selected.OptionId != "decline" || c.deniedEdit != `reviewer requested execute permission: Run command ("go test ./...")` {
		t.Fatalf("decline: %+v %v %q", got, err, c.deniedEdit)
	}
	kind := acp.ToolKindEdit
	c = &reviewClient{}
	got, err = c.RequestPermission(context.Background(), acp.RequestPermissionRequest{ToolCall: acp.ToolCallUpdate{Kind: &kind}})
	if err != nil || got.Outcome.Cancelled == nil {
		t.Fatalf("missing reject: %+v %v", got, err)
	}
}
