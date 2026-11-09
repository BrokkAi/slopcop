package service

// CertaintyDefinite is the certainty of a finding Bifrost proved holds at its
// location. Every other certainty (possible, heuristic) depends on something
// Bifrost could not settle.
const CertaintyDefinite = "definite"

// OnlyDefinite returns the response with every finding whose certainty is not
// definite left out, counted in Summary.LessCertainFindings, and the status
// the service would answer for the findings that remain.
//
// The status is lowered only from findings, never raised: a check the service
// called clean or incomplete stays that. When no remaining new finding crosses
// the response's failOn, the check is incomplete if any check did not
// complete or the analysis failed, otherwise clean, as the service decides it
// (check_status in bifrost-service).
func (r Response) OnlyDefinite() Response {
	kept := make([]Finding, 0, len(r.Findings))
	hiddenNew := 0
	for _, finding := range r.Findings {
		switch {
		case finding.Certainty == CertaintyDefinite:
			kept = append(kept, finding)
		case finding.New:
			hiddenNew++
		}
	}
	hidden := len(r.Findings) - len(kept)
	if hidden == 0 {
		return r
	}
	r.Findings = kept
	r.Summary.LessCertainFindings = hidden
	r.Summary.Findings = max(r.Summary.Findings-hidden, 0)
	r.Summary.NewFindings = max(r.Summary.NewFindings-hiddenNew, 0)
	if r.Status == StatusFindings && !r.gated() {
		if r.Summary.IncompletePolicies > 0 || r.Failure != "" {
			r.Status = StatusIncomplete
		} else {
			r.Status = StatusClean
		}
	}
	return r
}

// gated reports whether a new finding crosses the response's failOn, the way
// the service's FailOn::gates decides it: finding gates every finding,
// including unrated ones, and each severity gates itself and those above it.
func (r Response) gated() bool {
	threshold, known := severityRank(r.Summary.FailOn)
	for _, finding := range r.Findings {
		if !finding.New {
			continue
		}
		switch r.Summary.FailOn {
		case "never":
			continue
		case "finding", "":
			return true
		}
		rank, _ := severityRank(finding.Severity)
		// A failOn this client does not know keeps the service's answer.
		if !known || rank >= threshold {
			return true
		}
	}
	return false
}

// severityRank orders severities as the service does; any other value is
// unrated, below note.
func severityRank(severity string) (int, bool) {
	switch severity {
	case "note":
		return 1, true
	case "warning":
		return 2, true
	case "error":
		return 3, true
	}
	return 0, false
}
