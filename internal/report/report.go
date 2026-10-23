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
	// ShowExisting lists the findings that were already in the code, then
	// the ones reported as false positives, after the new ones. They are
	// counted either way.
	ShowExisting bool
}

// reportHint is the one line after the new findings that says how to report
// one that is wrong.
const reportHint = `False positive? slopcop report <id> --reason "why"`

// dismissedJustification is the SARIF suppression's justification for a
// finding reported as a false positive.
const dismissedJustification = "reported as false positive"

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

// groups are a response's findings by what they mean for the check, each in
// the service's order.
type groups struct {
	// fresh are the findings the change introduced or touched; only these
	// can fail the check.
	fresh []service.Finding
	// existing were already in the code.
	existing []service.Finding
	// dismissed were reported as false positives in this repository. The
	// service sends them with new false; a dismissed finding is never fresh,
	// whatever else it says.
	dismissed []service.Finding
}

func split(findings []service.Finding) groups {
	g := groups{fresh: []service.Finding{}, existing: []service.Finding{}, dismissed: []service.Finding{}}
	for _, finding := range findings {
		switch {
		case finding.Dismissed:
			g.dismissed = append(g.dismissed, finding)
		case finding.New:
			g.fresh = append(g.fresh, finding)
		default:
			g.existing = append(g.existing, finding)
		}
	}
	return g
}

// listed is what a report lists: the new findings first, then the existing
// ones and the dismissed ones only when asked for.
func listed(response service.Response, options Options) []service.Finding {
	g := split(response.Findings)
	if options.ShowExisting {
		return append(append(g.fresh, g.existing...), g.dismissed...)
	}
	return g.fresh
}

// reportable reports whether any of findings carries an id to report it by.
// A service from before findings had ids sends none.
func reportable(findings []service.Finding) bool {
	for _, finding := range findings {
		if finding.ID != "" {
			return true
		}
	}
	return false
}

// human is close to Bifrost's own human report: one block per new finding
// and the line that says how to report one that is wrong, then the existing
// and dismissed ones when asked for, then the summary line, then what was
// left out or did not complete.
func human(response service.Response, options Options) string {
	var out strings.Builder
	g := split(response.Findings)
	writeBlock(&out, "New findings", g.fresh)
	if reportable(g.fresh) {
		out.WriteString(reportHint + "\n\n")
	}
	if options.ShowExisting {
		writeBlock(&out, "Already in the code (never fail the check)", g.existing)
		writeBlock(&out, "Reported as false positive (never fail the check)", g.dismissed)
	}
	out.WriteString(summaryLine(response, options))
	out.WriteString("\n")
	if notice := omittedNotice(response); notice != "" {
		fmt.Fprintf(&out, "Omitted: %s.\n", notice)
	}
	if line := incompleteLine(response); line != "" {
		out.WriteString(line)
		out.WriteString("\n")
	}
	if line := elsewhereLine(response); line != "" {
		out.WriteString(line)
		out.WriteString("\n")
	}
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
		if finding.ID != "" {
			fmt.Fprintf(out, "    Id: %s\n", printable(finding.ID))
		}
		out.WriteString("\n")
	}
}

// summaryLine is one line, like the first line of Bifrost's human report:
// the new findings, then how many were already in the code and how many were
// reported as false positives.
func summaryLine(response service.Response, options Options) string {
	g := split(response.Findings)
	scope := "No new findings"
	switch len(g.fresh) {
	case 0:
	case 1:
		scope = "1 new finding"
	default:
		scope = fmt.Sprintf("%d new findings", len(g.fresh))
	}
	if len(g.fresh) > 0 {
		scope = fmt.Sprintf("%s across %d file(s)", scope, filesWithFindings(g.fresh))
	}
	if len(g.existing) > 0 {
		scope = fmt.Sprintf("%s; %d already in the code", scope, len(g.existing))
	}
	if len(g.dismissed) > 0 {
		scope = fmt.Sprintf("%s; %d reported as false positive", scope, len(g.dismissed))
	}
	if len(g.existing)+len(g.dismissed) > 0 && !options.ShowExisting {
		scope += " (--show-existing lists them)"
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
func omittedNotice(response service.Response) string {
	omitted := response.Summary.OmittedFindings
	if omitted == 0 {
		return ""
	}
	return fmt.Sprintf(
		"%d more finding(s) were not reported because a check found more than "+
			"it may report; the most severe are listed",
		omitted,
	)
}

// incompleteLine names the incomplete checks, such as
// `Incomplete: acme.slow (timed_out), and 3 more`.
func incompleteLine(response service.Response) string {
	if len(response.Incomplete) == 0 {
		return ""
	}
	named := make([]string, 0, len(response.Incomplete)+1)
	for _, policy := range response.Incomplete {
		named = append(named, fmt.Sprintf("%s (%s)", printable(policy.PolicyID), printable(policy.Reason)))
	}
	if unnamed := response.Summary.IncompletePolicies - len(response.Incomplete); unnamed > 0 {
		named = append(named, fmt.Sprintf("and %d more", unnamed))
	}
	return "Incomplete: " + strings.Join(named, ", ")
}

// elsewhereLine names the inconclusive checks about files the change does
// not write, which never fail the check.
func elsewhereLine(response service.Response) string {
	total := response.Summary.InconclusiveElsewhere
	if total == 0 {
		return ""
	}
	named := make([]string, 0, len(response.InconclusiveElsewhere)+1)
	for _, policy := range response.InconclusiveElsewhere {
		named = append(named, printable(policy.PolicyID))
	}
	if unnamed := total - len(response.InconclusiveElsewhere); unnamed > 0 {
		named = append(named, fmt.Sprintf("and %d more", unnamed))
	}
	return fmt.Sprintf(
		"Inconclusive outside the change (never fail the check): %s",
		strings.Join(named, ", "),
	)
}

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
// stdout. Findings lists the new findings, then the existing and dismissed
// ones only with --show-existing; summary counts all three either way.
type jsonReport struct {
	Status string `json:"status"`
	// CheckID is empty against a service from before checks had ids.
	CheckID    string                     `json:"checkId"`
	BaseCommit string                     `json:"baseCommit"`
	Summary    service.Summary            `json:"summary"`
	Findings   []service.Finding          `json:"findings"`
	Incomplete []service.IncompletePolicy `json:"incomplete"`
	// InconclusiveElsewhere lists inconclusive checks about files the change
	// does not write.
	InconclusiveElsewhere []service.IncompletePolicy `json:"inconclusiveElsewhere"`
	Failure               string                     `json:"failure,omitempty"`
}

func writeJSON(w io.Writer, response service.Response, options Options) error {
	return encode(w, jsonReport{
		Status:     response.Status,
		CheckID:    response.CheckID,
		BaseCommit: response.BaseCommit,
		Summary:    response.Summary,
		Findings:   listed(response, options),
		Incomplete: response.Incomplete,
		// A response built without the service has no list; print an empty one.
		InconclusiveElsewhere: append([]service.IncompletePolicy{}, response.InconclusiveElsewhere...),
		Failure:               response.Failure,
	}, "json report")
}

// pendingReport is the document `--format json` prints for a check that was
// still running when --timeout ran out.
type pendingReport struct {
	Status     string `json:"status"`
	RetryAfter int    `json:"retryAfter"`
	Message    string `json:"message"`
	Remedy     string `json:"remedy"`
}

// WritePending renders a check that is still running. The human format
// prints nothing on stdout: the message is on stderr and the exit code is 3.
func WritePending(w io.Writer, pending service.Pending, format string) error {
	switch format {
	case "", FormatHuman:
		return nil
	case FormatJSON:
		return encode(w, pendingReport{
			Status:     service.StatusPending,
			RetryAfter: pending.RetryAfter,
			Message:    pending.Message,
			Remedy:     pending.Remedy,
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
			Properties: map[string]any{"slopcop.status": service.StatusPending},
		}), "sarif report")
	default:
		return fmt.Errorf("unknown format %q (expected human, json, or sarif)", format)
	}
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
	// `unchanged` for one already in the code or reported as a false
	// positive.
	BaselineState string `json:"baselineState"`
	// Suppressions marks a finding reported as a false positive.
	Suppressions []sarifSuppression `json:"suppressions,omitempty"`
	// Properties carries the finding's id as findingId.
	Properties map[string]any `json:"properties,omitempty"`
}

type sarifSuppression struct {
	Kind          string `json:"kind"`
	Justification string `json:"justification"`
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
// baselineState says whether the change touched it, and a dismissed one is
// suppressed; existing and dismissed findings are results only with
// --show-existing.
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
		if finding.Dismissed {
			result.Suppressions = []sarifSuppression{{Kind: "external", Justification: dismissedJustification}}
		}
		if finding.ID != "" {
			result.Properties = map[string]any{"findingId": finding.ID}
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
		notifications = append(notifications, sarifNotification{
			Level:      "warning",
			Message:    sarifMessage{Text: fmt.Sprintf("%s did not complete (%s)", policy.PolicyID, policy.Reason)},
			Descriptor: &sarifDescriptor{ID: policy.PolicyID},
			Properties: map[string]any{"slopcop.reason": policy.Reason},
		})
	}
	// Inconclusive checks outside the change are notes: they never fail it.
	for _, policy := range response.InconclusiveElsewhere {
		notifications = append(notifications, sarifNotification{
			Level:      "note",
			Message:    sarifMessage{Text: fmt.Sprintf("%s was inconclusive outside the change", policy.PolicyID)},
			Descriptor: &sarifDescriptor{ID: policy.PolicyID},
			Properties: map[string]any{"slopcop.reason": policy.Reason, "slopcop.outsideChange": true},
		})
	}

	properties := map[string]any{
		"slopcop.status":                response.Status,
		"slopcop.baseCommit":            response.BaseCommit,
		"slopcop.failOn":                response.Summary.FailOn,
		"slopcop.newFindings":           response.Summary.NewFindings,
		"slopcop.existingFindings":      response.Summary.ExistingFindings,
		"slopcop.dismissedFindings":     response.Summary.DismissedFindings,
		"slopcop.inconclusiveElsewhere": response.Summary.InconclusiveElsewhere,
	}
	if response.CheckID != "" {
		properties["checkId"] = response.CheckID
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

// baselineState is SARIF's word for whether the change touched a finding. A
// dismissed finding was already seen by the check it was reported from.
func baselineState(finding service.Finding) string {
	if finding.New && !finding.Dismissed {
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

// WriteFalsePositive renders the service's answer to a false-positive
// report: in human, which finding and rule it dismissed and what that means;
// in json, the answer itself.
func WriteFalsePositive(w io.Writer, response service.ReportResponse, format string) error {
	switch format {
	case "", FormatHuman:
		_, err := io.WriteString(w, falsePositiveText(response))
		return err
	case FormatJSON:
		return encode(w, response, "report response")
	default:
		return fmt.Errorf("unknown format %q (expected human or json)", format)
	}
}

func falsePositiveText(response service.ReportResponse) string {
	finding := "finding " + printable(response.FindingID)
	if response.PolicyID != "" {
		finding += " (" + printable(response.PolicyID) + ")"
	}
	if response.Status == service.ReportAlreadyReported {
		return "slopcop: " + finding + " was already reported as a false positive\n" +
			"It no longer fails checks in this repository, and the team sees the earlier report on the SlopCop dashboard.\n"
	}
	return "slopcop: reported " + finding + " as a false positive\n" +
		"It no longer fails checks in this repository, and the team sees the report and its reason on the SlopCop dashboard.\n"
}
