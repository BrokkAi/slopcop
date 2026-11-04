package cli

import (
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/config"
)

func TestConfigCommandGetSetUnset(t *testing.T) {
	isolateUserConfig(t)
	for _, tc := range []struct{ key, want string }{{"mode", "both"}, {"review.count", "2"}, {"review.timeout", "20m0s"}} {
		code, out, errOut := runCLI(t, "config", "get", tc.key)
		if code != 0 || strings.TrimSpace(out) != tc.want {
			t.Fatalf("get %s: code=%d out=%q err=%q", tc.key, code, out, errOut)
		}
	}
	cfg := config.Config{Token: "token"}
	if _, err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runCLI(t, "config", "set", "review.count", "4")
	if code == 0 || !strings.Contains(errOut, "invalid review.count") {
		t.Fatalf("invalid set: code=%d err=%q", code, errOut)
	}
	code, _, errOut = runCLI(t, "config", "set", "mode", "ai")
	if code != 0 {
		t.Fatalf("set: code=%d err=%q", code, errOut)
	}
	code, _, errOut = runCLI(t, "config", "set", "review.count", "1")
	if code != 0 {
		t.Fatalf("set count: code=%d err=%q", code, errOut)
	}
	code, _, errOut = runCLI(t, "config", "unset", "review.count")
	if code != 0 {
		t.Fatalf("unset: code=%d err=%q", code, errOut)
	}
	got, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != "token" || got.Review != nil || got.Mode != "ai" {
		t.Fatalf("config after unset = %+v", got)
	}
}
