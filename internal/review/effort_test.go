package review

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestReviewerSpecs(t *testing.T) {
	for _, effort := range append([]string{""}, allowedEfforts...) {
		spec := FormatReviewerSpec(Terra, effort)
		f, e, err := ParseReviewerSpec(spec)
		if err != nil || f != Terra || e != effort {
			t.Fatalf("%q: %s %q %v", spec, f, e, err)
		}
	}
	for _, spec := range []string{"terra/", "terra/auto", "terra/high/low", "bad/high", "/high"} {
		if _, _, err := ParseReviewerSpec(spec); err == nil {
			t.Errorf("accepted %q", spec)
		}
	}
}

func TestEffortCacheIdentity(t *testing.T) {
	families := []Family{Terra, Terra, OpenRouter, Ollama}
	s := Settings{}
	a := effortKeys(families, nil, s)
	b := effortKeys(families, []string{"high", "high"}, s)
	if !slices.Equal(a, b) {
		t.Fatalf("%v != %v", a, b)
	}
	c := effortKeys(families, []string{"xhigh", "medium", "high", "high"}, s)
	if slices.Equal(a, c) || c[0] != "xhigh" || c[1] != "medium" || a[2] != "" || a[3] != "" {
		t.Fatalf("%v / %v", a, c)
	}
}

func TestKimiEffortConfig(t *testing.T) {
	for _, effort := range []string{"low", "high", "max", "none"} {
		cfg := kimiConfig("https://example.test", "/workspace", effort)
		if effort == "none" {
			if !strings.Contains(cfg, "enabled = false") {
				t.Fatal(cfg)
			}
		} else if !strings.Contains(cfg, "default_effort = \""+effort+"\"") || !strings.Contains(cfg, "\neffort = \""+effort+"\"") {
			t.Fatal(cfg)
		}
	}
}

func TestUnsupportedEffortsDoNotRetry(t *testing.T) {
	ctx := context.Background()
	_, err := launch(ctx, "", reviewTrees{}, ollamaAdapter, nil, nil, "", Ollama, "test", "", time.Second, "high")
	if !errors.Is(err, errEffort) || retryable(ctx, err) || !strings.Contains(err.Error(), "ollama does not support") {
		t.Fatal(err)
	}
	_, err = startKimi(ctx, "", reviewTrees{}, nil, Kimi, "test", "", time.Second, "medium")
	if !errors.Is(err, errEffort) || retryable(ctx, err) {
		t.Fatal(err)
	}
	_, err = startCopilotModel(ctx, "", nil, Terra, copilotAuto, true, "test", "", time.Second, "high")
	if !errors.Is(err, errEffort) || retryable(ctx, err) {
		t.Fatal(err)
	}
}

func TestFallbackEffortFollowsReorderedSlot(t *testing.T) {
	p := &panel{min: 2, fallbacks: []Family{Terra, Flash, Terra}, fallbackEfforts: []string{"xhigh", "low", "medium"}}
	f, n, ok := p.takeFallback(Terra)
	if !ok || f != Flash || n != 1 || p.efforts["fallback1-flash"] != "low" {
		t.Fatalf("%s %d %v %+v", f, n, ok, p)
	}
	f, n, ok = p.takeFallback(Flash)
	if !ok || f != Terra || n != 2 || p.efforts["fallback2-terra"] != "xhigh" {
		t.Fatalf("%s %d %v %+v", f, n, ok, p)
	}
}

func TestACPExplicitEffortAndDuplicateSlots(t *testing.T) {
	root, capture, store := reviewRepo(t)
	settings := Settings{Reviewers: []Family{Terra, Terra}, ReviewerEfforts: []string{"xhigh", "medium"}, Adapters: map[string][]string{"codex": fakeAdapter("efforts", "gpt-6-terra", "")}}
	opts := Options{Root: root, Settings: settings}
	v := Run(context.Background(), store, capture, opts)
	if v.Status != StatusClean || len(v.Reviewers) != 2 || v.Reviewers[0].Effort != "xhigh" || v.Reviewers[1].Effort != "medium" {
		t.Fatalf("%+v", v)
	}
	if v = Run(context.Background(), store, capture, opts); !v.Reused {
		t.Fatal("identical effort not reused")
	}
	opts.Settings.ReviewerEfforts[0] = "high"
	if v = Run(context.Background(), store, capture, opts); v.Reused || v.Status != StatusClean {
		t.Fatalf("%+v", v)
	}
	opts.Settings.ReviewerEfforts[0] = "max"
	v = Run(context.Background(), store, capture, opts)
	if v.Reviewers[0].Status != StatusFailed || v.Reviewers[0].FirstError != "" || !strings.Contains(v.Reviewers[0].Error, "max effort unavailable") {
		t.Fatalf("%+v", v)
	}
}

func TestCopilotExplicitRefusalDoesNotDowngrade(t *testing.T) {
	root, _, _ := reviewRepo(t)
	s, err := startCopilotModel(context.Background(), root, copilotFake("copilot-no-effort", "claude-haiku-4.5", ""), Haiku, "claude-haiku-4.5", false, "test", t.TempDir(), time.Second*10, "max")
	if err == nil && s != nil {
		defer s.stop()
	}
	if !errors.Is(err, errEffort) || retryable(context.Background(), err) {
		t.Fatal(err)
	}
}
