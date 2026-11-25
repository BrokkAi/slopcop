package prcomment

import (
	"fmt"
	"strings"

	"github.com/BrokkAi/slopcop/internal/report"
	"github.com/BrokkAi/slopcop/internal/review"
	"github.com/BrokkAi/slopcop/internal/service"
)

const (
	// summaryMarker identifies the summary comment, so a later run updates
	// it rather than adding another.
	summaryMarker = "<!-- slopcop:summary -->"
	// findingMarkerPrefix starts the marker that identifies the inline
	// comment for one finding, so a later run does not comment it again.
	findingMarkerPrefix = "<!-- slopcop:finding:"
	// maxTextRunes bounds the quoted title or message of one finding.
	maxTextRunes = 2000
	// maxListed bounds the findings the summary lists itself; the rest are
	// counted.
	maxListed = 50
	// maxSummaryBytes bounds the summary comment under GitHub's
	// 65,536-character limit, which bytes never undercount. The findings
	// list gets what the rest leaves, and counts the findings it leaves out.
	maxSummaryBytes = 60000
	// maxIncompleteBytes bounds the list of rules that did not finish.
	maxIncompleteBytes = 8000
	docsExitCodes      = "https://docs.slopcop.com/checks/exit-codes/"
	docsFalsePosURL    = "https://docs.slopcop.com/checks/false-positives/"
)

// kind says whether an item is a static or a review finding.
type kind int

const (
	staticKind kind = iota
	reviewKind
)

// item is one finding to comment: a new static finding, or one review
// finding with every reviewer that reported it.
type item struct {
	// key marks the item's comment.
	key string
	// aliases are the other keys an earlier comment of the same finding may
	// be marked with: a static finding's id, which marked it before the
	// service answered commentKey.
	aliases    []string
	kind       kind
	path       string
	start, end int
	static     service.Finding
	review     review.Finding
	reviewers  []string
}

// items gathers the report's new static findings and its review findings,
// one item per review finding id however many reviewers reported it.
func items(r Report) []item {
	var all []item
	for _, f := range r.NewStatic() {
		// A finding's id is new in each check, so a later check would
		// comment it again; its comment key is not.
		key, aliases := f.CommentKey, []string{f.ID}
		if key == "" {
			key, aliases = f.ID, nil
		}
		if key == "" {
			key = fmt.Sprintf("%s:%s:%d", f.PolicyID, f.Path, deref(f.StartLine))
		}
		all = append(all, item{key: key, aliases: aliases, kind: staticKind, path: f.Path, start: deref(f.StartLine), end: deref(f.EndLine), static: f})
	}
	byID := map[string]int{}
	for _, f := range r.ReviewFindings() {
		key := f.ID
		if key == "" {
			key = fmt.Sprintf("review:%s:%s", f.Path, f.Title)
		}
		if at, seen := byID[key]; seen {
			all[at].reviewers = appendNew(all[at].reviewers, f.Reviewer)
			continue
		}
		byID[key] = len(all)
		all = append(all, item{key: key, kind: reviewKind, path: f.Path, start: deref(f.StartLine), end: deref(f.EndLine), review: f, reviewers: appendNew(nil, f.Reviewer)})
	}
	return all
}

func appendNew(list []string, value string) []string {
	if value == "" {
		return list
	}
	for _, have := range list {
		if have == value {
			return list
		}
	}
	return append(list, value)
}

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// marker is the hidden line that identifies an item's inline comment.
func (it item) marker() string {
	return findingMarkerPrefix + safeMarker(it.key) + " -->"
}

// commentedIn reports whether a marker in commented already names the item,
// by its key or by one of its aliases.
func (it item) commentedIn(commented map[string]bool) bool {
	for _, key := range append([]string{it.key}, it.aliases...) {
		if key != "" && commented[safeMarker(key)] {
			return true
		}
	}
	return false
}

// safeMarker keeps a key from closing the HTML comment it sits in.
func safeMarker(key string) string {
	return strings.NewReplacer("-->", "", "<", "", ">", "", "\n", " ").Replace(key)
}

// title is the item's one-line heading: severity or priority, then the
// title.
func (it item) title() string {
	if it.kind == staticKind {
		return fmt.Sprintf("[%s] %s", quote(it.static.Severity), quote(it.static.Title))
	}
	if it.review.Priority != "" {
		return fmt.Sprintf("[%s] %s", quote(it.review.Priority), quote(it.review.Title))
	}
	return quote(it.review.Title)
}

// location is the item's path and lines, such as `src/a.go:12-14`.
func (it item) location() string {
	if it.path == "" {
		return ""
	}
	where := it.path
	if it.start > 0 {
		where += fmt.Sprintf(":%d", it.start)
		if it.end > it.start {
			where += fmt.Sprintf("-%d", it.end)
		}
	}
	return "`" + strings.ReplaceAll(report.Printable(where), "`", "'") + "`"
}

// detail is the item's explanation and how to answer it.
func (it item) detail() string {
	var b strings.Builder
	if it.kind == staticKind {
		if it.static.Message != "" {
			b.WriteString(quote(it.static.Message))
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "<sub>Static rule `%s`", code(it.static.PolicyID))
		if it.static.Certainty != "" {
			fmt.Fprintf(&b, ", %s", quote(it.static.Certainty))
		}
		if it.static.ID != "" {
			fmt.Fprintf(&b, ". A false positive? `slopcop ignore %s` keeps it out of later checks ([how](%s))", code(it.static.ID), docsFalsePosURL)
		}
		b.WriteString(".</sub>")
		return b.String()
	}
	if it.review.Message != "" {
		b.WriteString(quote(it.review.Message))
		b.WriteString("\n\n")
	}
	b.WriteString("<sub>From the local LLM review")
	if len(it.reviewers) > 0 {
		quoted := make([]string, len(it.reviewers))
		for i, name := range it.reviewers {
			quoted[i] = "`" + code(name) + "`"
		}
		fmt.Fprintf(&b, " (%s)", strings.Join(quoted, ", "))
	}
	if it.review.ID != "" {
		fmt.Fprintf(&b, ", finding `%s`", code(it.review.ID))
	}
	b.WriteString(". A reviewer's finding is a judgement, not a proof: check it against the code.</sub>")
	return b.String()
}

// inlineBody is the text of an item's inline comment.
func (it item) inlineBody() string {
	label := "SlopCop"
	if it.kind == reviewKind {
		label = "SlopCop review"
	}
	return fmt.Sprintf("**%s** · **%s**\n\n%s\n\n%s", label, it.title(), it.detail(), it.marker())
}

// quote renders repository or reviewer text as inert markdown: control
// characters replaced, HTML tags and entities shown as text, and @ broken so
// no one is mentioned. It is bounded to maxTextRunes.
func quote(text string) string {
	text = report.Printable(text)
	if runes := []rune(text); len(runes) > maxTextRunes {
		text = string(runes[:maxTextRunes]) + "…"
	}
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "@", "@​").Replace(text)
}

// code renders an identifier inside backticks.
func code(text string) string {
	return strings.ReplaceAll(report.Printable(text), "`", "'")
}

// Outcome is what the check found, in words, and the counts the action
// exports.
type Outcome struct {
	// Headline is one sentence, such as "SlopCop found 3 new findings (1
	// static, 2 from the review)".
	Headline       string
	Status         string
	ExitReason     string
	StaticFindings int
	ReviewFindings int
}

// outcome reads the report's result.
func outcome(r Report, all []item) Outcome {
	o := Outcome{Status: r.Status, ExitReason: r.ExitReason}
	for _, it := range all {
		if it.kind == staticKind {
			o.StaticFindings++
		} else {
			o.ReviewFindings++
		}
	}
	if r.Summary != nil && r.Summary.NewFindings > o.StaticFindings {
		o.StaticFindings = r.Summary.NewFindings
	}
	switch r.Status {
	case "clean":
		if r.EmptyChange {
			o.Headline = "SlopCop: no change to check"
		} else {
			o.Headline = "SlopCop found no new findings"
		}
	case "findings":
		o.Headline = "SlopCop found " + findingsPhrase(o.StaticFindings, o.ReviewFindings)
	case "incomplete":
		o.Headline = "SlopCop could not finish every rule, so this is not a pass"
		if total := o.StaticFindings + o.ReviewFindings; total > 0 {
			o.Headline = "SlopCop found " + findingsPhrase(o.StaticFindings, o.ReviewFindings) + ", and could not finish every rule"
		}
	case "pending":
		o.Headline = "SlopCop's check was still running when the timeout ran out"
	case "skipped":
		o.Headline = "SlopCop did not check this change"
	default:
		o.Headline = "SlopCop could not check this change"
	}
	return o
}

func findingsPhrase(static, reviewCount int) string {
	total := static + reviewCount
	noun := "new findings"
	if total == 1 {
		noun = "new finding"
	}
	switch {
	case static > 0 && reviewCount > 0:
		return fmt.Sprintf("%d %s (%d from the static check, %d from the review)", total, noun, static, reviewCount)
	case reviewCount > 0:
		return fmt.Sprintf("%d %s, all from the review", total, noun)
	case total == 0:
		return "new findings"
	default:
		return fmt.Sprintf("%d %s", total, noun)
	}
}

// summary is the summary comment's text: the outcome, what each half of the
// check said, the findings no line of the diff could hold, and what to do.
// inline counts the findings that have an inline comment, this run's or an
// earlier one's. onPR marks the text as the pull request's summary comment.
func summary(r Report, o Outcome, elsewhere []item, inline int, commit string, onPR bool) string {
	head := summaryHead(r, o, inline, onPR)
	tail := summaryTail(r, commit)
	if len(elsewhere) == 0 {
		return head + tail
	}
	var b strings.Builder
	b.WriteString(head)
	if onPR || inline > 0 {
		// Off the diff, past the inline cap, or on a line GitHub refused:
		// the list does not say which.
		b.WriteString("**Not commented on a line**\n\n")
	} else {
		b.WriteString("**Findings**\n\n")
	}
	// The head quotes at most a few bounded texts and the tail is bounded,
	// so the budget is always well above one entry.
	budget := maxSummaryBytes - b.Len() - len(tail) - 100
	listed, used := 0, 0
	for _, it := range elsewhere {
		entry := listEntry(it)
		if listed == maxListed || used+len(entry) > budget {
			break
		}
		b.WriteString(entry)
		listed++
		used += len(entry)
	}
	if rest := len(elsewhere) - listed; rest > 0 {
		fmt.Fprintf(&b, "- and %d more; the job log has the full report\n", rest)
	}
	b.WriteString("\n")
	b.WriteString(tail)
	return b.String()
}

// summaryHead is the summary's marker, headline, what each half of the
// check said, any failure, and how many findings have inline comments.
func summaryHead(r Report, o Outcome, inline int, onPR bool) string {
	var b strings.Builder
	if onPR {
		b.WriteString(summaryMarker + "\n")
	}
	icon := "✅"
	switch r.Status {
	case "clean":
	case "findings":
		icon = "❌"
	default:
		icon = "⚠️"
	}
	fmt.Fprintf(&b, "### %s %s\n\n", icon, quote(o.Headline))
	b.WriteString(staticLine(r, o))
	b.WriteString(reviewLine(r, o))
	b.WriteString("\n")
	failOn := ""
	if r.Summary != nil {
		failOn = r.Summary.FailOn
	}
	b.WriteString(thresholdSection(r.RepositoryFailOn, failOn))
	if r.Status != "clean" && r.Status != "findings" && r.Status != "incomplete" && (r.Code != "" || r.Message != "") {
		fmt.Fprintf(&b, "> %s%s\n", codePrefix(r.Code), quote(r.Message))
		if r.Remedy != "" {
			fmt.Fprintf(&b, ">\n> %s\n", quote(r.Remedy))
		}
		b.WriteString("\n")
	}
	if inline > 0 {
		text := fmt.Sprintf("%d findings are commented on the lines they concern.", inline)
		if inline == 1 {
			text = "1 finding is commented on the line it concerns."
		}
		b.WriteString(text + "\n\n")
	}
	return b.String()
}

// thresholdSection names the repository's [check] fail_on when it is set, and
// says so under its own heading when the pull request itself moves it, since
// a change can lower the bar it is checked against. failOn is the threshold
// that decided the check; when a --fail-on of the job's own overrode the
// repository's, the section names both.
func thresholdSection(t *service.RepositoryFailOn, failOn string) string {
	if t == nil {
		return ""
	}
	level := func(value string) string {
		if value == "" {
			return "none (any new finding)"
		}
		return "`" + code(bounded(value)) + "`"
	}
	var b strings.Builder
	if t.Changed {
		fmt.Fprintf(&b, "**This change moves what fails its check**: `[check] fail_on` in `%s` goes from %s to %s.\n\n",
			code(bounded(t.Config)), level(t.BaseLevel), level(t.Level))
	}
	switch {
	case failOn != "" && failOn != t.Level:
		fmt.Fprintf(&b, "This job fails at %s, set by its own `fail-on`; the repository's `[check] fail_on` is %s.\n\n", level(failOn), level(t.Level))
	case !t.Changed:
		fmt.Fprintf(&b, "Fails at %s, from `[check] fail_on` in `%s`.\n\n", level(t.Level), code(bounded(t.Config)))
	}
	return b.String()
}

// summaryTail is the rules that did not finish, bounded, and the footer.
func summaryTail(r Report, commit string) string {
	var b strings.Builder
	if len(r.Incomplete) > 0 {
		b.WriteString("<details><summary>Rules that did not finish</summary>\n\n")
		listed, used := 0, 0
		for _, inc := range r.Incomplete {
			entry := fmt.Sprintf("- `%s`: %s\n", code(bounded(inc.PolicyID)), quote(bounded(inc.Reason)))
			if listed == maxListed || used+len(entry) > maxIncompleteBytes {
				break
			}
			b.WriteString(entry)
			listed++
			used += len(entry)
		}
		if rest := len(r.Incomplete) - listed; rest > 0 {
			fmt.Fprintf(&b, "- and %d more\n", rest)
		}
		b.WriteString("\n</details>\n\n")
	}
	b.WriteString("<sub>")
	if commit != "" {
		fmt.Fprintf(&b, "Commit `%s`. ", code(shortSHA(commit)))
	}
	if r.CheckID != "" {
		fmt.Fprintf(&b, "Check `%s`. ", code(bounded(r.CheckID)))
	}
	fmt.Fprintf(&b, "[What the result means](%s).</sub>\n", docsExitCodes)
	return b.String()
}

// bounded cuts an identifier or reason a service sends to a sane length.
func bounded(text string) string {
	if runes := []rune(text); len(runes) > 200 {
		return string(runes[:200]) + "…"
	}
	return text
}

// listEntry is one finding in the summary's list.
func listEntry(it item) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- **%s**", it.title())
	if where := it.location(); where != "" {
		fmt.Fprintf(&b, " %s", where)
	}
	b.WriteString("\n")
	detail := strings.ReplaceAll(it.detail(), "\n\n", "\n")
	for _, line := range strings.Split(detail, "\n") {
		fmt.Fprintf(&b, "  %s\n", line)
	}
	return b.String()
}

func codePrefix(c string) string {
	if c == "" {
		return ""
	}
	return "`" + code(c) + "`: "
}

// staticLine says what the hosted static check found.
func staticLine(r Report, o Outcome) string {
	status := r.StaticStatus
	if status == "" {
		status = r.Status
	}
	if r.Summary == nil && r.Review != nil && r.StaticStatus == "" {
		return "- **Static check:** did not run\n"
	}
	existing := ""
	if r.Summary != nil && r.Summary.ExistingFindings > 0 {
		existing = fmt.Sprintf(" (%d already in the code, which never fail the check)", r.Summary.ExistingFindings)
	}
	switch status {
	case "clean":
		return "- **Static check:** no new findings" + existing + "\n"
	case "findings":
		noun := "new findings"
		if o.StaticFindings == 1 {
			noun = "new finding"
		}
		if o.StaticFindings == 0 {
			return "- **Static check:** no new findings" + existing + "\n"
		}
		return fmt.Sprintf("- **Static check:** %d %s%s\n", o.StaticFindings, noun, existing)
	case "incomplete":
		return fmt.Sprintf("- **Static check:** %d new findings; some rules did not finish%s\n", o.StaticFindings, existing)
	case "pending":
		return "- **Static check:** still running when the timeout ran out\n"
	case "skipped":
		return "- **Static check:** skipped\n"
	default:
		return "- **Static check:** did not complete\n"
	}
}

// reviewLine says what the local review found, or why it did not run.
func reviewLine(r Report, o Outcome) string {
	if r.Review == nil {
		if r.Skipped != "" {
			return "- **Review:** did not run: " + quote(r.Skipped) + "\n"
		}
		return ""
	}
	names := make([]string, 0, len(r.Review.Reviewers))
	for _, reviewer := range r.Review.Reviewers {
		names = append(names, "`"+code(reviewer.Name)+"`")
	}
	by := ""
	if len(names) > 0 {
		by = " by " + strings.Join(names, ", ")
	}
	switch {
	case r.Review.Status == string(review.StatusFailed) || r.Review.Error != "":
		message := "did not complete"
		if r.Review.Error != "" {
			message += ": " + quote(r.Review.Error)
		}
		return "- **Review" + by + ":** " + message + "\n"
	}
	incomplete := ""
	if r.Review.Incomplete {
		incomplete = "; not every reviewer finished, so this is not a full review"
	}
	switch o.ReviewFindings {
	case 0:
		return "- **Review" + by + ":** no findings" + incomplete + "\n"
	case 1:
		return "- **Review" + by + ":** 1 finding" + incomplete + "\n"
	default:
		return fmt.Sprintf("- **Review%s:** %d findings%s\n", by, o.ReviewFindings, incomplete)
	}
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
