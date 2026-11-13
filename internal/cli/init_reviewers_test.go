package cli

import (
	"github.com/BrokkAi/slopcop/internal/config"
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
		})
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
			code, _, errOut := runCLI(t, args...)
			if code != 0 {
				t.Fatal(errOut)
			}
			got, _ := config.Load()
			if strings.Join(got.Review.Reviewers, ",") != strings.Join(tc.existing, ",") {
				t.Fatalf("%+v", got.Review)
			}
		})
	}
}
