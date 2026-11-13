package service

import (
	"encoding/json"
	"regexp"
	"time"
	"unicode"
	"unicode/utf8"
)

// DependencyAdvisoryPolicy is the check whose findings are known-vulnerable
// dependency versions a change brings in. Its findings carry a Dependency.
const DependencyAdvisoryPolicy = "slopcop.dependencies.known-vulnerability"

// osvRecordURL is where an advisory is published. SourceURL is always this
// followed by the advisory ID, never a URL taken from the response as sent.
const osvRecordURL = "https://osv.dev/vulnerability/"

// The service's bounds on dependency evidence. A value past one is not
// shown at all.
const (
	maxDependencyText      = 256
	maxDependencyChainStep = 512
	maxDependencyPath      = 1024
	maxUpgradeCommand      = 512
	maxDependencyChain     = 16
	maxDependencyList      = 64
	maxAdvisoryID          = 128
)

// Dependency mirrors CheckDependency: what a dependency advisory finding is
// about and how to fix it. The values come from the repository's lockfile and
// from OSV.dev, so they are hostile; Finding keeps one only when every value
// is within the service's bounds and safe to print.
type Dependency struct {
	// Ecosystem is OSV's ecosystem name, such as npm.
	Ecosystem string `json:"ecosystem"`
	Package   string `json:"package"`
	// Version is the exact resolved version.
	Version string `json:"version"`
	// Direct says a root manifest names the package. False also when that
	// could not be established.
	Direct bool `json:"direct"`
	// ManifestPath is the manifest of the project the package is installed
	// for.
	ManifestPath string `json:"manifestPath"`
	// DependencyChain is name@version from the direct dependency down to
	// this package. Empty when no bounded chain could be established.
	DependencyChain []string `json:"dependencyChain"`
	// AdvisoryID is the advisory's primary OSV ID, a GHSA ID when there is
	// one; Aliases are its other IDs.
	AdvisoryID string   `json:"advisoryId"`
	Aliases    []string `json:"aliases"`
	SourceURL  string   `json:"sourceUrl"`
	// FixedVersions lists the fixed versions on the installed version's
	// line, lowest first. Empty when OSV lists none.
	FixedVersions []string `json:"fixedVersions"`
	// UpgradeCommand is set only for a direct npm dependency with a fixed
	// version, and only when it is exactly `npm install <package>@<fix>
	// --save-exact`: the client clears any other.
	UpgradeCommand string `json:"upgradeCommand,omitempty"`
	// DatabaseModified is when OSV last changed the advisory, RFC 3339.
	DatabaseModified string `json:"databaseModified"`
}

// UnmarshalJSON decodes a finding, dropping a dependency object that is
// malformed, oversized or unsafe to print instead of failing the check: the
// finding itself still answers, as the service does with stored evidence it
// cannot validate.
func (f *Finding) UnmarshalJSON(data []byte) error {
	type plain Finding
	var decoded struct {
		plain
		Dependency json.RawMessage `json:"dependency"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*f = Finding(decoded.plain)
	f.Dependency = nil
	if len(decoded.Dependency) == 0 {
		return nil
	}
	var dependency Dependency
	if json.Unmarshal(decoded.Dependency, &dependency) == nil && dependency.validate() {
		f.Dependency = &dependency
	}
	return nil
}

// validate reports whether every value is within the service's bounds and
// safe to print, and rebuilds SourceURL from the advisory ID.
func (d *Dependency) validate() bool {
	valid := dependencyText(d.Ecosystem, maxDependencyText) &&
		dependencyText(d.Package, maxDependencyText) &&
		dependencyText(d.Version, maxDependencyText) &&
		dependencyText(d.ManifestPath, maxDependencyPath) &&
		dependencyList(d.DependencyChain, maxDependencyChain, maxDependencyChainStep) &&
		advisoryID(d.AdvisoryID) &&
		len(d.Aliases) <= maxDependencyList &&
		dependencyList(d.FixedVersions, maxDependencyList, maxDependencyText) &&
		(d.UpgradeCommand == "" || dependencyText(d.UpgradeCommand, maxUpgradeCommand)) &&
		len(d.DatabaseModified) <= maxDependencyText
	if !valid {
		return false
	}
	for _, alias := range d.Aliases {
		if !advisoryID(alias) {
			return false
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, d.DatabaseModified); err != nil {
		return false
	}
	d.SourceURL = osvRecordURL + d.AdvisoryID
	if !safeUpgradeCommand(*d) {
		d.UpgradeCommand = ""
	}
	return true
}

// npmName is a registry package name, scoped or not, with nothing a shell
// would read as more than one word.
var npmName = regexp.MustCompile(`^(@[a-z0-9~-][a-z0-9._~-]*/)?[a-z0-9~-][a-z0-9._~-]*$`)

// npmVersion is a semver version, with no shell metacharacter.
var npmVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.+-]+)?$`)

// safeUpgradeCommand reports whether UpgradeCommand is exactly the command
// the contract describes, `npm install <package>@<fix> --save-exact` for one
// of the fixed versions, with a package name and version a shell reads as
// they are. An agent may run it, so the client keeps nothing else, even from
// the service: the package name comes from the repository's lockfile.
func safeUpgradeCommand(d Dependency) bool {
	if d.UpgradeCommand == "" || d.Ecosystem != "npm" || !d.Direct || !npmName.MatchString(d.Package) {
		return false
	}
	for _, fixed := range d.FixedVersions {
		if npmVersion.MatchString(fixed) && d.UpgradeCommand == "npm install "+d.Package+"@"+fixed+" --save-exact" {
			return true
		}
	}
	return false
}

func dependencyList(values []string, maxLen, maxChars int) bool {
	if len(values) > maxLen {
		return false
	}
	for _, value := range values {
		if !dependencyText(value, maxChars) {
			return false
		}
	}
	return true
}

// dependencyText is a non-empty, bounded, single-line value with no control
// or invisible formatting character, which could make a terminal show text
// other than what it holds.
func dependencyText(value string, maxChars int) bool {
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxChars {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == 0x2028 || r == 0x2029 ||
			(r >= 0x200B && r <= 0x200F) || (r >= 0x202A && r <= 0x202E) ||
			(r >= 0x2060 && r <= 0x2069) || r == 0xFEFF {
			return false
		}
	}
	return true
}

// advisoryID is an OSV record ID: ASCII letters, digits, '.', '_', ':' and
// '-', starting with a letter or digit, so it is one URL path segment.
func advisoryID(value string) bool {
	if value == "" || len(value) > maxAdvisoryID || !isASCIIAlnum(value[0]) {
		return false
	}
	for i := 0; i < len(value); i++ {
		if b := value[i]; !isASCIIAlnum(b) && b != '.' && b != '_' && b != ':' && b != '-' {
			return false
		}
	}
	return true
}

func isASCIIAlnum(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}
