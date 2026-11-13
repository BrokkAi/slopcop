package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/report"
	"github.com/BrokkAi/slopcop/internal/review"
)

func TestResolveMode(t *testing.T) {
	reviewers := &config.Review{Reviewers: []string{"terra", "sol"}}
	noEnv := func(string) string { return "" }
	ci := func(name string) string {
		if name == "CI" {
			return "true"
		}
		return ""
	}
	for _, tc := range []struct {
		name, flag     string
		static, ai     bool
		cfg            config.Config
		getenv         func(string) string
		want, fallback string
		bad            bool
	}{
		{"default with reviewers", "", false, false, config.Config{Review: reviewers}, noEnv, modeBoth, "", false},
		{"default without reviewers", "", false, false, config.Config{}, noEnv, modeStatic, staticForNoReviewers, false},
		{"default in CI", "", false, false, config.Config{Review: reviewers}, ci, modeStatic, staticForCI, false},
		{"config both in CI", "", false, false, config.Config{Mode: modeBoth}, ci, modeBoth, "", false},
		{"flag both without reviewers", modeBoth, false, false, config.Config{}, noEnv, modeBoth, "", false},
		{"ai flag in CI", "", false, true, config.Config{}, ci, modeAI, "", false},
		{"config", "", false, false, config.Config{Mode: modeAI}, noEnv, modeAI, "", false},
		{"flag wins", modeStatic, false, false, config.Config{Mode: modeAI}, noEnv, modeStatic, "", false},
		{"unknown config", "", false, false, config.Config{Mode: "bad"}, noEnv, "", "", true},
		{"conflict", "", true, true, config.Config{}, noEnv, "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, fallback, err := resolveMode(tc.flag, tc.static, tc.ai, tc.cfg, tc.getenv)
			if (err != nil) != tc.bad || got != tc.want || fallback != tc.fallback {
				t.Fatalf("got %q (%q), %v", got, fallback, err)
			}
		})
	}
}

func TestInCI(t *testing.T) {
	for env, want := range map[string]bool{
		"":                     false,
		"CI=false":             false,
		"CI=0":                 false,
		"CI=true":              true,
		"CI=1":                 true,
		"TF_BUILD=True":        true,
		"JENKINS_URL=http://j": true,
	} {
		name, value, _ := strings.Cut(env, "=")
		getenv := func(key string) string {
			if key == name {
				return value
			}
			return ""
		}
		if got := inCI(getenv); got != want {
			t.Errorf("%q: got %v", env, got)
		}
	}
}

func TestReviewSettingsValidation(t *testing.T) {
	got, err := reviewSettings(config.Config{}, 0)
	if err != nil || len(got.Reviewers) != 0 || got.Timeout != review.DefaultTimeout {
		t.Fatalf("defaults = %+v, %v", got, err)
	}
	for _, cfg := range []config.Config{{Review: &config.Review{Reviewers: []string{"terra", "sol", "flash", "haiku", "terra"}}}, {Review: &config.Review{Timeout: "0s"}}, {Review: &config.Review{Adapters: map[string]config.Adapter{"bad": {Command: []string{"x"}}}}}, {Review: &config.Review{Reviewers: []string{"terra", "sol"}, MinCompleted: 3}}, {Review: &config.Review{MinCompleted: -1}}} {
		if _, err := reviewSettings(cfg, 0); err == nil || !strings.Contains(err.Error(), "config") {
			t.Errorf("expected config validation error; got %v", err)
		}
	}
	got, err = reviewSettings(config.Config{Review: &config.Review{Reviewers: []string{"terra"}, Timeout: "3s", Adapters: map[string]config.Adapter{"codex": {Command: []string{"runner"}}}}}, 5*time.Second)
	if err != nil || got.Timeout != 5*time.Second || got.Adapters["codex"][0] != "runner" {
		t.Fatalf("override = %+v, %v", got, err)
	}
	got, err = reviewSettings(config.Config{Review: &config.Review{Reviewers: []string{"terra", "sol"}, MinCompleted: 2}}, 0)
	if err != nil || got.MinCompleted != 2 {
		t.Fatalf("minCompleted = %+v, %v", got, err)
	}
}

// TestCheckWithNoModeAndNoReviewersRunsStatic covers a machine or CI job that
// never ran slopcop init: a check with no mode set runs only the static half,
// keeps its exit code, and outside CI says once why the review did not run.
func TestCheckWithNoModeAndNoReviewersRunsStatic(t *testing.T) {
	for _, ci := range []string{"", "true"} {
		t.Run("CI="+ci, func(t *testing.T) {
			isolateUserConfig(t)
			// isolateUserConfig pins mode static; this test needs it unset.
			if _, err := config.Save(config.Config{}); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CI", ci)
			repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
			base := runGit(t, repo, "rev-parse", "HEAD")
			if err := os.WriteFile(filepath.Join(repo, "added.go"), []byte("package main\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status": "clean", "baseCommit": "` + base + `", "applied": true,
					"summary": {"files": 1, "findings": 0, "incompletePolicies": 0, "failOn": "finding"},
					"findings": [], "incomplete": []}`))
			}))
			defer server.Close()
			code, stdout, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL, "--format", "json")
			if code != 0 {
				t.Fatalf("exit = %d, stderr = %s", code, stderr)
			}
			var report map[string]any
			if err := json.Unmarshal([]byte(stdout), &report); err != nil {
				t.Fatalf("stdout is not json: %v\n%s", err, stdout)
			}
			if report["status"] != "clean" || report["review"] != nil {
				t.Fatalf("report = %v", report)
			}
			if notice := strings.Contains(stderr, "no reviewers are configured"); notice != (ci == "") {
				t.Fatalf("notice printed = %v with CI=%q: %s", notice, ci, stderr)
			}
			if strings.Contains(stderr, "reviewing with") {
				t.Fatalf("a review ran: %s", stderr)
			}
		})
	}
}

func TestReviewSkippedReasonNamesTheFallback(t *testing.T) {
	for fallback, want := range map[string]string{
		staticForCI:          report.ReviewSkippedCI,
		staticForNoReviewers: report.ReviewSkippedNoReviewers,
		"":                   report.ReviewSkippedStaticMode,
	} {
		if got := reviewSkippedReason(fallback); got != want {
			t.Errorf("reviewSkippedReason(%q) = %q, want %q", fallback, got, want)
		}
	}
}
