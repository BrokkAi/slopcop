package cli

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// checkCapture is a check service that answers clean and keeps every request.
func checkCapture(t *testing.T) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var captured []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		captured = append(captured, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": "clean",
			"baseCommit": "` + body["baseCommit"].(string) + `",
			"applied": true,
			"summary": {"files": 1, "findings": 0, "incompletePolicies": 0, "failOn": "finding"},
			"findings": [],
			"incomplete": []
		}`))
	}))
	t.Cleanup(server.Close)
	return server, &captured
}

func decodedPatch(t *testing.T, request map[string]any) string {
	t.Helper()
	patch, err := base64.StdEncoding.DecodeString(request["change"].(string))
	if err != nil {
		t.Fatalf("change is not base64: %v", err)
	}
	return string(patch)
}

func commitFile(t *testing.T, repo, name, content string) {
	t.Helper()
	writeFile(t, repo, name, content)
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-qm", name)
}

// publishMain points origin/main, and origin/HEAD with it, at the local main,
// as a fetch would.
func publishMain(t *testing.T, repo string) {
	t.Helper()
	runGit(t, repo, "update-ref", "refs/remotes/origin/main", "main")
	runGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
}

// stackedRepo builds main, a prerequisite branch off it, and a feature branch
// stacked on the prerequisite with one file of its own, left checked out.
func stackedRepo(t *testing.T) string {
	t.Helper()
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	runGit(t, repo, "checkout", "-q", "-b", "prereq")
	commitFile(t, repo, "prereq.txt", "prerequisite\n")
	runGit(t, repo, "checkout", "-q", "-b", "feature")
	commitFile(t, repo, "own.txt", "the branch's own work\n")
	return repo
}

// A branch stacked on a prerequisite that has since landed, and that merged
// the newer main, sends only its own work: the merge base with the stale
// prerequisite would also send everything main gained.
func TestBaseLeavesOutMergedInDefaultBranch(t *testing.T) {
	isolateUserConfig(t)
	repo := stackedRepo(t)
	runGit(t, repo, "checkout", "-q", "main")
	runGit(t, repo, "merge", "-q", "--no-ff", "-m", "land prereq", "prereq")
	commitFile(t, repo, "others.txt", "someone else's work\n")
	publishMain(t, repo)
	mainTip := runGit(t, repo, "rev-parse", "main")
	runGit(t, repo, "checkout", "-q", "feature")
	runGit(t, repo, "merge", "-q", "--no-edit", "origin/main")
	writeFile(t, repo, "own.txt", "the branch's own work, edited\n")

	server, captured := checkCapture(t)
	code, _, stderr := runCLI(t,
		"check", "--root", repo, "--service", server.URL, "--format", "json", "--verbose", "--base", "prereq",
	)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	request := (*captured)[0]
	if request["baseCommit"] != mainTip {
		t.Errorf("baseCommit = %v, want origin/main's merge base %s", request["baseCommit"], mainTip)
	}
	patch := decodedPatch(t, request)
	if !strings.Contains(patch, "b/own.txt") {
		t.Errorf("patch should carry the branch's own file:\n%s", patch)
	}
	for _, other := range []string{"others.txt", "prereq.txt"} {
		if strings.Contains(patch, other) {
			t.Errorf("patch should leave out merged-in %s:\n%s", other, patch)
		}
	}
	if !strings.Contains(stderr, "HEAD merged origin/main") {
		t.Errorf("--verbose should say why the base moved: %q", stderr)
	}
	if strings.Contains(stderr, "carries their changes") {
		t.Errorf("a base that holds every merged-in commit should not warn: %q", stderr)
	}
}

// The prerequisite's own upstream counts too: a stale local prerequisite
// whose pushed branch moved on and was merged in.
func TestBaseLeavesOutMergedInUpstreamOfTheBase(t *testing.T) {
	isolateUserConfig(t)
	repo := stackedRepo(t)
	runGit(t, repo, "checkout", "-q", "prereq")
	commitFile(t, repo, "review-fix.txt", "fix from review\n")
	runGit(t, repo, "update-ref", "refs/remotes/origin/prereq", "prereq")
	runGit(t, repo, "branch", "-q", "--set-upstream-to", "origin/prereq")
	pushed := runGit(t, repo, "rev-parse", "prereq")
	runGit(t, repo, "reset", "-q", "--hard", "HEAD~1")
	runGit(t, repo, "checkout", "-q", "feature")
	runGit(t, repo, "merge", "-q", "--no-edit", "origin/prereq")

	server, captured := checkCapture(t)
	code, _, stderr := runCLI(t,
		"check", "--root", repo, "--service", server.URL, "--format", "json", "--base", "prereq",
	)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	request := (*captured)[0]
	if request["baseCommit"] != pushed {
		t.Errorf("baseCommit = %v, want origin/prereq %s", request["baseCommit"], pushed)
	}
	patch := decodedPatch(t, request)
	if !strings.Contains(patch, "b/own.txt") || strings.Contains(patch, "review-fix.txt") {
		t.Errorf("patch should carry only the branch's own file:\n%s", patch)
	}
}

// When the prerequisite has not landed, no commit the service can fetch holds
// both lines, so the base stays and the check says the patch is too big.
func TestBaseWarnsWhenMergedInLineDiverged(t *testing.T) {
	isolateUserConfig(t)
	repo := stackedRepo(t)
	prereqTip := runGit(t, repo, "rev-parse", "prereq")
	runGit(t, repo, "checkout", "-q", "main")
	commitFile(t, repo, "others.txt", "someone else's work\n")
	publishMain(t, repo)
	runGit(t, repo, "checkout", "-q", "feature")
	runGit(t, repo, "merge", "-q", "--no-edit", "origin/main")

	server, captured := checkCapture(t)
	code, _, stderr := runCLI(t,
		"check", "--root", repo, "--service", server.URL, "--format", "json", "--base", "prereq",
	)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if got := (*captured)[0]["baseCommit"]; got != prereqTip {
		t.Errorf("baseCommit = %v, want the prerequisite's tip %s", got, prereqTip)
	}
	for _, want := range []string{"HEAD merged 1 commit(s) from origin/main that prereq does not have", "--base origin/main"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr should contain %q: %q", want, stderr)
		}
	}
}

// A commit that has already landed on origin's default branch keeps the
// --base it names: HEAD merged nothing from that line, so the base must not
// move to HEAD and empty the patch.
func TestBaseKeepsNamedBaseOnLandedCommit(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	commitFile(t, repo, "landed.txt", "landed work\n")
	commitFile(t, repo, "later.txt", "later work on main\n")
	publishMain(t, repo)
	runGit(t, repo, "checkout", "-q", "--detach", "HEAD~1")
	parent := runGit(t, repo, "rev-parse", "HEAD^")

	server, captured := checkCapture(t)
	code, _, stderr := runCLI(t,
		"check", "--root", repo, "--service", server.URL, "--format", "json", "--base", parent,
	)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	request := (*captured)[0]
	if request["baseCommit"] != parent {
		t.Errorf("baseCommit = %v, want the named base %s", request["baseCommit"], parent)
	}
	if patch := decodedPatch(t, request); !strings.Contains(patch, "b/landed.txt") {
		t.Errorf("patch should carry the landed commit's file:\n%s", patch)
	}
	if strings.Contains(stderr, "patch is empty") || strings.Contains(stderr, "carries their changes") {
		t.Errorf("stderr should not report an empty or oversized patch: %q", stderr)
	}
}
