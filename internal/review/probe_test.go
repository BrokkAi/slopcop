package review

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProbeAvailability(t *testing.T) {
	root, _, _ := reviewRepo(t)
	settings := Settings{Adapters: map[string][]string{
		"codex":  append(fakeAdapter("clean", "gpt-6.1-terra", ""), "--fake-models=gpt-6.1-terra,gpt-6.1-sol"),
		"dsh":    fakeAdapter("no-high", "deepseek-flash", ""),
		"claude": fakeAdapter("auth-required", "claude-4-haiku", ""),
	}}
	got := Probe(context.Background(), root, filepath.Join(t.TempDir(), "probe"), Families, settings, nil)
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
			got := Probe(context.Background(), root, filepath.Join(t.TempDir(), "probe"), []Family{Terra}, settings, nil)
			if len(got) != 1 || got[0].Available || !strings.Contains(got[0].Reason, tc.reason) {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestProbeReportsEachAdapterAsItFinishes(t *testing.T) {
	root, _, _ := reviewRepo(t)
	defer func(old time.Duration) { probeTimeout = old }(probeTimeout)
	probeTimeout = 3 * time.Second
	settings := Settings{Adapters: map[string][]string{
		"codex":  fakeAdapter("hang", "gpt-6.1-terra", ""),
		"claude": fakeAdapter("clean", "claude-4-haiku", ""),
	}}
	var order []Family
	got := Probe(context.Background(), root, filepath.Join(t.TempDir(), "probe"), []Family{Terra, Haiku}, settings, func(r ProbeResult) {
		order = append(order, r.Family)
	})
	// A hung adapter must not hold up the others: haiku reports before terra
	// times out.
	if len(order) != 2 || order[0] != Haiku || order[1] != Terra {
		t.Fatalf("order %v", order)
	}
	if got[0].Family != Terra || got[0].Available || got[1].Family != Haiku || !got[1].Available {
		t.Fatalf("%+v", got)
	}
}

func TestProbeSerializesSharedAdapter(t *testing.T) {
	root, _, _ := reviewRepo(t)
	defer func(old time.Duration) { probeTimeout = old }(probeTimeout)
	probeTimeout = 2 * time.Second
	// The barrier adapter answers only once two prompts have reached it, so
	// it can succeed only if terra and sol run at the same time.
	trace := filepath.Join(t.TempDir(), "trace")
	settings := Settings{Adapters: map[string][]string{
		"codex": append(fakeAdapter("barrier", "gpt-6.1-terra", trace), "--fake-models=gpt-6.1-terra,gpt-6.1-sol"),
	}}
	got := Probe(context.Background(), root, filepath.Join(t.TempDir(), "probe"), []Family{Terra, Sol}, settings, nil)
	if got[0].Available {
		t.Fatalf("terra and sol overlapped on one adapter command: %+v", got)
	}
}

func TestProbeSkipsFlashWithoutDeepSeekKey(t *testing.T) {
	root, _, _ := reviewRepo(t)
	t.Setenv("DEEPSEEK_API_KEY", "")
	got := Probe(context.Background(), root, filepath.Join(t.TempDir(), "probe"), []Family{Flash}, Settings{}, nil)
	if len(got) != 1 || got[0].Available || !strings.Contains(got[0].Reason, "DEEPSEEK_API_KEY") {
		t.Fatalf("%+v", got)
	}
}
