// Package service speaks the SlopCop check API defined by the bifrost-service
// control plane in server/src/check.rs.
package service

import "strings"

// Check statuses and their exit codes, mirrored from CheckStatus. Pending is
// the CLI's own: the service answered check_running until --timeout ran out.
const (
	StatusClean      = "clean"
	StatusFindings   = "findings"
	StatusIncomplete = "incomplete"
	StatusPending    = "pending"
)

// Exit codes. Pending has its own code so a caller can tell "run the same
// check again" from a check that finished incomplete.
const (
	ExitClean      = 0
	ExitFindings   = 1
	ExitIncomplete = 2
	ExitPending    = 3
)

// Request is the POST /v1/check body. The service decodes with
// `deny_unknown_fields`, so the fields and their JSON names must match
// CheckRequest in server/src/check.rs exactly.
type Request struct {
	ContractVersion string `json:"contractVersion"`
	SinceCommit     string `json:"sinceCommit"`
	ChangeCommit    string `json:"changeCommit"`
	// RepoURL is the canonical https repository URL. SSH remotes must be
	// canonicalized by the caller.
	RepoURL string `json:"repoUrl"`
	// BaseCommit is the full 40- or 64-character hex commit the service fetches.
	BaseCommit string `json:"baseCommit"`
	// Change is the standard base64 encoding of one git packfile.
	Change string `json:"change"`
	// Selection is optional; an empty selection runs the hosted catalog.
	Selection *Selection `json:"selection,omitempty"`
	// FailOn is never, finding (unrated, or note and above), info, note, warning,
	// or error.
	FailOn string `json:"failOn,omitempty"`
}

// Selection mirrors CheckSelection. Files are repository-relative .rqlp paths
// in NOW. Includes select named rules and default rules in named categories;
// without includes, default rules run. Skips apply after includes.
type Selection struct {
	Files          []string `json:"files,omitempty"`
	PolicyIDs      []string `json:"policyIds,omitempty"`
	SkipPolicyIDs  []string `json:"skipPolicyIds,omitempty"`
	Categories     []string `json:"categories,omitempty"`
	SkipCategories []string `json:"skipCategories,omitempty"`
}

// IsEmpty reports whether the selection would serialize to an empty object.
func (s Selection) IsEmpty() bool {
	return len(s.Files) == 0 && len(s.PolicyIDs) == 0 && len(s.SkipPolicyIDs) == 0 &&
		len(s.Categories) == 0 && len(s.SkipCategories) == 0
}

// Response is the successful POST /v1/check body, mirrored from
// CheckResponse. The service answers structure only; the client renders it.
type Response struct {
	Status     string             `json:"status"`
	BaseCommit string             `json:"baseCommit"`
	Applied    bool               `json:"applied"`
	Summary    Summary            `json:"summary"`
	Findings   []Finding          `json:"findings"`
	Incomplete []IncompletePolicy `json:"incomplete"`
	// InconclusiveElsewhere lists inconclusive checks about files the change
	// does not write. Like existing findings, they never change the status.
	InconclusiveElsewhere []IncompletePolicy `json:"inconclusiveElsewhere"`
	// Failure says why the analysis did not complete, when it failed.
	Failure string `json:"failure,omitempty"`
}

// Summary mirrors CheckSummary.
type Summary struct {
	Files int `json:"files"`
	// Findings counts every finding: NewFindings plus ExistingFindings.
	Findings int `json:"findings"`
	// NewFindings counts the findings the change introduced or touched. Only
	// these decide the status.
	NewFindings int `json:"newFindings"`
	// ExistingFindings counts the findings that were already in the code.
	// They never change the status.
	ExistingFindings   int `json:"existingFindings"`
	IncompletePolicies int `json:"incompletePolicies"`
	// IncompletePolicyIDs names at most 50 incomplete checks;
	// IncompletePolicies counts them all.
	IncompletePolicyIDs []string `json:"incompletePolicyIds"`
	// InconclusiveElsewhere counts the inconclusive checks about files the
	// change does not write; IncompletePolicies does not include them.
	InconclusiveElsewhere int `json:"inconclusiveElsewhere"`
	// OmittedFindings counts findings left out because a check found more
	// than it may report. They never change the status.
	OmittedFindings uint64 `json:"omittedFindings"`
	FailOn          string `json:"failOn"`
}

// IncompletePolicy mirrors IncompletePolicy: a check that did not complete
// and why (timed_out, inconclusive, base_comparison_failed, or other).
type IncompletePolicy struct {
	PolicyID string `json:"policyId"`
	Reason   string `json:"reason"`
}

// Finding mirrors CheckFinding. Text fields come from the analyzed repository
// and are hostile.
type Finding struct {
	PolicyID  string `json:"policyId"`
	Severity  string `json:"severity"`
	Certainty string `json:"certainty"`
	Path      string `json:"path"`
	StartLine *int   `json:"startLine"`
	EndLine   *int   `json:"endLine"`
	Title     string `json:"title"`
	// New says the change introduced or touched the finding.
	New bool `json:"new"`
}

// ExitCode maps the response status to the analyzer exit contract. Only an
// explicit clean status passes; a status this client does not know fails
// closed as incomplete.
func (r Response) ExitCode() int {
	switch r.Status {
	case StatusClean:
		return ExitClean
	case StatusFindings:
		return ExitFindings
	default:
		return ExitIncomplete
	}
}

// Pending is a check that was still running when --timeout ran out. The
// service keeps running it, and the same check asked again attaches to it.
type Pending struct {
	// RetryAfter is the service's suggested wait in seconds before asking
	// again.
	RetryAfter int
	// Message and Remedy are the service's check_running text.
	Message string
	Remedy  string
}

const (
	pendingMessage = "The check is still running."
	pendingRemedy  = "Run the same check again to wait for its result."
)

func (p *Pending) Error() string { return p.Message }

// RemedyText is the message and remedy an agent prints verbatim.
func (p *Pending) RemedyText() string {
	return strings.TrimSpace(p.Message + "\n" + p.Remedy)
}
