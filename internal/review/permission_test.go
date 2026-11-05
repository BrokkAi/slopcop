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
		{"execute", acp.ToolKindExecute, "allow", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var log bytes.Buffer
			c := &reviewClient{stderr: &log}
			got, err := c.RequestPermission(context.Background(), acp.RequestPermissionRequest{ToolCall: acp.ToolCallUpdate{Kind: &tc.kind}, Options: []acp.PermissionOption{{Kind: acp.PermissionOptionKindAllowOnce, OptionId: "allow"}, {Kind: acp.PermissionOptionKindRejectOnce, OptionId: "reject"}}})
			if err != nil || got.Outcome.Selected == nil || got.Outcome.Selected.OptionId != tc.want {
				t.Fatalf("outcome=%+v err=%v", got, err)
			}
			if (c.deniedEdit != "") != tc.denied || (strings.Contains(log.String(), "requested")) != tc.denied {
				t.Fatalf("note=%q log=%q", c.deniedEdit, log.String())
			}
		})
	}
	kind := acp.ToolKindEdit
	c := &reviewClient{}
	got, err := c.RequestPermission(context.Background(), acp.RequestPermissionRequest{ToolCall: acp.ToolCallUpdate{Kind: &kind}})
	if err != nil || got.Outcome.Cancelled == nil {
		t.Fatalf("missing reject: %+v %v", got, err)
	}
}
