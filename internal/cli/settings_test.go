package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/review"
)

func TestResolveMode(t *testing.T) {
	for _, tc := range []struct {
		name, flag string
		static, ai bool
		cfg        config.Config
		want       string
		bad        bool
	}{
		{"default", "", false, false, config.Config{}, modeBoth, false},
		{"config", "", false, false, config.Config{Mode: modeAI}, modeAI, false},
		{"flag wins", modeStatic, false, false, config.Config{Mode: modeAI}, modeStatic, false},
		{"unknown config", "", false, false, config.Config{Mode: "bad"}, "", true},
		{"conflict", "", true, true, config.Config{}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveMode(tc.flag, tc.static, tc.ai, tc.cfg)
			if (err != nil) != tc.bad || got != tc.want {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

func TestReviewSettingsValidation(t *testing.T) {
	got, err := reviewSettings(config.Config{}, 0)
	if err != nil || got.Count != review.DefaultCount || got.Timeout != review.DefaultTimeout {
		t.Fatalf("defaults = %+v, %v", got, err)
	}
	for _, cfg := range []config.Config{{Review: &config.Review{Count: 4}}, {Review: &config.Review{Timeout: "0s"}}, {Review: &config.Review{Adapters: map[string]config.Adapter{"bad": {Command: []string{"x"}}}}}} {
		if _, err := reviewSettings(cfg, 0); err == nil || !strings.Contains(err.Error(), "config") {
			t.Errorf("expected config validation error; got %v", err)
		}
	}
	got, err = reviewSettings(config.Config{Review: &config.Review{Count: 1, Timeout: "3s", Adapters: map[string]config.Adapter{"terra": {Command: []string{"runner"}}}}}, 5*time.Second)
	if err != nil || got.Timeout != 5*time.Second || got.Adapters[review.Terra][0] != "runner" {
		t.Fatalf("override = %+v, %v", got, err)
	}
}
