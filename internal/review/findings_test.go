package review

import (
	"fmt"
	"reflect"
	"testing"
)

func lines(start, end int) (*int, *int) { return &start, &end }

// TestParseFindings checks the shapes reviewers write the rubric's format
// in: dashes or backticks around the location, the explanation on the same
// line or the next, a bold or numbered item, and a rule citation after the
// location that must not be taken for it.
func TestParseFindings(t *testing.T) {
	s1, e1 := lines(198, 199)
	s2, e2 := lines(13, 13)
	s3, e3 := lines(1843, 1847)
	s4, e4 := lines(7, 9)
	tests := []struct {
		name    string
		text    string
		changed map[string]bool
		want    []Finding
	}{
		{
			name: "dash and same-line explanation",
			text: "- [P2] Validate skill frontmatter — internal/cli/doctor.go:198-199. If a skill file is malformed, doctor reports ready.",
			want: []Finding{{Reviewer: "1-terra", Priority: "P2", Path: "internal/cli/doctor.go", StartLine: s1, EndLine: e1, Title: "Validate skill frontmatter", Message: "If a skill file is malformed, doctor reports ready."}},
		},
		{
			name: "backticked location, explanation on the next lines",
			text: "- [P1] Keep the rules release independent — `release-config.rules.json:13`  \n  The rules release now waits\n  for the model release.\n",
			want: []Finding{{Reviewer: "1-terra", Priority: "P1", Path: "release-config.rules.json", StartLine: s2, EndLine: e2, Title: "Keep the rules release independent", Message: "The rules release now waits for the model release."}},
		},
		{
			name: "rule citation after the location",
			text: "1. **[P1] Propagate verdicts** (`crates/flow/witness.rs:1843-1847`): consumers ignore it, see [AGENTS.md:153](AGENTS.md#L153).",
			want: []Finding{{Reviewer: "1-terra", Priority: "P1", Path: "crates/flow/witness.rs", StartLine: s3, EndLine: e3, Title: "Propagate verdicts", Message: "consumers ignore it, see [AGENTS.md:153](AGENTS.md#L153)."}},
		},
		{
			name: "two findings and a preamble",
			text: "Two problems:\n\n- [P0] Crash on empty input in ./a.go:7-9\n- [P3] No location given\n",
			want: []Finding{
				{Reviewer: "1-terra", Priority: "P0", Path: "a.go", StartLine: s4, EndLine: e4, Title: "Crash on empty input"},
				{Reviewer: "1-terra", Priority: "P3", Title: "No location given"},
			},
		},
		{
			name: "extensionless file, citation later",
			text: "- [P2] Wrong target in Makefile:7-9. Contradicts [AGENTS.md:13](AGENTS.md#L13).",
			want: []Finding{{Reviewer: "1-terra", Priority: "P2", Path: "Makefile", StartLine: s4, EndLine: e4, Title: "Wrong target", Message: "Contradicts [AGENTS.md:13](AGENTS.md#L13)."}},
		},
		{
			name: "brackets in a bare path",
			text: "- [P1] Leaks the id — src/app/[id]/page.tsx:13. Body.",
			want: []Finding{{Reviewer: "1-terra", Priority: "P1", Path: "src/app/[id]/page.tsx", StartLine: s2, EndLine: e2, Title: "Leaks the id", Message: "Body."}},
		},
		{
			name: "spaces in a quoted path, time before it",
			text: "- [P2] Runs at 10:30 — `src/My File.go:7-9`",
			want: []Finding{{Reviewer: "1-terra", Priority: "P2", Path: "src/My File.go", StartLine: s4, EndLine: e4, Title: "Runs at 10:30"}},
		},
		{
			name:    "spaces in a bare changed path",
			text:    "- [P2] Bug in src/My File.go:7-9",
			changed: ChangedPaths("1\t2\tsrc/My File.go\n3\t0\told.go => new.go\n"),
			want:    []Finding{{Reviewer: "1-terra", Priority: "P2", Path: "src/My File.go", StartLine: s4, EndLine: e4, Title: "Bug"}},
		},
		{
			name:    "full changed path over a changed suffix",
			text:    "- [P2] Bug in src/My File.go:7-9",
			changed: ChangedPaths("1\t2\tsrc/My File.go\n1\t2\tFile.go\n"),
			want:    []Finding{{Reviewer: "1-terra", Priority: "P2", Path: "src/My File.go", StartLine: s4, EndLine: e4, Title: "Bug"}},
		},
		{
			name: "parentheses in a bare path",
			text: "- [P2] Bug in (src/app/(admin)/page.tsx:13)",
			want: []Finding{{Reviewer: "1-terra", Priority: "P2", Path: "src/app/(admin)/page.tsx", StartLine: s2, EndLine: e2, Title: "Bug"}},
		},
		{
			name: "non-ASCII bare path",
			text: "- [P3] Bug in src/café.go:7-9",
			want: []Finding{{Reviewer: "1-terra", Priority: "P3", Path: "src/café.go", StartLine: s4, EndLine: e4, Title: "Bug"}},
		},
		{
			name: "untagged reply is one finding",
			text: "The change drops errors.\nSee run.go.",
			want: []Finding{{Reviewer: "1-terra", Title: "The change drops errors.", Message: "See run.go."}},
		},
		{name: "empty reply", text: " \n", want: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ParseFindings("1-terra", test.text, test.changed); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got  %s\nwant %s", describe(got), describe(test.want))
			}
		})
	}
}

// TestVerdictFindingsSkipsCleanAndFailedReviewers checks that only
// reviewers that reported findings contribute, each under its own name.
func TestVerdictFindingsSkipsCleanAndFailedReviewers(t *testing.T) {
	v := Verdict{Reviewers: []Reviewer{
		{Name: "1-terra", Status: StatusClean, Text: CleanReply},
		{Name: "2-sol", Status: StatusFindings, Text: "- [P2] One in x.go:1\n- [P2] Two in y.go:2"},
		{Name: "3-haiku", Status: StatusFailed, Error: "down"},
		{Name: "4-flash", Status: StatusFindings, Text: "- [P1] Three in z.go:3"},
	}}
	got := v.Findings()
	if len(got) != 3 || got[0].Reviewer != "2-sol" || got[2].Reviewer != "4-flash" || got[2].Path != "z.go" {
		t.Fatal(describe(got))
	}
	if empty := (Verdict{}).Findings(); empty == nil || len(empty) != 0 {
		t.Fatalf("no reviewers: %#v", empty)
	}
}

func describe(findings []Finding) string {
	out := ""
	for _, f := range findings {
		start, end := 0, 0
		if f.StartLine != nil {
			start, end = *f.StartLine, *f.EndLine
		}
		out += fmt.Sprintf("{%s %s:%d-%d title=%q message=%q} ", f.Priority, f.Path, start, end, f.Title, f.Message)
	}
	return out
}

func TestChangedPaths(t *testing.T) {
	got := ChangedPaths("1\t2\ta b.go\n-\t-\timg.png\n3\t0\told.go => new.go\n1\t1\tsrc/{a => b}/x.go\n1\t1\tsrc/{Old => }/y.go\n4\t1\t\"caf\\303\\251.go\"\n")
	want := map[string]bool{"a b.go": true, "img.png": true, "new.go": true, "src/b/x.go": true, "src/y.go": true, "café.go": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
}
