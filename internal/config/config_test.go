package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveLoadReviewConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/config")
	cfg := Config{Logins: map[string]Login{DefaultServiceURL: {Token: "kept"}}, Mode: "ai", Review: &Review{Reviewers: []string{"terra", "sol", "haiku"}, Timeout: "5m", Adapters: map[string]Adapter{"codex": {Command: []string{"runner", "--x"}}}}}
	if _, err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.TokenFor(DefaultServiceURL) != "kept" || got.Mode != cfg.Mode || got.Review == nil || len(got.Review.Reviewers) != 3 || got.Review.Timeout != "5m" || got.Review.Adapters["codex"].Command[1] != "--x" {
		t.Fatalf("loaded config = %+v", got)
	}
	cfg.Review = &Review{}
	path, err := Save(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["review"]; ok || !strings.Contains(string(b), `"token": "kept"`) {
		t.Fatalf("saved empty review/token incorrectly: %s", b)
	}
}

func TestOriginNormalizesAServiceURL(t *testing.T) {
	for input, want := range map[string]string{
		"":                                  DefaultServiceURL,
		"https://slopcop.com/":              "https://slopcop.com",
		"HTTPS://Staging.SlopCop.com:443/x": "https://staging.slopcop.com",
		"https://staging.slopcop.com:8443":  "https://staging.slopcop.com:8443",
		"http://127.0.0.1:9999/":            "http://127.0.0.1:9999",
		"http://localhost:80":               "http://localhost",
		"http://[::1]:8080/":                "http://[::1]:8080",
		"not a url":                         "not a url",
	} {
		if got := Origin(input); got != want {
			t.Errorf("Origin(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestLoadMigratesTheSingleLogin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/config")
	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// A login an older release stored against staging stays staging's, and
	// staging stays the default.
	write(`{"serviceUrl": "https://staging.slopcop.com/", "token": "old", "mode": "static"}`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TokenFor("https://staging.slopcop.com") != "old" || cfg.TokenFor(DefaultServiceURL) != "" {
		t.Fatalf("logins = %+v", cfg.Logins)
	}
	if cfg.DefaultService() != "https://staging.slopcop.com" || cfg.Mode != "static" {
		t.Fatalf("config = %+v", cfg)
	}
	if _, err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(saved, &fields); err != nil {
		t.Fatal(err)
	}
	if _, legacy := fields["token"]; legacy || !strings.Contains(string(saved), `"https://staging.slopcop.com"`) {
		t.Fatalf("saved = %s", saved)
	}

	// With no stored service the login was the hosted one's.
	write(`{"token": "hosted"}`)
	if cfg, err = Load(); err != nil || cfg.TokenFor(DefaultServiceURL) != "hosted" {
		t.Fatalf("config = %+v, err = %v", cfg, err)
	}

	// A login already stored per service is newer than the leftover.
	write(`{"token": "stale", "logins": {"https://slopcop.com": {"token": "fresh"}}}`)
	if cfg, err = Load(); err != nil || cfg.TokenFor(DefaultServiceURL) != "fresh" {
		t.Fatalf("config = %+v, err = %v", cfg, err)
	}
}

func TestForgetLeavesOtherLogins(t *testing.T) {
	var cfg Config
	cfg.SetToken("https://slopcop.com", "prod")
	cfg.SetToken("https://staging.slopcop.com/", "staging")
	if !cfg.Forget("https://staging.slopcop.com") || cfg.Forget("https://staging.slopcop.com") {
		t.Fatal("Forget reported the wrong thing")
	}
	if got := cfg.SignedIn(); len(got) != 1 || got[0] != "https://slopcop.com" {
		t.Fatalf("signed in = %v", got)
	}
}
