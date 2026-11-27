package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/gitx"
	"github.com/BrokkAi/slopcop/internal/review"
)

// reviewOnlyRepo is a logged-in checkout with a GitHub origin and a committed
// change, whose checks run only the review, by a reviewer that answers mode.
func reviewOnlyRepo(t *testing.T, mode string) (repo, tree string) {
	t.Helper()
	isolateUserConfig(t)
	t.Setenv("SLOPCOP_TOKEN", "test-token")
	t.Setenv("SLOPCOP_FAKE_ACP", "1")
	cfg := config.Config{Review: &config.Review{Reviewers: []string{"terra"}, Adapters: map[string]config.Adapter{
		"codex": {Command: []string{os.Args[0], "-test.run=^TestFakeAgent$", "--fake-mode=" + mode}},
	}}}
	if _, err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	repo = newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "README.md", "changed\n")
	runGit(t, repo, "commit", "-qam", "change")
	return repo, runGit(t, repo, "rev-parse", "HEAD^{tree}")
}

// withOriginBranches makes origin report whether it has any branch.
func withOriginBranches(t *testing.T, has bool) {
	t.Helper()
	original := originHasNoBranches
	originHasNoBranches = func(context.Context, string) bool { return !has }
	t.Cleanup(func() { originHasNoBranches = original })
}

func approvedField(t *testing.T, out string) bool {
	t.Helper()
	var doc struct {
		Approved bool `json:"approved"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("report: %v\n%s", err, out)
	}
	return doc.Approved
}

// A review-only check must not stand in for the static half at the push gate
// where the static half could have run: that was a way past it (#239).
func TestReviewOnlyApprovalDoesNotPassTheGate(t *testing.T) {
	repo, tree := reviewOnlyRepo(t, "clean")
	withOriginBranches(t, true)

	code, out, stderr := runCLI(t, "check", "--root", repo, "--base-exact", "HEAD~1", "--mode", "ai", "--format", "json")
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, out, stderr)
	}
	if approvedField(t, out) {
		t.Fatalf("a review-only check approved the tree: %s", out)
	}
	if !strings.Contains(stderr, "is not approved for push: a review-only check (--mode ai)") {
		t.Fatalf("stderr does not say why: %s", stderr)
	}
	if ok, err := review.Reviewed(repo, tree); err != nil || ok {
		t.Fatalf("approval ref written: %v %v", ok, err)
	}
	if code, _, stderr := runCLI(t, "gate", "--root", repo); code != 2 || !strings.Contains(stderr, "push blocked") {
		t.Fatalf("gate exit %d: %s", code, stderr)
	}
}

// An override must not approve a tree on a review-only record either.
func TestOverrideRefusesAReviewOnlyRecord(t *testing.T) {
	repo, tree := reviewOnlyRepo(t, "findings")
	withOriginBranches(t, true)

	if code, out, stderr := runCLI(t, "check", "--root", repo, "--base-exact", "HEAD~1", "--mode", "ai", "--format", "json"); code != 1 {
		t.Fatalf("exit %d: %s %s", code, out, stderr)
	}
	code, _, stderr := runCLI(t, "override", "--root", repo, "--reason", "wrong")
	if code == 0 || !strings.Contains(stderr, "ran only the review (--mode ai)") {
		t.Fatalf("override exit %d: %s", code, stderr)
	}
	if ok, err := review.Reviewed(repo, tree); err != nil || ok {
		t.Fatalf("approval ref written: %v %v", ok, err)
	}
}

// Before the first push of a new repository the static half cannot run, so a
// passing review approves the tree on its own, and says so.
func TestReviewOnlyApprovalPassesTheGateBeforeTheFirstPush(t *testing.T) {
	repo, tree := reviewOnlyRepo(t, "clean")
	withOriginBranches(t, false)

	code, out, stderr := runCLI(t, "check", "--root", repo, "--base-exact", "HEAD~1", "--mode", "ai", "--format", "json")
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, out, stderr)
	}
	if !approvedField(t, out) {
		t.Fatalf("not approved: %s %s", out, stderr)
	}
	base := shortSHA(runGit(t, repo, "rev-parse", "HEAD~1"))
	if !strings.Contains(stderr, "approved without a static check") || !strings.Contains(stderr, "--mode static --base-exact "+base) {
		t.Fatalf("stderr does not say the approval skipped the static check: %s", stderr)
	}
	if ok, err := review.Reviewed(repo, tree); err != nil || !ok {
		t.Fatalf("approval ref missing: %v %v", ok, err)
	}
	if code, _, stderr := runCLI(t, "gate", "--root", repo); code != 0 {
		t.Fatalf("gate exit %d: %s", code, stderr)
	}
	// Once origin has a branch the static half can run, and the approval
	// the review alone gave no longer lets the tree through.
	withOriginBranches(t, true)
	if code, _, stderr := runCLI(t, "gate", "--root", repo); code != 2 || !strings.Contains(stderr, "approved by the review alone") {
		t.Fatalf("gate after the first push: exit %d: %s", code, stderr)
	}
}

// Logging out for one review-only check must not leave an approval that
// stands once the login is back.
func TestReviewOnlyApprovalWithoutALoginLapsesWhenTheLoginReturns(t *testing.T) {
	repo, tree := reviewOnlyRepo(t, "clean")
	withOriginBranches(t, true)
	t.Setenv("SLOPCOP_TOKEN", "")

	code, out, stderr := runCLI(t, "check", "--root", repo, "--base-exact", "HEAD~1", "--mode", "ai", "--format", "json")
	if code != 0 || !approvedField(t, out) {
		t.Fatalf("without a login: exit %d: %s %s", code, out, stderr)
	}
	if ok, err := review.ReviewedWithoutStatic(repo, tree); err != nil || !ok {
		t.Fatalf("review-only mark missing: %v %v", ok, err)
	}
	if code, _, stderr := runCLI(t, "gate", "--root", repo); code != 0 {
		t.Fatalf("gate without a login: exit %d: %s", code, stderr)
	}
	t.Setenv("SLOPCOP_TOKEN", "test-token")
	if code, _, stderr := runCLI(t, "gate", "--root", repo); code != 2 || !strings.Contains(stderr, "approved by the review alone") {
		t.Fatalf("gate with the login back: exit %d: %s", code, stderr)
	}
}

// Before the first push the service has no commit of the repository, and
// its remedy, pushing the base, is what the gate blocks; the hint gives the
// way that works.
func TestBaseNotFoundHintBeforeTheFirstPush(t *testing.T) {
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	base := runGit(t, repo, "rev-parse", "HEAD")
	writeFile(t, repo, "b.txt", "b\n")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-qm", "b")

	withOriginBranches(t, false)
	hint := baseNotFoundHint(context.Background(), repo, base)
	for _, want := range []string{
		"origin has no branches yet",
		"`slopcop check --mode ai --base-exact " + shortSHA(base) + "`",
		"`slopcop check --mode static --base-exact " + shortSHA(base) + "`",
	} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint = %q, want %q", hint, want)
		}
	}
	// A base at HEAD leaves nothing to review, so the hint names no commit.
	if hint := firstPushHint(repo, runGit(t, repo, "rev-parse", "HEAD")); !strings.Contains(hint, "<the commit the change starts from>") {
		t.Errorf("base at HEAD: hint = %q", hint)
	}
	// A repository's only commit has nothing to be checked against, so the
	// way through is a person's push.
	single := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	if hint := firstPushHint(single, runGit(t, single, "rev-parse", "HEAD")); !strings.Contains(hint, "only commit") || !strings.Contains(hint, "push this first commit themselves") {
		t.Errorf("single commit: hint = %q", hint)
	}
}

func TestOriginHasNoBranches(t *testing.T) {
	origin := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, t.TempDir(), "init", "-q", "--bare", "-b", "main", origin)
	work := newRepo(t, origin)
	ctx := context.Background()
	if !gitx.OriginHasNoBranches(ctx, work) {
		t.Fatal("an empty origin has branches")
	}
	runGit(t, work, "push", "-q", "-u", "origin", "main")
	if gitx.OriginHasNoBranches(ctx, work) {
		t.Fatal("origin with a pushed branch has none")
	}
	// A deleted remote-tracking branch is no way to an empty origin: origin
	// itself is asked.
	runGit(t, work, "update-ref", "-d", "refs/remotes/origin/main")
	if gitx.OriginHasNoBranches(ctx, work) {
		t.Fatal("origin has none once the tracking branch is gone")
	}
	// Tracking branches left from an earlier origin do not hide a new,
	// empty one.
	runGit(t, work, "fetch", "-q", "origin")
	fresh := filepath.Join(t.TempDir(), "fresh.git")
	runGit(t, t.TempDir(), "init", "-q", "--bare", "-b", "main", fresh)
	runGit(t, work, "remote", "set-url", "origin", fresh)
	if !gitx.OriginHasNoBranches(ctx, work) {
		t.Fatal("a new empty origin has branches because of stale tracking branches")
	}
	// An origin that cannot be asked is not taken to be empty.
	runGit(t, work, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
	if gitx.OriginHasNoBranches(ctx, work) {
		t.Fatal("an unreachable origin has no branches")
	}
}

// An origin on a host the service does not check cannot run the static half,
// so a review-only approval stands there even with a login.
func TestReviewOnlyApprovalStandsOnAnUnsupportedHost(t *testing.T) {
	repo, _ := reviewOnlyRepo(t, "clean")
	withOriginBranches(t, true)
	runGit(t, repo, "remote", "set-url", "origin", "https://gitea.example.com/team/project.git")

	code, out, stderr := runCLI(t, "check", "--root", repo, "--base-exact", "HEAD~1", "--mode", "ai", "--format", "json")
	if code != 0 || !approvedField(t, out) {
		t.Fatalf("exit %d: %s %s", code, out, stderr)
	}
	if code, _, stderr := runCLI(t, "gate", "--root", repo); code != 0 {
		t.Fatalf("gate exit %d: %s", code, stderr)
	}
}
