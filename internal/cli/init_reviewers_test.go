package cli

import (
	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/review"
	"os"
	"strings"
	"testing"
)

func TestInitPicksReviewers(t *testing.T) {
	for _, tc := range []struct {
		name            string
		codex, claude   []string
		want, fallbacks string
	}{
		{"codex", []string{os.Args[0], "-test.run=^TestFakeAgent$", "--fake-models=gpt-6.1-terra,gpt-6.1-sol"}, []string{"/missing/claude"}, "terra,sol", ""},
		{"claude", []string{"/missing/codex"}, []string{os.Args[0], "-test.run=^TestFakeAgent$", "--fake-model=claude-4-haiku"}, "haiku", ""},
		// Claude Code alone gives a second Claude reviewer, and Opus, the
		// costliest, stands by.
		{"claude models", []string{"/missing/codex"}, []string{os.Args[0], "-test.run=^TestFakeAgent$", "--fake-models=default,opus,sonnet,haiku"}, "haiku,sonnet", "opus"},
		{"both", []string{os.Args[0], "-test.run=^TestFakeAgent$", "--fake-models=gpt-6.1-terra,gpt-6.1-sol"}, []string{os.Args[0], "-test.run=^TestFakeAgent$", "--fake-model=claude-4-haiku"}, "terra,sol", "haiku"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateUserConfig(t)
			t.Setenv("SLOPCOP_FAKE_ACP", "1")
			repo := newRepo(t, "")
			cfg := config.Config{Review: &config.Review{Adapters: map[string]config.Adapter{"codex": {Command: tc.codex}, "dsh": {Command: []string{"/missing/dsh"}}, "claude": {Command: tc.claude}}}}
			if _, err := config.Save(cfg); err != nil {
				t.Fatal(err)
			}
			code, out, errOut := runCLI(t, "init", "--root", repo, "--no-skill", "--no-hooks")
			if code != 0 {
				t.Fatalf("%s: %s", out, errOut)
			}
			got, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got.Review.Reviewers, ",") != tc.want || strings.Join(got.Review.Fallbacks, ",") != tc.fallbacks {
				t.Fatalf("%+v, %s", got.Review, out)
			}
			// Reviewers that do not work are named once, without their
			// raw errors, which --verbose prints.
			_, why, ok := strings.Cut(out, "not available here; why: ")
			if strings.Contains(out, "unavailable:") || !ok {
				t.Fatal(out)
			}
			why, _, _ = strings.Cut(why, "\n")
			if saved, err := os.ReadFile(why); err != nil || !strings.Contains(string(saved), "unavailable: ") {
				t.Fatalf("%s: %q %v", why, saved, err)
			}
		})
	}
}

// clearReviewerLogins hides any reviewer login or API key the machine running
// the tests has, so init sees a fresh machine.
func clearReviewerLogins(t *testing.T) {
	t.Helper()
	managed := review.ClaudeManagedSettings
	t.Cleanup(func() { review.ClaudeManagedSettings = managed })
	review.ClaudeManagedSettings = nil
	for _, name := range []string{"DEEPSEEK_API_KEY", "OPENAI_API_KEY", "CODEX_API_KEY", "DEFAULT_AUTH_REQUEST", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		t.Setenv(name, "")
	}
	// Copilot CLI also finds its own login and the GitHub CLI's.
	t.Setenv("COPILOT_HOME", t.TempDir())
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
}

// TestInitFallsBackToCopilot covers a machine whose only login is GitHub
// Copilot, such as one that runs Pi on Copilot: init probes the families
// Copilot serves through Copilot CLI and records the provider.
func TestInitFallsBackToCopilot(t *testing.T) {
	for _, tc := range []struct {
		name, models, want, fallbacks, warning string
	}{
		{"paid", "auto,gpt-6.1-terra,gpt-6.1-sol,claude-haiku-5.5", "terra,sol", "haiku", ""},
		{"free", "auto,auto", "terra", "", "only Auto"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateUserConfig(t)
			clearReviewerLogins(t)
			t.Setenv("SLOPCOP_FAKE_ACP", "1")
			t.Setenv("GH_TOKEN", "fake")
			repo := newRepo(t, "")
			copilot := []string{os.Args[0], "-test.run=^TestFakeAgent$", "--", "--fake-mode=copilot", "--fake-models=" + tc.models}
			cfg := config.Config{Review: &config.Review{Adapters: map[string]config.Adapter{"copilot": {Command: copilot}}}}
			if _, err := config.Save(cfg); err != nil {
				t.Fatal(err)
			}
			code, out, errOut := runCLI(t, "init", "--root", repo, "--no-skill", "--no-hooks")
			if code != 0 {
				t.Fatalf("%s: %s", out, errOut)
			}
			got, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			if got.Review.Provider != "copilot" || strings.Join(got.Review.Reviewers, ",") != tc.want || strings.Join(got.Review.Fallbacks, ",") != tc.fallbacks || got.Review.MinCompleted != 0 {
				t.Fatalf("%+v, %s", got.Review, out)
			}
			if !strings.Contains(out, "trying the same reviewers through GitHub Copilot CLI (terra, sol, haiku, sonnet, opus)") || !strings.Contains(out, "review.provider set to copilot") {
				t.Fatal(out)
			}
			if tc.warning != "" && !strings.Contains(out, "slopcop: warning: ") || !strings.Contains(out, tc.warning) {
				t.Fatal(out)
			}
		})
	}
}

// TestInitOnFreshMachine checks that init on a machine with no reviewer
// logged in probes nothing and ends with one line, not a failure per
// reviewer (#169).
func TestInitOnFreshMachine(t *testing.T) {
	isolateUserConfig(t)
	clearReviewerLogins(t)
	repo := newRepo(t, "")
	if _, err := config.Save(config.Config{}); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runCLI(t, "init", "--root", repo, "--no-skill", "--no-hooks")
	if code != 0 {
		t.Fatalf("%s: %s", out, errOut)
	}
	want := "slopcop: left the automatic hooks alone (--no-hooks)\n" +
		"slopcop: local review: none available yet (log in to Claude Code, Codex or GitHub Copilot CLI, or set DEEPSEEK_API_KEY or review.dsh.apiKeyFile, then run `slopcop init`). Hosted checks work now.\n"
	if !strings.HasSuffix(out, want) {
		t.Fatalf("got %q", out)
	}
	// A mode set to both cannot fall back to the hosted check alone.
	if _, err := config.Save(config.Config{Mode: "both"}); err != nil {
		t.Fatal(err)
	}
	code, out, _ = runCLI(t, "init", "--root", repo, "--no-skill", "--no-hooks", "--verbose")
	if code != 0 || !strings.Contains(out, "Until then, mode both needs a reviewer, so run checks with --static.") || !strings.Contains(out, "haiku unavailable: Claude Code is not logged in") || !strings.Contains(out, "sonnet unavailable: Claude Code is not logged in") || !strings.Contains(out, "opus unavailable: Claude Code is not logged in") || !strings.Contains(out, "terra unavailable: Codex is not logged in") {
		t.Fatal(out)
	}
}

// TestInitHidesProbeErrors checks that a reviewer that was probed and failed
// shows its raw adapter error only under --verbose.
func TestInitHidesProbeErrors(t *testing.T) {
	isolateUserConfig(t)
	clearReviewerLogins(t)
	t.Setenv("SLOPCOP_FAKE_ACP", "1")
	repo := newRepo(t, "")
	claude := []string{os.Args[0], "-test.run=^TestFakeAgent$", "--fake-mode=auth-required", "--fake-model=claude-4-haiku"}
	cfg := config.Config{Review: &config.Review{Adapters: map[string]config.Adapter{"claude": {Command: claude}}}}
	if _, err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runCLI(t, "init", "--root", repo, "--no-skill", "--no-hooks")
	if code != 0 {
		t.Fatalf("%s: %s", out, errOut)
	}
	if strings.Contains(out, "Authentication") || strings.Contains(out, "unavailable:") || !strings.Contains(out, "checking which local reviewers work here (haiku, sonnet, opus)") || !strings.Contains(out, "`slopcop init --verbose` says why each failed). Hosted checks work now.") {
		t.Fatal(out)
	}
	code, out, _ = runCLI(t, "init", "--root", repo, "--no-skill", "--no-hooks", "--verbose")
	if code != 0 || !strings.Contains(out, "haiku unavailable: ") || !strings.Contains(out, "opus unavailable: ") || !strings.Contains(out, "Authentication required") {
		t.Fatal(out)
	}
}
func TestInitKeepsOrSkipsReviewers(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		existing   []string
		flag       string
	}{
		{"existing", "", []string{"haiku"}, ""}, {"static", "static", nil, ""}, {"flag", "", nil, "--no-reviewers"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateUserConfig(t)
			repo := newRepo(t, "")
			cfg := config.Config{Mode: tc.mode, Review: &config.Review{Reviewers: tc.existing, Adapters: map[string]config.Adapter{"codex": {Command: []string{"/missing/codex"}}}}}
			if _, err := config.Save(cfg); err != nil {
				t.Fatal(err)
			}
			args := []string{"init", "--root", repo, "--no-skill", "--no-hooks"}
			if tc.flag != "" {
				args = append(args, tc.flag)
			}
			code, out, errOut := runCLI(t, args...)
			if code != 0 {
				t.Fatal(errOut)
			}
			if tc.mode == "static" && !strings.Contains(out, "mode is static, so init did not look for local reviewers") {
				t.Fatal(out)
			}
			got, _ := config.Load()
			if strings.Join(got.Review.Reviewers, ",") != strings.Join(tc.existing, ",") {
				t.Fatalf("%+v", got.Review)
			}
		})
	}
}

// TestInitClearsUnreachableMinimum checks that init does not save a
// review.minCompleted larger than the reviewers it picks, which would make
// every later check reject the config.
func TestInitClearsUnreachableMinimum(t *testing.T) {
	isolateUserConfig(t)
	t.Setenv("SLOPCOP_FAKE_ACP", "1")
	repo := newRepo(t, "")
	codex := []string{os.Args[0], "-test.run=^TestFakeAgent$", "--fake-models=gpt-6.1-terra,gpt-6.1-sol"}
	cfg := config.Config{Review: &config.Review{MinCompleted: 3, Adapters: map[string]config.Adapter{"codex": {Command: codex}, "dsh": {Command: []string{"/missing/dsh"}}, "claude": {Command: []string{"/missing/claude"}}}}}
	if _, err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runCLI(t, "init", "--root", repo, "--no-skill", "--no-hooks")
	if code != 0 || !strings.Contains(out, "review.minCompleted was 3") {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	got, err := config.Load()
	if err != nil || got.Review.MinCompleted != 0 || len(got.Review.Reviewers) != 2 {
		t.Fatalf("%+v %v", got.Review, err)
	}
}
