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
