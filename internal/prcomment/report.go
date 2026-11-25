// Package prcomment puts a check's report on a pull request: new static and
// review findings as inline review comments on the lines the pull request
// changes, and one summary comment, updated in place on every run, that says
// in words what the check found and lists the findings no line could hold.
//
// It reads the report `slopcop check --format json` printed, so the check runs
// once and the comments say exactly what it said. Every finding's text comes
// from the checked repository or from a reviewer that read it, so the comments
// quote it as data: control characters replaced, HTML and mentions defused.
package prcomment

import (
	"encoding/json"
	"fmt"

	"github.com/BrokkAi/slopcop/internal/review"
	"github.com/BrokkAi/slopcop/internal/service"
)

// Report is the part of a `--format json` report the comments use. It reads
// every report shape a check prints: a completed check, a failed or refused
// one, one still running, a skipped one, and the review-only report.
type Report struct {
	CheckID      string `json:"checkId"`
	Status       string `json:"status"`
	StaticStatus string `json:"staticStatus"`
	// ExitReason names what made the check exit 1: static_findings,
	// review_findings or static_and_review_findings.
	ExitReason  string                     `json:"exitReason"`
	EmptyChange bool                       `json:"emptyChange"`
	BaseCommit  string                     `json:"baseCommit"`
	Summary     *Summary                   `json:"summary"`
	Findings    []service.Finding          `json:"findings"`
	Review      *Review                    `json:"review"`
	Skipped     string                     `json:"reviewSkipped"`
	Incomplete  []service.IncompletePolicy `json:"incomplete"`
	// Code, Message and Remedy are set on a failed, refused, skipped or
	// pending check.
	Code    string `json:"code"`
	Message string `json:"message"`
	Remedy  string `json:"remedy"`
	// Failure is set when the analysis ran but did not complete.
	Failure string `json:"failure"`
	// RepositoryFailOn is the repository's [check] fail_on, set when the
	// checked tree or its base sets one.
	RepositoryFailOn *service.RepositoryFailOn `json:"repositoryFailOn"`
}

// Summary is the service's counts, plus the review's.
type Summary struct {
	NewFindings        int  `json:"newFindings"`
	ExistingFindings   int  `json:"existingFindings"`
	OmittedFindings    int  `json:"omittedFindings"`
	IncompletePolicies int  `json:"incompletePolicies"`
	ReviewFindings     *int `json:"reviewFindings"`
	// FailOn is the threshold that decided the static status.
	FailOn string `json:"failOn"`
}

// Review is the local review's verdict.
type Review struct {
	Status     string            `json:"status"`
	Error      string            `json:"error"`
	Incomplete bool              `json:"incomplete"`
	Reviewers  []review.Reviewer `json:"reviewers"`
	Findings   []review.Finding  `json:"findings"`
}

// ParseReport reads a `--format json` report.
func ParseReport(data []byte) (Report, error) {
	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		return Report{}, fmt.Errorf("read the check report: %w", err)
	}
	if report.Status == "" {
		return Report{}, fmt.Errorf("read the check report: it has no status; pass the output of `slopcop check --format json`")
	}
	return report, nil
}

// NewStatic is the report's new static findings. A report from
// --show-existing also lists findings already in the code, which never fail
// a check and are not the pull request's to answer for.
func (r Report) NewStatic() []service.Finding {
	findings := []service.Finding{}
	for _, finding := range r.Findings {
		if finding.New {
			findings = append(findings, finding)
		}
	}
	return findings
}

// ReviewFindings is what the local reviewers reported, overridden findings
// left out.
func (r Report) ReviewFindings() []review.Finding {
	if r.Review == nil {
		return nil
	}
	return r.Review.Findings
}
