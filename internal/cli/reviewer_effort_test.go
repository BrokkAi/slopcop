package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/review"
)

func TestConfigReviewerEfforts(t *testing.T) {
	isolateUserConfig(t)
	for _, tc := range []struct{ key, value string }{
		{"review.reviewers", "terra/xhigh,sol/high"},
		{"review.fallbacks", "opus/max,flash/high"},
		{"review.reviewers", "terra/xhigh,terra/medium"},
	} {
		if code, _, stderr := runCLI(t, "config", "set", tc.key, tc.value); code != 0 {
			t.Fatal(stderr)
		}
		if code, out, stderr := runCLI(t, "config", "get", tc.key); code != 0 || strings.TrimSpace(out) != tc.value {
			t.Fatalf("%s %s", out, stderr)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	settings, err := reviewSettings(cfg, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(settings.Reviewers, []review.Family{review.Terra, review.Terra}) || !reflect.DeepEqual(settings.ReviewerEfforts, []string{"xhigh", "medium"}) || !reflect.DeepEqual(settings.FallbackEfforts, []string{"max", "high"}) {
		t.Fatalf("%+v", settings)
	}
	if got := reviewerNames(settings.Reviewers, settings.ReviewerEfforts); got != "terra/xhigh, terra/medium" {
		t.Fatal(got)
	}
	if got := reviewerNames([]review.Family{review.Terra, review.Sol}, []string{"xhigh"}); got != "terra/xhigh, sol" {
		t.Fatal(got)
	}
}
