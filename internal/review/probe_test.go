package review

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestProbeAvailability(t *testing.T) {
	root, _, _ := reviewRepo(t)
	settings := Settings{Adapters: map[string][]string{
		"codex":  append(fakeAdapter("clean", "gpt-6.1-terra", ""), "--fake-models=gpt-6.1-terra,gpt-6.1-sol"),
		"dsh":    fakeAdapter("no-high", "deepseek-flash", ""),
		"claude": fakeAdapter("auth-required", "claude-4-haiku", ""),
	}}
	got := Probe(context.Background(), root, filepath.Join(t.TempDir(), "probe"), Families, settings)
	if !got[0].Available || !got[1].Available || got[2].Available || !strings.Contains(got[2].Reason, "high") || got[3].Available || !strings.Contains(got[3].Reason, "Authentication") {
		t.Fatalf("%+v", got)
	}

}

func TestProbeFailures(t *testing.T) {
	root, _, _ := reviewRepo(t)
	for _, tc := range []struct{ name, mode, model, reason string }{
		{"session", "new-error", "gpt-6-terra", "session/new"},
		{"model", "clean", "gpt-6-astra", "no model"},
		{"effort", "no-high", "gpt-6-terra", "high effort"},
		{"auth", "auth-required", "gpt-6-terra", "Authentication"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := Settings{Adapters: map[string][]string{"codex": fakeAdapter(tc.mode, tc.model, "")}}
			got := Probe(context.Background(), root, filepath.Join(t.TempDir(), "probe"), []Family{Terra}, settings)
			if len(got) != 1 || got[0].Available || !strings.Contains(got[0].Reason, tc.reason) {
				t.Fatalf("%+v", got)
			}
		})
	}
}
