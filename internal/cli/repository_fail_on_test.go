package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/service"
)

// The rest of BrokkAi/slopcop#207: a repository commits one threshold,
// [check] fail_on, which its pull request checks apply. A check by hand sends
// it when --fail-on is not given, the push gate takes it when it is stricter
// than the default, and the advisory count measures against it.
func TestRepositoryFailOnDecidesExitPushAndAdvisory(t *testing.T) {
	for _, tc := range []struct {
		name string
		// repository is the committed [check] fail_on, "" for none.
		repository string
		// blockOn is the user's gate.blockOn, "" for unset.
		blockOn string
		// flag is --fail-on, "" for none.
		flag     string
		severity string
		// sent is the failOn the service must receive.
		sent     string
		exit     int
		approved bool
		advisory int
		// notice is a fragment stderr must hold, "" for no advisory notice.
		notice string
	}{
		{name: "no threshold keeps the default", severity: "note", sent: "finding", exit: 1, approved: true, advisory: 1,
			notice: "a SlopCop pull request check fails on any new finding"},
		{name: "a note below the repository's warning passes everywhere", repository: "warning", severity: "note", sent: "warning", exit: 0, approved: true},
		{name: "a warning at the repository's warning blocks the push", repository: "warning", severity: "warning", sent: "warning", exit: 1, approved: false},
		{name: "--fail-on wins for its own check", repository: "warning", flag: "finding", severity: "note", sent: "finding", exit: 1, approved: true},
		{name: "the repository cannot lower the push level", repository: "never", severity: "error", sent: "never", exit: 0, approved: false},
		{name: "the user's push level stands", repository: "warning", blockOn: "error", severity: "warning", sent: "warning", exit: 1, approved: true, advisory: 1,
			notice: "fails on new findings at or above warning, this repository's [check] fail_on"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateUserConfig(t)
			installUserGate(t)
			repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
			if tc.repository != "" {
				writeFile(t, repo, ".slopcop/config.toml", fmt.Sprintf("[check]\nfail_on = %q\n", tc.repository))
				runGit(t, repo, "add", "-A")
				runGit(t, repo, "commit", "-qm", "threshold")
			}
			writeFile(t, repo, "fixtures/policy/case.py", "def f(): pass\n")
			cfg := config.Config{Mode: "static"}
			if tc.blockOn != "" {
				cfg.Gate = &config.Gate{BlockOn: tc.blockOn}
			}
			if _, err := config.Save(cfg); err != nil {
				t.Fatal(err)
			}
			rank := map[string]int{"note": 1, "warning": 2, "error": 3}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body service.Request
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request: %v", err)
				}
				if body.FailOn != tc.sent {
					t.Errorf("sent failOn %q, want %q", body.FailOn, tc.sent)
				}
				// The service gates on the failOn it was sent.
				status := "clean"
				if body.FailOn == "finding" || (body.FailOn != "never" && rank[tc.severity] >= rank[body.FailOn]) {
					status = "findings"
				}
				threshold := ""
				if tc.repository != "" {
					threshold = fmt.Sprintf(`,"repositoryFailOn":{"config":".slopcop/config.toml","level":%q,"baseLevel":%q}`, tc.repository, tc.repository)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"status":%q,"baseCommit":"0123456789abcdef0123456789abcdef01234567","applied":true,"summary":{"files":1,"newFindings":1,"failOn":%q},"findings":[{"policyId":"missing-docstring","severity":%q,"certainty":"definite","new":true,"path":"fixtures/policy/case.py","title":"issue"}],"incomplete":[]%s}`,
					status, body.FailOn, tc.severity, threshold)
			}))
			defer server.Close()
			args := []string{"check", "--root", repo, "--service", server.URL, "--format", "json"}
			if tc.flag != "" {
				args = append(args, "--fail-on", tc.flag)
			}
			code, out, stderr := runCLI(t, args...)
			if code != tc.exit {
				t.Fatalf("exit %d, want %d: %s %s", code, tc.exit, out, stderr)
			}
			var doc struct {
				Approved         bool                      `json:"approved"`
				AdvisoryFindings int                       `json:"advisoryFindings"`
				RepositoryFailOn *service.RepositoryFailOn `json:"repositoryFailOn"`
			}
			if err := json.Unmarshal([]byte(out), &doc); err != nil {
				t.Fatal(err)
			}
			if doc.Approved != tc.approved || doc.AdvisoryFindings != tc.advisory {
				t.Fatalf("approved=%v advisoryFindings=%d, want %v and %d: %s", doc.Approved, doc.AdvisoryFindings, tc.approved, tc.advisory, out)
			}
			if (doc.RepositoryFailOn != nil) != (tc.repository != "") {
				t.Fatalf("repositoryFailOn = %+v with repository %q", doc.RepositoryFailOn, tc.repository)
			}
			warned := strings.Contains(stderr, "below the push level")
			if warned != (tc.notice != "") || (tc.notice != "" && !strings.Contains(stderr, tc.notice)) {
				t.Fatalf("stderr %q, want notice %q", stderr, tc.notice)
			}
		})
	}
}

// A change that moves the repository's threshold says so in the human
// report, since a change can lower the bar it is checked against.
func TestHumanReportNamesAMovedRepositoryThreshold(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, ".slopcop/config.toml", "[check]\nfail_on = \"never\"\n")
	if _, err := config.Save(config.Config{Mode: "static"}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"clean","baseCommit":"0123456789abcdef0123456789abcdef01234567","applied":true,"summary":{"files":1,"failOn":"never"},"findings":[],"incomplete":[],"repositoryFailOn":{"config":".slopcop/config.toml","level":"never","changed":true}}`))
	}))
	defer server.Close()
	_, out, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL)
	if !strings.Contains(out, "Threshold: this change sets [check] fail_on to never in .slopcop/config.toml") {
		t.Fatalf("stdout %q stderr %q", out, stderr)
	}
}

// The Stop hook hands an agent only the findings that fail a check at the
// threshold it was sent with or block the push, so a note below the
// repository's fail_on does not hold the agent, and an error the push gate
// blocks always does.
func TestHookHandsOnlyFindingsAtTheThreshold(t *testing.T) {
	findings := []service.Finding{
		{New: true, Severity: "note", Path: "a.py"},
		{New: true, Severity: "warning", Path: "a.py"},
		{New: true, Severity: "info", Path: "a.py"},
		{New: true, Severity: "error", Path: "a.py"},
		{New: false, Severity: "error", Path: "a.py"},
	}
	for level, want := range map[string]int{"finding": 3, "note": 3, "warning": 2, "error": 1, "never": 1} {
		if got := len(handedFindings(findings, level, "error")); got != want {
			t.Errorf("at %s handed %d findings, want %d", level, got, want)
		}
	}
	if got := len(handedFindings(findings, "never", "finding")); got != 3 {
		t.Errorf("push level finding handed %d findings, want 3", got)
	}
}

// The hook caches every new finding and filters when it answers, so a push
// level the user tightens after the check holds the agent on the same cached
// result.
func TestHookAnswerFollowsAPushLevelChangedSinceTheCheck(t *testing.T) {
	var state hookState
	state.completed("w", "f", service.Response{Findings: []service.Finding{{New: true, Severity: "note", Path: "a.py"}}})
	written := map[string]bool{"a.py": true}
	if out := state.answer(written, true, false, true, "warning", "error"); out.Decision == "block" {
		t.Fatalf("a note below both levels held the agent: %+v", out)
	}
	if out := state.answer(written, false, false, true, "warning", "finding"); out.Decision != "block" {
		t.Fatalf("a note at the tightened push level did not hold the agent: %+v", out)
	}
}

// Findings handed over at one push level are handed again when the user
// tightens it, since more of them may now block the push.
func TestHookHandsAgainWhenThePushLevelTightens(t *testing.T) {
	var state hookState
	state.completed("w", "f", service.Response{Findings: []service.Finding{
		{New: true, Severity: "warning", Path: "a.py"},
		{New: true, Severity: "note", Path: "a.py"},
	}})
	written := map[string]bool{"a.py": true}
	if out := state.answer(written, true, false, true, "warning", "error"); out.Decision != "block" || strings.Contains(out.Reason, "note") {
		t.Fatalf("first stop: %+v", out)
	}
	if out := state.answer(written, false, false, true, "warning", "error"); out.Decision == "block" {
		t.Fatalf("the same levels handed the findings again: %+v", out)
	}
	if out := state.answer(written, false, false, true, "warning", "finding"); out.Decision != "block" {
		t.Fatalf("a tightened push level did not hand the note over: %+v", out)
	}
}

// A repository's fail_on rates static severities only: a change that sets it
// to never cannot keep its own review findings from failing the check.
func TestRepositoryFailOnNeverLeavesReviewFindingsFailing(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "")
	writeFile(t, repo, ".slopcop/config.toml", "[check]\nfail_on = \"never\"\n")
	writeFile(t, repo, "README.md", "changed\n")
	t.Setenv("SLOPCOP_FAKE_ACP", "1")
	cfg := config.Config{Mode: "ai", Review: &config.Review{Reviewers: []string{"terra"}, Adapters: map[string]config.Adapter{
		"codex": {Command: []string{os.Args[0], "-test.run=^TestFakeAgent$", "--fake-mode=findings"}},
	}}}
	if _, err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runCLI(t, "check", "--root", repo, "--format", "json")
	if code != 1 || !strings.Contains(out, `"exitReason": "review_findings"`) {
		t.Fatalf("exit %d, stdout %s stderr %s", code, out, stderr)
	}
}
