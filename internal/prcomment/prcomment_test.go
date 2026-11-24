package prcomment

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const samplePatch = "@@ -1,3 +1,4 @@\n line one\n-old two\n+new two\n+added three\n line four\n@@ -20,2 +21,3 @@ func f() {\n context 21\n+added 22\n context 23\n\\ No newline at end of file"

func TestParsePatchKeepsNewSideLines(t *testing.T) {
	lines := parsePatch(samplePatch)
	want := map[int]int{1: 0, 2: 0, 3: 0, 4: 0, 21: 1, 22: 1, 23: 1}
	if len(lines) != len(want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}
	for line, hunk := range want {
		if got, ok := lines[line]; !ok || got != hunk {
			t.Errorf("line %d: hunk %d %v, want %d", line, got, ok, hunk)
		}
	}
}

func TestAnchorKeepsARangeInOneHunkAndFallsBackToItsFirstShownLine(t *testing.T) {
	lines := parsePatch(samplePatch)
	cases := []struct {
		start, end, from, to int
		ok                   bool
	}{
		{2, 3, 2, 3, true},     // both ends in the first hunk
		{3, 0, 3, 3, true},     // a single line
		{3, 22, 3, 3, true},    // ends in two hunks: the first shown line
		{10, 22, 21, 21, true}, // starts outside the diff
		{8, 12, 0, 0, false},   // never shown
		{0, 0, 0, 0, false},    // no line
	}
	for _, c := range cases {
		from, to, ok := lines.anchor(c.start, c.end)
		if from != c.from || to != c.to || ok != c.ok {
			t.Errorf("anchor(%d, %d) = %d, %d, %v; want %d, %d, %v", c.start, c.end, from, to, ok, c.from, c.to, c.ok)
		}
	}
}

func TestQuoteDefusesMentionsAndHTML(t *testing.T) {
	got := quote("cc @alice <img src=x onerror=y> &amp; \x1b[31m")
	for _, bad := range []string{"@alice", "<img", "\x1b"} {
		if strings.Contains(got, bad) {
			t.Errorf("quote kept %q: %q", bad, got)
		}
	}
	if !strings.Contains(got, "&lt;img") || !strings.Contains(got, "&amp;amp;") {
		t.Errorf("quote = %q", got)
	}
	long := quote(strings.Repeat("x", maxTextRunes+10))
	if n := len([]rune(long)); n != maxTextRunes+1 {
		t.Errorf("quoted length = %d, want %d", n, maxTextRunes+1)
	}
}

func TestMarkerCannotBeClosedByAFindingID(t *testing.T) {
	it := item{key: "x --> <b>evil</b>"}
	if strings.Count(it.marker(), "-->") != 1 {
		t.Fatalf("marker = %q", it.marker())
	}
	if keys := markedKeys("text " + it.marker()); len(keys) != 1 || keys[0] != safeMarker(it.key) {
		t.Fatalf("keys = %q", keys)
	}
}

const reviewReport = `{
  "status": "findings", "staticStatus": "findings", "exitReason": "static_and_review_findings",
  "checkId": "chk-1",
  "summary": {"newFindings": 1, "existingFindings": 4, "reviewFindings": 3},
  "findings": [
    {"id": "f-static", "policyId": "rule.a", "severity": "warning", "certainty": "definite", "path": "src/a.go", "startLine": 22, "endLine": 22, "title": "a static finding", "new": true},
    {"id": "f-old", "policyId": "rule.b", "severity": "warning", "path": "src/a.go", "startLine": 1, "title": "already there", "new": false}
  ],
  "review": {"status": "findings", "error": "",
    "reviewers": [{"name": "1-sol", "family": "sol", "status": "findings"}, {"name": "2-terra", "family": "terra", "status": "findings"}],
    "findings": [
      {"id": "rv-1", "reviewer": "1-sol", "priority": "P2", "path": "src/a.go", "startLine": 2, "endLine": 3, "title": "on the diff", "message": "ping @bob"},
      {"id": "rv-1", "reviewer": "2-terra", "priority": "P2", "path": "src/a.go", "startLine": 2, "endLine": 3, "title": "on the diff", "message": "ping @bob"},
      {"id": "rv-2", "reviewer": "1-sol", "priority": "P1", "path": "src/b.go", "startLine": 9, "title": "in a file the pull request does not change"}
    ]}
}`

// fakeGitHub is a pull request's comments, kept in memory.
type fakeGitHub struct {
	mu             sync.Mutex
	t              *testing.T
	reviews        []newReview
	issueComments  []comment
	reviewComments []comment
	refuseWrites   int
	refuseReview   int
	// refusePatch refuses edits, as GitHub does a comment another app wrote.
	refusePatch bool
	patches     []string
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer tok" {
		f.t.Errorf("%s %s without the token", r.Method, r.URL.Path)
	}
	page := r.URL.Query().Get("page")
	if r.Method == http.MethodGet && page != "" && page != "1" {
		_, _ = w.Write([]byte("[]"))
		return
	}
	write := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	refuse := func(status int) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/user":
		refuse(http.StatusForbidden)
	case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/pulls/7/files":
		write([]prFile{{Filename: "src/a.go", Status: "modified", Patch: samplePatch}})
	case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/pulls/7/comments":
		write(f.reviewComments)
	case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/7/comments":
		write(f.issueComments)
	case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/pulls/7/reviews":
		if f.refuseWrites != 0 {
			refuse(f.refuseWrites)
			return
		}
		if f.refuseReview != 0 {
			refuse(f.refuseReview)
			return
		}
		var review newReview
		_ = json.NewDecoder(r.Body).Decode(&review)
		f.reviews = append(f.reviews, review)
		for _, c := range review.Comments {
			f.reviewComments = append(f.reviewComments, comment{ID: int64(100 + len(f.reviewComments)), Body: c.Body, User: user{Login: "github-actions[bot]", Type: "Bot"}})
		}
		write(map[string]int{"id": 1})
	case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues/7/comments":
		if f.refuseWrites != 0 {
			refuse(f.refuseWrites)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		c := comment{ID: int64(500 + len(f.issueComments)), Body: body["body"], HTMLURL: fmt.Sprintf("https://github.test/c/%d", 500+len(f.issueComments)), User: user{Login: "github-actions[bot]", Type: "Bot"}}
		f.issueComments = append(f.issueComments, c)
		write(c)
	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/repos/o/r/issues/comments/"):
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.patches = append(f.patches, r.URL.Path)
		if f.refusePatch {
			refuse(http.StatusForbidden)
			return
		}
		for i := range f.issueComments {
			if fmt.Sprintf("/repos/o/r/issues/comments/%d", f.issueComments[i].ID) == r.URL.Path {
				f.issueComments[i].Body = body["body"]
				write(f.issueComments[i])
				return
			}
		}
		refuse(http.StatusNotFound)
	default:
		f.t.Errorf("unexpected %s %s", r.Method, r.URL.String())
		refuse(http.StatusNotFound)
	}
}

func newFake(t *testing.T) (*fakeGitHub, *GitHub) {
	t.Helper()
	fake := &fakeGitHub{t: t}
	server := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(server.Close)
	return fake, &GitHub{BaseURL: server.URL, Token: "tok", UserAgent: "slopcop/test"}
}

func mustParse(t *testing.T, text string) Report {
	t.Helper()
	r, err := ParseReport([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

var target = Target{Repo: "o/r", PR: 7, Commit: "0123456789abcdef"}

func TestPublishCommentsDiffLinesAndSummarizesTheRest(t *testing.T) {
	fake, gh := newFake(t)
	result, err := Publish(context.Background(), gh, mustParse(t, reviewReport), target)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.reviews) != 1 {
		t.Fatalf("reviews = %d, want 1", len(fake.reviews))
	}
	review := fake.reviews[0]
	if review.Event != "COMMENT" || review.CommitID != target.Commit {
		t.Errorf("review = %+v", review)
	}
	if len(review.Comments) != 2 {
		t.Fatalf("comments = %+v, want the static finding and one for rv-1", review.Comments)
	}
	static, rv1 := review.Comments[0], review.Comments[1]
	if static.Path != "src/a.go" || static.Line != 22 || static.StartLine != 0 || !strings.Contains(static.Body, "slopcop ignore f-static") {
		t.Errorf("static comment = %+v", static)
	}
	if rv1.Line != 3 || rv1.StartLine != 2 || rv1.Side != "RIGHT" || rv1.StartSide != "RIGHT" {
		t.Errorf("review comment range = %+v", rv1)
	}
	if !strings.Contains(rv1.Body, "`1-sol`, `2-terra`") || strings.Contains(rv1.Body, "@bob") {
		t.Errorf("review comment body = %q", rv1.Body)
	}
	if result.Posted != 2 || result.Inline != 2 || result.Elsewhere != 1 || result.ReadOnly {
		t.Errorf("result = %+v", result)
	}
	if len(fake.issueComments) != 1 {
		t.Fatalf("summary comments = %d, want 1", len(fake.issueComments))
	}
	body := fake.issueComments[0].Body
	for _, want := range []string{summaryMarker, "SlopCop found 3 new findings (1 from the static check, 2 from the review)", "in a file the pull request does not change", "`src/b.go:9`", "2 findings are commented"} {
		if !strings.Contains(body, want) {
			t.Errorf("summary lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "already there") {
		t.Errorf("summary lists a finding already in the code:\n%s", body)
	}
	if result.SummaryURL == "" || result.Outcome.StaticFindings != 1 || result.Outcome.ReviewFindings != 2 {
		t.Errorf("result = %+v", result)
	}
}

func TestPublishAgainUpdatesTheSummaryAndDoesNotRepeatComments(t *testing.T) {
	fake, gh := newFake(t)
	report := mustParse(t, reviewReport)
	if _, err := Publish(context.Background(), gh, report, target); err != nil {
		t.Fatal(err)
	}
	result, err := Publish(context.Background(), gh, report, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.reviews) != 1 || result.Posted != 0 || result.Inline != 2 {
		t.Fatalf("second run posted a review: reviews=%d result=%+v", len(fake.reviews), result)
	}
	if len(fake.issueComments) != 1 || len(fake.patches) != 1 {
		t.Fatalf("second run did not update the summary in place: comments=%d patches=%v", len(fake.issueComments), fake.patches)
	}

	clean := mustParse(t, `{"status":"clean","summary":{"newFindings":0},"findings":[]}`)
	if _, err := Publish(context.Background(), gh, clean, target); err != nil {
		t.Fatal(err)
	}
	if body := fake.issueComments[0].Body; !strings.Contains(body, "SlopCop found no new findings") {
		t.Errorf("a clean run left the old summary:\n%s", body)
	}
}

func TestPublishIgnoresAPersonsCommentThatQuotesTheMarkers(t *testing.T) {
	fake, gh := newFake(t)
	fake.issueComments = []comment{{ID: 9, Body: summaryMarker + " mine", User: user{Login: "mallory", Type: "User"}}}
	fake.reviewComments = []comment{{ID: 10, Body: item{key: "rv-1"}.marker(), User: user{Login: "mallory", Type: "User"}}}
	result, err := Publish(context.Background(), gh, mustParse(t, reviewReport), target)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.patches) != 0 || len(fake.issueComments) != 2 {
		t.Errorf("edited a person's comment: patches=%v", fake.patches)
	}
	if result.Posted != 2 {
		t.Errorf("a person's marker suppressed a finding: %+v", result)
	}
}

func TestPublishAfterTheAppDoesNotRepeatItsComments(t *testing.T) {
	fake, gh := newFake(t)
	app := user{Login: "slopcop[bot]", Type: "Bot"}
	fake.reviewComments = []comment{{ID: 10, Body: item{key: "rv-1"}.marker(), User: app}}
	fake.issueComments = []comment{{ID: 11, Body: summaryMarker + " the App's", User: app}}
	fake.refusePatch = true
	result, err := Publish(context.Background(), gh, mustParse(t, reviewReport), target)
	if err != nil {
		t.Fatal(err)
	}
	// The App's inline comment stands; only the static finding is new.
	if result.Posted != 1 || result.Inline != 2 || result.ReadOnly {
		t.Errorf("result = %+v", result)
	}
	// The App's summary cannot be edited, so the token writes its own.
	if len(fake.issueComments) != 2 || result.SummaryURL == "" {
		t.Errorf("comments = %+v, result = %+v", fake.issueComments, result)
	}
}

func TestPublishCleanCheckLeavesAQuietPullRequestAlone(t *testing.T) {
	fake, gh := newFake(t)
	result, err := Publish(context.Background(), gh, mustParse(t, `{"status":"clean","summary":{"newFindings":0},"findings":[]}`), target)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.issueComments) != 0 || len(fake.reviews) != 0 {
		t.Errorf("a clean check commented: %+v", fake.issueComments)
	}
	if !strings.Contains(result.Summary, "no new findings") {
		t.Errorf("summary = %q", result.Summary)
	}
}

func TestPublishWithAReadOnlyTokenWritesNothingAndListsEveryFinding(t *testing.T) {
	fake, gh := newFake(t)
	fake.refuseWrites = http.StatusForbidden
	result, err := Publish(context.Background(), gh, mustParse(t, reviewReport), target)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ReadOnly || len(fake.issueComments) != 0 {
		t.Fatalf("result = %+v", result)
	}
	for _, want := range []string{"a static finding", "on the diff", "in a file the pull request does not change"} {
		if !strings.Contains(result.Summary, want) {
			t.Errorf("job summary lacks %q:\n%s", want, result.Summary)
		}
	}
}

func TestPublishListsFindingsGitHubWouldNotPlace(t *testing.T) {
	fake, gh := newFake(t)
	fake.refuseReview = http.StatusUnprocessableEntity
	result, err := Publish(context.Background(), gh, mustParse(t, reviewReport), target)
	if err != nil {
		t.Fatal(err)
	}
	if result.Posted != 0 || result.Elsewhere != 3 {
		t.Fatalf("result = %+v", result)
	}
	if body := fake.issueComments[0].Body; !strings.Contains(body, "a static finding") || !strings.Contains(body, "on the diff") {
		t.Errorf("summary lacks the refused findings:\n%s", body)
	}
}

func TestOutcomeHeadlines(t *testing.T) {
	cases := map[string]string{
		`{"status":"findings","exitReason":"review_findings","review":{"findings":[{"id":"rv-1","title":"t"}]}}`:     "SlopCop found 1 new finding, all from the review",
		`{"status":"findings","summary":{"newFindings":2},"findings":[{"id":"a","new":true},{"id":"b","new":true}]}`: "SlopCop found 2 new findings",
		`{"status":"incomplete","summary":{"newFindings":0}}`:                                                        "SlopCop could not finish every rule, so this is not a pass",
		`{"status":"pending","retryAfter":5}`:                                                                        "SlopCop's check was still running when the timeout ran out",
		`{"status":"failed","code":"insufficient_credits","message":"no credits"}`:                                   "SlopCop could not check this change",
		`{"status":"clean","emptyChange":true,"summary":{"newFindings":0}}`:                                          "SlopCop: no change to check",
	}
	for input, want := range cases {
		r := mustParse(t, input)
		if got := outcome(r, items(r)).Headline; got != want {
			t.Errorf("%s: headline %q, want %q", input, got, want)
		}
	}
}

func TestParseReportRefusesTheHumanFormat(t *testing.T) {
	if _, err := ParseReport([]byte("No new findings | Analysis complete\n")); err == nil {
		t.Fatal("parsed a human report")
	}
	if _, err := ParseReport([]byte(`{"findings":[]}`)); err == nil {
		t.Fatal("parsed a report with no status")
	}
}

func TestSummaryStaysUnderGitHubsCommentLimit(t *testing.T) {
	long := strings.Repeat("<&>", maxTextRunes)
	var findings []string
	for i := 0; i < 50; i++ {
		findings = append(findings, fmt.Sprintf(`{"id":"rv-%d","reviewer":"1-sol","priority":"P2","path":"x.go","startLine":%d,"title":%q,"message":%q}`, i, i+1, long, long))
	}
	var incomplete []string
	for i := 0; i < 60; i++ {
		incomplete = append(incomplete, fmt.Sprintf(`{"policyId":"rule.%d","reason":%q}`, i, long))
	}
	r := mustParse(t, `{"status":"failed","code":"x","message":`+fmt.Sprintf("%q", long)+`,"remedy":`+fmt.Sprintf("%q", long)+
		`,"incomplete":[`+strings.Join(incomplete, ",")+`],"review":{"status":"failed","error":`+fmt.Sprintf("%q", long)+`,"findings":[`+strings.Join(findings, ",")+`]}}`)
	all := items(r)
	body := summary(r, outcome(r, all), all, 0, "abc", true)
	if n := len([]rune(body)); n > 65536 {
		t.Fatalf("summary is %d characters", n)
	}
	if !strings.Contains(body, "more; the job log has the full report") {
		t.Errorf("summary does not count the findings it left out")
	}
}

func TestReviewLineSaysAnUnfinishedReviewIsNotFull(t *testing.T) {
	r := mustParse(t, `{"status":"incomplete","review":{"status":"clean","incomplete":true,"reviewers":[{"name":"1-sol"}],"findings":[]}}`)
	line := reviewLine(r, outcome(r, items(r)))
	if !strings.Contains(line, "no findings") || !strings.Contains(line, "not every reviewer finished") {
		t.Fatalf("review line = %q", line)
	}
}
