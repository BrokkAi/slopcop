// Package service speaks the SlopCop check API defined by the bifrost-service
// control plane in server/src/check.rs.
package service

// Check statuses and their exit codes, mirrored from CheckStatus.
const (
	StatusClean      = "clean"
	StatusFindings   = "findings"
	StatusIncomplete = "incomplete"
)

// Request is the POST /v1/check body. The service decodes with
// `deny_unknown_fields`, so the fields and their JSON names must match
// CheckRequest in server/src/check.rs exactly.
type Request struct {
	// RepoURL is the canonical https repository URL. SSH remotes must be
	// canonicalized by the caller.
	RepoURL string `json:"repoUrl"`
	// BaseCommit is the full 40- or 64-character hex commit the change applies to.
	BaseCommit string `json:"baseCommit"`
	// Change is the standard base64 encoding of one git patch.
	Change string `json:"change"`
	// Selection is optional; an empty selection runs the hosted catalog.
	Selection *Selection `json:"selection,omitempty"`
	// FailOn is never, finding, note, warning, or error.
	FailOn string `json:"failOn,omitempty"`
}

// Selection mirrors CheckSelection. File entries are repository-relative
// .rqlp paths resolved inside the patched tree. The service currently
// evaluates only Files, so the CLI exposes only the file selector.
type Selection struct {
	Files      []string `json:"files,omitempty"`
	Packs      []string `json:"packs,omitempty"`
	PolicyIDs  []string `json:"policyIds,omitempty"`
	Categories []string `json:"categories,omitempty"`
}

// IsEmpty reports whether the selection would serialize to an empty object.
func (s Selection) IsEmpty() bool {
	return len(s.Files) == 0 && len(s.Packs) == 0 &&
		len(s.PolicyIDs) == 0 && len(s.Categories) == 0
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
	// Failure says why the analysis did not complete, when it failed.
	Failure string `json:"failure,omitempty"`
}

// Summary mirrors CheckSummary.
type Summary struct {
	Files              int `json:"files"`
	Findings           int `json:"findings"`
	IncompletePolicies int `json:"incompletePolicies"`
	// IncompletePolicyIDs names at most 50 incomplete checks;
	// IncompletePolicies counts them all.
	IncompletePolicyIDs []string `json:"incompletePolicyIds"`
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
}

// ExitCode maps the response status to the analyzer exit contract.
func (r Response) ExitCode() int {
	switch r.Status {
	case StatusFindings:
		return 1
	case StatusIncomplete:
		return 2
	default:
		return 0
	}
}
