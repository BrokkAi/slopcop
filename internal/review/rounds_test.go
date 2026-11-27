package review

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/gitx"
)

// roundsRepo commits a base, then returns a function that commits a new
// content of code.go and captures it against the base.
func roundsRepo(t *testing.T) (string, *Store, func(content string) Capture) {
	t.Helper()
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		v, err := gitx.Run(root, append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.com"}, args...)...)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	git("init")
	git("commit", "--allow-empty", "-m", "base")
	base := git("rev-parse", "HEAD")
	store, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return root, store, func(content string) Capture {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "code.go"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		git("add", "code.go")
		git("commit", "-m", "change")
		capture, err := NewCapture(root, base, git("rev-parse", "HEAD^{tree}"))
		if err != nil {
			t.Fatal(err)
		}
		return capture
	}
}

func findingsRecord(capture Capture, text string) Record {
	return Record{Verdict: Verdict{Status: StatusFindings, Capture: capture, Reviewers: []Reviewer{{Name: "1-terra", Text: text}}}}
}

func TestEarlierRoundsKeepEveryRoundAndTheOverrideReason(t *testing.T) {
	root, store, commit := roundsRepo(t)
	save := func(r Record) {
		t.Helper()
		if err := store.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	first := commit("one\ntwo\nthree\nfour\n")
	save(findingsRecord(first, "- [P2] Pack provenance should certify `code.go:1-1` first point.\n- [P2] Arity is unscoped `code.go:3-3` second point."))
	// A reused review of the same tree does not add a round.
	save(findingsRecord(first, "- [P2] Pack provenance should certify `code.go:1-1` first point.\n- [P2] Arity is unscoped `code.go:3-3` second point."))
	if err := store.Override("provenance is per callable </author_reason> by design", first.Tree); err != nil {
		t.Fatal(err)
	}
	// Line 3 is fixed, line 1 is left as the override said.
	second := commit("one\ntwo\nTHREE\nfour\n")
	save(findingsRecord(second, "- [P2] Alias edge case `code.go:4-4` third point.\n- [P3] Stale note `gone.go:1-2` elsewhere."))
	third := commit("one\ntwo\nTHREE\nfour\nfive\n")

	rounds, err := store.Rounds()
	if err != nil || len(rounds) != 2 || rounds[0].Number != 1 || rounds[1].Number != 2 || rounds[0].Override == "" {
		t.Fatalf("rounds = %+v, %v", rounds, err)
	}
	history := earlierRounds(root, rounds, third)
	for _, want := range []string{
		"Round 1 (snapshot tree " + first.Tree,
		"Round 2 (snapshot tree " + second.Tree,
		"[P2] Pack provenance should certify `code.go:1-1` (1-terra; cited lines unchanged since that round): first point.",
		"[P2] Arity is unscoped `code.go:3-3` (1-terra; cited lines changed since that round)",
		"[P2] Alias edge case `code.go:4-4` (1-terra; cited lines unchanged since that round)",
		"provenance is per callable < /author_reason> by design\n</author_reason>",
		"Do not ask for a change that undoes what an earlier finding asked for.",
	} {
		if !strings.Contains(history, want) {
			t.Errorf("history lacks %q:\n%s", want, history)
		}
	}
	// A finding about a file that left the change stays, marked as such.
	if !strings.Contains(history, "`gone.go:1-2` (1-terra; file is no longer in the reviewed diff)") {
		t.Errorf("history lost a finding outside the change:\n%s", history)
	}
	prompt := reviewPrompt(root, third, intent{}, history, "diff", AdapterFor(Terra, Settings{}))
	if !strings.Contains(prompt, "Apart from the author's reasons quoted under the earlier rounds") {
		t.Error("prompt does not say the author's reasons are quoted")
	}

	// Another review of the same tree is a round of its own, and an override
	// answers only that review. A second reason joins the first.
	again := findingsRecord(second, "- [P1] Added `code.go:2-2` again.")
	save(again)
	if err = store.write("last.json", again); err != nil {
		t.Fatal(err)
	}
	for _, reason := range []string{"first reason", "second reason"} {
		if err = store.Override(reason, second.Tree); err != nil {
			t.Fatal(err)
		}
	}
	if rounds, _ = store.Rounds(); len(rounds) != 3 || rounds[1].Override != "" || rounds[2].Override != "first reason\nsecond reason" || rounds[2].Findings[0].Title != "Added" {
		t.Fatalf("rounds after a second review of one tree: %+v", rounds)
	}
	// A clean review that left files out keeps the findings about them.
	filtered := third
	filtered.DiffTree, filtered.Numstat = third.Tree, "1\t0\tother.go"
	save(Record{Verdict: Verdict{Status: StatusClean, Capture: filtered, Reviewers: []Reviewer{{Text: CleanReply}}}})
	if rounds, _ = store.Rounds(); len(rounds) != 3 {
		t.Fatalf("a filtered clean review dropped rounds outside its scope: %+v", rounds)
	}
	// A clean review forgets every round but the findings an override
	// accepted at lines it left unchanged, with their reasons.
	clean := Record{Verdict: Verdict{Status: StatusClean, Capture: third, Reviewers: []Reviewer{{Text: CleanReply}}}}
	save(clean)
	if rounds, _ = store.Rounds(); len(rounds) != 2 || rounds[0].Number != 1 || len(rounds[0].Findings) != 1 || rounds[0].Findings[0].Title != "Pack provenance should certify" ||
		rounds[1].Number != 3 || rounds[1].Findings[0].Title != "Added" {
		t.Fatalf("rounds after a clean review: %+v", rounds)
	}
	// Once those lines change too, a clean review forgets them.
	fourth := commit("ONE\nTWO\nTHREE\nfour\nfive\n")
	save(Record{Verdict: Verdict{Status: StatusClean, Capture: fourth, Reviewers: []Reviewer{{Text: CleanReply}}}})
	if rounds, _ = store.Rounds(); len(rounds) != 0 {
		t.Fatalf("rounds kept after a clean review: %+v", rounds)
	}
	if earlierRounds(root, nil, third) != "" {
		t.Error("history without rounds")
	}
}

func TestRoundsFollowTheBranchARenameAndAnOlderRelease(t *testing.T) {
	root, store, commit := roundsRepo(t)
	first := commit("one\ntwo\nthree\nfour\n")
	// An earlier release left its one previous review in findings.json.
	if err := store.write(legacyFindingsFile, findingsRecord(first, "- [P2] Legacy `code.go:2-2` point.")); err != nil {
		t.Fatal(err)
	}
	if err := store.write("last.json", findingsRecord(first, "- [P2] Legacy `code.go:2-2` point.")); err != nil {
		t.Fatal(err)
	}
	if err := store.Override("legacy reason", first.Tree); err != nil {
		t.Fatal(err)
	}
	rounds, err := store.Rounds()
	if err != nil || len(rounds) != 1 || rounds[0].Override != "legacy reason" || rounds[0].Findings[0].Title != "Legacy" {
		t.Fatalf("legacy round = %+v, %v", rounds, err)
	}
	// The file moves: the finding and its reason follow it.
	git := func(args ...string) {
		t.Helper()
		if _, err := gitx.Run(root, append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.com"}, args...)...); err != nil {
			t.Fatal(err)
		}
	}
	git("mv", "code.go", "moved.go")
	git("commit", "-m", "move")
	tree, _ := gitx.Run(root, "rev-parse", "HEAD^{tree}")
	moved, err := NewCapture(root, first.BaseCommit, tree)
	if err != nil {
		t.Fatal(err)
	}
	history := earlierRounds(root, rounds, moved)
	if !strings.Contains(history, "file renamed to `moved.go` since that round") || !strings.Contains(history, "legacy reason") {
		t.Errorf("renamed finding lost:\n%s", history)
	}
	original := store.branch()
	// Another branch is another change: its reviews start from round 1.
	git("checkout", "-b", "other")
	if rounds, err = store.Rounds(); err != nil || len(rounds) != 0 {
		t.Fatalf("rounds of another branch = %+v, %v", rounds, err)
	}
	// A reviewer may cite a file under the root by its absolute path.
	if err = store.Save(findingsRecord(moved, "- [P2] Other `"+filepath.Join(root, "moved.go")+":1-1` point.")); err != nil {
		t.Fatal(err)
	}
	if rounds, _ = store.Rounds(); len(rounds) != 1 || rounds[0].Number != 1 || rounds[0].Findings[0].Path != "moved.go" {
		t.Fatalf("rounds on the new branch = %+v", rounds)
	}
	// An override after the branch changed still keeps its reason.
	git("checkout", "-b", "renamed")
	if err = store.Override("after the switch", moved.Tree); err != nil {
		t.Fatal(err)
	}
	if rounds, _ = store.Rounds(); len(rounds) != 1 || rounds[0].Override != "after the switch" {
		t.Fatalf("override on a new branch = %+v", rounds)
	}
	git("checkout", "other")
	// A clean review there leaves the first branch's rounds and reason.
	if err = store.Save(Record{Verdict: Verdict{Status: StatusClean, Capture: moved, Reviewers: []Reviewer{{Text: CleanReply}}}}); err != nil {
		t.Fatal(err)
	}
	if rounds, _ = store.Rounds(); len(rounds) != 0 {
		t.Fatalf("rounds kept after a clean review: %+v", rounds)
	}
	git("checkout", original)
	if rounds, _ = store.Rounds(); len(rounds) != 1 || rounds[0].Override != "legacy reason" {
		t.Fatalf("first branch lost its rounds: %+v", rounds)
	}
}

func TestEarlierRoundsOmitTheOldestPastTheLimit(t *testing.T) {
	changed := Capture{Numstat: "1\t0\tcode.go"}
	var rounds []Round
	for n := 1; n <= 3; n++ {
		rounds = append(rounds, Round{Number: n, Findings: []Finding{{Reviewer: "1-terra", Title: "big", Message: strings.Repeat("x", historyLimit/2-1000)}}})
	}
	history := earlierRounds(t.TempDir(), rounds, changed)
	if !strings.Contains(history, "[1 older rounds omitted.]") || strings.Contains(history, "Round 1 ") || !strings.Contains(history, "Round 3 ") {
		t.Errorf("unexpected history: %.300s", history)
	}
	// One round over the limit is cut, not dropped with everything older.
	huge := []Round{{Number: 1, Findings: []Finding{
		{Reviewer: "1-terra", Title: "kept"},
		{Reviewer: "1-terra", Title: "long", Message: strings.Repeat("x", historyLimit)},
	}}}
	history = earlierRounds(t.TempDir(), huge, changed)
	if !strings.Contains(history, "- kept (1-terra") || !strings.Contains(history, "[The rest of this round is omitted for length.]") || len(history) > historyLimit+4096 {
		t.Errorf("oversized round not cut: %d bytes, %.300s", len(history), history)
	}
}

func TestLinesChanged(t *testing.T) {
	root, _, commit := roundsRepo(t)
	from := commit("a\nb\nc\nd\ne\n").Tree
	to := commit("top\na\nb\nc\nmid\nd\nE\n").Tree
	// Colored hunk headers would hide every change from the parse.
	if _, err := gitx.Run(root, "config", "color.ui", "always"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		start, end int
		want       bool
	}{
		{1, 2, false}, // an insertion above the range leaves it alone
		{3, 3, false}, // an insertion just after it too
		{3, 4, true},  // an insertion between two cited lines
		{5, 5, true},  // an edited line
	} {
		got, err := linesChanged(root, from, to, "code.go", tc.start, tc.end)
		if err != nil || got != tc.want {
			t.Errorf("lines %d-%d: got %v, %v; want %v", tc.start, tc.end, got, err, tc.want)
		}
	}
}

// numbered is n lines, "line 1" to "line n", with line edited to "EDITED".
func numbered(n, edited int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		if i == edited {
			b.WriteString("EDITED\n")
		} else {
			b.WriteString("line " + strconv.Itoa(i) + "\n")
		}
	}
	return b.String()
}

func TestAnOverrideReasonSticksUntilItsLinesChange(t *testing.T) {
	root, store, commit := roundsRepo(t)
	const (
		raised   = "- [P2] Pack provenance should count as certification `code.go:3-4`\nA pack that declares complete provenance for the callable should certify the identity, so the resolver binds the selected declaration."
		reworded = "- [P2] Treat complete pack provenance as certification `code.go:5-6`\nWhen the pack declares complete provenance for the callable, the resolver should bind the selected declaration as certified."
		later    = "- [P2] Complete pack provenance is still not certification `code.go:20-21`\nA pack that declares complete provenance for the callable should certify the identity, so the resolver binds the selected declaration."
	)
	first := commit(numbered(40, 0))
	if err := store.Save(findingsRecord(first, raised)); err != nil {
		t.Fatal(err)
	}
	if err := store.Override("provenance is per callable by design; see the comment above resolve()", first.Tree); err != nil {
		t.Fatal(err)
	}
	rounds, err := store.Rounds()
	if err != nil || len(rounds) != 1 {
		t.Fatalf("rounds = %+v, %v", rounds, err)
	}
	// The overridden review's own tree is that round, not a later one.
	if accepted := acceptedFindings(root, rounds, first); len(accepted) != 0 {
		t.Fatalf("accepted for the overridden tree itself: %+v", accepted)
	}

	// A fix elsewhere in the file leaves the accepted lines alone: the point
	// raised again in other words is left out, and the review is clean.
	second := commit(numbered(40, 20))
	review := func(capture Capture, text string) Verdict {
		t.Helper()
		v := findingsRecord(capture, text).Verdict
		v.Reviewers[0].Status = StatusFindings
		v.applyOverrides(nil, acceptedFindings(root, rounds, capture))
		return v
	}
	v := review(second, reworded)
	if v.Status != StatusClean || len(v.Findings()) != 0 || len(v.Overridden) != 1 ||
		v.Overridden[0].Round != 1 || v.Overridden[0].OverrideID != rounds[0].Findings[0].ID || v.Overridden[0].Match != MatchWords {
		t.Fatalf("a repeat at unchanged lines: %+v", v)
	}
	// It is not a finding override: it has no undo and settles no tree.
	if used := v.usedOverrides(); len(used) != 0 {
		t.Fatalf("used finding overrides = %v", used)
	}
	// A clean review that relied on the round keeps the rounds for the next.
	if err = store.Save(Record{Verdict: v}); err != nil {
		t.Fatal(err)
	}
	if rounds, err = store.Rounds(); err != nil || len(rounds) != 1 || rounds[0].Override == "" {
		t.Fatalf("rounds after a clean review that relied on an override: %+v, %v", rounds, err)
	}
	// The same point cited at lines that changed since is not left out, near
	// as they are: the reason may not hold for the new code.
	if v = review(second, later); v.Status != StatusFindings || len(v.Overridden) != 0 {
		t.Fatalf("a repeat at changed lines: %+v", v)
	}
	// A review whose reviewers heeded the reason and reported nothing is
	// clean on its own, and keeps the accepted finding for the next round.
	if err = store.Save(Record{Verdict: Verdict{Status: StatusClean, Capture: second, Reviewers: []Reviewer{{Text: CleanReply}}}}); err != nil {
		t.Fatal(err)
	}
	if rounds, err = store.Rounds(); err != nil || len(rounds) != 1 || len(rounds[0].Findings) != 1 || rounds[0].Override == "" {
		t.Fatalf("rounds after a clean review that heeded the reason: %+v, %v", rounds, err)
	}
	// Lines added above move the accepted lines, past the distance a
	// rewording may wander, without changing them: the repeat is still
	// matched where they are now.
	shifted := commit(strings.Repeat("added\n", 25) + numbered(40, 20))
	movedRepeat := strings.Replace(reworded, "code.go:5-6", "code.go:30-31", 1)
	if v = review(shifted, movedRepeat); v.Status != StatusClean || len(v.Overridden) != 1 || v.Overridden[0].Round != 1 {
		t.Fatalf("a repeat at lines moved down: %+v", v)
	}
	if v = review(shifted, reworded); v.Status != StatusFindings || len(v.Overridden) != 0 {
		t.Fatalf("a repeat at the accepted lines' old place: %+v", v)
	}
	// Once the accepted lines change, the reason no longer sticks there.
	third := commit(numbered(40, 4))
	if v = review(third, reworded); v.Status != StatusFindings || len(v.Overridden) != 0 || len(v.Findings()) != 1 {
		t.Fatalf("a repeat after the accepted lines changed: %+v", v)
	}
	// A truly clean review forgets the rounds and their reasons.
	if err = store.Save(Record{Verdict: Verdict{Status: StatusClean, Capture: third, Reviewers: []Reviewer{{Text: CleanReply}}}}); err != nil {
		t.Fatal(err)
	}
	if rounds, _ = store.Rounds(); len(rounds) != 0 {
		t.Fatalf("rounds kept after a clean review: %+v", rounds)
	}
}

func TestLaterRoundSeesTheOverrideAndLeavesOutItsRepeat(t *testing.T) {
	root, c, s := reviewRepo(t)
	git := func(args ...string) string {
		t.Helper()
		v, err := gitx.Run(root, append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.com"}, args...)...)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	capture := func(edited int) Capture {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "change.txt"), []byte(numbered(20, edited)), 0600); err != nil {
			t.Fatal(err)
		}
		git("add", "change.txt")
		git("commit", "-m", "edit")
		capture, err := NewCapture(root, c.BaseCommit, git("rev-parse", "HEAD^{tree}"))
		if err != nil {
			t.Fatal(err)
		}
		return capture
	}
	trace := filepath.Join(t.TempDir(), "trace")
	o := Options{Root: root, Intent: "request", Settings: Settings{Reviewers: []Family{Terra}, MinCompleted: 1, Adapters: map[string][]string{"codex": fakeAdapter("release-race", "gpt-6.10-terra", trace)}}}
	first := capture(0)
	if v := Run(context.Background(), s, first, o); v.Status != StatusFindings || len(v.Findings()) != 1 {
		t.Fatalf("%+v", v)
	}
	const reason = "the npm job waits for goreleaser, so the release always exists first"
	if err := s.Override(reason, first.Tree); err != nil {
		t.Fatal(err)
	}

	// The next round fixes something else. Its reviewer is shown the earlier
	// round with the reason, and its rewording of the point is left out.
	o.Settings.Adapters["codex"] = fakeAdapter("release-race-reworded", "gpt-6.10-terra", trace)
	v := Run(context.Background(), s, capture(15), o)
	if v.Reused || v.Status != StatusClean || len(v.Findings()) != 0 || len(v.Overridden) != 1 || v.Overridden[0].Round != 1 {
		t.Fatalf("%+v", v)
	}
	prompt, err := os.ReadFile(filepath.Join(v.LogDir, "prompt.txt"))
	if err != nil || !strings.Contains(string(prompt), "<earlier_rounds>") || !strings.Contains(string(prompt), "Round 1 (snapshot tree "+first.Tree) ||
		!strings.Contains(string(prompt), "npm can publish a newer version than the latest GitHub release `change.txt:3-4` (1-terra; cited lines unchanged since that round)") ||
		!strings.Contains(string(prompt), "<author_reason>\n"+reason+"\n</author_reason>") {
		t.Fatalf("prompt: %v\n%s", err, prompt)
	}
	// What `slopcop review show` prints agrees with the check.
	shown, err := ApplyFindingOverrides(root, v)
	if err != nil || shown.Status != StatusClean || len(shown.Overridden) != 1 || shown.Overridden[0].Round != 1 {
		t.Fatalf("shown: %+v, %v", shown, err)
	}

	// A round that edits the accepted lines is reviewed on its own merits.
	if v = Run(context.Background(), s, capture(4), o); v.Status != StatusFindings || len(v.Overridden) != 0 || len(v.Findings()) != 1 {
		t.Fatalf("%+v", v)
	}
}

func TestRoundLimitDropsRoundsWithoutAReasonFirst(t *testing.T) {
	var rounds []Round
	for n := 1; n <= maxRounds+5; n++ {
		rounds = appendRound(rounds, Record{}, []Finding{{Title: "point"}})
		if n == 2 {
			rounds[len(rounds)-1].Override = "accepted"
		}
	}
	if len(rounds) != maxRounds || rounds[0].Number != 2 || rounds[0].Override != "accepted" || rounds[1].Number != 7 || rounds[len(rounds)-1].Number != maxRounds+5 {
		t.Fatalf("rounds kept: %d, first %+v, second %d", len(rounds), rounds[0], rounds[1].Number)
	}
	// Only reasons left: the oldest goes.
	for i := range rounds {
		rounds[i].Override = "accepted"
	}
	if rounds = appendRound(rounds, Record{}, nil); rounds[0].Number != 7 {
		t.Fatalf("first kept = %d", rounds[0].Number)
	}
}
