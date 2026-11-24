package review

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func reportOf(t *testing.T, v Verdict) Report {
	t.Helper()
	report, ok := BuildReport(v, "1.2.3", func(Family) []string {
		return []string{"npx", "--yes", "@agentclientprotocol/codex-acp@2.5.0"}
	})
	if !ok {
		t.Fatal("no report")
	}
	return report
}

func TestBuildReportFromVerdicts(t *testing.T) {
	reply := "- [P1] Handle leaks `src/a.go:10-12`\nThe file is never closed, see `short()`.\nTags: category=resource-leak api=os.File\n" +
		"- [P3] Unlocated point\nNo place named.\n"
	v := Verdict{
		Status:       StatusFindings,
		IntentSource: "actual user messages from the identified Codex rollout",
		Capture:      Capture{BaseTree: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)},
		DurationsMs:  map[string]int64{"1-terra": 1500},
		Reviewers: []Reviewer{
			{Name: "1-terra", Family: Terra, Adapter: "codex", Model: "gpt-terra-5", Effort: "high", Status: StatusFindings, Text: reply},
			{Name: "2-flash", Family: Flash, Status: StatusFindings, Text: "I looked around and this seems off to me.\nSecond line."},
			{Name: "3-haiku", Family: Haiku, Status: StatusClean, Text: CleanReply},
			{Name: "fallback1-sol", Family: Sol, Status: StatusFailed, Error: "boom", StandsInFor: "1-terra"},
		},
	}
	report := reportOf(t, v)
	if report.IntentSource != "codex-rollout" || report.Mode != "both" || report.CLIVersion != "1.2.3" {
		t.Fatalf("report = %+v", report)
	}
	first := report.Reviewers[0]
	if first.Position != 1 || first.AdapterVersion != "2.5.0" || first.Model != "gpt-terra-5" || *first.DurationMs != 1500 ||
		first.ParseQuality != ParsePartial || first.FindingCount != 2 {
		t.Fatalf("first = %+v", first)
	}
	got := first.Findings[0]
	if got.Category != "resource-leak" || got.API != "os.File" || got.Path != "src/a.go" || *got.StartLine != 10 || *got.EndLine != 12 ||
		got.Message != "The file is never closed, see `short()`." {
		t.Fatalf("finding = %+v", got)
	}
	if un := first.Findings[1]; un.Title != "" || un.Message != "" || un.Path != "" || un.Priority != "P3" {
		t.Fatalf("a finding without a location keeps no text: %+v", un)
	}
	broken := report.Reviewers[1]
	if broken.ParseQuality != ParseUnparsed || broken.FindingCount != 1 || broken.Status != StatusFindings ||
		len(broken.Findings) != 1 || broken.Findings[0].Title != "" || broken.Findings[0].Message != "" {
		t.Fatalf("a reply that breaks the format = %+v", broken)
	}
	if clean := report.Reviewers[2]; clean.ParseQuality != ParseParsed || clean.FindingCount != 0 {
		t.Fatalf("clean = %+v", clean)
	}
	if failed := report.Reviewers[3]; failed.Position != 4 || failed.Status != StatusFailed || failed.ParseQuality != "" {
		t.Fatalf("fallback = %+v", failed)
	}
	body, _ := json.Marshal(report)
	for _, never := range []string{"looked around", "boom", "actual user messages"} {
		if strings.Contains(string(body), never) {
			t.Fatalf("body holds %q: %s", never, body)
		}
	}
}

func TestBuildReportNothingRan(t *testing.T) {
	if _, ok := BuildReport(Verdict{Status: StatusEmpty}, "1", nil); ok {
		t.Fatal("a review no reviewer ran has no report")
	}
}

func TestReportStripsCodeAndUnsafePaths(t *testing.T) {
	long := strings.Repeat("x", 60)
	reply := "- [P1] Leak `src/a.go:1`\nSee ```\nsecret := 1\n``` and ~~~\ntilde := 2\n~~~ and `" + long + "` here and ``" + long + "`x`` too.\n- [P2] Abs `/Users/me/a.go:3`\nHome path.\n"
	report := reportOf(t, Verdict{Status: StatusFindings, Reviewers: []Reviewer{{Name: "1-terra", Family: Terra, Status: StatusFindings, Text: reply}}})
	findings := report.Reviewers[0].Findings
	if m := findings[0].Message; strings.Contains(m, "secret") || strings.Contains(m, "tilde") || strings.Contains(m, long) || !strings.Contains(m, codeOmitted) {
		t.Fatalf("message = %q", m)
	}
	if findings[1].Path != "" || findings[1].Title != "" {
		t.Fatalf("an absolute path is not sent: %+v", findings[1])
	}
}

func TestReportBounds(t *testing.T) {
	var reply strings.Builder
	for i := range 150 {
		fmt.Fprintf(&reply, "- [P2] Finding %d `src/a.go:%d`\n%s\n", i, i+1, strings.Repeat("é<", 240))
	}
	var reviewers []Reviewer
	for i := range 10 {
		reviewers = append(reviewers, Reviewer{Name: fmt.Sprintf("%d-terra", i+1), Family: Terra, Status: StatusFindings, Text: reply.String()})
	}
	report := reportOf(t, Verdict{Status: StatusFindings, Reviewers: reviewers})
	if len(report.Reviewers) != maxReportReviewers {
		t.Fatalf("reviewers = %d", len(report.Reviewers))
	}
	body, _ := json.Marshal(report)
	if len(body) > 256*1024 {
		t.Fatalf("body = %d bytes", len(body))
	}
	for _, r := range report.Reviewers {
		if r.FindingCount != 150 || len(r.Findings) > maxReportFindings || len(r.Findings) == 0 {
			t.Fatalf("count = %d, kept = %d", r.FindingCount, len(r.Findings))
		}
	}
}

func TestReportSaysWhatAStandingOverrideLeftOut(t *testing.T) {
	reply := "- [P1] Kept `src/a.go:1`\nStays.\n- [P2] Left out `src/b.go:5`\nOverridden earlier.\n"
	v := Verdict{Status: StatusFindings, Reviewers: []Reviewer{{Name: "1-terra", Family: Terra, Status: StatusFindings, Text: reply}}}
	left := v.reported()[1]
	v.Overridden = []OverriddenFinding{{Finding: left, OverrideID: "rv-aaaaaaaaaaaa", Match: MatchWords}}
	v.PromptOverrides = []string{"rv-aaaaaaaaaaaa", "rv-bbbbbbbbbbbb"}
	report := reportOf(t, v)
	if len(report.PromptOverrides) != 2 {
		t.Fatalf("prompt overrides = %v", report.PromptOverrides)
	}
	row := report.Reviewers[0]
	if row.FindingCount != 2 || len(row.Findings) != 1 || row.OverriddenCount != 1 ||
		row.Overridden[0] != (ReportOverridden{Key: left.ID, OverrideKey: "rv-aaaaaaaaaaaa", Match: MatchWords}) {
		t.Fatalf("row = %+v", row)
	}
}

func TestTagLine(t *testing.T) {
	plain := "- [P1] Title `a.go:1`\nBody text.\n"
	tagged := "- [P1] Title `a.go:1`\nBody text.\nTags: category=security api=net/http\n"
	unknown := "- [P1] Title `a.go:1`\nBody text.\nTags: category=vibes\n"
	prose := "- [P1] Title `a.go:1`\nBody text.\nTags: this is a sentence about tags\n"
	want := ParseFindings("r", plain, nil)[0]
	if want.Category != "" || want.API != "" || want.Message != "Body text." {
		t.Fatalf("plain = %+v", want)
	}
	got := ParseFindings("r", tagged, nil)[0]
	if got.ID != want.ID || got.Message != want.Message || got.Category != "security" || got.API != "net/http" {
		t.Fatalf("tagged = %+v", got)
	}
	if got := ParseFindings("r", unknown, nil)[0]; got.Category != "" || got.ID != want.ID {
		t.Fatalf("unknown = %+v", got)
	}
	if got := ParseFindings("r", prose, nil)[0]; !strings.Contains(got.Message, "sentence about tags") {
		t.Fatalf("prose = %+v", got)
	}
	if b, _ := json.Marshal(got); strings.Contains(string(b), "category") {
		t.Fatalf("the JSON report gains a field: %s", b)
	}
}

func TestTrimKeepsChecksAStandingOverrideConcerns(t *testing.T) {
	var checks []trackedCheck
	for i := range maxTrackedChecks + 5 {
		checks = append(checks, trackedCheck{CheckRef: CheckRef{ID: fmt.Sprint(i)}})
	}
	checks[0].Keys = []string{"rv-aaaaaaaaaaaa"}
	kept := trimTrackedChecks(checks, []FindingOverride{{ID: "rv-aaaaaaaaaaaa"}})
	if len(kept) != maxTrackedChecks || kept[0].ID != "0" || kept[1].ID != "6" {
		t.Fatalf("kept %d, first %s then %s", len(kept), kept[0].ID, kept[1].ID)
	}
}

func TestReportNamesTheTreeTheReviewersRead(t *testing.T) {
	v := Verdict{Status: StatusClean, Capture: Capture{Tree: strings.Repeat("a", 40), DiffTree: strings.Repeat("b", 40)},
		Reviewers: []Reviewer{{Name: "1-terra", Family: Terra, Status: StatusClean, Text: CleanReply}}}
	if got := reportOf(t, v).Tree; got != v.Capture.DiffTree {
		t.Fatalf("tree = %s", got)
	}
}

func TestReportKeysAreTheFindingsSent(t *testing.T) {
	var reply strings.Builder
	for i := range 150 {
		fmt.Fprintf(&reply, "- [P2] Finding %d `src/a.go:%d`\nBody.\n", i, i+1)
	}
	report := reportOf(t, Verdict{Status: StatusFindings, Reviewers: []Reviewer{{Name: "1-terra", Family: Terra, Status: StatusFindings, Text: reply.String()}}})
	if keys := report.Keys(); len(keys) != maxReportFindings {
		t.Fatalf("keys = %d", len(keys))
	}
}

func TestUnparsedOllamaReplyIsAnUnparsedRow(t *testing.T) {
	v := Verdict{Status: StatusFailed, Reviewers: []Reviewer{{Name: "1-ollama", Family: Ollama, Adapter: ollamaAdapter, Status: StatusFailed,
		Text: "Looks mostly fine, maybe check the loop.", Error: "the reviewer's reply was not a verdict"}}}
	row := reportOf(t, v).Reviewers[0]
	if row.ParseQuality != ParseUnparsed || row.FindingCount != 1 || len(row.Findings) != 0 || row.Status != StatusFailed {
		t.Fatalf("row = %+v", row)
	}
	body, _ := json.Marshal(row)
	if strings.Contains(string(body), "loop") {
		t.Fatalf("reply text sent: %s", body)
	}
}

func TestAPITagGetsTheCodeFilter(t *testing.T) {
	long := strings.Repeat("y", 50)
	reply := "- [P1] Leak `src/a.go:1`\nBody.\nTags: api=`" + long + "`\n"
	f := reportOf(t, Verdict{Status: StatusFindings, Reviewers: []Reviewer{{Name: "1-terra", Family: Terra, Status: StatusFindings, Text: reply}}}).Reviewers[0].Findings[0]
	if f.API != "" {
		t.Fatalf("api = %q", f.API)
	}
}
