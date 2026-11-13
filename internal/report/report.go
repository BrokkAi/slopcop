// Package report renders a check response in the requested format. The
// service answers structure only; these renderers follow the ones it used to
// run before it stopped rendering.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"

	"github.com/BrokkAi/slopcop/internal/review"
	"github.com/BrokkAi/slopcop/internal/service"
)

// Formats the CLI accepts.
const (
	FormatHuman = "human"
	FormatJSON  = "json"
	FormatSARIF = "sarif"
)

const sarifSchema = "https://docs.oasis-open.org/sarif/sarif/v2.1.0/errata01/os/schemas/sarif-schema-2.1.0.json"

// Options shape what a report lists. Rendering never changes the check, so
// none of them is sent to the service.
type Options struct {
	// Approved says this run recorded push approval for the checked tree.
	Approved bool
	// Review is the local reviewers' verdict, when one ran.
	Review *review.Verdict
	// Status overrides the service status with the combined check status.
	Status string
	// StaticStatus preserves the service status when it differs.
	StaticStatus string
	// ShowExisting lists the findings that were already in the code, after
	// the new ones. They are counted either way.
	ShowExisting bool
	// EmptyChange marks a check whose patch was empty: the working tree
	// matched the base, so no change was checked. The human format leaves it
	// to the CLI's note on stderr.
	EmptyChange bool
	// Verbose lists the inconclusive checks in the human format, which
	// otherwise only counts them, and the files and rules each
	// .slopcop/config.toml exclusion resolved to. The machine formats always
	// list them.
	Verbose bool
	// SkillUpdated lists the skills directories whose slopcop skill this run
	// installed or replaced, so an agent knows to reread its instructions.
	// The human format leaves it to the CLI's note on stderr.
	SkillUpdated []SkillUpdate
}

// SkillUpdate is one skills directory whose slopcop skill a run wrote.
type SkillUpdate struct {
	Dir string `json:"dir"`
	// From is the version that was there: a release, "unknown" for a skill
	// that matches none, or empty when there was no skill.
	From string `json:"from,omitempty"`
	To   string `json:"to"`
	// Changed names the files, relative to Dir, that were added, rewritten or
	// removed.
	Changed []string `json:"changed"`
	// Backup is where a copy of a skill with local edits was kept.
	Backup string `json:"backup,omitempty"`
}

// writeBase renders the static response to w.
func writeBase(w io.Writer, response service.Response, format string, options Options) error {
	switch format {
	case "", FormatHuman:
		_, err := io.WriteString(w, human(response, options))
		return err
	case FormatJSON:
		return writeJSON(w, response, options)
	case FormatSARIF:
		return writeSARIF(w, response, options)
	default:
		return fmt.Errorf("unknown format %q (expected human, json, or sarif)", format)
	}
}

// split returns the new findings, then the existing ones, each in the
// service's order.
func split(findings []service.Finding) (fresh, existing []service.Finding) {
	fresh, existing = []service.Finding{}, []service.Finding{}
	for _, finding := range findings {
		if finding.New {
			fresh = append(fresh, finding)
		} else {
			existing = append(existing, finding)
		}
	}
	return fresh, existing
}

// splitRemapped returns the existing findings that match an earlier ignore,
// then the rest. A finding that matches one is always listed, so a reader
// learns the ignore no longer covers it whether or not the service counts it
// as new.
func splitRemapped(existing []service.Finding) (remapped, rest []service.Finding) {
	remapped, rest = []service.Finding{}, []service.Finding{}
	for _, finding := range existing {
		if finding.PreviouslyIgnored != "" {
			remapped = append(remapped, finding)
		} else {
			rest = append(rest, finding)
		}
	}
	return remapped, rest
}

// listed is what a report lists: the new findings first, then the existing
// ones that match an earlier ignore, and the other existing ones after them
// only when asked for.
func listed(response service.Response, options Options) []service.Finding {
	fresh, existing := split(response.Findings)
	remapped, rest := splitRemapped(existing)
	findings := append(fresh, remapped...)
	if options.ShowExisting {
		findings = append(findings, rest...)
	}
	return findings
}

// remappedCount is how many findings match an earlier ignore. A service from
// before summary.remappedIgnores sends none, so the listed ones count too.
func remappedCount(response service.Response) int {
	flagged := 0
	for _, finding := range response.Findings {
		if finding.PreviouslyIgnored != "" {
			flagged++
		}
	}
	return max(response.Summary.RemappedIgnores, flagged)
}

// human is close to Bifrost's own human report: one block per new finding,
// then the existing ones when asked for, then the summary line, then what was
// left out or did not complete.
func human(response service.Response, options Options) string {
	var out strings.Builder
	fresh, existing := split(response.Findings)
	remapped, rest := splitRemapped(existing)
	writeBlock(&out, "New findings", fresh)
	writeBlock(&out, "Previously ignored, code changed (never fail the check)", remapped)
	if options.ShowExisting {
		writeBlock(&out, "Already in the code (never fail the check)", rest)
	}
	// A comment is the easiest way to silence a finding, so every finding one
	// accepted is shown, not only counted.
	writeInlineSuppressed(&out, response)
	writeDirectiveWarnings(&out, response)
	out.WriteString(summaryLine(response))
	out.WriteString("\n")
	if notice := omittedNotice(response); notice != "" {
		fmt.Fprintf(&out, "Omitted: %s.\n", notice)
	}
	for _, failure := range response.RunFailures {
		out.WriteString(runFailureLine(failure))
		out.WriteString("\n")
	}
	if line := incompleteLine(response); line != "" {
		out.WriteString(line)
		out.WriteString("\n")
	}
	// One that could not prove there is nothing more to find is the norm for
	// static analysis, so by default they get one line, for a reader judging
	// what a clean result is worth.
	if options.Verbose {
		writeInconclusive(&out, response)
	} else if line := inconclusiveLine(response); line != "" {
		out.WriteString(line)
		out.WriteString("\n")
	}
	writeExclusions(&out, response.Exclusions, options.Verbose)
	return out.String()
}

// onlyExcluded says .slopcop/config.toml [check] left the change nothing to
// check, so its clean status means nothing ran.
func onlyExcluded(response service.Response) bool {
	return response.Summary.OnlyExcluded ||
		(response.Exclusions != nil && response.Exclusions.OnlyExcluded && response.Status == service.StatusClean)
}

// writeExclusions lists what .slopcop/config.toml [check] excluded, each
// scope with its reason, and warns about every rule that matched nothing.
// Verbose adds the changed files and rules each scope resolved to. An
// exclusion is never silent, so the scopes are listed either way.
func writeExclusions(out *strings.Builder, exclusions *service.Exclusions, verbose bool) {
	if exclusions == nil {
		return
	}
	fmt.Fprintf(
		out,
		"Excluded by %s: %d changed file(s) for every rule, %d (file, rule) pair(s) for named rules\n",
		printable(exclusions.Config), exclusions.ExcludedFiles, exclusions.ExcludedPairs,
	)
	for _, scope := range exclusions.Scopes {
		rules := "every rule"
		if !scope.AllRules {
			rules = printableList(scope.RulePatterns)
		}
		fmt.Fprintf(out, "  %s for %s: %d changed file(s)", printableList(scope.Paths), rules, scope.FileCount)
		if scope.Reason != "" {
			fmt.Fprintf(out, " (%s)", printable(scope.Reason))
		}
		out.WriteString("\n")
		if !verbose {
			continue
		}
		if !scope.AllRules {
			fmt.Fprintf(out, "    Rules: %s%s\n", valueOrNone(printableList(scope.Rules)), more(scope.RuleCount, len(scope.Rules)))
			if len(scope.NotRun) > 0 {
				fmt.Fprintf(out, "    Not run (excluded on every changed file): %s\n", printableList(scope.NotRun))
			}
		}
		fmt.Fprintf(out, "    Files: %s%s\n", valueOrNone(printableList(scope.Files)), more(scope.FileCount, len(scope.Files)))
	}
	for _, warning := range exclusions.Warnings {
		fmt.Fprintf(
			out,
			"Warning: [check] rule %s matches no selected policy, so it excludes nothing\n",
			printable(warning.Rule),
		)
	}
	if exclusions.ExcludedFindings > 0 {
		fmt.Fprintf(out, "  %d finding(s) on excluded files were dropped\n", exclusions.ExcludedFindings)
	}
	if !verbose && len(exclusions.Scopes) > 0 {
		out.WriteString("  --verbose lists the files and rules each one resolved to.\n")
	}
}

func printableList(values []string) string {
	shown := make([]string, len(values))
	for i, value := range values {
		shown[i] = printable(value)
	}
	return strings.Join(shown, ", ")
}

func valueOrNone(value string) string {
	if value == "" {
		return "none"
	}
	return value
}

// more says how many a list left out, such as ` and 3 more`.
func more(total, listed int) string {
	if total > listed {
		return fmt.Sprintf(" and %d more", total-listed)
	}
	return ""
}

// Findings lists findings the way a human report lists its new ones, for a
// caller that shows only some of a check's findings.
func Findings(findings []service.Finding) string {
	var out strings.Builder
	writeBlock(&out, "New findings", findings)
	return out.String()
}

func writeBlock(out *strings.Builder, heading string, findings []service.Finding) {
	if len(findings) == 0 {
		return
	}
	out.WriteString(heading + "\n")
	for _, finding := range findings {
		location := printable(finding.Path)
		if finding.StartLine != nil {
			location = fmt.Sprintf("%s:%d", location, *finding.StartLine)
		}
		fmt.Fprintf(
			out,
			"  [%s] %s: %s\n    Location: %s\n    Certainty: %s\n",
			printable(finding.Severity),
			printable(finding.PolicyID),
			printable(finding.Title),
			location,
			printable(finding.Certainty),
		)
		// The id is what `slopcop ignore` takes; older services send none.
		if finding.ID != "" {
			fmt.Fprintf(out, "    ID: %s\n", printable(finding.ID))
		}
		if line := previouslyIgnoredLine(finding); line != "" {
			fmt.Fprintf(out, "    %s\n", line)
		}
		out.WriteString("\n")
	}
}

// writeInlineSuppressed lists the findings inline `slopcop: ignore` comments
// accepted, with each comment, and counts the ones past the service's bound.
func writeInlineSuppressed(out *strings.Builder, response service.Response) {
	accepted := response.InlineSuppressed
	total := max(response.Summary.InlineSuppressed, len(accepted))
	if total == 0 {
		return
	}
	out.WriteString("Ignored inline by slopcop: ignore (never fail the check)\n")
	for _, finding := range accepted {
		location := printable(finding.Path)
		if finding.StartLine != nil {
			location = fmt.Sprintf("%s:%d", location, *finding.StartLine)
		}
		fmt.Fprintf(
			out,
			"  [%s] %s: %s\n    Location: %s\n    Ignored by: %s\n",
			printable(finding.Severity),
			printable(finding.PolicyID),
			printable(finding.Title),
			location,
			directiveLine(finding),
		)
		if finding.New {
			out.WriteString("    In this change\n")
		}
		out.WriteString("\n")
	}
	if unlisted := total - len(accepted); unlisted > 0 {
		fmt.Fprintf(out, "  and %d more\n\n", unlisted)
	}
}

// directiveLine describes the comment that accepted a finding, such as
// `blanket comment on line 11 -- generated code`.
func directiveLine(finding service.InlineSuppressedFinding) string {
	directive := finding.Directive
	what := "comment"
	if directive.Blanket {
		what = "blanket comment"
	} else if len(directive.Policies) > 0 {
		what = "comment for " + printable(strings.Join(directive.Policies, ", "))
	}
	if directive.Line != nil {
		what = fmt.Sprintf("%s on line %d", what, *directive.Line)
	}
	if directive.Reason != nil && *directive.Reason != "" {
		what += " -- " + printable(*directive.Reason)
	}
	return what
}

// writeDirectiveWarnings lists the inline comments that accepted nothing,
// each with what to do about it.
func writeDirectiveWarnings(out *strings.Builder, response service.Response) {
	warnings := response.DirectiveWarnings
	total := max(response.Summary.DirectiveWarnings, len(warnings))
	if total == 0 {
		return
	}
	out.WriteString("slopcop: ignore comments that ignore nothing (never fail the check)\n")
	for _, warning := range warnings {
		location := printable(warning.Path)
		if warning.Line != nil {
			location = fmt.Sprintf("%s:%d", location, *warning.Line)
		}
		fmt.Fprintf(out, "  %s: %s\n", location, directiveWarningText(warning))
	}
	if unlisted := total - len(warnings); unlisted > 0 {
		fmt.Fprintf(out, "  and %d more\n", unlisted)
	}
	out.WriteString("\n")
}

func directiveWarningText(warning service.DirectiveWarning) string {
	switch warning.Kind {
	case service.DirectiveMissingReason:
		return "a blanket ignore needs a reason after `--`; it ignores nothing until it has one"
	case service.DirectiveUnused:
		text := "matches no finding; name the right policy, move it onto or above the finding's line, or remove it"
		if len(warning.Policies) > 0 {
			text = "names " + printable(strings.Join(warning.Policies, ", ")) + ", which " + text
		}
		return text
	case service.DirectiveMalformed:
		if warning.Detail != "" {
			return "does not parse: " + printable(warning.Detail)
		}
		return "does not parse"
	default:
		return "ignores nothing (" + printable(warning.Kind) + ")"
	}
}

// previouslyIgnoredLine says a finding matches an ignored one whose code
// changed, and how to keep ignoring it, such as `Matches ignored finding
// f5a9bad1-… (code changed); ` + "`slopcop ignore 98a2d745-…`" + ` to keep
// ignoring it`.
func previouslyIgnoredLine(finding service.Finding) string {
	if finding.PreviouslyIgnored == "" {
		return ""
	}
	line := fmt.Sprintf("Matches ignored finding %s (code changed)", printable(finding.PreviouslyIgnored))
	if finding.ID == "" {
		return line
	}
	return fmt.Sprintf("%s; `slopcop ignore %s` to keep ignoring it", line, printable(finding.ID))
}

// summaryLine is one line, like the first line of Bifrost's human report:
// the new findings, then how many were already in the code.
func summaryLine(response service.Response) string {
	fresh, existing := split(response.Findings)
	scope := "No new findings"
	if onlyExcluded(response) {
		scope = "Nothing checked: every changed file is excluded by .slopcop/config.toml"
	}
	switch len(fresh) {
	case 0:
	case 1:
		scope = "1 new finding"
	default:
		scope = fmt.Sprintf("%d new findings", len(fresh))
	}
	if len(fresh) > 0 {
		scope = fmt.Sprintf("%s across %d file(s)", scope, filesWithFindings(fresh))
	}
	// The service lists only the findings the change introduced or touched
	// and counts the rest; a service from before that also listed them.
	if count := max(response.Summary.ExistingFindings, len(existing)); count > 0 {
		scope = fmt.Sprintf("%s; %d already in the code", scope, count)
	}
	if count := remappedCount(response); count > 0 {
		scope = fmt.Sprintf("%s; %d match(es) an earlier ignore", scope, count)
	}
	if response.Summary.OmittedFindings > 0 {
		scope = fmt.Sprintf("%s; +%d not reported", scope, response.Summary.OmittedFindings)
	}
	// The service leaves out findings Bifrost did not prove unless asked for
	// all; say how many, so a clean line is not read as none at all.
	if count := response.Summary.LessCertainFindings; count != nil && *count > 0 {
		scope = fmt.Sprintf("%s; %d less certain not shown (--certainty all)", scope, *count)
	}
	state := "Analysis complete"
	switch {
	case response.Failure != "":
		state = "Analysis did not complete: " + printable(response.Failure)
	case response.Summary.IncompletePolicies > 0:
		state = fmt.Sprintf(
			"Analysis incomplete: %d check(s) did not complete",
			response.Summary.IncompletePolicies,
		)
	}
	return scope + " | " + state
}

// filesWithFindings counts files with at least one of findings, not files
// the change touches.
func filesWithFindings(findings []service.Finding) int {
	paths := make(map[string]bool, len(findings))
	for _, finding := range findings {
		paths[finding.Path] = true
	}
	return len(paths)
}

// omittedNotice says what the omitted findings mean, for each format to say.
// The service counts only new findings here: a check keeps the findings the
// change introduced or touched first, and counts the ones that were already
// in the code in `existingFindings` instead.
func omittedNotice(response service.Response) string {
	omitted := response.Summary.OmittedFindings
	if omitted == 0 {
		return ""
	}
	return fmt.Sprintf(
		"%d more new finding(s) were not reported because the change has more "+
			"than a check may report; the most severe are listed",
		omitted,
	)
}

// incompleteLine names the incomplete checks, such as
// `Incomplete: acme.slow (timed_out), and 3 more`.
// A check with a detail says it after the reason:
// `acme.rule (unsupported: type=taint_evaluation)`.
// When most named checks share one reason, it is said once, up front, and
// only the others carry their own:
// `Incomplete (48 of 50 named: unreliable): acme.a, acme.b, acme.c (timed_out), and 157 more`.
// When the service names run failures, their lines have said those reasons
// already, so a check one of them covers carries only its id.
func incompleteLine(response service.Response) string {
	if len(response.Incomplete) == 0 {
		return ""
	}
	prefix := "Incomplete: "
	said := make(map[[2]string]bool, len(response.RunFailures))
	for _, failure := range response.RunFailures {
		said[[2]string{failure.Reason, failure.Detail}] = true
	}
	shared, ok := sharedReason(response.Incomplete)
	if len(said) > 0 {
		ok = false
	}
	if ok {
		sharedWhy := printable(why(shared.Reason, shared.Detail))
		if shared.Policies == len(response.Incomplete) {
			prefix = fmt.Sprintf("Incomplete (all %d named: %s): ", shared.Policies, sharedWhy)
		} else {
			prefix = fmt.Sprintf("Incomplete (%d of %d named: %s): ", shared.Policies, len(response.Incomplete), sharedWhy)
		}
	}
	named := make([]string, 0, len(response.Incomplete)+1)
	for _, policy := range response.Incomplete {
		if said[[2]string{policy.Reason, policy.Detail}] ||
			(ok && policy.Reason == shared.Reason && policy.Detail == shared.Detail) {
			named = append(named, printable(policy.PolicyID))
			continue
		}
		named = append(named, fmt.Sprintf("%s (%s)", printable(policy.PolicyID), printable(incompleteWhy(policy))))
	}
	if unnamed := incompleteUnlisted(response); unnamed > 0 {
		named = append(named, fmt.Sprintf("and %d more", unnamed))
	}
	return prefix + strings.Join(named, ", ")
}

// runFailureLine says one cause that left a whole analyzer run incomplete,
// such as `Analyzer run failed (unreliable: type=incomplete
// reasons=analyzer_process_failed exit_code=137): 207 check(s) did not
// complete`.
func runFailureLine(failure service.RunFailure) string {
	return fmt.Sprintf(
		"Analyzer run failed (%s): %d check(s) did not complete",
		printable(why(failure.Reason, failure.Detail)), failure.Policies,
	)
}

// incompleteWhy is a check's reason, followed by its detail when it has one.
func incompleteWhy(policy service.IncompletePolicy) string {
	return why(policy.Reason, policy.Detail)
}

func why(reason, detail string) string {
	if detail == "" {
		return reason
	}
	return reason + ": " + detail
}

// incompleteUnlisted counts the incomplete checks the service counted but did
// not name: it names at most 50.
func incompleteUnlisted(response service.Response) int {
	return max(response.Summary.IncompletePolicies-len(response.Incomplete), 0)
}

// incompleteReason is one reason, with its detail, and how many of the named
// incomplete checks gave it.
type incompleteReason struct {
	Reason   string `json:"reason"`
	Detail   string `json:"detail,omitempty"`
	Policies int    `json:"policies"`
}

// incompleteReasons groups the named incomplete checks by reason and detail,
// most common first, so a run where most checks failed for one cause says it
// once.
func incompleteReasons(policies []service.IncompletePolicy) []incompleteReason {
	type key struct{ reason, detail string }
	counts := make(map[key]int, len(policies))
	for _, policy := range policies {
		counts[key{policy.Reason, policy.Detail}]++
	}
	reasons := make([]incompleteReason, 0, len(counts))
	for k, count := range counts {
		reasons = append(reasons, incompleteReason{Reason: k.reason, Detail: k.detail, Policies: count})
	}
	sort.Slice(reasons, func(i, j int) bool {
		a, b := reasons[i], reasons[j]
		if a.Policies != b.Policies {
			return a.Policies > b.Policies
		}
		if a.Reason != b.Reason {
			return a.Reason < b.Reason
		}
		return a.Detail < b.Detail
	})
	return reasons
}

// sharedReason is the reason more than half of two or more named incomplete
// checks share, if there is one.
func sharedReason(policies []service.IncompletePolicy) (incompleteReason, bool) {
	if len(policies) < 2 {
		return incompleteReason{}, false
	}
	top := incompleteReasons(policies)[0]
	return top, top.Policies*2 > len(policies)
}

// inconclusiveCheck is one inconclusive check as the reports name it.
type inconclusiveCheck struct {
	PolicyID string `json:"policyId"`
	Reason   string `json:"reason"`
	// Detail is the service's detail for the check, when it sent one.
	Detail string `json:"detail,omitempty"`
	// Elsewhere says the check concerns only files the change does not
	// write. Without it, it may concern the change itself.
	Elsewhere bool `json:"elsewhere"`
}

// inconclusiveChecks merges the service's two lists, sorted by policy id.
// The elsewhere checks are a subset of the others and both lists are cut at
// the same bound in id order, so the flag is exact for every check listed. A
// service from before `inconclusive` sent only the elsewhere list.
func inconclusiveChecks(response service.Response) []inconclusiveCheck {
	elsewhere := make(map[string]bool, len(response.InconclusiveElsewhere))
	for _, policy := range response.InconclusiveElsewhere {
		elsewhere[policy.PolicyID] = true
	}
	checks := []inconclusiveCheck{}
	seen := make(map[string]bool)
	for _, list := range [][]service.IncompletePolicy{response.Inconclusive, response.InconclusiveElsewhere} {
		for _, policy := range list {
			if seen[policy.PolicyID] {
				continue
			}
			seen[policy.PolicyID] = true
			checks = append(checks, inconclusiveCheck{
				PolicyID:  policy.PolicyID,
				Reason:    policy.Reason,
				Detail:    policy.Detail,
				Elsewhere: elsewhere[policy.PolicyID],
			})
		}
	}
	sort.Slice(checks, func(i, j int) bool { return checks[i].PolicyID < checks[j].PolicyID })
	return checks
}

// inconclusiveCounts is how many checks were inconclusive, and how many of
// them concern only files the change does not write. The summary counts
// past the listed bound.
func inconclusiveCounts(response service.Response, checks []inconclusiveCheck) (total, elsewhere int) {
	for _, check := range checks {
		if check.Elsewhere {
			elsewhere++
		}
	}
	return max(response.Summary.Inconclusive, len(checks)),
		max(response.Summary.InconclusiveElsewhere, elsewhere)
}

// inconclusiveLine counts the inconclusive checks, such as `Inconclusive: 23
// check(s), 3 only about files the change does not write; they never fail
// the check. --verbose names them.`
func inconclusiveLine(response service.Response) string {
	checks := inconclusiveChecks(response)
	total, elsewhere := inconclusiveCounts(response, checks)
	if total == 0 {
		return ""
	}
	line := fmt.Sprintf("Inconclusive: %d check(s)", total)
	if elsewhere > 0 {
		line = fmt.Sprintf("%s, %d only about files the change does not write", line, elsewhere)
	}
	line += "; they never fail the check."
	if len(checks) > 0 {
		line += " --verbose names them."
	}
	return line
}

// writeInconclusive names each inconclusive check, one per line, and counts
// the ones past the listed bound.
func writeInconclusive(out *strings.Builder, response service.Response) {
	checks := inconclusiveChecks(response)
	if total, _ := inconclusiveCounts(response, checks); total == 0 {
		return
	}
	out.WriteString("Inconclusive (never fail the check)\n")
	for _, check := range checks {
		where := ""
		if check.Elsewhere {
			where = " (only files the change does not write)"
		}
		detail := ""
		if check.Detail != "" {
			detail = " (" + printable(check.Detail) + ")"
		}
		fmt.Fprintf(out, "  %s%s%s\n", printable(check.PolicyID), detail, where)
	}
	if unnamed := inconclusiveUnlisted(response, checks); unnamed > 0 {
		fmt.Fprintf(out, "  and %d more\n", unnamed)
	}
}

// Printable keeps control and bidirectional-override characters from hostile
// repository content off the terminal, as a report does.
func Printable(value string) string { return printable(value) }

// printable keeps control and bidirectional-override characters from hostile
// repository content off the terminal.
func printable(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || (r >= '‪' && r <= '‮') || (r >= '⁦' && r <= '⁩') {
			return unicode.ReplacementChar
		}
		return r
	}, value)
}

// jsonReport is the document `--format json` prints: what an agent needs from
// stdout. Findings lists the new findings, then, with --show-existing, any
// existing ones a service from before BrokkAi/bifrost-service#760 still
// sent; summary counts both either way.
type jsonReport struct {
	Status   string `json:"status"`
	Approved bool   `json:"approved"`
	// StaticStatus preserves the hosted status when the combined status differs.
	StaticStatus string `json:"staticStatus,omitempty"`
	// Review is the local verdict, when one ran.
	Review     *reviewJSON                `json:"review,omitempty"`
	BaseCommit string                     `json:"baseCommit"`
	Summary    service.Summary            `json:"summary"`
	Findings   []service.Finding          `json:"findings"`
	Incomplete []service.IncompletePolicy `json:"incomplete"`
	// IncompleteUnlisted counts the incomplete checks summary.incompletePolicies
	// counts but incomplete does not name, because the service names at most
	// 50. It is set only when the list was cut.
	IncompleteUnlisted int `json:"incompleteUnlisted,omitempty"`
	// IncompleteReasons groups the named incomplete checks by reason and
	// detail, most common first. It is set only when a check did not complete.
	IncompleteReasons []incompleteReason `json:"incompleteReasons,omitempty"`
	// Inconclusive names the checks summary.inconclusive counts, up to the
	// service's bound. They never change the status.
	// RunFailures is the service's list of causes that left a whole analyzer
	// run incomplete, as it sent it. It is set only when there is one.
	RunFailures  []service.RunFailure `json:"runFailures,omitempty"`
	Inconclusive []inconclusiveCheck  `json:"inconclusive"`
	// InconclusiveUnlisted counts the inconclusive checks summary.inconclusive
	// counts but inconclusive does not name. It is set only when the list was
	// cut.
	InconclusiveUnlisted int    `json:"inconclusiveUnlisted,omitempty"`
	Failure              string `json:"failure,omitempty"`
	// RepositoryAdded is set only when the check added the repository to
	// the workspace.
	RepositoryAdded bool `json:"repositoryAdded,omitempty"`
	// InlineSuppressed is the service's list of findings inline
	// `slopcop: ignore` comments accepted, as it sent it; summary counts
	// them all. It is set only when there is one.
	InlineSuppressed []service.InlineSuppressedFinding `json:"inlineSuppressed,omitempty"`
	// DirectiveWarnings is the service's list of inline comments that
	// accepted nothing. It is set only when there is one.
	DirectiveWarnings []service.DirectiveWarning `json:"directiveWarnings,omitempty"`
	// EmptyChange is set only when the patch was empty.
	EmptyChange bool `json:"emptyChange,omitempty"`
	// Exclusions is what .slopcop/config.toml [check] excluded, as the
	// service sent it. It is set only when the checked tree has [check].
	Exclusions *service.Exclusions `json:"exclusions,omitempty"`
	// SkillUpdated is set only when this run wrote the skill.
	SkillUpdated []SkillUpdate `json:"skillUpdated,omitempty"`
}

func writeJSON(w io.Writer, response service.Response, options Options) error {
	inconclusive := inconclusiveChecks(response)
	return encode(w, jsonReport{
		Status:               status(response.Status, options),
		Approved:             options.Approved,
		StaticStatus:         options.StaticStatus,
		Review:               reviewValue(options.Review),
		BaseCommit:           response.BaseCommit,
		Summary:              response.Summary,
		Findings:             listed(response, options),
		Incomplete:           response.Incomplete,
		IncompleteUnlisted:   incompleteUnlisted(response),
		IncompleteReasons:    incompleteReasons(response.Incomplete),
		RunFailures:          response.RunFailures,
		Inconclusive:         inconclusive,
		InconclusiveUnlisted: inconclusiveUnlisted(response, inconclusive),
		Failure:              response.Failure,
		RepositoryAdded:      response.RepositoryAdded,
		InlineSuppressed:     response.InlineSuppressed,
		DirectiveWarnings:    response.DirectiveWarnings,
		EmptyChange:          options.EmptyChange,
		SkillUpdated:         options.SkillUpdated,
		Exclusions:           response.Exclusions,
	}, "json report")
}

// inconclusiveUnlisted counts the inconclusive checks the service counted but
// did not name.
func inconclusiveUnlisted(response service.Response, checks []inconclusiveCheck) int {
	total, _ := inconclusiveCounts(response, checks)
	return total - len(checks)
}

// pendingReport is the document `--format json` prints for a check that was
// still running when --timeout ran out.
type pendingReport struct {
	Status       string        `json:"status"`
	Approved     bool          `json:"approved"`
	StaticStatus string        `json:"staticStatus,omitempty"`
	Review       *reviewJSON   `json:"review,omitempty"`
	RetryAfter   int           `json:"retryAfter"`
	Message      string        `json:"message"`
	Remedy       string        `json:"remedy"`
	SkillUpdated []SkillUpdate `json:"skillUpdated,omitempty"`
}

// writePendingBase renders a check that is still running. The human format
// prints nothing on stdout: the message is on stderr and the exit code is 3.
func writePendingBase(w io.Writer, pending service.Pending, format string, options Options) error {
	switch format {
	case "", FormatHuman:
		return nil
	case FormatJSON:
		return encode(w, pendingReport{
			Status:       status(service.StatusPending, options),
			StaticStatus: options.StaticStatus,
			Approved:     options.Approved,
			Review:       reviewValue(options.Review),
			RetryAfter:   pending.RetryAfter,
			Message:      pending.Message,
			Remedy:       pending.Remedy,
			SkillUpdated: options.SkillUpdated,
		}, "json report")
	case FormatSARIF:
		return encode(w, sarifLogWithReview(options, sarifRun{
			Tool:    sarifTool{Driver: sarifDriver{Name: "SlopCop", InformationURI: "https://slopcop.com", Rules: []sarifRule{}}},
			Results: []sarifResult{},
			Invocations: []sarifInvocation{{
				ExecutionSuccessful: false,
				ToolExecutionNotifications: []sarifNotification{{
					Level:      "note",
					Message:    sarifMessage{Text: strings.TrimSpace(pending.Message + " " + pending.Remedy)},
					Descriptor: &sarifDescriptor{ID: "slopcop.pending"},
					Properties: map[string]any{"slopcop.retryAfter": pending.RetryAfter},
				}},
			}},
			Properties: runProperties(map[string]any{"slopcop.status": service.StatusPending}, options),
		}), "sarif report")
	default:
		return fmt.Errorf("unknown format %q (expected human, json, or sarif)", format)
	}
}

// Skipped is a check the CLI did not send because the repository is not active
// in the workspace: nothing was checked, and that must not read as a pass.
type Skipped struct {
	Code       string
	State      string
	Repository string
	Message    string
}

// skippedReport is the document `--format json` prints for a skipped check.
type skippedReport struct {
	Status       string        `json:"status"`
	Approved     bool          `json:"approved"`
	StaticStatus string        `json:"staticStatus,omitempty"`
	Review       *reviewJSON   `json:"review,omitempty"`
	Code         string        `json:"code"`
	State        string        `json:"state"`
	Repository   string        `json:"repository"`
	Message      string        `json:"message"`
	SkillUpdated []SkillUpdate `json:"skillUpdated,omitempty"`
}

// writeSkippedBase renders a check that was not sent because the repository is not
// active. The human format prints nothing on stdout: the CLI says why on
// stderr and exits 0, as an automatic path asked it to. The machine formats
// carry the skip, so it cannot be mistaken for a clean check.
func writeSkippedBase(w io.Writer, skipped Skipped, format string, options Options) error {
	switch format {
	case "", FormatHuman:
		return nil
	case FormatJSON:
		return encode(w, skippedReport{
			Status:       status("skipped", options),
			StaticStatus: options.StaticStatus,
			Approved:     options.Approved,
			Review:       reviewValue(options.Review),
			Code:         skipped.Code,
			State:        skipped.State,
			Repository:   skipped.Repository,
			Message:      skipped.Message,
			SkillUpdated: options.SkillUpdated,
		}, "json report")
	case FormatSARIF:
		return encode(w, sarifLogWithReview(options, sarifRun{
			Tool:    sarifTool{Driver: sarifDriver{Name: "SlopCop", InformationURI: "https://slopcop.com", Rules: []sarifRule{}}},
			Results: []sarifResult{},
			Invocations: []sarifInvocation{{
				ExecutionSuccessful: false,
				ToolExecutionNotifications: []sarifNotification{{
					Level:      "note",
					Message:    sarifMessage{Text: skipped.Message},
					Descriptor: &sarifDescriptor{ID: skipped.Code},
					Properties: map[string]any{"slopcop.state": skipped.State},
				}},
			}},
			Properties: runProperties(map[string]any{"slopcop.status": "skipped"}, options),
		}), "sarif report")
	default:
		return fmt.Errorf("unknown format %q (expected human, json, or sarif)", format)
	}
}

// failureReport is the document `--format json` prints for a check the
// service refused: its closed error code, verbatim message and remedy, and
// whether the same request may succeed later.
type failureReport struct {
	Status       string        `json:"status"`
	Approved     bool          `json:"approved"`
	StaticStatus string        `json:"staticStatus,omitempty"`
	Review       *reviewJSON   `json:"review,omitempty"`
	Code         string        `json:"code"`
	Message      string        `json:"message"`
	Remedy       string        `json:"remedy"`
	Retryable    bool          `json:"retryable"`
	InstallURL   string        `json:"installUrl,omitempty"`
	TopUpURL     string        `json:"topUpUrl,omitempty"`
	SkillUpdated []SkillUpdate `json:"skillUpdated,omitempty"`
}

// writeFailureBase renders a check the service refused. The human format prints
// nothing on stdout: the code, message, and remedy are on stderr and the exit
// code is 2.
func writeFailureBase(w io.Writer, failure *service.Error, format string, options Options) error {
	switch format {
	case "", FormatHuman:
		return nil
	case FormatJSON:
		return encode(w, failureReport{
			Status:       status(service.StatusFailed, options),
			StaticStatus: options.StaticStatus,
			Approved:     options.Approved,
			Review:       reviewValue(options.Review),
			Code:         failure.Body.Code,
			Message:      failure.Error(),
			Remedy:       failure.Body.Remedy,
			Retryable:    failure.Body.Retryable,
			InstallURL:   failure.Body.InstallURL,
			TopUpURL:     failure.Body.TopUpURL,
			SkillUpdated: options.SkillUpdated,
		}, "json report")
	case FormatSARIF:
		id := failure.Body.Code
		if id == "" {
			id = "slopcop.failed"
		}
		return encode(w, sarifLogWithReview(options, sarifRun{
			Tool:    sarifTool{Driver: sarifDriver{Name: "SlopCop", InformationURI: "https://slopcop.com", Rules: []sarifRule{}}},
			Results: []sarifResult{},
			Invocations: []sarifInvocation{{
				ExecutionSuccessful: false,
				ToolExecutionNotifications: []sarifNotification{{
					Level:      "error",
					Message:    sarifMessage{Text: strings.TrimSpace(failure.Error() + " " + failure.Body.Remedy)},
					Descriptor: &sarifDescriptor{ID: id},
					Properties: map[string]any{"slopcop.retryable": failure.Body.Retryable},
				}},
			}},
			Properties: runProperties(map[string]any{"slopcop.status": service.StatusFailed}, options),
		}), "sarif report")
	default:
		return fmt.Errorf("unknown format %q (expected human, json, or sarif)", format)
	}
}

// runProperties adds what the options mark to a SARIF run's properties.
func runProperties(properties map[string]any, options Options) map[string]any {
	properties["slopcop.approved"] = options.Approved
	if options.EmptyChange {
		properties["slopcop.emptyChange"] = true
	}
	if len(options.SkillUpdated) > 0 {
		properties["slopcop.skillUpdated"] = options.SkillUpdated
	}
	return properties
}

func sarifLog(run sarifRun) sarifDocument {
	return sarifDocument{Schema: sarifSchema, Version: "2.1.0", Runs: []sarifRun{run}}
}

type sarifDocument struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool        sarifTool         `json:"tool"`
	Results     []sarifResult     `json:"results"`
	Invocations []sarifInvocation `json:"invocations"`
	Properties  map[string]any    `json:"properties"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string      `json:"name"`
	InformationURI string      `json:"informationUri"`
	Rules          []sarifRule `json:"rules"`
}

type sarifRule struct {
	ID string `json:"id"`
}

type sarifResult struct {
	RuleID    string          `json:"ruleId"`
	RuleIndex int             `json:"ruleIndex"`
	Level     string          `json:"level"`
	Message   sarifMessage    `json:"message"`
	Locations []sarifLocation `json:"locations"`
	// BaselineState is `new` for a finding the change touched and
	// `unchanged` for one already in the code.
	BaselineState string `json:"baselineState"`
	// Properties carries the finding id `slopcop ignore` takes, when the
	// service sent one, and the id of the ignored finding it matches.
	Properties map[string]any `json:"properties,omitempty"`
	// Suppressions is set only for a finding an inline comment accepted.
	Suppressions []sarifSuppression `json:"suppressions,omitempty"`
}

// sarifSuppression is an `inSource` suppression: a comment in the code.
type sarifSuppression struct {
	Kind          string `json:"kind"`
	Justification string `json:"justification,omitempty"`
}

type sarifMessage struct {
	Text string `json:"text"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysicalLocation `json:"physicalLocation"`
}

type sarifPhysicalLocation struct {
	ArtifactLocation sarifArtifactLocation `json:"artifactLocation"`
	Region           *sarifRegion          `json:"region,omitempty"`
}

type sarifArtifactLocation struct {
	URI       string `json:"uri"`
	URIBaseID string `json:"uriBaseId"`
}

type sarifRegion struct {
	StartLine int  `json:"startLine"`
	EndLine   *int `json:"endLine,omitempty"`
}

type sarifInvocation struct {
	ExecutionSuccessful        bool                `json:"executionSuccessful"`
	ToolExecutionNotifications []sarifNotification `json:"toolExecutionNotifications,omitempty"`
}

type sarifNotification struct {
	Level      string           `json:"level"`
	Message    sarifMessage     `json:"message"`
	Descriptor *sarifDescriptor `json:"descriptor,omitempty"`
	Properties map[string]any   `json:"properties,omitempty"`
}

type sarifDescriptor struct {
	ID string `json:"id"`
}

// writeSARIF prints a SARIF 2.1.0 log with one run. Each result's
// baselineState says whether the change touched it; the existing findings a
// service from before BrokkAi/bifrost-service#760 sent are results only
// with --show-existing.
func writeSARIF(w io.Writer, response service.Response, options Options) error {
	findings := listed(response, options)
	var rules []string
	seen := make(map[string]bool)
	for _, finding := range findings {
		if !seen[finding.PolicyID] {
			seen[finding.PolicyID] = true
			rules = append(rules, finding.PolicyID)
		}
	}
	for _, finding := range response.InlineSuppressed {
		if !seen[finding.PolicyID] {
			seen[finding.PolicyID] = true
			rules = append(rules, finding.PolicyID)
		}
	}
	sort.Strings(rules)
	ruleIndex := make(map[string]int, len(rules))
	driverRules := make([]sarifRule, 0, len(rules))
	for index, id := range rules {
		ruleIndex[id] = index
		driverRules = append(driverRules, sarifRule{ID: id})
	}

	results := make([]sarifResult, 0, len(findings))
	for _, finding := range findings {
		physical := sarifPhysicalLocation{
			ArtifactLocation: sarifArtifactLocation{URI: sarifURI(finding.Path), URIBaseID: "SRCROOT"},
		}
		if start := finding.StartLine; start != nil && *start >= 1 {
			physical.Region = &sarifRegion{StartLine: *start}
			if end := finding.EndLine; end != nil && *end >= *start {
				physical.Region.EndLine = end
			}
		}
		text := finding.Title
		if text == "" {
			text = finding.PolicyID
		}
		result := sarifResult{
			RuleID:        finding.PolicyID,
			RuleIndex:     ruleIndex[finding.PolicyID],
			Level:         sarifLevel(finding.Severity),
			Message:       sarifMessage{Text: text},
			Locations:     []sarifLocation{{PhysicalLocation: physical}},
			BaselineState: baselineState(finding),
		}
		if finding.ID != "" || finding.PreviouslyIgnored != "" {
			result.Properties = map[string]any{}
		}
		if finding.ID != "" {
			result.Properties["slopcop.findingId"] = finding.ID
		}
		if finding.PreviouslyIgnored != "" {
			result.Properties["slopcop.previouslyIgnored"] = finding.PreviouslyIgnored
		}
		results = append(results, result)
	}
	// Findings an inline comment accepted are suppressed results, as the
	// analyzer's own SARIF reports them, so a consumer hides them by default
	// and can still audit each one.
	for _, finding := range response.InlineSuppressed {
		physical := sarifPhysicalLocation{
			ArtifactLocation: sarifArtifactLocation{URI: sarifURI(finding.Path), URIBaseID: "SRCROOT"},
		}
		if start := finding.StartLine; start != nil && *start >= 1 {
			physical.Region = &sarifRegion{StartLine: *start}
			if end := finding.EndLine; end != nil && *end >= *start {
				physical.Region.EndLine = end
			}
		}
		text := finding.Title
		if text == "" {
			text = finding.PolicyID
		}
		state := "unchanged"
		if finding.New {
			state = "new"
		}
		suppression := sarifSuppression{Kind: "inSource"}
		if finding.Directive.Reason != nil {
			suppression.Justification = *finding.Directive.Reason
		}
		properties := map[string]any{"slopcop.inlineBlanket": finding.Directive.Blanket}
		if finding.Directive.Line != nil {
			properties["slopcop.inlineLine"] = *finding.Directive.Line
		}
		results = append(results, sarifResult{
			RuleID:        finding.PolicyID,
			RuleIndex:     ruleIndex[finding.PolicyID],
			Level:         sarifLevel(finding.Severity),
			Message:       sarifMessage{Text: text},
			Locations:     []sarifLocation{{PhysicalLocation: physical}},
			BaselineState: state,
			Properties:    properties,
			Suppressions:  []sarifSuppression{suppression},
		})
	}

	// The overall notice, findings left out, then one per named check, so a
	// SARIF consumer can tell which checks to rerun or exclude.
	var notifications []sarifNotification
	switch {
	case response.Failure != "":
		notifications = append(notifications, sarifNotification{
			Level:   "error",
			Message: sarifMessage{Text: "Analysis did not complete: " + response.Failure},
		})
	case response.Summary.IncompletePolicies > 0:
		notifications = append(notifications, sarifNotification{
			Level: "warning",
			Message: sarifMessage{Text: fmt.Sprintf(
				"Analysis incomplete: %d check(s) did not complete",
				response.Summary.IncompletePolicies,
			)},
		})
	}
	for _, failure := range response.RunFailures {
		properties := map[string]any{
			"slopcop.runFailure": true,
			"slopcop.reason":     failure.Reason,
			"slopcop.policies":   failure.Policies,
		}
		if failure.Detail != "" {
			properties["slopcop.detail"] = failure.Detail
		}
		notifications = append(notifications, sarifNotification{
			Level:      "warning",
			Message:    sarifMessage{Text: runFailureLine(failure)},
			Properties: properties,
		})
	}
	if notice := omittedNotice(response); notice != "" {
		notifications = append(notifications, sarifNotification{
			Level:      "note",
			Message:    sarifMessage{Text: notice},
			Properties: map[string]any{"slopcop.omittedFindings": response.Summary.OmittedFindings},
		})
	}
	for _, policy := range response.Incomplete {
		properties := map[string]any{"slopcop.reason": policy.Reason}
		if policy.Detail != "" {
			properties["slopcop.detail"] = policy.Detail
		}
		if policy.Scope != "" {
			properties["slopcop.scope"] = policy.Scope
		}
		notifications = append(notifications, sarifNotification{
			Level:      "warning",
			Message:    sarifMessage{Text: fmt.Sprintf("%s did not complete (%s)", policy.PolicyID, incompleteWhy(policy))},
			Descriptor: &sarifDescriptor{ID: policy.PolicyID},
			Properties: properties,
		})
	}
	// The service names at most 50 incomplete checks; say how many it left
	// out, so a consumer does not take the named ones for all of them. A run
	// that failed has already said why none of them completed.
	if unnamed := incompleteUnlisted(response); unnamed > 0 && response.Failure == "" {
		notifications = append(notifications, sarifNotification{
			Level:      "warning",
			Message:    sarifMessage{Text: fmt.Sprintf("%d more check(s) did not complete and are not named", unnamed)},
			Properties: map[string]any{"slopcop.unlisted": unnamed},
		})
	}
	// Inconclusive checks are notes: they never change the status or the
	// execution's success.
	inconclusive := inconclusiveChecks(response)
	for _, check := range inconclusive {
		checkProperties := map[string]any{
			"slopcop.reason":       check.Reason,
			"slopcop.inconclusive": true,
			"slopcop.elsewhere":    check.Elsewhere,
		}
		if check.Detail != "" {
			checkProperties["slopcop.detail"] = check.Detail
		}
		notifications = append(notifications, sarifNotification{
			Level: "note",
			Message: sarifMessage{Text: fmt.Sprintf(
				"%s could not prove there is nothing more to find (%s)", check.PolicyID, why(check.Reason, check.Detail),
			)},
			Descriptor: &sarifDescriptor{ID: check.PolicyID},
			Properties: checkProperties,
		})
	}

	// Inline comments that ignore nothing are warnings; they never change
	// the status or the execution's success.
	for _, warning := range response.DirectiveWarnings {
		location := warning.Path
		if warning.Line != nil {
			location = fmt.Sprintf("%s:%d", location, *warning.Line)
		}
		notifications = append(notifications, sarifNotification{
			Level:      "warning",
			Message:    sarifMessage{Text: location + ": slopcop: ignore comment " + directiveWarningText(warning)},
			Properties: map[string]any{"slopcop.directiveWarning": warning.Kind, "slopcop.path": warning.Path},
		})
	}

	properties := runProperties(map[string]any{
		"slopcop.status":                response.Status,
		"slopcop.baseCommit":            response.BaseCommit,
		"slopcop.failOn":                response.Summary.FailOn,
		"slopcop.newFindings":           response.Summary.NewFindings,
		"slopcop.existingFindings":      response.Summary.ExistingFindings,
		"slopcop.remappedIgnores":       remappedCount(response),
		"slopcop.inconclusive":          response.Summary.Inconclusive,
		"slopcop.inconclusiveElsewhere": response.Summary.InconclusiveElsewhere,
		"slopcop.inlineSuppressed":      max(response.Summary.InlineSuppressed, len(response.InlineSuppressed)),
		"slopcop.directiveWarnings":     max(response.Summary.DirectiveWarnings, len(response.DirectiveWarnings)),
	}, options)
	// A service from before lessCertainFindings omits it; say nothing
	// rather than claim none were left out.
	if count := response.Summary.LessCertainFindings; count != nil {
		properties["slopcop.lessCertainFindings"] = *count
	}
	if unnamed := incompleteUnlisted(response); unnamed > 0 {
		properties["slopcop.incompleteUnlisted"] = unnamed
	}
	if len(response.RunFailures) > 0 {
		properties["slopcop.runFailures"] = response.RunFailures
	}
	if reasons := incompleteReasons(response.Incomplete); len(reasons) > 0 {
		properties["slopcop.incompleteReasons"] = reasons
	}
	if unnamed := inconclusiveUnlisted(response, inconclusive); unnamed > 0 {
		properties["slopcop.inconclusiveUnlisted"] = unnamed
	}
	// What .slopcop/config.toml [check] excluded is never silent: the run
	// carries it, and each rule that matched nothing is a warning.
	if exclusions := response.Exclusions; exclusions != nil {
		properties["slopcop.exclusions"] = exclusions
		properties["slopcop.onlyExcluded"] = onlyExcluded(response)
		for _, warning := range exclusions.Warnings {
			notifications = append(notifications, sarifNotification{
				Level: "warning",
				Message: sarifMessage{Text: fmt.Sprintf(
					"[check] rule %s matches no selected policy, so it excludes nothing", warning.Rule,
				)},
				Properties: map[string]any{"slopcop.exclusionWarning": warning.Kind},
			})
		}
		if onlyExcluded(response) {
			notifications = append(notifications, sarifNotification{
				Level:   "note",
				Message: sarifMessage{Text: "Nothing checked: every changed file is excluded by .slopcop/config.toml"},
			})
		}
	}

	return encode(w, sarifLogWithReview(options, sarifRun{
		Tool: sarifTool{Driver: sarifDriver{
			Name:           "SlopCop",
			InformationURI: "https://slopcop.com",
			Rules:          driverRules,
		}},
		Results: results,
		Invocations: []sarifInvocation{{
			// New findings can answer `findings` while a check did not
			// complete, so the execution is judged on its own.
			ExecutionSuccessful:        response.Failure == "" && response.Summary.IncompletePolicies == 0,
			ToolExecutionNotifications: notifications,
		}},
		Properties: properties,
	}), "sarif report")
}

// baselineState is SARIF's word for whether the change touched a finding.
func baselineState(finding service.Finding) string {
	if finding.New {
		return "new"
	}
	return "unchanged"
}

// sarifLevel maps Bifrost's severities. SARIF has no unrated level; `none` is
// its level for a result without a severity.
func sarifLevel(severity string) string {
	switch severity {
	case "error", "warning", "note":
		return severity
	default:
		return "none"
	}
}

// sarifURI writes a repository-relative path as a URI reference: everything
// but unreserved characters and `/` is percent-encoded.
func sarifURI(path string) string {
	var uri strings.Builder
	for i := 0; i < len(path); i++ {
		b := path[i]
		switch {
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9',
			b == '-', b == '.', b == '_', b == '~', b == '/':
			uri.WriteByte(b)
		default:
			fmt.Fprintf(&uri, "%%%02X", b)
		}
	}
	return uri.String()
}

func encode(w io.Writer, value any, what string) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("encode %s: %w", what, err)
	}
	return nil
}

// Write renders the static response and optional review.
func Write(w io.Writer, response service.Response, format string, options Options) error {
	if format == "" || format == FormatHuman {
		if _, err := io.WriteString(w, human(response, options)); err != nil {
			return err
		}
		return writeReview(w, options.Review)
	}
	return writeBase(w, response, format, options)
}

// WritePending renders a still-running static check and optional review.
func WritePending(w io.Writer, pending service.Pending, format string, options Options) error {
	if format == "" || format == FormatHuman {
		return writeReview(w, options.Review)
	}
	return writePendingBase(w, pending, format, options)
}

// WriteSkipped renders a skipped static check and optional review.
func WriteSkipped(w io.Writer, skipped Skipped, format string, options Options) error {
	if format == "" || format == FormatHuman {
		return writeReview(w, options.Review)
	}
	return writeSkippedBase(w, skipped, format, options)
}

// WriteFailure renders a static failure and optional review.
func WriteFailure(w io.Writer, failure *service.Error, format string, options Options) error {
	if format == "" || format == FormatHuman {
		return writeReview(w, options.Review)
	}
	return writeFailureBase(w, failure, format, options)
}

// WriteAI renders a local-only review.
func WriteAI(w io.Writer, format string, options Options) error {
	switch format {
	case "", FormatHuman:
		return writeReview(w, options.Review)
	case FormatJSON:
		return encode(w, aiReport{Status: status("clean", options), Approved: options.Approved, EmptyChange: options.EmptyChange, Review: reviewValue(options.Review), SkillUpdated: options.SkillUpdated}, "json report")
	case FormatSARIF:
		return encode(w, sarifLogWithReview(options, sarifRun{Tool: sarifTool{Driver: sarifDriver{Name: "SlopCop", Rules: []sarifRule{}}}, Results: []sarifResult{}, Invocations: []sarifInvocation{{ExecutionSuccessful: true}}, Properties: runProperties(map[string]any{"slopcop.status": status("clean", options)}, options)}), "sarif report")
	default:
		return fmt.Errorf("unknown format %q", format)
	}
}

// aiReport is the JSON document for a check without a static half.
type aiReport struct {
	// Status is the AI-only report's Status field.
	Status   string `json:"status"`
	Approved bool   `json:"approved"`
	// EmptyChange is the AI-only report's EmptyChange field.
	EmptyChange bool `json:"emptyChange,omitempty"`
	// Review is the AI-only report's Review field.
	Review *reviewJSON `json:"review,omitempty"`
	// SkillUpdated is the AI-only report's SkillUpdated field.
	SkillUpdated []SkillUpdate `json:"skillUpdated,omitempty"`
}

// reviewJSON gives the local verdict its public JSON field names.
type reviewJSON struct {
	// Status is the local review's Status field.
	Status review.Status `json:"status"`
	// BaseTree is the local review's BaseTree field.
	BaseTree string `json:"baseTree"`
	// Tree is the local review's Tree field.
	Tree string `json:"tree"`
	// IntentSource is the local review's IntentSource field.
	IntentSource string `json:"intentSource"`
	// LogDir is the local review's LogDir field.
	LogDir string `json:"logDir"`
	// Reviewers is the local review's Reviewers field.
	Reviewers []review.Reviewer `json:"reviewers"`
	// Error is the local review's Error field.
	Error string `json:"error"`
	// Reused is the local review's Reused field.
	Reused bool `json:"reused"`
}

// status applies the combined status only when one was supplied.
func status(static string, options Options) string {
	if options.Status != "" {
		return options.Status
	}
	return static
}

// reviewValue converts a verdict to its report representation.
func reviewValue(v *review.Verdict) *reviewJSON {
	if v == nil {
		return nil
	}
	result := &reviewJSON{Status: v.Status, BaseTree: v.Capture.BaseTree, Tree: v.Capture.Tree, IntentSource: v.IntentSource, LogDir: v.LogDir, Reviewers: v.Reviewers, Error: v.Error, Reused: v.Reused}
	if result.Reviewers == nil {
		result.Reviewers = []review.Reviewer{}
	}
	return result
}

// sarifLogWithReview adds a second typed run for the local review.
func sarifLogWithReview(options Options, static sarifRun) sarifDocument {
	log := sarifLog(static)
	if options.Review == nil {
		return log
	}
	v := options.Review
	run := sarifRun{Tool: sarifTool{Driver: sarifDriver{Name: "slopcop-review", Rules: []sarifRule{}}}, Results: []sarifResult{}, Invocations: []sarifInvocation{{ExecutionSuccessful: v.Status != review.StatusFailed}}, Properties: map[string]any{"slopcop.status": v.Status}}
	for _, r := range v.Reviewers {
		if r.Status == review.StatusFindings {
			run.Results = append(run.Results, sarifResult{RuleID: r.Name, Level: "warning", Message: sarifMessage{Text: r.Text}})
		}
		if r.Status == review.StatusFailed {
			run.Invocations[0].ToolExecutionNotifications = append(run.Invocations[0].ToolExecutionNotifications, sarifNotification{Level: "error", Message: sarifMessage{Text: r.Error}})
		}
	}
	if v.Error != "" {
		run.Invocations[0].ToolExecutionNotifications = append(run.Invocations[0].ToolExecutionNotifications, sarifNotification{Level: "error", Message: sarifMessage{Text: v.Error}})
	}
	log.Runs = append(log.Runs, run)
	return log
}

// writeReview renders the review section when a verdict exists.
func writeReview(w io.Writer, v *review.Verdict) error {
	if v == nil {
		return nil
	}
	writeReviewHuman(w, *v)
	return nil
}

// writeReviewHuman renders one review verdict for a person.
func writeReviewHuman(w io.Writer, v review.Verdict) {
	fmt.Fprintf(w, "\nReview: %s\n", v.Status)
	for _, r := range v.Reviewers {
		fmt.Fprintf(w, "  %s %s %s: %s\n", printable(r.Name), printable(r.Model), printable(r.Effort), r.Status)
		if r.Text != "" {
			for _, line := range strings.Split(r.Text, "\n") {
				fmt.Fprintf(w, "    %s\n", printable(line))
			}
		}
		if r.Error != "" {
			fmt.Fprintf(w, "    Error: %s\n", printable(r.Error))
		}
		if r.Log != "" {
			fmt.Fprintf(w, "    Log: %s\n", printable(r.Log))
		}
	}
	if v.Error != "" {
		fmt.Fprintf(w, "  Error: %s\n", printable(v.Error))
	}
	if v.IntentSource != "" {
		fmt.Fprintf(w, "  Intent: %s\n", printable(v.IntentSource))
	}
	if v.LogDir != "" {
		fmt.Fprintf(w, "  Logs: %s\n", printable(v.LogDir))
	}
	if v.Reused {
		fmt.Fprintln(w, "  Reused previous review")
	}
}
