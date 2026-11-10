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
		"claude": fakeAdapter("auth-required", "claude-4-sonnet,claude-4-haiku", ""),
	}}
	got := Probe(context.Background(), root, filepath.Join(t.TempDir(), "probe"), Families, settings)
	for _, r := range got {
		switch r.Family {
		case Terra, Sol, Flash:
			if !r.Available {
				t.Fatalf("%+v", r)
			}
		case Sonnet, Haiku:
			if r.Available || !strings.Contains(r.Reason, "Authentication") {
				t.Fatalf("%+v", r)
			}
		case DeepSeek:
			if r.Available || !strings.Contains(r.Reason, "no model") {
				t.Fatalf("%+v", r)
			}
		}
	}

}

func TestProbeFailures(t *testing.T) {
	root, _, _ := reviewRepo(t)
	for _, tc := range []struct{ name, mode, model, reason string }{
		{"session", "new-error", "gpt-6-terra", "session/new"},
		{"model", "clean", "gpt-6-astra", "no model"},
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

func TestDefaultReviewers(t *testing.T) {
	for _, tc := range []struct {
		name      string
		available []Family
		want      string
	}{
		{"all", Families, "terra,sol"},
		{"codex", []Family{Terra, Sol}, "terra,sol"},
		{"sonnet before deepseek", []Family{DeepSeek, Sonnet, Haiku}, "sonnet,sonnet"},
		{"sonnet before lone terra", []Family{Terra, Sonnet}, "sonnet,sonnet"},
		{"deepseek before reduced", []Family{DeepSeek, Flash, Haiku}, "deepseek,deepseek"},
		{"lone terra", []Family{Terra}, "terra,terra"},
		{"lone sol", []Family{Sol}, "sol,sol"},
		{"haiku", []Family{Haiku}, "haiku,haiku"},
		{"flash", []Family{Flash}, "flash,flash"},
		{"none", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			results := []ProbeResult{{Family: Sonnet, Available: false}}
			for _, f := range tc.available {
				results = append(results, ProbeResult{Family: f, Available: true})
			}
			var names []string
			for _, f := range DefaultReviewers(results) {
				names = append(names, string(f))
			}
			if got := strings.Join(names, ","); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
