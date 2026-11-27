package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/report"
	"github.com/BrokkAi/slopcop/internal/service"
)

func TestExitReason(t *testing.T) {
	for _, tc := range []struct {
		exit, staticExit int
		reviewFailed     bool
		want             string
	}{
		{service.ExitFindings, service.ExitFindings, false, report.ExitReasonStaticFindings},
		{service.ExitFindings, service.ExitClean, true, report.ExitReasonReviewFindings},
		{service.ExitFindings, service.ExitNeedsPerson, true, report.ExitReasonReviewFindings},
		{service.ExitFindings, service.ExitFindings, true, report.ExitReasonStaticAndReviewFindings},
		{service.ExitClean, service.ExitClean, false, ""},
		{service.ExitIncomplete, service.ExitClean, false, ""},
	} {
		if got := exitReason(tc.exit, tc.staticExit, tc.reviewFailed); got != tc.want {
			t.Errorf("exitReason(%d, %d, %v) = %q, want %q", tc.exit, tc.staticExit, tc.reviewFailed, got, tc.want)
		}
	}
}

func TestReviewFailureNotice(t *testing.T) {
	if got := reviewFailureNotice(report.ExitReasonStaticFindings, 0, "finding", report.FormatJSON, true); got != "" {
		t.Errorf("static-only failure got a review notice: %q", got)
	}
	if got := reviewFailureNotice("", 2, "finding", report.FormatJSON, true); got != "" {
		t.Errorf("no failure got a review notice: %q", got)
	}
	for _, tc := range []struct {
		reason, format string
		count          int
		overridable    bool
		want           []string
	}{
		{report.ExitReasonReviewFindings, report.FormatJSON, 1, true, []string{
			"reported 1 review finding,", "no static finding crossed --fail-on warning",
			"review.findings", "summary.findings counts static findings only",
			"`slopcop override <rv-id> --reason", "`slopcop override --reason \"why they do not hold\"` approves it for push", "--fail-on never",
		}},
		// An override of the whole review would be refused, so it is not
		// offered; overriding one finding still is.
		{report.ExitReasonReviewFindings, report.FormatJSON, 1, false, []string{
			"`slopcop override <rv-id> --reason", "for the whole review cannot accept them yet", "every reviewer to finish", "--fail-on never",
		}},
		{report.ExitReasonReviewFindings, report.FormatHuman, 2, true, []string{"reported 2 review findings,", "under Review above"}},
		{report.ExitReasonReviewFindings, report.FormatSARIF, 2, true, []string{"slopcop-review run"}},
		{report.ExitReasonStaticAndReviewFindings, report.FormatJSON, 3, false, []string{
			"new static findings crossed --fail-on warning", "reported 3 review findings.", "cannot accept them yet",
		}},
	} {
		got := reviewFailureNotice(tc.reason, tc.count, "warning", tc.format, tc.overridable)
		if offered := strings.Contains(got, "approves it for push"); offered != tc.overridable {
			t.Errorf("%s/%s override offered = %v, want %v: %q", tc.reason, tc.format, offered, tc.overridable, got)
		}
		if !strings.HasPrefix(got, "slopcop: the check failed (exit 1): ") {
			t.Errorf("%s/%s notice = %q", tc.reason, tc.format, got)
		}
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Errorf("%s/%s notice = %q, want %q", tc.reason, tc.format, got, want)
			}
		}
	}
}

// A check the review alone fails says why on stderr in the human format too,
// where the headline counts only static findings.
func TestHumanCheckExplainsAReviewOnlyFailure(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "README.md", "changed\n")
	t.Setenv("SLOPCOP_FAKE_ACP", "1")
	cfg := config.Config{Mode: "both", Review: &config.Review{Reviewers: []string{"terra"}, Adapters: map[string]config.Adapter{"codex": {Command: []string{os.Args[0], "-test.run=^TestFakeAgent$", "--fake-mode=findings"}}}}}
	if _, err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"clean","baseCommit":"0123456789abcdef0123456789abcdef01234567","applied":true,"summary":{"files":1,"newFindings":0,"incompletePolicies":0,"failOn":"finding"},"findings":[],"incomplete":[]}`)
	}))
	defer server.Close()
	code, out, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL)
	if code != service.ExitFindings {
		t.Fatalf("exit %d: stdout %s stderr %s", code, out, stderr)
	}
	if !strings.Contains(out, "Review: findings") {
		t.Errorf("report does not show the review findings: %s", out)
	}
	for _, want := range []string{"slopcop: the check failed (exit 1): the local reviewers reported 1 review finding", "under Review above"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %s, want %q", stderr, want)
		}
	}
}

func TestIncompletePoliciesNotice(t *testing.T) {
	loop := service.IncompletePolicy{PolicyID: "bifrost.correctness.loop-body-never-repeats", Reason: "timed_out"}
	self := service.IncompletePolicy{PolicyID: "bifrost.correctness.local-self-assignment", Reason: "timed_out"}
	withIncomplete := func(count int, policies ...service.IncompletePolicy) *service.Response {
		response := &service.Response{Status: service.StatusIncomplete, Incomplete: policies}
		response.Summary.IncompletePolicies = count
		for _, policy := range policies {
			response.Summary.IncompletePolicyIDs = append(response.Summary.IncompletePolicyIDs, policy.PolicyID)
		}
		return response
	}
	for _, tc := range []struct {
		name     string
		response *service.Response
		format   string
		want     string
	}{
		{"no response", nil, report.FormatJSON, ""},
		{"none incomplete", withIncomplete(0), report.FormatJSON, ""},
		{"human report names them itself", withIncomplete(1, loop), report.FormatHuman, ""},
		{"one", withIncomplete(1, loop), report.FormatJSON,
			"slopcop: the check is incomplete: 1 policy didn't finish: bifrost.correctness.loop-body-never-repeats (timed out). " +
				"The code wasn't checked against it, so it may have findings this check did not report; " +
				"summary.incompletePolicyIds and incomplete list them."},
		{"several", withIncomplete(2, loop, self), report.FormatJSON,
			"slopcop: the check is incomplete: 2 policies didn't finish: bifrost.correctness.loop-body-never-repeats (timed out), " +
				"bifrost.correctness.local-self-assignment (timed out). " +
				"The code wasn't checked against them, so it may have findings this check did not report; " +
				"summary.incompletePolicyIds and incomplete list them."},
		{"reason missing", withIncomplete(1, service.IncompletePolicy{PolicyID: "acme.slow"}), report.FormatSARIF,
			"slopcop: the check is incomplete: 1 policy didn't finish: acme.slow (no reason given). " +
				"The code wasn't checked against it, so it may have findings this check did not report; " +
				"the run's tool notifications list them."},
	} {
		if got := incompletePoliciesNotice(tc.response, tc.format); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}

	// Only ids: no reasons to give.
	idsOnly := withIncomplete(1)
	idsOnly.Summary.IncompletePolicyIDs = []string{"acme.slow"}
	if got := incompletePoliciesNotice(idsOnly, report.FormatJSON); !strings.Contains(got, "1 policy didn't finish: acme.slow (no reason given).") {
		t.Errorf("ids only = %q", got)
	}
	// More than it names: the rest are counted, including those the service
	// counted but did not name.
	many := withIncomplete(60)
	for i := range 7 {
		many.Incomplete = append(many.Incomplete, service.IncompletePolicy{PolicyID: fmt.Sprintf("acme.p%d", i), Reason: "timed_out"})
	}
	got := incompletePoliciesNotice(many, report.FormatJSON)
	if !strings.Contains(got, "60 policies didn't finish: acme.p0 (timed out),") || !strings.Contains(got, "acme.p4 (timed out), and 55 more.") ||
		strings.Contains(got, "acme.p5") {
		t.Errorf("many = %q", got)
	}
	// Counted but none named.
	unnamed := withIncomplete(3)
	if got := incompletePoliciesNotice(unnamed, report.FormatJSON); !strings.HasPrefix(got, "slopcop: the check is incomplete: 3 policies didn't finish. ") {
		t.Errorf("unnamed = %q", got)
	}
	// The whole analysis failing is the document's failure, not this notice.
	failed := withIncomplete(1, loop)
	failed.Failure = "The analysis could not be completed."
	if got := incompletePoliciesNotice(failed, report.FormatJSON); got != "" {
		t.Errorf("failed analysis = %q", got)
	}
}
