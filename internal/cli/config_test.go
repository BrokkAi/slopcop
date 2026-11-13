package cli

import (
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/config"
)

func TestConfigCommandGetSetUnset(t *testing.T) {
	isolateUserConfig(t)
	for _, tc := range []struct{ key, want string }{{"mode", "static (not set; no reviewers are configured)"}, {"review.reviewers", "(not set)"}, {"review.timeout", "20m0s"}, {"review.minCompleted", "2 (not set; at most the number of reviewers)"}, {"review.fallbacks", "(not set)"}} {
		code, out, errOut := runCLI(t, "config", "get", tc.key)
		if code != 0 || strings.TrimSpace(out) != tc.want {
			t.Fatalf("get %s: code=%d out=%q err=%q", tc.key, code, out, errOut)
		}
	}
	cfg := config.Config{LegacyToken: "token"}
	if _, err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runCLI(t, "config", "set", "review.reviewers", "bad")
	if code == 0 || !strings.Contains(errOut, "invalid review.reviewers") {
		t.Fatalf("invalid set: code=%d err=%q", code, errOut)
	}
	code, _, errOut = runCLI(t, "config", "set", "mode", "ai")
	if code != 0 {
		t.Fatalf("set: code=%d err=%q", code, errOut)
	}
	code, _, errOut = runCLI(t, "config", "set", "review.reviewers", "terra,sol")
	if code != 0 {
		t.Fatalf("set count: code=%d err=%q", code, errOut)
	}
	code, _, errOut = runCLI(t, "config", "set", "review.minCompleted", "0")
	if code == 0 || !strings.Contains(errOut, "invalid review.minCompleted") {
		t.Fatalf("invalid minCompleted: code=%d err=%q", code, errOut)
	}
	code, _, errOut = runCLI(t, "config", "set", "review.minCompleted", "3")
	if code == 0 || !strings.Contains(errOut, "at most the number of reviewers") {
		t.Fatalf("minCompleted above reviewers: code=%d err=%q", code, errOut)
	}
	code, _, errOut = runCLI(t, "config", "set", "review.minCompleted", "2")
	if code != 0 {
		t.Fatalf("set minCompleted: code=%d err=%q", code, errOut)
	}
	if code, out, _ := runCLI(t, "config", "get", "review.minCompleted"); code != 0 || strings.TrimSpace(out) != "2" {
		t.Fatalf("get minCompleted: %q", out)
	}
	code, _, errOut = runCLI(t, "config", "set", "review.reviewers", "terra")
	if code == 0 || !strings.Contains(errOut, "review.minCompleted is 2") {
		t.Fatalf("reviewers below minCompleted: code=%d err=%q", code, errOut)
	}
	code, _, errOut = runCLI(t, "config", "unset", "review.minCompleted")
	if code != 0 {
		t.Fatalf("unset minCompleted: code=%d err=%q", code, errOut)
	}
	code, _, errOut = runCLI(t, "config", "unset", "review.reviewers")
	if code != 0 {
		t.Fatalf("unset: code=%d err=%q", code, errOut)
	}
	got, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.TokenFor(config.DefaultServiceURL) != "token" || got.Review != nil || got.Mode != "ai" {
		t.Fatalf("config after unset = %+v", got)
	}
}
