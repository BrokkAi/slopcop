// Package service speaks the SlopCop check API defined by the bifrost-service
// control plane in server/src/check.rs.
package service

import "strings"

// Check statuses and their exit codes, mirrored from CheckStatus. Pending and
// failed are the CLI's own: the service answered check_running until
// --timeout ran out, or refused the check with an error.
const (
	StatusClean      = "clean"
	StatusFindings   = "findings"
	StatusIncomplete = "incomplete"
	StatusPending    = "pending"
	StatusFailed     = "failed"
)

// Exit codes. Pending has its own code so a caller can tell "run the same
// check again" from a check that finished incomplete. NeedsPerson is a
// refusal only a person can clear, so a script stops instead of retrying.
// Interrupted is the shell's code for a process stopped by Ctrl-C: no result
// was waited for.
const (
	ExitClean       = 0
	ExitFindings    = 1
	ExitIncomplete  = 2
	ExitPending     = 3
	ExitNeedsPerson = 4
	ExitInterrupted = 130
)

// Request is the POST /v1/check body. The service decodes with
// `deny_unknown_fields`, so the fields and their JSON names must match
// CheckRequest in server/src/check.rs exactly.
type Request struct {
	// RepoURL is the canonical https repository URL. SSH remotes must be
	// canonicalized by the caller.
	RepoURL string `json:"repoUrl"`
	// BaseCommit is the full 40-character hex commit the change applies to; the
	// service does not support SHA-256 repositories.
	BaseCommit string `json:"baseCommit"`
	// Change is the standard base64 encoding of one git patch.
	Change string `json:"change"`
	// Selection is optional; an empty selection runs the hosted catalog.
	Selection *Selection `json:"selection,omitempty"`
	// FailOn is never, finding, note, warning, or error.
	FailOn string `json:"failOn,omitempty"`
	// Certainty is all, or empty for the service's default, definite: it
	// then answers only definite findings and never sends the rest.
	Certainty string `json:"certainty,omitempty"`
	// Attribution is optional and advisory: the service validates each field
	// on its own and drops what fails, so it never refuses a check.
	Attribution *Attribution `json:"attribution,omitempty"`
	// AddRepository confirms adding the repository to the workspace that
	// then checks and bills it. The CLI sends it only for an explicit,
	// approved add: `slopcop enable` at a terminal, or --add-repository for
	// the workspace a person named. A check never adds a repository by
	// itself.
	AddRepository bool `json:"addRepository,omitempty"`
}

// Attribution mirrors the check request's attribution: which agent harness
// and models wrote the change. All of it is asserted by the client.
type Attribution struct {
	Harness        string `json:"harness,omitempty"`
	HarnessVersion string `json:"harnessVersion,omitempty"`
	// SessionID is the harness's own opaque session id.
	SessionID string            `json:"sessionId,omitempty"`
	Models    []AttributedModel `json:"models,omitempty"`
}

// AttributedModel is one model, at one effort level, and the
// repository-relative files it wrote. The service keeps only the files the
// change writes, and ties a finding to the model whose files hold its path.
type AttributedModel struct {
	Model  string   `json:"model"`
	Effort string   `json:"effort,omitempty"`
	Source string   `json:"source,omitempty"`
	Files  []string `json:"files,omitempty"`
}

// Selection mirrors CheckSelection. File entries are repository-relative
// .rqlp paths resolved inside the patched tree, which the service runs on top
// of its hosted catalog. The CLI sends only Files.
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
	// CheckID is the check that answered. A service from before it omits it.
	CheckID    string             `json:"checkId,omitempty"`
	Status     string             `json:"status"`
	BaseCommit string             `json:"baseCommit"`
	Applied    bool               `json:"applied"`
	Summary    Summary            `json:"summary"`
	Findings   []Finding          `json:"findings"`
	Incomplete []IncompletePolicy `json:"incomplete"`
	// Inconclusive names at most 50 of the Summary.Inconclusive checks, one
	// per policy, sorted by id. A service from before it omits it.
	Inconclusive []IncompletePolicy `json:"inconclusive"`
	// InconclusiveElsewhere names the Inconclusive checks about files the
	// change does not write, at most 50, sorted by id.
	InconclusiveElsewhere []IncompletePolicy `json:"inconclusiveElsewhere"`
	// RunFailures names each cause that left every check in an analyzer run
	// incomplete, most-affected first. A service from before them omits it.
	RunFailures []RunFailure `json:"runFailures,omitempty"`
	// Failure says why the analysis did not complete, when it failed.
	Failure string `json:"failure,omitempty"`
	// RepositoryAdded says the check carried an approved add and the service
	// added the repository to the workspace. A service from before it omits
	// it.
	RepositoryAdded bool `json:"repositoryAdded,omitempty"`
	// Exclusions is what the [check] section of the checked tree's
	// .slopcop/config.toml excluded. Nil when the tree has none, or from a
	// service from before BrokkAi/bifrost-service#1103.
	Exclusions *Exclusions `json:"exclusions,omitempty"`
}

// Exclusions mirrors CheckExclusions. Paths, rules and reasons come from the
// analyzed repository and are hostile.
type Exclusions struct {
	// Config is the file the scopes come from, .slopcop/config.toml.
	Config string `json:"config"`
	// Scopes lists exclude first, when set, then each exclude_rules entry in
	// the file's order, including one that matched nothing in this change.
	Scopes   []ExclusionScope   `json:"scopes"`
	Warnings []ExclusionWarning `json:"warnings"`
	// ExcludedFiles counts the changed files exclude kept from every rule.
	ExcludedFiles int `json:"excludedFiles"`
	// ExcludedPairs counts the distinct (file, rule) pairs the
	// exclude_rules entries kept from the check.
	ExcludedPairs int `json:"excludedPairs"`
	// ExcludedFindings counts the findings those entries dropped.
	ExcludedFindings int `json:"excludedFindings"`
	// OnlyExcluded says the change wrote files and [check] left nothing to
	// check, so a clean status means nothing ran.
	OnlyExcluded bool `json:"onlyExcluded"`
}

// ExclusionScope mirrors ExclusionScope: exclude (AllRules) or one
// exclude_rules entry.
type ExclusionScope struct {
	// Paths are the entry's gitignore patterns.
	Paths    []string `json:"paths"`
	AllRules bool     `json:"allRules,omitempty"`
	// RulePatterns are the entry's rules as written; Rules the selected
	// policies they name, at most 300, which RuleCount counts.
	RulePatterns []string `json:"rulePatterns,omitempty"`
	Rules        []string `json:"rules,omitempty"`
	RuleCount    int      `json:"ruleCount,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	// Files are the changed files the paths match, at most 300, which
	// FileCount counts.
	Files     []string `json:"files"`
	FileCount int      `json:"fileCount"`
	Pairs     int      `json:"pairs,omitempty"`
	// NotRun names the rules excluded on every changed file, which did not
	// run at all.
	NotRun []string `json:"notRun,omitempty"`
}

// ExclusionWarning mirrors ExclusionWarning. Kind is unmatched_rule for a
// rule pattern that names no selected policy, or unknown.
type ExclusionWarning struct {
	Kind string `json:"kind"`
	Rule string `json:"rule"`
}

// Summary mirrors CheckSummary.
type Summary struct {
	Files int `json:"files"`
	// Findings counts the listed findings. The service lists only new ones,
	// so it equals NewFindings; ExistingFindings counts the rest.
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
	// Inconclusive counts the checks that ran and could not prove there is
	// nothing more to find. That is the norm for static analysis, not a
	// fault: they never change the status and IncompletePolicies does not
	// include them.
	Inconclusive int `json:"inconclusive"`
	// InconclusiveElsewhere counts the Inconclusive checks about files the
	// change does not write. A service from before Inconclusive counted only
	// these, and IncompletePolicies counted the rest.
	InconclusiveElsewhere int `json:"inconclusiveElsewhere"`
	// OmittedFindings counts new findings left out because a check found
	// more than it may report. A check keeps new findings first, so this is
	// not zero only when the change itself has more than the bound; findings
	// already in the code that were left out count in ExistingFindings. They
	// never change the status.
	OmittedFindings uint64 `json:"omittedFindings"`
	// RemappedIgnores counts the findings flagged PreviouslyIgnored, so a
	// match to an earlier ignore can be audited. A service from before
	// BrokkAi/bifrost-service#953 omits it.
	RemappedIgnores int    `json:"remappedIgnores"`
	FailOn          string `json:"failOn"`
	// LessCertainFindings counts the new findings the service left out
	// because their certainty is not definite and the request did not ask
	// for all. Findings and NewFindings do not count them and they never
	// change the status. A service from before it omits it.
	LessCertainFindings int `json:"lessCertainFindings,omitempty"`
	// ExcludedFiles and ExcludedPairs count what .slopcop/config.toml [check]
	// kept from the check (Exclusions lists it). OnlyExcluded says the change
	// wrote files and [check] left nothing to check: the status is then
	// clean, and nothing ran. A service from before them omits them.
	ExcludedFiles int  `json:"excludedFiles,omitempty"`
	ExcludedPairs int  `json:"excludedPairs,omitempty"`
	OnlyExcluded  bool `json:"onlyExcluded,omitempty"`
}

// IncompletePolicy mirrors IncompletePolicy: a check that did not complete
// and why (timed_out, unreliable, base_comparison_failed, unsupported, or
// other; a service from before unreliable says inconclusive).
type IncompletePolicy struct {
	PolicyID string `json:"policyId"`
	Reason   string `json:"reason"`
	// Detail names, for an unreliable, unsupported or other check, Bifrost's
	// identifiers for why it did not complete as key=value pairs, such as
	// `type=failed reasons=source_read_failed`. A service from before
	// BrokkAi/bifrost-service#993 omits it, and one from before run failures
	// omits it for unreliable.
	Detail string `json:"detail,omitempty"`
	// Scope is "run" when every check in the analyzer run shares the cause,
	// which RunFailures then names once. Empty otherwise.
	Scope string `json:"scope,omitempty"`
}

// ScopeRun marks an incomplete check whose cause is the whole analyzer run's.
const ScopeRun = "run"

// RunFailure mirrors RunFailure: one cause that left every check in an
// analyzer run incomplete. Policies counts every check it left incomplete,
// including those past the listed bound.
type RunFailure struct {
	Reason   string `json:"reason"`
	Detail   string `json:"detail,omitempty"`
	Policies int    `json:"policies"`
}

// Finding mirrors CheckFinding. Text fields come from the analyzed repository
// and are hostile.
type Finding struct {
	// ID names this finding for `slopcop ignore`. A service from before
	// findings could be ignored omits it.
	ID        string `json:"id,omitempty"`
	PolicyID  string `json:"policyId"`
	Severity  string `json:"severity"`
	Certainty string `json:"certainty"`
	Path      string `json:"path"`
	StartLine *int   `json:"startLine"`
	EndLine   *int   `json:"endLine"`
	Title     string `json:"title"`
	// New says the change introduced or touched the finding.
	New bool `json:"new"`
	// PreviouslyIgnored is the id of an ignored finding this one plausibly
	// is: the same rule in the same file at the same place, under a new id
	// because its code changed. The ignore does not cover it; `slopcop
	// ignore` with ID does. A service from before
	// BrokkAi/bifrost-service#953 omits it.
	PreviouslyIgnored string `json:"previouslyIgnored,omitempty"`
}

// KnownStatus reports whether this client knows the response status.
func (r Response) KnownStatus() bool {
	switch r.Status {
	case StatusClean, StatusFindings, StatusIncomplete:
		return true
	}
	return false
}

// ExitCode maps the response status to the analyzer exit contract. Only
// clean exits 0: a status this client does not know never passes a check.
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
