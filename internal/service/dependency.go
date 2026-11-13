package service

import (
	"encoding/json"
	"net/url"
	"strings"
	"unicode/utf8"
)

// Dependency is the optional structured evidence the service attaches to a
// dependency-advisory finding: which resolved package carries which known
// advisory, how the repository depends on it, and how to remediate it. The
// service reads the manifests and lockfiles and looks the advisory up; the
// client only shows what it sent.
//
// A service from before dependency advisories omits it. Every string comes
// from the analyzed repository or an advisory database and is hostile, so
// decoding never fails the response: a field of the wrong type is dropped,
// and an oversized string or list is cut to a bound and marked Truncated.
type Dependency struct {
	Ecosystem string `json:"ecosystem,omitempty"`
	Package   string `json:"package,omitempty"`
	// Version is the exact resolved version.
	Version string `json:"version,omitempty"`
	// Direct says the manifest names the package; false says it is
	// transitive. Nil when the service did not say.
	Direct       *bool  `json:"direct,omitempty"`
	ManifestPath string `json:"manifestPath,omitempty"`
	// DependencyChain runs from the direct dependency to the vulnerable
	// package, such as ["express@4.17.1", "qs@6.7.0"].
	DependencyChain []string `json:"dependencyChain,omitempty"`
	// DependencyChainOmitted counts the middle links the client cut from a
	// chain longer than its bound. The client sets it; the service does not.
	DependencyChainOmitted int    `json:"dependencyChainOmitted,omitempty"`
	AdvisoryID             string `json:"advisoryId,omitempty"`
	// Aliases are the advisory's other ids, such as a CVE.
	Aliases []string `json:"aliases,omitempty"`
	// SourceURL is the advisory's page. Only an https URL is kept.
	SourceURL string `json:"sourceUrl,omitempty"`
	// Summary is the advisory's short summary.
	Summary       string   `json:"summary,omitempty"`
	FixedVersions []string `json:"fixedVersions,omitempty"`
	// UpgradeCommand is a safe upgrade the service suggests, such as
	// `npm install lodash@4.17.21 --save-exact`. The client never runs it.
	UpgradeCommand   string `json:"upgradeCommand,omitempty"`
	DatabaseModified string `json:"databaseModified,omitempty"`
	// Truncated says the client cut a string or list that was longer than
	// its bound. The client sets it; the service does not.
	Truncated bool `json:"truncated,omitempty"`
}

// Bounds on the dependency evidence the client keeps. They are generous for
// real advisories and keep a hostile or broken response from printing
// without end.
const (
	maxDependencyText    = 200
	maxDependencySummary = 500
	maxDependencyURL     = 500
	maxDependencyList    = 10
	maxDependencyChain   = 12
)

// UnmarshalJSON decodes the evidence field by field, so a malformed field,
// or an object that is not one at all, never fails the check response.
func (d *Dependency) UnmarshalJSON(data []byte) error {
	*d = Dependency{}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return nil
	}
	text := func(name string, bound int) string {
		var value string
		if json.Unmarshal(fields[name], &value) != nil {
			return ""
		}
		return d.cut(value, bound)
	}
	list := func(name string) []string {
		var values []string
		if json.Unmarshal(fields[name], &values) != nil {
			return nil
		}
		if len(values) > maxDependencyList {
			values = values[:maxDependencyList]
			d.Truncated = true
		}
		kept := make([]string, 0, len(values))
		for _, value := range values {
			if value = d.cut(value, maxDependencyText); value != "" {
				kept = append(kept, value)
			}
		}
		if len(kept) == 0 {
			return nil
		}
		return kept
	}

	d.Ecosystem = text("ecosystem", maxDependencyText)
	d.Package = text("package", maxDependencyText)
	d.Version = text("version", maxDependencyText)
	// A JSON null decodes into a bool without error, so it is checked apart.
	var direct bool
	if raw := fields["direct"]; string(raw) != "null" && json.Unmarshal(raw, &direct) == nil {
		d.Direct = &direct
	}
	d.ManifestPath = text("manifestPath", maxDependencyText)
	d.decodeChain(fields)
	d.AdvisoryID = text("advisoryId", maxDependencyText)
	d.Aliases = list("aliases")
	d.SourceURL = httpsURL(text("sourceUrl", maxDependencyURL))
	d.Summary = text("summary", maxDependencySummary)
	d.FixedVersions = list("fixedVersions")
	d.UpgradeCommand = text("upgradeCommand", maxDependencyText)
	d.DatabaseModified = text("databaseModified", maxDependencyText)
	var truncated bool
	if json.Unmarshal(fields["truncated"], &truncated) == nil && truncated {
		d.Truncated = true
	}
	return nil
}

// decodeChain keeps the first links and the vulnerable package at the end of
// a chain longer than the bound, and counts the links it cut. A chain the
// client cut before, read back from hook state, keeps its count.
func (d *Dependency) decodeChain(fields map[string]json.RawMessage) {
	var chain []string
	if json.Unmarshal(fields["dependencyChain"], &chain) != nil {
		return
	}
	var omitted int
	if json.Unmarshal(fields["dependencyChainOmitted"], &omitted) == nil && omitted > 0 {
		d.DependencyChainOmitted = omitted
	}
	if len(chain) > maxDependencyChain {
		d.Truncated = true
		d.DependencyChainOmitted += len(chain) - maxDependencyChain
		chain = append(chain[:maxDependencyChain-1:maxDependencyChain-1], chain[len(chain)-1])
	}
	for _, link := range chain {
		if link = d.cut(link, maxDependencyText); link != "" {
			d.DependencyChain = append(d.DependencyChain, link)
		}
	}
}

// cut trims a string to at most bound runes, marking the evidence Truncated
// when it had to cut.
func (d *Dependency) cut(value string, bound int) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= bound {
		return value
	}
	d.Truncated = true
	runes := []rune(value)
	return string(runes[:bound-1]) + "…"
}

// httpsURL keeps an absolute https URL with a host, and drops anything else,
// so a report never links to a scheme a terminal or viewer might act on.
func httpsURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || strings.HasSuffix(value, "…") {
		return ""
	}
	return value
}

// UnmarshalJSON decodes a finding and drops dependency evidence that names no
// package, so every report treats it like a finding without any.
func (f *Finding) UnmarshalJSON(data []byte) error {
	type plain Finding
	if err := json.Unmarshal(data, (*plain)(f)); err != nil {
		return err
	}
	if !f.Dependency.Usable() {
		f.Dependency = nil
	}
	return nil
}

// Usable reports whether the evidence names a package, the least a report
// can show. Anything less is treated as absent.
func (d *Dependency) Usable() bool {
	return d != nil && d.Package != ""
}
