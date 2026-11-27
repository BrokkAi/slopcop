package cli

import (
	"fmt"
	"strings"

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
// fail the check. overridable says `slopcop override` would accept the
// findings now; only then does the notice offer it.
func reviewFailureNotice(reason string, count int, failOn, format string, overridable bool) string {
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
	remedy := "Fix the real ones and check again. For one that is wrong, run `slopcop override <rv-id> --reason \"why it does not hold\"` " +
		"with its id from the report, then check again: the same review is reused and leaves it out, and later reviews do not report it. " +
		"`slopcop override --reason` for the whole review cannot accept them yet: it needs every reviewer to finish and no static result that blocks the push"
	if overridable {
		remedy = "Fix the real ones and check again. For one that is wrong, run `slopcop override <rv-id> --reason \"why it does not hold\"` " +
			"with its id from the report, then check again: the same review is reused and leaves it out, and later reviews do not report it. " +
			"To accept every finding for this tree only, `slopcop override --reason \"why they do not hold\"` approves it for push, but the check still exits 1"
	}
	return fmt.Sprintf("slopcop: the check failed (exit 1): %s. The review findings are listed %s. %s; "+
		"`--fail-on never` keeps review findings from failing it.", what, where, remedy)
}

// incompleteNoticeNamed is how many incomplete policies the stderr notice
// names before it counts the rest.
const incompleteNoticeNamed = 5

// incompletePoliciesNotice is the stderr line for a static check in which
// some policies did not finish, such as one that timed out. The human report
// names them on stdout already; in the JSON and SARIF formats they are only
// in the document, so a reader of the stderr lines alone would take a check
// with no findings for a complete one. It is "" for the human format, when
// every policy finished, and when the whole analysis failed, which the
// document's failure says instead.
func incompletePoliciesNotice(response *service.Response, format string) string {
	if response == nil || format == report.FormatHuman || response.Failure != "" {
		return ""
	}
	type named struct{ id, reason string }
	policies := make([]named, 0, len(response.Incomplete))
	for _, policy := range response.Incomplete {
		policies = append(policies, named{policy.PolicyID, policy.Reason})
	}
	// A service that sent only the ids gave no reasons.
	if len(policies) == 0 {
		for _, id := range response.Summary.IncompletePolicyIDs {
			policies = append(policies, named{id: id})
		}
	}
	count := max(response.Summary.IncompletePolicies, len(policies))
	if count == 0 {
		return ""
	}
	names := make([]string, 0, incompleteNoticeNamed+1)
	for i, policy := range policies {
		if i == incompleteNoticeNamed {
			break
		}
		reason := strings.ReplaceAll(strings.TrimSpace(policy.reason), "_", " ")
		if reason == "" {
			reason = "no reason given"
		}
		names = append(names, fmt.Sprintf("%s (%s)", report.Printable(policy.id), report.Printable(reason)))
	}
	if rest := count - len(names); rest > 0 && len(names) > 0 {
		names = append(names, fmt.Sprintf("and %d more", rest))
	}
	subject := fmt.Sprintf("%d policies didn't finish", count)
	them := "them"
	if count == 1 {
		subject = "1 policy didn't finish"
		them = "it"
	}
	where := "summary.incompletePolicyIds and incomplete list them"
	if format == report.FormatSARIF {
		where = "the run's tool notifications list them"
	}
	if len(names) > 0 {
		subject += ": " + strings.Join(names, ", ")
	}
	return fmt.Sprintf("slopcop: the check is incomplete: %s. The code wasn't checked against %s, "+
		"so it may have findings this check did not report; %s.", subject, them, where)
}
