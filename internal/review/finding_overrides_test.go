package review

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/gitx"
)

// The motivating false positive, as two reviews in a row worded it.
const (
	releaseRace = "- [P2] npm can publish a newer version than the latest GitHub release `setup.sh:40-42`\n" +
		"The setup script compares the installed npm package with the latest GitHub release, so when the npm job publishes before goreleaser creates the release, the check reports a newer version that does not exist yet."
	releaseRaceReworded = "- [P2] Check the installed package's channel for updates `setup.sh:44-45`\n" +
		"When the npm package is published before goreleaser finishes the GitHub release, the setup script sees a newer npm version than the latest release and reports an update that is not available yet."
	unrelated = "- [P1] Quote the install directory `setup.sh:43`\n" +
		"An install directory with a space in it splits into two arguments to mkdir, so the script creates the wrong directories."
)

func parsedOne(t *testing.T, reviewer, text string) Finding {
	t.Helper()
	findings := ParseFindings(reviewer, text, nil)
	if len(findings) != 1 {
		t.Fatalf("findings = %+v", findings)
	}
	return findings[0]
}

func TestFindingIDIgnoresReviewerPriorityAndLines(t *testing.T) {
	a := parsedOne(t, "1-terra", "- [P1] Leaks the handle `a.go:3`\nThe file is never closed.")
	b := parsedOne(t, "2-sol", "- [P3] leaks  the handle `a.go:30-31`\nThe file is never   closed.")
	if !IsFindingID(a.ID) || a.ID != b.ID {
		t.Fatalf("ids = %q, %q", a.ID, b.ID)
	}
	for _, other := range []string{
		"- [P1] Leaks the handle `b.go:3`\nThe file is never closed.",
		"- [P1] Leaks the socket `a.go:3`\nThe file is never closed.",
		"- [P1] Leaks the handle `a.go:3`\nThe file is closed twice.",
	} {
		if id := parsedOne(t, "1-terra", other).ID; id == a.ID {
			t.Errorf("%q has the same id", other)
		}
	}
	// The same text twice in one reply is two findings with two ids; another
	// reviewer's first one shares the first id.
	twice := ParseFindings("1-terra", "- [P1] Leaks the handle `a.go:3`\nThe file is never closed.\n- [P1] Leaks the handle `a.go:90`\nThe file is never closed.", nil)
	if len(twice) != 2 || twice[0].ID != a.ID || twice[1].ID == a.ID || !IsFindingID(twice[1].ID) {
		t.Fatalf("%+v", twice)
	}
	malformed := parsedOne(t, "1-terra", "Looks wrong to me.")
	if !malformed.Malformed || !IsFindingID(malformed.ID) {
		t.Fatalf("%+v", malformed)
	}
	for _, id := range []string{"0f8fad5b-d9cb-469f-a165-70867728950e", "rv-123", "RV-0123456789ab", "rv-0123456789ag"} {
		if IsFindingID(id) {
			t.Errorf("IsFindingID(%q)", id)
		}
	}
}

func TestMatchOverrideFollowsARewording(t *testing.T) {
	overridden := parsedOne(t, "1-terra", releaseRace)
	overrides := []FindingOverride{{ID: overridden.ID, Finding: overridden}}
	if got := matchedID(overrides, parsedOne(t, "2-sol", releaseRace)); got != overridden.ID {
		t.Fatalf("same text matched %q", got)
	}
	if got := matchedID(overrides, parsedOne(t, "2-sol", releaseRaceReworded)); got != overridden.ID {
		t.Fatalf("rewording matched %q", got)
	}
	if got := matchedID(overrides, parsedOne(t, "2-sol", unrelated)); got != "" {
		t.Fatalf("another point at a nearby line matched %q", got)
	}
	elsewhere := strings.ReplaceAll(releaseRaceReworded, "setup.sh", "install.sh")
	if got := matchedID(overrides, parsedOne(t, "2-sol", elsewhere)); got != "" {
		t.Fatalf("the same words in another file matched %q", got)
	}
	// A reviewer may cite a changed file by its absolute path, from any
	// worktree; parsing makes it relative, so it is the same finding.
	absolute := strings.ReplaceAll(releaseRace, "`setup.sh", "`/elsewhere/worktree/setup.sh")
	if f := ParseFindings("2-sol", absolute, map[string]bool{"setup.sh": true, "tools/setup.sh": true})[0]; f.Path != "setup.sh" || f.ID != overridden.ID {
		t.Fatalf("absolute path parsed as %+v", f)
	}
	if f := ParseFindings("2-sol", absolute, map[string]bool{"config.go": true})[0]; f.Path != "/elsewhere/worktree/setup.sh" {
		t.Fatalf("absolute path outside the change parsed as %+v", f)
	}
	// Two changed paths fit: only the repository root could tell which.
	ambiguous := strings.ReplaceAll(releaseRace, "`setup.sh", "`/tmp/tools/setup.sh")
	if f := ParseFindings("2-sol", ambiguous, map[string]bool{"setup.sh": true, "tools/setup.sh": true})[0]; f.Path != "/tmp/tools/setup.sh" {
		t.Fatalf("ambiguous absolute path parsed as %+v", f)
	}
	// Far from the overridden lines, even a close rewording is another finding.
	farReworded := strings.ReplaceAll(releaseRaceReworded, "setup.sh:44-45", "setup.sh:400-401")
	if got := matchedID(overrides, parsedOne(t, "2-sol", farReworded)); got != "" {
		t.Fatalf("a rewording far away matched %q", got)
	}
	// Far from the overridden lines, a looser rewording needs more words in
	// common than it shares.
	loose := "- [P2] Version check can report an npm update early `setup.sh:44`\n" +
		"The latest GitHub release may lag the npm package that goreleaser publishes."
	if got := matchedID(overrides, parsedOne(t, "2-sol", loose)); got != overridden.ID {
		t.Fatalf("a loose rewording nearby matched %q", got)
	}
	far := strings.ReplaceAll(loose, "setup.sh:44", "setup.sh:400")
	if got := matchedID(overrides, parsedOne(t, "2-sol", far)); got != "" {
		t.Fatalf("a loose rewording far away matched %q", got)
	}
	// Another relative path is another file, however its name ends.
	nested := strings.ReplaceAll(releaseRaceReworded, "`setup.sh", "`scripts/setup.sh")
	if got := matchedID(overrides, parsedOne(t, "2-sol", nested)); got != "" {
		t.Fatalf("another file with the same name matched %q", got)
	}
	// A short finding a word apart can be another defect: only its id matches.
	short := parsedOne(t, "1-terra", "- [P1] Fails to validate token `auth.go:10`")
	shortOverrides := []FindingOverride{{ID: short.ID, Finding: short}}
	if got := matchedID(shortOverrides, parsedOne(t, "1-terra", "- [P1] Fails to validate expired token `auth.go:12`")); got != "" {
		t.Fatalf("a short finding matched by its words: %q", got)
	}
	malformed := parsedOne(t, "1-terra", "npm can publish a newer version than the latest GitHub release")
	if got := matchedID([]FindingOverride{{ID: malformed.ID, Finding: malformed}}, parsedOne(t, "1-terra", "npm can publish a newer version than the latest GitHub release!")); got != "" {
		t.Fatalf("a malformed finding matched another text: %q", got)
	}
}

func TestApplyOverridesSettlesTheStatus(t *testing.T) {
	overridden := parsedOne(t, "1-terra", releaseRace)
	overrides := []FindingOverride{{ID: overridden.ID, Finding: overridden}}
	both := releaseRace + "\n" + unrelated
	for _, test := range []struct {
		name       string
		texts      []string
		incomplete bool
		want       Status
		findings   int
	}{
		{"every finding overridden", []string{releaseRace, releaseRaceReworded}, false, StatusClean, 0},
		{"too few reviewers finished", []string{releaseRace}, true, StatusFailed, 0},
		{"one finding left", []string{both}, false, StatusFindings, 1},
	} {
		v := Verdict{Status: StatusFindings, Incomplete: test.incomplete}
		for i, text := range test.texts {
			v.Reviewers = append(v.Reviewers, Reviewer{Name: string(rune('1'+i)) + "-terra", Status: StatusFindings, Text: text})
		}
		v.applyOverrides(overrides)
		if v.Status != test.want || len(v.Findings()) != test.findings || len(v.Overridden) != len(test.texts) {
			t.Errorf("%s: status %s, findings %+v, overridden %+v", test.name, v.Status, v.Findings(), v.Overridden)
		}
		for _, f := range v.Overridden {
			if f.OverrideID != overridden.ID {
				t.Errorf("%s: %+v", test.name, f)
			}
		}
	}
}

func TestOverriddenFindingIsLeftOutOfLaterReviews(t *testing.T) {
	root, c, s := reviewRepo(t)
	trace := filepath.Join(t.TempDir(), "trace")
	o := Options{Root: root, Intent: "request", Settings: Settings{Reviewers: []Family{Terra}, MinCompleted: 1, Adapters: map[string][]string{"codex": fakeAdapter("release-race", "gpt-6.10-terra", trace)}}}
	v := Run(context.Background(), s, c, o)
	if v.Status != StatusFindings || len(v.Findings()) != 1 {
		t.Fatalf("%+v", v)
	}
	id := v.Findings()[0].ID
	if _, _, err := OverrideFinding(root, "rv-000000000000", "x"); err == nil {
		t.Fatal("overrode an id no review reported")
	}
	if _, _, err := OverrideFinding(root, id, "  "); err == nil {
		t.Fatal("overrode a finding without a reason")
	}
	override, added, err := OverrideFinding(root, id, "the npm job needs goreleaser")
	if err != nil || !added || override.Finding.Title == "" {
		t.Fatalf("%+v %v %v", override, added, err)
	}
	if _, added, err = OverrideFinding(root, id, "the npm job needs goreleaser"); err != nil || added {
		t.Fatalf("second override: %v %v", added, err)
	}

	// The same change again reuses the review, now clean.
	v = Run(context.Background(), s, c, o)
	if !v.Reused || v.Status != StatusClean || len(v.Findings()) != 0 || len(v.Overridden) != 1 || v.Overridden[0].OverrideID != id {
		t.Fatalf("%+v", v)
	}
	if rounds, err := s.Rounds(); err != nil || len(rounds) != 0 {
		t.Fatalf("rounds = %+v, %v", rounds, err)
	}

	// Another review words it differently and is told about the override.
	o.Intent = "request, again"
	o.Settings.Adapters["codex"] = fakeAdapter("release-race-reworded", "gpt-6.10-terra", trace)
	v = Run(context.Background(), s, c, o)
	if v.Reused || v.Status != StatusClean || len(v.Overridden) != 1 || v.Overridden[0].OverrideID != id || v.Overridden[0].ID == id {
		t.Fatalf("%+v", v)
	}
	prompt, err := os.ReadFile(filepath.Join(v.LogDir, "prompt.txt"))
	if err != nil || !strings.Contains(string(prompt), "<overridden_findings>") || !strings.Contains(string(prompt), override.Finding.Title) ||
		!strings.Contains(string(prompt), "<author_reason>the npm job needs goreleaser</author_reason>") {
		t.Fatalf("prompt: %v\n%s", err, prompt)
	}
	if len(v.PromptOverrides) != 1 || v.PromptOverrides[0] != id {
		t.Fatalf("prompt overrides = %v", v.PromptOverrides)
	}

	// Undoing by the reworded finding's id undoes the override it matched.
	if err := MarkReviewed(root, c.Tree); err != nil {
		t.Fatal(err)
	}
	removed, found, err := UndoFindingOverride(root, v.Overridden[0].ID)
	if err != nil || !found || removed.ID != id {
		t.Fatalf("%+v %v %v", removed, found, err)
	}
	if approved, err := Reviewed(root, c.Tree); err != nil || approved {
		t.Fatalf("tree still approved after undo: %v", err)
	}
	if _, found, err = UndoFindingOverride(root, id); err != nil || found {
		t.Fatalf("second undo: %v %v", found, err)
	}
	// The reviewers were told to leave the finding out, so the review runs
	// again rather than trust a reply the override may have shaped.
	v = Run(context.Background(), s, c, o)
	if v.Reused || v.Status != StatusFindings || len(v.Overridden) != 0 || len(v.Findings()) != 1 || len(v.PromptOverrides) != 0 {
		t.Fatalf("%+v", v)
	}
}

func TestFindingOverridesAreSharedByWorktrees(t *testing.T) {
	root, c, s := reviewRepo(t)
	trace := filepath.Join(t.TempDir(), "trace")
	o := Options{Root: root, Settings: Settings{Reviewers: []Family{Terra}, MinCompleted: 1, Adapters: map[string][]string{"codex": fakeAdapter("release-race", "gpt-6.10-terra", trace)}}}
	v := Run(context.Background(), s, c, o)
	if _, _, err := OverrideFinding(root, v.Findings()[0].ID, "the npm job needs goreleaser"); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other")
	if _, err := gitx.Run(root, "worktree", "add", "--detach", other); err != nil {
		t.Fatal(err)
	}
	overrides, err := LoadFindingOverrides(other)
	if err != nil || len(overrides) != 1 {
		t.Fatalf("%+v %v", overrides, err)
	}
	// The review passes in the first worktree because of the override; undoing
	// it from the other withdraws that approval.
	v = Run(context.Background(), s, c, o)
	if v.Status != StatusClean {
		t.Fatalf("%+v", v)
	}
	if err = MarkReviewed(root, c.Tree); err != nil {
		t.Fatal(err)
	}
	if _, found, err := UndoFindingOverride(other, overrides[0].ID); err != nil || !found {
		t.Fatalf("%v %v", found, err)
	}
	if approved, err := Reviewed(root, c.Tree); err != nil || approved {
		t.Fatalf("approval kept after undo in another worktree: %v", err)
	}
}

func TestSettleOverridesRecordsTreesAndFailsOnAnUndoneOverride(t *testing.T) {
	root, c, s := reviewRepo(t)
	trace := filepath.Join(t.TempDir(), "trace")
	o := Options{Root: root, Settings: Settings{Reviewers: []Family{Terra}, MinCompleted: 1, Adapters: map[string][]string{"codex": fakeAdapter("release-race", "gpt-6.10-terra", trace)}}}
	id := Run(context.Background(), s, c, o).Findings()[0].ID
	if _, _, err := OverrideFinding(root, id, "the npm job needs goreleaser"); err != nil {
		t.Fatal(err)
	}
	// A reviewer that obeyed the prompt reported nothing to leave out, but
	// the clean review still relied on the override.
	v := Verdict{Status: StatusClean, Capture: Capture{Tree: c.Tree}, PromptOverrides: []string{id}}
	v.settleOverrides(root)
	overrides, err := LoadFindingOverrides(root)
	if err != nil || v.Status != StatusClean || len(overrides[0].Trees) != 1 || overrides[0].Trees[0] != c.Tree {
		t.Fatalf("%+v %+v %v", v, overrides, err)
	}
	if err = MarkReviewed(root, c.Tree); err != nil {
		t.Fatal(err)
	}
	if _, _, err = UndoFindingOverride(root, id); err != nil {
		t.Fatal(err)
	}
	if approved, err := Reviewed(root, c.Tree); err != nil || approved {
		t.Fatalf("approval kept: %v", err)
	}
	// Undone while the reviewers ran: the review fails.
	v = Verdict{Status: StatusClean, Capture: Capture{Tree: c.Tree}, PromptOverrides: []string{id}}
	v.settleOverrides(root)
	if v.Status != StatusFailed || !strings.Contains(v.Error, "was undone while it ran") {
		t.Fatalf("%+v", v)
	}
}

func TestOverriddenSectionListsOnlyTheChangedFiles(t *testing.T) {
	here := parsedOne(t, "1-terra", releaseRace)
	there := parsedOne(t, "1-terra", strings.ReplaceAll(releaseRace, "setup.sh", "other.sh"))
	nowhere := parsedOne(t, "1-terra", "- [P2] Something about the whole change\nNo file.")
	section, ids := overriddenSection([]FindingOverride{{ID: here.ID, Finding: here}, {ID: there.ID, Finding: there}, {ID: nowhere.ID, Finding: nowhere}}, Capture{Numstat: "1\t1\tsetup.sh"})
	if !strings.Contains(section, "setup.sh:40-42") || strings.Contains(section, "other.sh") || !strings.Contains(section, "the whole change") || !strings.Contains(section, overriddenRules) {
		t.Fatal(section)
	}
	if len(ids) != 2 || ids[0] != nowhere.ID || ids[1] != here.ID {
		t.Fatalf("ids = %v", ids)
	}
	if section, ids = overriddenSection(nil, Capture{}); section != "" || ids != nil {
		t.Fatal("empty section")
	}
}

// matchedID is the id matchOverride returns, without how it matched.
func matchedID(overrides []FindingOverride, f Finding) string {
	id, _ := matchOverride(overrides, f)
	return id
}

func TestMatchOverrideNeedsTheSamePlaceAndSaysHow(t *testing.T) {
	first := parsedOne(t, "1-terra", releaseRace)
	overrides := []FindingOverride{{ID: first.ID, Finding: first}}
	if id, match := matchOverride(overrides, parsedOne(t, "2-sol", releaseRace)); id != first.ID || match != MatchText {
		t.Fatalf("same text: %q %q", id, match)
	}
	if id, match := matchOverride(overrides, parsedOne(t, "2-sol", releaseRaceReworded)); id != first.ID || match != MatchWords {
		t.Fatalf("rewording: %q %q", id, match)
	}
	// The same text far away is another instance, even when it got the
	// overridden finding's id because the first instance was not reported.
	far := parsedOne(t, "2-sol", strings.ReplaceAll(releaseRace, "setup.sh:40-42", "setup.sh:400-402"))
	if far.ID != first.ID {
		t.Fatalf("ids differ: %q %q", far.ID, first.ID)
	}
	if id := matchedID(overrides, far); id != "" {
		t.Fatalf("the same text far away matched %q", id)
	}
}

func TestOverrideReasonAndUndoOutliveTheReviews(t *testing.T) {
	root, c, s := reviewRepo(t)
	trace := filepath.Join(t.TempDir(), "trace")
	o := Options{Root: root, Intent: "first", Settings: Settings{Reviewers: []Family{Terra}, MinCompleted: 1, Adapters: map[string][]string{"codex": fakeAdapter("release-race", "gpt-6.10-terra", trace)}}}
	id := Run(context.Background(), s, c, o).Findings()[0].ID
	if _, _, err := OverrideFinding(root, id, "first reason"); err != nil {
		t.Fatal(err)
	}
	// A clean review replaces the record and clears the rounds; the reason
	// can still be changed.
	o.Intent, o.Settings.Adapters["codex"] = "second", fakeAdapter("clean", "gpt-6.10-terra", trace)
	if v := Run(context.Background(), s, c, o); v.Status != StatusClean {
		t.Fatalf("%+v", v)
	}
	if o, added, err := OverrideFinding(root, id, "second reason"); err != nil || added || o.Reason != "second reason" {
		t.Fatalf("%+v %v %v", o, added, err)
	}
	// A reworded finding the latest record does not list as overridden is
	// still resolved to the override it matches now.
	o.Intent, o.Settings.Adapters["codex"] = "third", fakeAdapter("release-race-reworded", "gpt-6.10-terra", trace)
	v := Run(context.Background(), s, c, o)
	if len(v.Overridden) != 1 || v.Overridden[0].Match != MatchWords {
		t.Fatalf("%+v", v)
	}
	if err := s.write("last.json", Record{Verdict: Verdict{Status: StatusFindings, Capture: v.Capture, Reviewers: v.Reviewers}}); err != nil {
		t.Fatal(err)
	}
	if removed, found, err := UndoFindingOverride(root, v.Overridden[0].ID); err != nil || !found || removed.ID != id {
		t.Fatalf("%+v %v %v", removed, found, err)
	}
	if hold, err := OverridesHold(root, v); err != nil || hold {
		t.Fatalf("overrides hold after undo: %v", err)
	}
}

func TestOverriddenInstancesAreFilteredOneByOne(t *testing.T) {
	near := releaseRace
	far := strings.ReplaceAll(releaseRace, "setup.sh:40-42", "setup.sh:400-402")
	first := parsedOne(t, "1-terra", near)
	v := Verdict{Status: StatusFindings, Reviewers: []Reviewer{
		{Name: "1-terra", Status: StatusFindings, Text: near},
		{Name: "2-sol", Status: StatusFindings, Text: far},
	}}
	v.applyOverrides([]FindingOverride{{ID: first.ID, Finding: first, Reason: "x"}})
	// Both carry the same id; only the one at the overridden place is left out.
	if len(v.Overridden) != 1 || v.Overridden[0].Reviewer != "1-terra" || len(v.Findings()) != 1 || v.Findings()[0].Reviewer != "2-sol" || v.Status != StatusFindings {
		t.Fatalf("%+v", v)
	}
}

func TestWholeReviewOverrideNeedsTheFindingOverridesItReliedOn(t *testing.T) {
	root, c, s := reviewRepo(t)
	trace := filepath.Join(t.TempDir(), "trace")
	o := Options{Root: root, Settings: Settings{Reviewers: []Family{Terra}, MinCompleted: 1, Adapters: map[string][]string{"codex": fakeAdapter("findings", "gpt-6.10-terra", trace)}}}
	v := Run(context.Background(), s, c, o)
	if err := s.write("last.json", Record{Verdict: Verdict{Status: v.Status, Capture: v.Capture, Reviewers: v.Reviewers, PromptOverrides: []string{"rv-0123456789ab"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Override("the finding is wrong", c.Tree); err == nil || !strings.Contains(err.Error(), "was undone since") {
		t.Fatalf("override = %v", err)
	}
}

func TestARecreatedOverrideDoesNotHoldForAnEarlierReview(t *testing.T) {
	root, c, s := reviewRepo(t)
	trace := filepath.Join(t.TempDir(), "trace")
	o := Options{Root: root, Settings: Settings{Reviewers: []Family{Terra}, MinCompleted: 1, Adapters: map[string][]string{"codex": fakeAdapter("release-race", "gpt-6.10-terra", trace)}}}
	id := Run(context.Background(), s, c, o).Findings()[0].ID
	if _, _, err := OverrideFinding(root, id, "first"); err != nil {
		t.Fatal(err)
	}
	v := Run(context.Background(), s, c, o)
	if hold, err := OverridesHold(root, v); err != nil || !hold || v.Status != StatusClean {
		t.Fatalf("%v %v %+v", hold, err, v)
	}
	if _, _, err := UndoFindingOverride(root, id); err != nil {
		t.Fatal(err)
	}
	if _, _, err := OverrideFinding(root, id, "again"); err != nil {
		t.Fatal(err)
	}
	// The new override never recorded v's tree, so undoing it would not
	// withdraw v's approval: v must not count as relying on it.
	if hold, err := OverridesHold(root, v); err != nil || hold {
		t.Fatalf("recreated override holds: %v", err)
	}
}

func TestAnOverrideStoredRelativeStillMatchesItsAbsoluteCitation(t *testing.T) {
	root, c, s := reviewRepo(t)
	text := strings.ReplaceAll(releaseRace, "`setup.sh", "`"+filepath.ToSlash(filepath.Join(root, "change.txt")))
	// Both changed paths end the absolute one when the root's own name is a
	// directory of the change.
	nested := filepath.Base(root) + "/change.txt"
	f := ParseFindings("1-terra", text, map[string]bool{"change.txt": true, nested: true})[0]
	if !filepath.IsAbs(f.Path) {
		t.Fatalf("ambiguous path parsed as %q", f.Path)
	}
	v := Verdict{Status: StatusFindings, Capture: c, Reviewers: []Reviewer{{Name: "1-terra", Status: StatusFindings, Text: text}}}
	v.Capture.Numstat = "1\t1\tchange.txt\n1\t1\t" + nested
	if err := s.write("last.json", Record{Verdict: v}); err != nil {
		t.Fatal(err)
	}
	o, _, err := OverrideFinding(root, f.ID, "reason")
	if err != nil || o.Finding.Path != "change.txt" {
		t.Fatalf("%+v %v", o, err)
	}
	v.applyOverrides([]FindingOverride{o})
	if v.Status != StatusClean || len(v.Overridden) != 1 {
		t.Fatalf("%+v", v)
	}
}
