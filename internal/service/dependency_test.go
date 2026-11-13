package service

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func decodeFinding(t *testing.T, body string) Finding {
	t.Helper()
	var finding Finding
	if err := json.Unmarshal([]byte(body), &finding); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return finding
}

func TestFindingDecodesDependencyEvidence(t *testing.T) {
	finding := decodeFinding(t, `{"id":"a","policyId":"slopcop.dependencies.known-vulnerability","path":"package-lock.json",
		"dependency":{"ecosystem":"npm","package":"lodash","version":"4.17.20","direct":true,"manifestPath":"package.json",
		"dependencyChain":["lodash@4.17.20"],"advisoryId":"GHSA-29mw-wpgm-hmr9","aliases":["CVE-2020-28500"],
		"sourceUrl":"https://osv.dev/vulnerability/GHSA-29mw-wpgm-hmr9","fixedVersions":["4.17.21"],
		"upgradeCommand":"npm install lodash@4.17.21 --save-exact","databaseModified":"2025-09-29T21:12:31Z","futureField":1}}`)
	direct := true
	want := &Dependency{
		Ecosystem: "npm", Package: "lodash", Version: "4.17.20", Direct: &direct, ManifestPath: "package.json",
		DependencyChain: []string{"lodash@4.17.20"}, AdvisoryID: "GHSA-29mw-wpgm-hmr9", Aliases: []string{"CVE-2020-28500"},
		SourceURL: "https://osv.dev/vulnerability/GHSA-29mw-wpgm-hmr9", FixedVersions: []string{"4.17.21"},
		UpgradeCommand: "npm install lodash@4.17.21 --save-exact", DatabaseModified: "2025-09-29T21:12:31Z",
	}
	if !reflect.DeepEqual(finding.Dependency, want) {
		t.Fatalf("dependency = %+v, want %+v", finding.Dependency, want)
	}
}

func TestFindingWithoutDependencyEvidenceIsUnchanged(t *testing.T) {
	for _, body := range []string{
		`{"id":"a","policyId":"p","path":"x.go","title":"t","new":true}`,
		`{"id":"a","policyId":"p","path":"x.go","title":"t","new":true,"dependency":null}`,
		// Malformed evidence never fails the response; without a package it
		// is treated as absent.
		`{"id":"a","policyId":"p","path":"x.go","title":"t","new":true,"dependency":"lodash"}`,
		`{"id":"a","policyId":"p","path":"x.go","title":"t","new":true,"dependency":[1,2]}`,
		`{"id":"a","policyId":"p","path":"x.go","title":"t","new":true,"dependency":{"package":7,"version":"1"}}`,
	} {
		finding := decodeFinding(t, body)
		if finding.Dependency != nil || finding.ID != "a" || finding.Title != "t" || !finding.New {
			t.Errorf("%s decoded to %+v", body, finding)
		}
	}
}

func TestDependencyDropsFieldsOfTheWrongType(t *testing.T) {
	finding := decodeFinding(t, `{"dependency":{"package":"qs","version":6,"direct":"yes","aliases":"CVE-1",
		"fixedVersions":[1,"6.7.3"],"dependencyChain":{"a":1},"sourceUrl":"javascript:alert(1)"}}`)
	got := finding.Dependency
	if got == nil || got.Package != "qs" || got.Version != "" || got.Direct != nil || got.Aliases != nil ||
		got.FixedVersions != nil || got.DependencyChain != nil || got.SourceURL != "" {
		t.Fatalf("dependency = %+v", got)
	}
}

func TestDependencyBoundsOversizedEvidence(t *testing.T) {
	chain := make([]string, 40)
	for i := range chain {
		chain[i] = "pkg" + strings.Repeat("x", i%3) + "@1"
	}
	chain[39] = "vulnerable@1.0.0"
	aliases := make([]string, 50)
	for i := range aliases {
		aliases[i] = "CVE-0"
	}
	evidence := map[string]any{
		"package":         strings.Repeat("p", 10000),
		"summary":         strings.Repeat("s", 100000),
		"sourceUrl":       "https://osv.dev/" + strings.Repeat("u", 1000),
		"dependencyChain": chain,
		"aliases":         aliases,
	}
	body, err := json.Marshal(map[string]any{"dependency": evidence})
	if err != nil {
		t.Fatal(err)
	}
	got := decodeFinding(t, string(body)).Dependency
	if got == nil || !got.Truncated {
		t.Fatalf("dependency = %+v", got)
	}
	if n := len([]rune(got.Package)); n != maxDependencyText {
		t.Errorf("package runes = %d", n)
	}
	if n := len([]rune(got.Summary)); n != maxDependencySummary {
		t.Errorf("summary runes = %d", n)
	}
	if got.SourceURL != "" {
		t.Errorf("a cut URL was kept: %q", got.SourceURL)
	}
	if len(got.Aliases) != maxDependencyList {
		t.Errorf("aliases = %d", len(got.Aliases))
	}
	if len(got.DependencyChain) != maxDependencyChain || got.DependencyChain[maxDependencyChain-1] != "vulnerable@1.0.0" ||
		got.DependencyChainOmitted != 40-maxDependencyChain {
		t.Errorf("chain = %v, omitted %d", got.DependencyChain, got.DependencyChainOmitted)
	}

	// Hook state stores the finding and reads it back unchanged.
	stored, err := json.Marshal(Finding{Dependency: got})
	if err != nil {
		t.Fatal(err)
	}
	if again := decodeFinding(t, string(stored)).Dependency; !reflect.DeepEqual(again, got) {
		t.Errorf("round trip = %+v, want %+v", again, got)
	}
}
