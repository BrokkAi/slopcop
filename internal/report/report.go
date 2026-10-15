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

// Write renders the response to w.
func Write(w io.Writer, response service.Response, format string) error {
	switch format {
	case "", FormatHuman:
		_, err := io.WriteString(w, human(response))
		return err
	case FormatJSON:
		return writeJSON(w, response)
	case FormatSARIF:
		return writeSARIF(w, response)
	default:
		return fmt.Errorf("unknown format %q (expected human, json, or sarif)", format)
	}
}

// human is close to Bifrost's own human report: one block per finding, then
// the summary line, then what was left out or did not complete.
func human(response service.Response) string {
	var out strings.Builder
	if len(response.Findings) > 0 {
		out.WriteString("Findings\n")
		for _, finding := range response.Findings {
			location := printable(finding.Path)
			if finding.StartLine != nil {
				location = fmt.Sprintf("%s:%d", location, *finding.StartLine)
			}
			fmt.Fprintf(
				&out,
				"  [%s] %s: %s\n    Location: %s\n    Certainty: %s\n\n",
				printable(finding.Severity),
				printable(finding.PolicyID),
				printable(finding.Title),
				location,
				printable(finding.Certainty),
			)
		}
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
	return out.String()
}

// summaryLine is one line, like the first line of Bifrost's human report.
func summaryLine(response service.Response) string {
	count := fmt.Sprintf("%d findings", len(response.Findings))
	if len(response.Findings) == 1 {
		count = "1 finding"
	}
	if response.Summary.OmittedFindings > 0 {
		count = fmt.Sprintf("%s (+%d not reported)", count, response.Summary.OmittedFindings)
	}
	scope := count
	if len(response.Findings) > 0 {
		scope = fmt.Sprintf("%s across %d file(s)", count, filesWithFindings(response))
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

// filesWithFindings counts files with at least one finding, not files the
// change touches.
func filesWithFindings(response service.Response) int {
	paths := make(map[string]bool, len(response.Findings))
	for _, finding := range response.Findings {
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
// stdout.
type jsonReport struct {
	Status     string                     `json:"status"`
	BaseCommit string                     `json:"baseCommit"`
	Summary    service.Summary            `json:"summary"`
	Findings   []service.Finding          `json:"findings"`
	Incomplete []service.IncompletePolicy `json:"incomplete"`
	Failure    string                     `json:"failure,omitempty"`
}

func writeJSON(w io.Writer, response service.Response) error {
	return encode(w, jsonReport{
		Status:     response.Status,
		BaseCommit: response.BaseCommit,
		Summary:    response.Summary,
		Findings:   response.Findings,
		Incomplete: response.Incomplete,
		Failure:    response.Failure,
	}, "json report")
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

// writeSARIF prints a SARIF 2.1.0 log with one run. Findings carry no
// baselineState: the service reports every finding the checked tree holds,
// not only the ones the change introduced.
func writeSARIF(w io.Writer, response service.Response) error {
	var rules []string
	seen := make(map[string]bool)
	for _, finding := range response.Findings {
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

	results := make([]sarifResult, 0, len(response.Findings))
	for _, finding := range response.Findings {
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
		results = append(results, sarifResult{
			RuleID:    finding.PolicyID,
			RuleIndex: ruleIndex[finding.PolicyID],
			Level:     sarifLevel(finding.Severity),
			Message:   sarifMessage{Text: text},
			Locations: []sarifLocation{{PhysicalLocation: physical}},
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

	return encode(w, sarifDocument{
		Schema:  sarifSchema,
		Version: "2.1.0",
		Runs: []sarifRun{{
			Tool: sarifTool{Driver: sarifDriver{
				Name:           "SlopCop",
				InformationURI: "https://slopcop.com",
				Rules:          driverRules,
			}},
			Results: results,
			Invocations: []sarifInvocation{{
				ExecutionSuccessful:        response.Status != service.StatusIncomplete,
				ToolExecutionNotifications: notifications,
			}},
			Properties: map[string]any{
				"slopcop.status":     response.Status,
				"slopcop.baseCommit": response.BaseCommit,
				"slopcop.failOn":     response.Summary.FailOn,
			},
		}},
	}, "sarif report")
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
