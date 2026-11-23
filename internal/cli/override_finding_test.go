package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/review"
)

const reviewReply = "- [P2] npm can publish a newer version than the latest GitHub release `README.md:1`\n" +
	"The npm job publishes before goreleaser creates the release."

// reviewedRepo is a repository whose latest local review reported one
// finding, and the finding's id.
func reviewedRepo(t *testing.T) (string, string) {
	t.Helper()
	repo := newRepo(t, "git@github.com:BrokkAi/slopcop.git")
	store, err := review.OpenStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	verdict := review.Verdict{Status: review.StatusFindings, Capture: review.Capture{Tree: runGit(t, repo, "rev-parse", "HEAD^{tree}")}, Reviewers: []review.Reviewer{
		{Name: "1-terra", Status: review.StatusFindings, Text: reviewReply},
	}}
	if err := store.Save(review.Record{Mode: "ai", Verdict: verdict}); err != nil {
		t.Fatal(err)
	}
	return repo, verdict.Findings()[0].ID
}

func TestOverrideAReviewFindingThenUndoIt(t *testing.T) {
	isolateUserConfig(t)
	repo, id := reviewedRepo(t)

	if code, _, stderr := runCLI(t, "override", "--root", repo, id); code == 0 || !strings.Contains(stderr, "--reason is required") {
		t.Fatalf("override without a reason: exit = %d, stderr = %q", code, stderr)
	}
	code, stdout, stderr := runCLI(t, "override", "--root", repo, id, "--reason", "the npm job needs goreleaser", "-w", "Acme team")
	if code != 0 || stdout != "slopcop: overrode review finding "+id+" (npm can publish a newer version than the latest GitHub release); "+
		"later reviews in this clone will not report it, even reworded. Run `slopcop check` again: the same review is reused and leaves it out.\n" {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	code, stdout, _ = runCLI(t, "override", "--root", repo, " "+id+" ", "--reason", "release job order: npm needs goreleaser")
	if code != 0 || !strings.HasPrefix(stdout, "slopcop: updated the reason of the override of review finding "+id) {
		t.Fatalf("second override: exit = %d, stdout = %q", code, stdout)
	}
	overrides, err := review.LoadFindingOverrides(repo)
	if err != nil || len(overrides) != 1 || overrides[0].ID != id || overrides[0].Finding.Path != "README.md" || overrides[0].Reason != "release job order: npm needs goreleaser" {
		t.Fatalf("overrides = %+v, %v", overrides, err)
	}

	code, stdout, _ = runCLI(t, "override", "--root", repo, "--list")
	for _, want := range []string{
		"slopcop: 1 review finding is overridden in this clone\n  " + id + " [P2] README.md:1 npm can publish a newer version than the latest GitHub release (1-terra) (overridden ",
		"    Reason: release job order: npm needs goreleaser\n",
		"slopcop: undo one with `slopcop override --undo <id>`\n",
	} {
		if code != 0 || !strings.Contains(stdout, want) {
			t.Errorf("list missing %q:\n%s", want, stdout)
		}
	}
	code, stdout, _ = runCLI(t, "override", "--root", repo, "--list", "--format", "json")
	var doc struct {
		Overrides []review.FindingOverride `json:"overrides"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); code != 0 || err != nil || len(doc.Overrides) != 1 || doc.Overrides[0].ID != id {
		t.Fatalf("json list: %v\n%s", err, stdout)
	}

	code, stdout, stderr = runCLI(t, "override", "--root", repo, "--undo", id)
	if code != 0 || !strings.HasPrefix(stdout, "slopcop: review finding "+id+" is no longer overridden; ") {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if code, stdout, _ = runCLI(t, "override", "--root", repo, "--undo", id); code != 0 || stdout != "slopcop: review finding "+id+" is not overridden; nothing changed\n" {
		t.Fatalf("second undo: exit = %d, stdout = %q", code, stdout)
	}
	if code, stdout, _ = runCLI(t, "override", "--root", repo, "--list"); code != 0 || stdout != "slopcop: no review findings are overridden in this clone\n" {
		t.Fatalf("empty list: exit = %d, stdout = %q", code, stdout)
	}
}

func TestOverrideRefusesWrongFindingIDs(t *testing.T) {
	isolateUserConfig(t)
	repo, id := reviewedRepo(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"rv-0123456789ab", "--reason", "x"}, "no review of this checkout reported finding rv-0123456789ab"},
		{[]string{findingID, "--reason", "x"}, "is a static finding's id; run `slopcop ignore " + findingID + "`"},
		{[]string{"nope", "--reason", "x"}, "is not a review finding id"},
		{[]string{"--undo", findingID}, "is a static finding's id"},
		{[]string{id, "--undo", id}, "give one of an rv- id, --undo <rv-id> or --list"},
	} {
		code, stdout, stderr := runCLI(t, append([]string{"override", "--root", repo}, tc.args...)...)
		if code == 0 || stdout != "" || !strings.Contains(stderr, tc.want) {
			t.Errorf("%v: exit = %d, stdout = %q, stderr = %q", tc.args, code, stdout, stderr)
		}
	}
}

func TestIgnoreSendsAReviewFindingToOverride(t *testing.T) {
	isolateUserConfig(t)
	repo, id := reviewedRepo(t)
	t.Chdir(repo)
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	for _, command := range []string{"ignore", "unignore"} {
		code, stdout, stderr := runCLI(t, command, id, "--service", server.URL)
		if code != 2 || stdout != "" || !strings.Contains(stderr, "is a local review finding's id, which `slopcop ignore` does not take; run `slopcop override "+id+" --reason") {
			t.Errorf("%s: exit = %d, stdout = %q, stderr = %q", command, code, stdout, stderr)
		}
	}
}
