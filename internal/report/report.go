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
	// ShowExisting lists the findings that were already in the code, after
	// the new ones. They are counted either way.
	ShowExisting bool
	// EmptyChange marks a check whose patch was empty: the working tree
	// matched the base, so no change was checked. The human format leaves it
	// to the CLI's note on stderr.
	EmptyChange bool
	// Verbose lists the inconclusive checks in the human format, which
	// otherwise only counts them. The machine formats always list them.
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

// Write renders the response to w.
func Write(w io.Writer, response service.Response, format string, options Options) error {
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
	out.WriteString(summaryLine(response))
	out.WriteString("\n")
	if notice := omittedNotice(response); notice != "" {
		fmt.Fprintf(&out, "Omitted: %s.\n", notice)
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
	return out.String()
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
func incompleteLine(response service.Response) string {
	if len(response.Incomplete) == 0 {
		return ""
	}
	prefix := "Incomplete: "
	shared, ok := sharedReason(response.Incomplete)
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
		if ok && policy.Reason == shared.Reason && policy.Detail == shared.Detail {
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
	Status     string                     `json:"status"`
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
	Inconclusive []inconclusiveCheck `json:"inconclusive"`
	// InconclusiveUnlisted counts the inconclusive checks summary.inconclusive
	// counts but inconclusive does not name. It is set only when the list was
	// cut.
	InconclusiveUnlisted int    `json:"inconclusiveUnlisted,omitempty"`
	Failure              string `json:"failure,omitempty"`
	// RepositoryAdded is set only when the check added the repository to
	// the workspace.
	RepositoryAdded bool `json:"repositoryAdded,omitempty"`
	// EmptyChange is set only when the patch was empty.
	EmptyChange bool `json:"emptyChange,omitempty"`
	// SkillUpdated is set only when this run wrote the skill.
	SkillUpdated []SkillUpdate `json:"skillUpdated,omitempty"`
}

func writeJSON(w io.Writer, response service.Response, options Options) error {
	inconclusive := inconclusiveChecks(response)
	return encode(w, jsonReport{
		Status:               response.Status,
		BaseCommit:           response.BaseCommit,
		Summary:              response.Summary,
		Findings:             listed(response, options),
		Incomplete:           response.Incomplete,
		IncompleteUnlisted:   incompleteUnlisted(response),
		IncompleteReasons:    incompleteReasons(response.Incomplete),
		Inconclusive:         inconclusive,
		InconclusiveUnlisted: inconclusiveUnlisted(response, inconclusive),
		Failure:              response.Failure,
		RepositoryAdded:      response.RepositoryAdded,
		EmptyChange:          options.EmptyChange,
		SkillUpdated:         options.SkillUpdated,
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
	RetryAfter   int           `json:"retryAfter"`
	Message      string        `json:"message"`
	Remedy       string        `json:"remedy"`
	SkillUpdated []SkillUpdate `json:"skillUpdated,omitempty"`
}

// WritePending renders a check that is still running. The human format
// prints nothing on stdout: the message is on stderr and the exit code is 3.
func WritePending(w io.Writer, pending service.Pending, format string, options Options) error {
	switch format {
	case "", FormatHuman:
		return nil
	case FormatJSON:
		return encode(w, pendingReport{
			Status:       service.StatusPending,
			RetryAfter:   pending.RetryAfter,
			Message:      pending.Message,
			Remedy:       pending.Remedy,
			SkillUpdated: options.SkillUpdated,
		}, "json report")
	case FormatSARIF:
		return encode(w, sarifLog(sarifRun{
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
	Code         string        `json:"code"`
	State        string        `json:"state"`
	Repository   string        `json:"repository"`
	Message      string        `json:"message"`
	SkillUpdated []SkillUpdate `json:"skillUpdated,omitempty"`
}

// WriteSkipped renders a check that was not sent because the repository is not
// active. The human format prints nothing on stdout: the CLI says why on
// stderr and exits 0, as an automatic path asked it to. The machine formats
// carry the skip, so it cannot be mistaken for a clean check.
func WriteSkipped(w io.Writer, skipped Skipped, format string, options Options) error {
	switch format {
	case "", FormatHuman:
		return nil
	case FormatJSON:
		return encode(w, skippedReport{
			Status:       "skipped",
			Code:         skipped.Code,
			State:        skipped.State,
			Repository:   skipped.Repository,
			Message:      skipped.Message,
			SkillUpdated: options.SkillUpdated,
		}, "json report")
	case FormatSARIF:
		return encode(w, sarifLog(sarifRun{
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
	Code         string        `json:"code"`
	Message      string        `json:"message"`
	Remedy       string        `json:"remedy"`
	Retryable    bool          `json:"retryable"`
	InstallURL   string        `json:"installUrl,omitempty"`
	TopUpURL     string        `json:"topUpUrl,omitempty"`
	SkillUpdated []SkillUpdate `json:"skillUpdated,omitempty"`
}

// WriteFailure renders a check the service refused. The human format prints
// nothing on stdout: the code, message, and remedy are on stderr and the exit
// code is 2.
func WriteFailure(w io.Writer, failure *service.Error, format string, options Options) error {
	switch format {
	case "", FormatHuman:
		return nil
	case FormatJSON:
		return encode(w, failureReport{
			Status:       service.StatusFailed,
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
		return encode(w, sarifLog(sarifRun{
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

	properties := runProperties(map[string]any{
		"slopcop.status":                response.Status,
		"slopcop.baseCommit":            response.BaseCommit,
		"slopcop.failOn":                response.Summary.FailOn,
		"slopcop.newFindings":           response.Summary.NewFindings,
		"slopcop.existingFindings":      response.Summary.ExistingFindings,
		"slopcop.remappedIgnores":       remappedCount(response),
		"slopcop.inconclusive":          response.Summary.Inconclusive,
		"slopcop.inconclusiveElsewhere": response.Summary.InconclusiveElsewhere,
	}, options)
	if unnamed := incompleteUnlisted(response); unnamed > 0 {
		properties["slopcop.incompleteUnlisted"] = unnamed
	}
	if reasons := incompleteReasons(response.Incomplete); len(reasons) > 0 {
		properties["slopcop.incompleteReasons"] = reasons
	}
	if unnamed := inconclusiveUnlisted(response, inconclusive); unnamed > 0 {
		properties["slopcop.inconclusiveUnlisted"] = unnamed
	}

	return encode(w, sarifLog(sarifRun{
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
