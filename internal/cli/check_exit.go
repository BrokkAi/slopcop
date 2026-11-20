package cli

import (
	"fmt"

	"github.com/BrokkAi/slopcop/internal/report"
	"github.com/BrokkAi/slopcop/internal/review"
	"github.com/BrokkAi/slopcop/internal/service"
)

// exitReason says which findings made a check exit 1: new static findings at
// or above --fail-on, review findings (which fail under every --fail-on but
// never), or both. It is "" for any other exit.
func exitReason(exit, staticExit int, reviewFailed bool) string {
	if exit != service.ExitFindings {
		return ""
	}
	staticFailed := staticExit == service.ExitFindings
	switch {
	case staticFailed && reviewFailed:
		return report.ExitReasonStaticAndReviewFindings
	case reviewFailed:
		return report.ExitReasonReviewFindings
	default:
		return report.ExitReasonStaticFindings
	}
}

// failingReviewFindings counts the review findings that fail the check.
func failingReviewFindings(v review.Verdict) int {
	return len(v.Findings())
}

// reviewFailureNotice is the stderr line for a check that exited 1 on review
// findings. summary.findings and the human report's headline count only
// static findings, so without it a check failed by the review alone reads as
// zero findings and a clean static check. It is "" when the review did not
// fail the check.
func reviewFailureNotice(reason string, count int, failOn, format string) string {
	if reason != report.ExitReasonReviewFindings && reason != report.ExitReasonStaticAndReviewFindings {
		return ""
	}
	noun := "review findings"
	if count == 1 {
		noun = "review finding"
	}
	where := "under Review above"
	switch format {
	case report.FormatJSON:
		where = "in review.findings (summary.reviewFindings counts them; summary.findings counts static findings only)"
	case report.FormatSARIF:
		where = "in the slopcop-review run"
	}
	what := fmt.Sprintf("the local reviewers reported %d %s, and no static finding crossed --fail-on %s", count, noun, failOn)
	if reason == report.ExitReasonStaticAndReviewFindings {
		what = fmt.Sprintf("new static findings crossed --fail-on %s, and the local reviewers reported %d %s", failOn, count, noun)
	}
	return fmt.Sprintf("slopcop: the check failed (exit 1): %s. The review findings are listed %s. "+
		"Fix the real ones and check again. For one that is wrong, `slopcop override --reason \"why it does not hold\"` approves the tree for push, "+
		"but the check still exits 1; `--fail-on never` keeps review findings from failing it.", what, where)
}
