package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/review"
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

// shownOverride is what 'review show --format json' says about an override.
type shownOverride struct {
	Status   string `json:"status"`
	Approved bool   `json:"approved"`
	Override *struct {
		Status      string `json:"status"`
		Reason      string `json:"reason"`
		Tree        string `json:"tree"`
		CurrentTree string `json:"currentTree"`
	} `json:"reviewOverride"`
	Review struct {
		Status   string            `json:"status"`
		Findings []json.RawMessage `json:"findings"`
	} `json:"review"`
}

// showOverride runs 'review show' in both formats. Every state keeps the
// findings and approves nothing, and no state changes the approval refs.
func showOverride(t *testing.T, repo string) (shownOverride, string) {
	t.Helper()
	refs := runGit(t, repo, "for-each-ref", "refs/slopcop/")
	code, out, stderr := runCLI(t, "review", "show", "--root", repo, "--format", "json")
	if code != 0 {
		t.Fatalf("review show exit %d stderr %s", code, stderr)
	}
	var shown shownOverride
	if err := json.Unmarshal([]byte(out), &shown); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if shown.Approved || shown.Status != "findings" || shown.Review.Status != "findings" || len(shown.Review.Findings) == 0 {
		t.Fatalf("review show must keep the findings and approve nothing: %s", out)
	}
	code, human, stderr := runCLI(t, "review", "show", "--root", repo)
	if code != 0 || !strings.Contains(human, "Review: findings") || !strings.Contains(human, "fake defect") {
		t.Fatalf("human: exit %d stderr %s\n%s", code, stderr, human)
	}
	if after := runGit(t, repo, "for-each-ref", "refs/slopcop/"); after != refs {
		t.Fatalf("review show changed the approval refs:\n%s\nto\n%s", refs, after)
	}
	return shown, human
}

// TestReviewShowReportsTheOverride covers a review with no override, one that
// applies, and one that no longer does because the tree changed or its
// approval was withdrawn.
func TestReviewShowReportsTheOverride(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "")
	writeFile(t, repo, "README.md", "changed\n")
	t.Setenv("SLOPCOP_FAKE_ACP", "1")
	cfg := config.Config{Mode: "ai", Review: &config.Review{Reviewers: []string{"terra"}, Adapters: map[string]config.Adapter{"codex": {Command: []string{os.Args[0], "-test.run=^TestFakeAgent$", "--fake-mode=findings"}}}}}
	if _, err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	runCLI(t, "check", "--root", repo, "--format", "json")

	shown, human := showOverride(t, repo)
	if shown.Override != nil || strings.Contains(human, "Override reason") || strings.Contains(human, "Accepted by") {
		t.Fatalf("no override: %+v\n%s", shown.Override, human)
	}

	const reason = "the finding misreads the release order"
	if code, out, stderr := runCLI(t, "override", "--root", repo, "--reason", reason); code != 0 {
		t.Fatalf("override exit %d: %s%s", code, out, stderr)
	}
	shown, human = showOverride(t, repo)
	if shown.Override == nil || shown.Override.Status != review.OverrideAccepted || shown.Override.Reason != reason || shown.Override.CurrentTree != "" {
		t.Fatalf("accepted: %+v", shown.Override)
	}
	tree := shown.Override.Tree
	if !strings.Contains(human, "Accepted by `slopcop override` for this working tree ("+tree+")") ||
		!strings.Contains(human, "Override reason: "+reason) || strings.Contains(human, "Fix the real findings") {
		t.Fatalf("accepted, human:\n%s", human)
	}

	writeFile(t, repo, "README.md", "changed again\n")
	shown, human = showOverride(t, repo)
	if shown.Override == nil || shown.Override.Status != review.OverrideTreeChanged || shown.Override.Tree != tree ||
		shown.Override.CurrentTree == "" || shown.Override.CurrentTree == tree {
		t.Fatalf("tree changed: %+v", shown.Override)
	}
	if strings.Contains(human, "Accepted by") || !strings.Contains(human, "Override not in effect") || !strings.Contains(human, "Fix the real findings") {
		t.Fatalf("tree changed, human:\n%s", human)
	}
	// Working out the current tree leaves nothing in the object store.
	if err := exec.Command("git", "-C", repo, "cat-file", "-e", shown.Override.CurrentTree).Run(); err == nil {
		t.Fatalf("review show wrote tree %s to the repository", shown.Override.CurrentTree)
	}

	// The same content is the accepted tree again.
	writeFile(t, repo, "README.md", "changed\n")
	if shown, _ = showOverride(t, repo); shown.Override == nil || shown.Override.Status != review.OverrideAccepted {
		t.Fatalf("restored: %+v", shown.Override)
	}

	if err := review.Unmark(repo, tree); err != nil {
		t.Fatal(err)
	}
	shown, human = showOverride(t, repo)
	if shown.Override == nil || shown.Override.Status != review.OverrideWithdrawn {
		t.Fatalf("withdrawn: %+v", shown.Override)
	}
	if strings.Contains(human, "Accepted by") || !strings.Contains(human, "was withdrawn") {
		t.Fatalf("withdrawn, human:\n%s", human)
	}

	// A later check of the tree is a new decision, and does not inherit the
	// override even when it reuses the review.
	runCLI(t, "check", "--root", repo, "--format", "json")
	if shown, _ = showOverride(t, repo); shown.Override != nil {
		t.Fatalf("after a later check: %+v", shown.Override)
	}
}
