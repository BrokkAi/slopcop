//go:build copilotspike

// Live check of the Copilot reviewer path against a real Copilot CLI. Not
// part of the suite; run with
//
//	go test -tags copilotspike -run TestCopilotLive -v ./internal/review
//
// SLOPCOP_SPIKE_CMD overrides the Copilot command (space separated). It
// probes terra, sol and haiku, which costs up to one Copilot request each.
package review

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestCopilotLive(t *testing.T) {
	cmd := []string{"copilot", "--acp", "--stdio"}
	if v := strings.TrimSpace(os.Getenv("SLOPCOP_SPIKE_CMD")); v != "" {
		cmd = strings.Fields(v)
	}
	logDir, err := os.MkdirTemp("", "copilot-live-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("logs: %s", logDir)
	settings := Settings{Provider: ProviderCopilot, Adapters: map[string][]string{copilotAdapter: cmd}}
	for _, r := range Probe(context.Background(), t.TempDir(), logDir, []Family{Terra, Sol, Haiku}, settings, nil) {
		t.Logf("%s available=%v model=%q effort=%q\n  reason: %s\n  warning: %s", r.Family, r.Available, r.Model, r.Effort, r.Reason, r.Warning)
	}
}
