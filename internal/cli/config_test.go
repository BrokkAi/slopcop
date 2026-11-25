package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/config"
)

func TestConfigCommandGetSetUnset(t *testing.T) {
	isolateUserConfig(t)
	for _, tc := range []struct{ key, want string }{{"mode", "static (not set; no reviewers are configured)"}, {"review.reviewers", "(not set)"}, {"review.timeout", "20m0s"}, {"review.minCompleted", "2 (not set; at most the number of reviewers)"}, {"review.fallbacks", "(not set)"}, {"review.provider", "(not set; each family uses its own adapter)"}} {
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
	code, _, errOut = runCLI(t, "config", "set", "review.provider", "pi")
	if code == 0 || !strings.Contains(errOut, "invalid review.provider") {
		t.Fatalf("invalid provider: code=%d err=%q", code, errOut)
	}
	if code, _, errOut = runCLI(t, "config", "set", "review.provider", "copilot"); code != 0 {
		t.Fatalf("set provider: code=%d err=%q", code, errOut)
	}
	if code, out, _ := runCLI(t, "config", "get", "review.provider"); code != 0 || strings.TrimSpace(out) != "copilot" {
		t.Fatalf("get provider: %q", out)
	}
	if code, _, errOut = runCLI(t, "config", "unset", "review.provider"); code != 0 {
		t.Fatalf("unset provider: code=%d err=%q", code, errOut)
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

func TestConfigOllamaModel(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if code, out, _ := runCLI(t, "config", "get", "review.models.ollama"); code != 0 || strings.TrimSpace(out) != "(not set)" {
		t.Fatalf("get unset: %d %q", code, out)
	}
	if code, _, errOut := runCLI(t, "config", "set", "review.models.ollama", "bad name"); code == 0 || !strings.Contains(errOut, "invalid review.models.ollama") || !strings.Contains(errOut, "ollama list") {
		t.Fatalf("set invalid: %d %q", code, errOut)
	}
	if code, _, errOut := runCLI(t, "config", "set", "review.models.ollama", "qwen3-coder:30b"); code != 0 {
		t.Fatalf("set: %s", errOut)
	}
	if code, out, _ := runCLI(t, "config", "get", "review.models.ollama"); code != 0 || strings.TrimSpace(out) != "qwen3-coder:30b" {
		t.Fatalf("get: %d %q", code, out)
	}
	if code, _, errOut := runCLI(t, "config", "set", "review.reviewers", "terra,ollama"); code != 0 {
		t.Fatalf("set reviewers: %s", errOut)
	}
	if code, _, errOut := runCLI(t, "config", "unset", "review.models.ollama"); code != 0 {
		t.Fatalf("unset: %s", errOut)
	}
	if code, out, _ := runCLI(t, "config", "get", "review.models.ollama"); code != 0 || strings.TrimSpace(out) != "(not set)" {
		t.Fatalf("get after unset: %d %q", code, out)
	}
}

// TestConfigOldOllamaKeyNamesTheNewOne covers the key an unreleased build
// used: every command refuses it and names review.models.ollama.
func TestConfigOldOllamaKeyNamesTheNewOne(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	for _, args := range [][]string{
		{"config", "get", "review.ollamaModel"},
		{"config", "set", "review.ollamaModel", "qwen3-coder:30b"},
		{"config", "unset", "review.ollamaModel"},
	} {
		if code, _, errOut := runCLI(t, args...); code == 0 || !strings.Contains(errOut, "review.models.ollama") {
			t.Errorf("%v: %d %q", args, code, errOut)
		}
	}
}

// TestConfigDSHKeyFile sets the DeepSeek key file by a relative path, which
// is saved absolute and reaches the review settings, and refuses a file
// that is missing or empty.
func TestConfigDSHKeyFile(t *testing.T) {
	isolateUserConfig(t)
	dir := t.TempDir()
	t.Chdir(dir)
	if code, out, _ := runCLI(t, "config", "get", "review.dsh.apiKeyFile"); code != 0 || !strings.Contains(out, "(not set") {
		t.Fatalf("get unset: %d %q", code, out)
	}
	if code, _, errOut := runCLI(t, "config", "set", "review.dsh.apiKeyFile", "missing"); code == 0 || !strings.Contains(errOut, "invalid review.dsh.apiKeyFile") {
		t.Fatalf("set missing: %d %q", code, errOut)
	}
	if err := os.WriteFile("key", nil, 0600); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runCLI(t, "config", "set", "review.dsh.apiKeyFile", "key"); code == 0 || !strings.Contains(errOut, "empty") {
		t.Fatalf("set empty: %d %q", code, errOut)
	}
	if err := os.WriteFile("key", []byte("sk-test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runCLI(t, "config", "set", "review.dsh.apiKeyFile", "key"); code != 0 {
		t.Fatalf("set: %d %q", code, errOut)
	}
	abs, err := filepath.Abs("key")
	if err != nil {
		t.Fatal(err)
	}
	if code, out, _ := runCLI(t, "config", "get", "review.dsh.apiKeyFile"); code != 0 || strings.TrimSpace(out) != abs {
		t.Fatalf("get: %d %q, want %q", code, out, abs)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	settings, err := reviewSettings(cfg, 0)
	if err != nil || settings.DSHKeyFile != abs {
		t.Fatalf("settings = %+v %v", settings, err)
	}
	if code, _, errOut := runCLI(t, "config", "unset", "review.dsh.apiKeyFile"); code != 0 {
		t.Fatalf("unset: %d %q", code, errOut)
	}
	if cfg, err = config.Load(); err != nil || cfg.Review != nil {
		t.Fatalf("config after unset = %+v %v", cfg, err)
	}
}

// TestReviewSettingsRefusesARelativeDSHKeyFile covers a hand-edited config:
// a relative key file would depend on where the check runs.
func TestReviewSettingsRefusesARelativeDSHKeyFile(t *testing.T) {
	isolateUserConfig(t)
	cfg := config.Config{Review: &config.Review{DSH: &config.DSH{APIKeyFile: "key"}}}
	if _, err := reviewSettings(cfg, 0); err == nil || !strings.Contains(err.Error(), "review.dsh.apiKeyFile") {
		t.Fatalf("accepted a relative key file: %v", err)
	}
}
