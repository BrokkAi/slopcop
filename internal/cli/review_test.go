package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/config"
)

// TestReviewShowReplaysTheLatestReview checks that 'review show' prints the
// latest review from the reviewers' logs, the same as the check reported it,
// and approves nothing.
func TestReviewShowReplaysTheLatestReview(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "")
	code, _, stderr := runCLI(t, "review", "show", "--root", repo)
	if code == 0 || !strings.Contains(stderr, "no local review has run") {
		t.Fatalf("before a review: exit %d stderr %s", code, stderr)
	}
	writeFile(t, repo, "README.md", "changed\n")
	t.Setenv("SLOPCOP_FAKE_ACP", "1")
	cfg := config.Config{Mode: "ai", Review: &config.Review{Reviewers: []string{"terra"}, Adapters: map[string]config.Adapter{"codex": {Command: []string{os.Args[0], "-test.run=^TestFakeAgent$", "--fake-mode=findings"}}}}}
	if _, err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	type doc struct {
		Status   string `json:"status"`
		Approved bool   `json:"approved"`
		Review   struct {
			Status    string `json:"status"`
			Reviewers []struct {
				Text string `json:"text"`
			} `json:"reviewers"`
			Findings []json.RawMessage `json:"findings"`
		} `json:"review"`
	}
	parse := func(out string) doc {
		var d doc
		if err := json.Unmarshal([]byte(out), &d); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return d
	}
	_, out, _ := runCLI(t, "check", "--root", repo, "--format", "json")
	checked := parse(out)
	code, out, stderr = runCLI(t, "review", "show", "--root", repo, "--format", "json")
	if code != 0 {
		t.Fatalf("review show exit %d stderr %s", code, stderr)
	}
	shown := parse(out)
	if shown.Status != "findings" || shown.Approved || shown.Review.Status != "findings" || len(shown.Review.Reviewers) != 1 ||
		shown.Review.Reviewers[0].Text == "" || shown.Review.Reviewers[0].Text != checked.Review.Reviewers[0].Text ||
		len(shown.Review.Findings) != len(checked.Review.Findings) {
		t.Fatalf("shown %+v, checked %+v", shown, checked)
	}
	if code, out, _ = runCLI(t, "review", "show", "--root", repo); code != 0 || !strings.Contains(out, "Review: findings") {
		t.Fatalf("human: exit %d %s", code, out)
	}
}
