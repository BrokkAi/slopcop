package report

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/review"
	"github.com/BrokkAi/slopcop/internal/service"
)

const baseCommit = "0123456789abcdef0123456789abcdef01234567"

func line(value int) *int { return &value }

func finding(path string, at int, severity, policy string) service.Finding {
	return service.Finding{
		PolicyID:  policy,
		Severity:  severity,
		Certainty: "definite",
		Path:      path,
		StartLine: line(at),
		EndLine:   line(at),
		Title:     policy + " title",
		New:       true,
	}
}

func existing(path string, at int, severity, policy string) service.Finding {
	found := finding(path, at, severity, policy)
	found.New = false
	return found
}

func sampleFindings() []service.Finding {
	note := finding("new file.py", 1, "info", "bifrost.maintainability.complexity")
	return []service.Finding{
		finding("cli.py", 2, "warning", "bifrost.correctness.dynamic-evaluation"),
		finding("cli.py", 5, "warning", "bifrost.correctness.dynamic-evaluation"),
		note,
	}
}

// response builds a completed check over a change touching three files.
func response(status, failOn string, findings []service.Finding) service.Response {
	fresh := 0
	for _, finding := range findings {
		if finding.New {
			fresh++
		}
	}
	return service.Response{
		Status:     status,
		BaseCommit: baseCommit,
		Applied:    true,
		Summary: service.Summary{
			Files:               3,
			Findings:            len(findings),
			NewFindings:         fresh,
			ExistingFindings:    len(findings) - fresh,
			IncompletePolicyIDs: []string{},
			FailOn:              failOn,
		},
		Findings:   findings,
		Incomplete: []service.IncompletePolicy{},
	}
}

// mixed holds one new finding among findings that were already in the code.
func mixed() service.Response {
	return response(service.StatusFindings, "finding", []service.Finding{
		existing("server/domain.rs", 12, "error", "acme.unwrap"),
		finding("server/domain.rs", 703, "warning", "bifrost.performance.regex-compile-in-loop"),
		existing("server/domain.rs", 910, "warning", "acme.guard"),
		existing("server/main.rs", 4, "note", "acme.length"),
	})
}

func failed() service.Response {
	failure := response(service.StatusIncomplete, "finding", []service.Finding{})
	failure.Applied = false
	failure.Summary.IncompletePolicies = 1
	failure.Failure = "The analysis could not be completed."
	return failure
}

func withIncomplete(r service.Response, total int, policies ...service.IncompletePolicy) service.Response {
	r.Status = service.StatusIncomplete
	r.Incomplete = policies
	r.Summary.IncompletePolicies = total
	r.Summary.IncompletePolicyIDs = nil
	for _, policy := range policies {
		r.Summary.IncompletePolicyIDs = append(r.Summary.IncompletePolicyIDs, policy.PolicyID)
	}
	return r
}

func render(t *testing.T, r service.Response, format string) string {
	t.Helper()
	return renderWith(t, r, format, Options{})
}

func renderWith(t *testing.T, r service.Response, format string, options Options) string {
	t.Helper()
	var out bytes.Buffer
	if err := Write(&out, r, format, options); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return out.String()
}

func TestHumanListsEachFindingThenTheSummary(t *testing.T) {
	got := render(t, response(service.StatusFindings, "finding", sampleFindings()), FormatHuman)
	want := "New findings\n" +
		"  [warning] bifrost.correctness.dynamic-evaluation: bifrost.correctness.dynamic-evaluation title\n" +
		"    Location: cli.py:2\n" +
		"    Certainty: definite\n" +
		"\n" +
		"  [warning] bifrost.correctness.dynamic-evaluation: bifrost.correctness.dynamic-evaluation title\n" +
		"    Location: cli.py:5\n" +
		"    Certainty: definite\n" +
		"\n" +
		"Notes\n" +
		"  [info] bifrost.maintainability.complexity: bifrost.maintainability.complexity title\n" +
		"    Location: new file.py:1\n" +
		"    Certainty: definite\n" +
		"\n" +
		"2 new findings across 1 file(s); 1 note: bifrost.maintainability.complexity title | Analysis complete\n"
	if got != want {
		t.Errorf("the file count is files with findings, not changed files:\n%s", got)
	}

	clean := render(t, response(service.StatusClean, "finding", []service.Finding{}), FormatHuman)
	if clean != "No new findings | Analysis complete\n" {
		t.Errorf("clean = %q", clean)
	}

	unlocated := finding("cli.py", 1, "warning", "p")
	unlocated.StartLine = nil
	unlocated.Title = "evil\x1b[2J‮title\nnext"
	got = render(t, response(service.StatusFindings, "finding", []service.Finding{unlocated}), FormatHuman)
	if !strings.Contains(got, "    Location: cli.py\n") {
		t.Errorf("unlocated = %q", got)
	}
	if !strings.Contains(got, "[warning] p: evil�[2J�title�next\n") {
		t.Errorf("control characters reached the terminal: %q", got)
	}
	if !strings.HasSuffix(got, "\n1 new finding across 1 file(s) | Analysis complete\n") {
		t.Errorf("summary = %q", got)
	}
}

func TestHumanListsNewFindingsAndCountsTheRest(t *testing.T) {
	got := render(t, mixed(), FormatHuman)
	want := "New findings\n" +
		"  [warning] bifrost.performance.regex-compile-in-loop: bifrost.performance.regex-compile-in-loop title\n" +
		"    Location: server/domain.rs:703\n" +
		"    Certainty: definite\n" +
		"\n" +
		"1 new finding across 1 file(s); 3 already in the code | Analysis complete\n"
	if got != want {
		t.Errorf("default = %q", got)
	}

	got = renderWith(t, mixed(), FormatHuman, Options{ShowExisting: true})
	for _, want := range []string{
		"New findings\n  [warning] bifrost.performance.regex-compile-in-loop",
		"Already in the code (never fail the check)\n  [error] acme.unwrap",
		"    Location: server/main.rs:4\n",
		"\n1 new finding across 1 file(s); 3 already in the code | Analysis complete\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("show existing = %q, want %q", got, want)
		}
	}
	if strings.Index(got, "acme.unwrap") < strings.Index(got, "regex-compile-in-loop") {
		t.Errorf("new findings come first: %q", got)
	}

	onlyExisting := response(service.StatusClean, "finding", []service.Finding{existing("a.go", 1, "error", "p")})
	if got := render(t, onlyExisting, FormatHuman); got != "No new findings; 1 already in the code | Analysis complete\n" {
		t.Errorf("only existing = %q", got)
	}

	// The service lists only new findings and counts the rest in the summary;
	// the summary line still says how many were already there.
	counted := response(service.StatusFindings, "finding", []service.Finding{
		finding("server/domain.rs", 703, "warning", "bifrost.performance.regex-compile-in-loop"),
	})
	counted.Summary.ExistingFindings = 72
	if got := render(t, counted, FormatHuman); !strings.HasSuffix(got, "\n1 new finding across 1 file(s); 72 already in the code | Analysis complete\n") {
		t.Errorf("counted existing = %q", got)
	}
}

func TestHumanSaysWhyTheAnalysisDidNotComplete(t *testing.T) {
	got := render(t, failed(), FormatHuman)
	if got != "No new findings | Analysis did not complete: The analysis could not be completed.\n" {
		t.Errorf("failed = %q", got)
	}

	partial := withIncomplete(
		response(service.StatusFindings, "finding", []service.Finding{finding("cli.py", 2, "warning", "p")}),
		3,
		service.IncompletePolicy{PolicyID: "a.first", Reason: "timed_out"},
		service.IncompletePolicy{PolicyID: "evil\x1b[2J", Reason: "base_comparison_failed"},
		service.IncompletePolicy{PolicyID: "z.last", Reason: "unsupported", Detail: "type=taint_evaluation\x1b[2J"},
	)
	got = render(t, partial, FormatHuman)
	want := "| Analysis incomplete: 3 check(s) did not complete\n" +
		"Incomplete: a.first (timed_out), evil�[2J (base_comparison_failed), z.last (unsupported: type=taint_evaluation�[2J)\n"
	if !strings.HasSuffix(got, want) {
		t.Errorf("incomplete = %q", got)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(render(t, partial, FormatJSON)), &document); err != nil {
		t.Fatalf("json: %v", err)
	}
	incomplete := document["incomplete"].([]any)
	if first := incomplete[0].(map[string]any); first["detail"] != nil {
		t.Errorf("a check without a detail = %v", first)
	}
	if last := incomplete[2].(map[string]any); last["detail"] != "type=taint_evaluation\x1b[2J" {
		t.Errorf("json detail = %v", last)
	}

	many := withIncomplete(
		response(service.StatusClean, "finding", []service.Finding{}),
		60,
		service.IncompletePolicy{PolicyID: "acme.p00", Reason: "timed_out"},
	)
	got = render(t, many, FormatHuman)
	if !strings.HasSuffix(got, "Incomplete: acme.p00 (timed_out), and 59 more\n") {
		t.Errorf("unnamed checks = %q", got)
	}
}

func TestOmittedFindingsAreSaidInEveryFormat(t *testing.T) {
	cut := response(service.StatusFindings, "warning", sampleFindings())
	cut.Summary.OmittedFindings = 45
	notice := "45 more new finding(s) were not reported because the change has more than " +
		"a check may report; the most severe are listed"

	got := render(t, cut, FormatHuman)
	want := "\n2 new findings across 1 file(s); 1 note: bifrost.maintainability.complexity title; +45 not reported | Analysis complete\n" +
		"Omitted: " + notice + ".\n"
	if !strings.HasSuffix(got, want) {
		t.Errorf("human = %q", got)
	}

	var document map[string]any
	if err := json.Unmarshal([]byte(render(t, cut, FormatJSON)), &document); err != nil {
		t.Fatalf("json: %v", err)
	}
	summary := document["summary"].(map[string]any)
	if summary["omittedFindings"] != float64(45) || document["status"] != "findings" {
		t.Errorf("json = %v", document)
	}

	var log map[string]any
	if err := json.Unmarshal([]byte(render(t, cut, FormatSARIF)), &log); err != nil {
		t.Fatalf("sarif: %v", err)
	}
	invocation := log["runs"].([]any)[0].(map[string]any)["invocations"].([]any)[0].(map[string]any)
	if invocation["executionSuccessful"] != true {
		t.Errorf("invocation = %v", invocation)
	}
	wantNotifications := []any{map[string]any{
		"level":      "note",
		"message":    map[string]any{"text": notice},
		"properties": map[string]any{"slopcop.omittedFindings": float64(45)},
	}}
	if !reflect.DeepEqual(invocation["toolExecutionNotifications"], wantNotifications) {
		t.Errorf("notifications = %v", invocation["toolExecutionNotifications"])
	}

	if strings.Contains(render(t, response(service.StatusFindings, "warning", sampleFindings()), FormatHuman), "not reported") {
		t.Error("a whole report said findings were left out")
	}
}

// lessCertainFindings is 0 under --certainty all, and every format must still
// carry it then, so a consumer can tell the filter took effect. A service from
// before the field omits it, and no format may claim 0 for that.
func TestLessCertainFindingsSurviveZeroAndAbsence(t *testing.T) {
	cases := []struct {
		name  string
		sent  string
		want  any
		human string
	}{
		{"all", `"lessCertainFindings": 0,`, float64(0), "No new findings | Analysis complete\n"},
		{"definite", `"lessCertainFindings": 2,`, float64(2),
			"No new findings; 2 less certain not shown (--certainty all) | Analysis complete\n"},
		{"older service", ``, nil, "No new findings | Analysis complete\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var decoded service.Response
			body := `{"status": "clean", "baseCommit": "` + baseCommit + `", "applied": true,
				"summary": {"files": 1, "findings": 0, "newFindings": 0, ` + tc.sent + ` "incompletePolicies": 0, "failOn": "finding"},
				"findings": [], "incomplete": []}`
			if err := json.Unmarshal([]byte(body), &decoded); err != nil {
				t.Fatal(err)
			}

			if got := render(t, decoded, FormatHuman); got != tc.human {
				t.Errorf("human = %q, want %q", got, tc.human)
			}

			var document map[string]any
			if err := json.Unmarshal([]byte(render(t, decoded, FormatJSON)), &document); err != nil {
				t.Fatalf("json: %v", err)
			}
			got, present := document["summary"].(map[string]any)["lessCertainFindings"]
			if present != (tc.want != nil) || got != tc.want {
				t.Errorf("json lessCertainFindings = %v (present %v), want %v", got, present, tc.want)
			}

			var log map[string]any
			if err := json.Unmarshal([]byte(render(t, decoded, FormatSARIF)), &log); err != nil {
				t.Fatalf("sarif: %v", err)
			}
			properties := log["runs"].([]any)[0].(map[string]any)["properties"].(map[string]any)
			got, present = properties["slopcop.lessCertainFindings"]
			if present != (tc.want != nil) || got != tc.want {
				t.Errorf("sarif slopcop.lessCertainFindings = %v (present %v), want %v", got, present, tc.want)
			}
		})
	}
}

func TestJSONIsTheStructuredReport(t *testing.T) {
	got := render(t, response(service.StatusClean, "error", sampleFindings()), FormatJSON)
	if !strings.HasSuffix(got, "}\n") || !strings.Contains(got, "\n  \"status\"") {
		t.Errorf("not pretty printed: %q", got)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(got), &document); err != nil {
		t.Fatalf("json: %v", err)
	}
	var keys []string
	for key := range document {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if want := []string{"approved", "baseCommit", "findings", "incomplete", "inconclusive", "status", "summary"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
	wantSummary := map[string]any{
		"files":                 float64(3),
		"findings":              float64(3),
		"newFindings":           float64(3),
		"existingFindings":      float64(0),
		"incompletePolicies":    float64(0),
		"incompletePolicyIds":   []any{},
		"inconclusive":          float64(0),
		"inconclusiveElsewhere": float64(0),
		"omittedFindings":       float64(0),
		"remappedIgnores":       float64(0),
		"failOn":                "error",
	}
	if !reflect.DeepEqual(document["summary"], wantSummary) {
		t.Errorf("summary = %v", document["summary"])
	}
	wantFinding := map[string]any{
		"policyId":  "bifrost.maintainability.complexity",
		"severity":  "info",
		"certainty": "definite",
		"path":      "new file.py",
		"startLine": float64(1),
		"endLine":   float64(1),
		"title":     "bifrost.maintainability.complexity title",
		"new":       true,
	}
	if findings := document["findings"].([]any); len(findings) != 3 || !reflect.DeepEqual(findings[2], wantFinding) {
		t.Errorf("findings = %v", document["findings"])
	}

	if err := json.Unmarshal([]byte(render(t, failed(), FormatJSON)), &document); err != nil {
		t.Fatalf("json: %v", err)
	}
	if document["status"] != "incomplete" || document["failure"] != "The analysis could not be completed." {
		t.Errorf("failed = %v", document)
	}
	if findings, ok := document["findings"].([]any); !ok || len(findings) != 0 {
		t.Errorf("failed findings = %v", document["findings"])
	}
}

func TestJSONListsNewFindingsAndCountsTheSplit(t *testing.T) {
	decode := func(options Options) (map[string]any, []any) {
		var document map[string]any
		if err := json.Unmarshal([]byte(renderWith(t, mixed(), FormatJSON, options)), &document); err != nil {
			t.Fatalf("json: %v", err)
		}
		return document, document["findings"].([]any)
	}
	document, findings := decode(Options{})
	summary := document["summary"].(map[string]any)
	if summary["findings"] != float64(4) || summary["newFindings"] != float64(1) || summary["existingFindings"] != float64(3) {
		t.Errorf("summary = %v", summary)
	}
	if len(findings) != 1 || findings[0].(map[string]any)["new"] != true {
		t.Errorf("default findings = %v", findings)
	}
	_, findings = decode(Options{ShowExisting: true})
	var marks []any
	for _, finding := range findings {
		marks = append(marks, finding.(map[string]any)["new"])
	}
	if !reflect.DeepEqual(marks, []any{true, false, false, false}) {
		t.Errorf("new findings first, then the existing ones: %v", marks)
	}
}

func TestPendingIsAParseableReport(t *testing.T) {
	pending := service.Pending{RetryAfter: 5, Message: "The check is still running.", Remedy: "Run the same check again."}
	write := func(format string) string {
		var out bytes.Buffer
		if err := WritePending(&out, pending, format, Options{}); err != nil {
			t.Fatalf("WritePending(%s): %v", format, err)
		}
		return out.String()
	}
	if got := write(FormatHuman); got != "" {
		t.Errorf("human pending writes stdout: %q", got)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(write(FormatJSON)), &document); err != nil {
		t.Fatalf("json: %v", err)
	}
	want := map[string]any{
		"status":     "pending",
		"approved":   false,
		"retryAfter": float64(5),
		"message":    "The check is still running.",
		"remedy":     "Run the same check again.",
	}
	if !reflect.DeepEqual(document, want) {
		t.Errorf("json = %v", document)
	}
	var log map[string]any
	if err := json.Unmarshal([]byte(write(FormatSARIF)), &log); err != nil {
		t.Fatalf("sarif: %v", err)
	}
	run := log["runs"].([]any)[0].(map[string]any)
	if run["properties"].(map[string]any)["slopcop.status"] != "pending" || len(run["results"].([]any)) != 0 {
		t.Errorf("run = %v", run)
	}
	invocation := run["invocations"].([]any)[0].(map[string]any)
	notification := invocation["toolExecutionNotifications"].([]any)[0].(map[string]any)
	if invocation["executionSuccessful"] != false ||
		notification["descriptor"].(map[string]any)["id"] != "slopcop.pending" ||
		notification["properties"].(map[string]any)["slopcop.retryAfter"] != float64(5) {
		t.Errorf("invocation = %v", invocation)
	}
	if err := WritePending(&bytes.Buffer{}, pending, "xml", Options{}); err == nil {
		t.Error("expected an error for an unknown format")
	}
}

func TestSkippedIsNeverMistakenForACleanCheck(t *testing.T) {
	skipped := Skipped{
		Code:       "repository_not_enabled",
		State:      "not_added",
		Repository: "Acme/widgets",
		Message:    "Acme/widgets is not enabled in your SlopCop workspace, so nothing was checked.",
	}
	write := func(format string) string {
		var out bytes.Buffer
		if err := WriteSkipped(&out, skipped, format, Options{}); err != nil {
			t.Fatalf("WriteSkipped(%s): %v", format, err)
		}
		return out.String()
	}
	if got := write(FormatHuman); got != "" {
		t.Errorf("human skip writes stdout: %q", got)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(write(FormatJSON)), &document); err != nil {
		t.Fatalf("json: %v", err)
	}
	want := map[string]any{
		"status":     "skipped",
		"approved":   false,
		"code":       "repository_not_enabled",
		"state":      "not_added",
		"repository": "Acme/widgets",
		"message":    skipped.Message,
	}
	if !reflect.DeepEqual(document, want) {
		t.Errorf("json = %v", document)
	}
	var log map[string]any
	if err := json.Unmarshal([]byte(write(FormatSARIF)), &log); err != nil {
		t.Fatalf("sarif: %v", err)
	}
	run := log["runs"].([]any)[0].(map[string]any)
	if run["properties"].(map[string]any)["slopcop.status"] != "skipped" || len(run["results"].([]any)) != 0 {
		t.Errorf("run = %v", run)
	}
	invocation := run["invocations"].([]any)[0].(map[string]any)
	notification := invocation["toolExecutionNotifications"].([]any)[0].(map[string]any)
	if invocation["executionSuccessful"] != false ||
		notification["descriptor"].(map[string]any)["id"] != "repository_not_enabled" ||
		notification["properties"].(map[string]any)["slopcop.state"] != "not_added" {
		t.Errorf("invocation = %v", invocation)
	}
}

func TestFailureIsAParseableReport(t *testing.T) {
	failure := &service.Error{StatusCode: 402, Body: service.ErrorBody{
		Code:     "insufficient_credits",
		Message:  "The workspace is out of credits.",
		Remedy:   "Add credits, then retry.",
		TopUpURL: "https://slopcop.com/credits",
	}}
	write := func(format string) string {
		var out bytes.Buffer
		if err := WriteFailure(&out, failure, format, Options{}); err != nil {
			t.Fatalf("WriteFailure(%s): %v", format, err)
		}
		return out.String()
	}
	if got := write(FormatHuman); got != "" {
		t.Errorf("human failure writes stdout: %q", got)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(write(FormatJSON)), &document); err != nil {
		t.Fatalf("json: %v", err)
	}
	want := map[string]any{
		"status":    "failed",
		"approved":  false,
		"code":      "insufficient_credits",
		"message":   "The workspace is out of credits.",
		"remedy":    "Add credits, then retry.",
		"retryable": false,
		"topUpUrl":  "https://slopcop.com/credits",
	}
	if !reflect.DeepEqual(document, want) {
		t.Errorf("json = %v", document)
	}
	var log map[string]any
	if err := json.Unmarshal([]byte(write(FormatSARIF)), &log); err != nil {
		t.Fatalf("sarif: %v", err)
	}
	run := log["runs"].([]any)[0].(map[string]any)
	if run["properties"].(map[string]any)["slopcop.status"] != "failed" || len(run["results"].([]any)) != 0 {
		t.Errorf("run = %v", run)
	}
	invocation := run["invocations"].([]any)[0].(map[string]any)
	notification := invocation["toolExecutionNotifications"].([]any)[0].(map[string]any)
	if invocation["executionSuccessful"] != false || notification["level"] != "error" ||
		notification["descriptor"].(map[string]any)["id"] != "insufficient_credits" {
		t.Errorf("invocation = %v", invocation)
	}
	if err := WriteFailure(&bytes.Buffer{}, failure, "xml", Options{}); err == nil {
		t.Error("expected an error for an unknown format")
	}
}

func TestOmittedFilesReachEveryReportShape(t *testing.T) {
	omitted := []OmittedFile{
		{Path: "large.py", Bytes: 7, Reason: "exceeds the upload limit"},
		{Path: "vendor/huge.json", Bytes: 5, Reason: "exceeds the local review limit"},
	}
	wantOmitted := []any{
		map[string]any{"path": "large.py", "bytes": float64(7), "reason": "exceeds the upload limit"},
		map[string]any{"path": "vendor/huge.json", "bytes": float64(5), "reason": "exceeds the local review limit"},
	}
	options := Options{OmittedFiles: omitted, Review: &review.Verdict{Status: review.StatusClean}}
	failure := &service.Error{StatusCode: 402, Body: service.ErrorBody{
		Code: "insufficient_credits", Message: "The workspace is out of credits.", Remedy: "Add credits, then retry.",
	}}
	writers := []struct {
		name   string
		status string
		write  func(io.Writer, string, Options) error
	}{
		{
			name:   "normal",
			status: service.StatusFindings,
			write: func(w io.Writer, format string, options Options) error {
				return Write(w, response(service.StatusFindings, "finding", sampleFindings()), format, options)
			},
		},
		{
			name:   "pending",
			status: service.StatusPending,
			write: func(w io.Writer, format string, options Options) error {
				return WritePending(w, service.Pending{RetryAfter: 5, Message: "Still running."}, format, options)
			},
		},
		{
			name:   "skipped",
			status: "skipped",
			write: func(w io.Writer, format string, options Options) error {
				return WriteSkipped(w, Skipped{Code: "repository_not_enabled", State: "not_added", Message: "Nothing was checked."}, format, options)
			},
		},
		{
			name:   "failure",
			status: service.StatusFailed,
			write: func(w io.Writer, format string, options Options) error {
				return WriteFailure(w, failure, format, options)
			},
		},
		{
			name:   "ai",
			status: "clean",
			write: func(w io.Writer, format string, options Options) error {
				return WriteAI(w, format, options)
			},
		},
	}

	for _, tc := range writers {
		t.Run(tc.name, func(t *testing.T) {
			var document map[string]any
			var out bytes.Buffer
			if err := tc.write(&out, FormatJSON, options); err != nil {
				t.Fatalf("json: %v", err)
			}
			if err := json.Unmarshal(out.Bytes(), &document); err != nil {
				t.Fatalf("json: %v", err)
			}
			if !reflect.DeepEqual(document["omittedFiles"], wantOmitted) {
				t.Errorf("json omittedFiles = %v", document["omittedFiles"])
			}
			if document["status"] != tc.status || document["approved"] != false {
				t.Errorf("omissions changed result status or approval: %v", document)
			}

			out.Reset()
			if err := tc.write(&out, FormatSARIF, options); err != nil {
				t.Fatalf("sarif: %v", err)
			}
			var log map[string]any
			if err := json.Unmarshal(out.Bytes(), &log); err != nil {
				t.Fatalf("sarif: %v", err)
			}
			runs := log["runs"].([]any)
			for index, rawRun := range runs {
				properties := rawRun.(map[string]any)["properties"].(map[string]any)
				if !reflect.DeepEqual(properties["slopcop.omittedFiles"], wantOmitted) {
					t.Errorf("sarif run %d omittedFiles = %v", index, properties["slopcop.omittedFiles"])
				}
				if index == 0 && properties["slopcop.status"] != tc.status {
					t.Errorf("omissions changed SARIF status: %v", properties)
				}
			}

			out.Reset()
			if err := tc.write(&out, FormatHuman, options); err != nil {
				t.Fatalf("human: %v", err)
			}
			for _, want := range []string{
				"Files omitted from both hosted upload and local review due to size: 2 file(s), 12 bytes total.",
				"Their contents were not analyzed.",
				"large.py (7 bytes): exceeds the upload limit",
				"vendor/huge.json (5 bytes): exceeds the local review limit",
			} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("human report missing %q: %s", want, out.String())
				}
			}

			out.Reset()
			if err := tc.write(&out, FormatHuman, Options{OmittedFiles: omitted, Verbose: true}); err != nil {
				t.Fatalf("verbose human: %v", err)
			}
			for _, want := range []string{
				"large.py (7 bytes): exceeds the upload limit",
				"vendor/huge.json (5 bytes): exceeds the local review limit",
			} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("verbose human report missing %q: %s", want, out.String())
				}
			}
		})
	}
}

func TestSARIFIsAValid210Log(t *testing.T) {
	findings := sampleFindings()
	unrated := finding("lib/util.py", 7, "unrated", "acme.custom")
	unrated.EndLine = nil
	unlocated := finding("lib/util.py", 1, "error", "acme.custom")
	unlocated.StartLine, unlocated.EndLine = nil, nil
	findings = append(findings, unrated, unlocated)

	var log map[string]any
	if err := json.Unmarshal([]byte(render(t, response(service.StatusClean, "never", findings), FormatSARIF)), &log); err != nil {
		t.Fatalf("sarif: %v", err)
	}
	if log["version"] != "2.1.0" || !strings.HasSuffix(log["$schema"].(string), "sarif-schema-2.1.0.json") {
		t.Fatalf("log = %v", log)
	}
	runs := log["runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("runs = %v", runs)
	}
	run := runs[0].(map[string]any)
	driver := run["tool"].(map[string]any)["driver"].(map[string]any)
	if driver["name"] != "SlopCop" {
		t.Errorf("driver = %v", driver)
	}
	var rules []string
	for _, rule := range driver["rules"].([]any) {
		rules = append(rules, rule.(map[string]any)["id"].(string))
	}
	wantRules := []string{
		"acme.custom",
		"bifrost.correctness.dynamic-evaluation",
		"bifrost.maintainability.complexity",
	}
	if !reflect.DeepEqual(rules, wantRules) {
		t.Errorf("rules = %v", rules)
	}

	results := run["results"].([]any)
	if len(results) != 5 {
		t.Fatalf("results = %d", len(results))
	}
	at := func(uri string, startLine float64) map[string]any {
		for _, raw := range results {
			result := raw.(map[string]any)
			physical := result["locations"].([]any)[0].(map[string]any)["physicalLocation"].(map[string]any)
			if physical["artifactLocation"].(map[string]any)["uri"] != uri {
				continue
			}
			region, _ := physical["region"].(map[string]any)
			if (region == nil && startLine == 0) || (region != nil && region["startLine"] == startLine) {
				return result
			}
		}
		t.Fatalf("no result at %s:%v", uri, startLine)
		return nil
	}
	for _, raw := range results {
		result := raw.(map[string]any)
		if rules[int(result["ruleIndex"].(float64))] != result["ruleId"] {
			t.Errorf("ruleIndex does not point at the rule: %v", result)
		}
		if result["baselineState"] != "new" {
			t.Errorf("every listed finding is new: %v", result)
		}
	}
	first := at("cli.py", 2)
	if first["ruleId"] != "bifrost.correctness.dynamic-evaluation" || first["level"] != "warning" {
		t.Errorf("first = %v", first)
	}
	wantPhysical := map[string]any{
		"artifactLocation": map[string]any{"uri": "cli.py", "uriBaseId": "SRCROOT"},
		"region":           map[string]any{"startLine": float64(2), "endLine": float64(2)},
	}
	if physical := first["locations"].([]any)[0].(map[string]any)["physicalLocation"]; !reflect.DeepEqual(physical, wantPhysical) {
		t.Errorf("physical = %v", physical)
	}
	if level := at("new%20file.py", 1)["level"]; level != "note" {
		t.Errorf("note level = %v", level)
	}
	unratedResult := at("lib/util.py", 7)
	region := unratedResult["locations"].([]any)[0].(map[string]any)["physicalLocation"].(map[string]any)["region"].(map[string]any)
	if unratedResult["level"] != "none" || region["endLine"] != nil {
		t.Errorf("unrated = %v", unratedResult)
	}
	if level := at("lib/util.py", 0)["level"]; level != "error" {
		t.Errorf("unlocated level = %v", level)
	}

	invocation := run["invocations"].([]any)[0].(map[string]any)
	if invocation["executionSuccessful"] != true || invocation["toolExecutionNotifications"] != nil {
		t.Errorf("invocation = %v", invocation)
	}
	properties := run["properties"].(map[string]any)
	if properties["slopcop.status"] != "clean" || properties["slopcop.failOn"] != "never" ||
		properties["slopcop.baseCommit"] != baseCommit ||
		properties["slopcop.newFindings"] != float64(5) || properties["slopcop.existingFindings"] != float64(0) {
		t.Errorf("properties = %v", properties)
	}
}

func TestSARIFMarksTheBaselineState(t *testing.T) {
	states := func(options Options) []any {
		var log map[string]any
		if err := json.Unmarshal([]byte(renderWith(t, mixed(), FormatSARIF, options)), &log); err != nil {
			t.Fatalf("sarif: %v", err)
		}
		var out []any
		for _, result := range log["runs"].([]any)[0].(map[string]any)["results"].([]any) {
			out = append(out, result.(map[string]any)["baselineState"])
		}
		return out
	}
	if got := states(Options{}); !reflect.DeepEqual(got, []any{"new"}) {
		t.Errorf("default = %v", got)
	}
	if got := states(Options{ShowExisting: true}); !reflect.DeepEqual(got, []any{"new", "unchanged", "unchanged", "unchanged"}) {
		t.Errorf("show existing = %v", got)
	}

	// New findings answer `findings` even when a check did not complete,
	// and the execution still reads as unsuccessful.
	partial := withIncomplete(mixed(), 1, service.IncompletePolicy{PolicyID: "acme.slow", Reason: "inconclusive"})
	partial.Status = service.StatusFindings
	var log map[string]any
	if err := json.Unmarshal([]byte(render(t, partial, FormatSARIF)), &log); err != nil {
		t.Fatalf("sarif: %v", err)
	}
	invocation := log["runs"].([]any)[0].(map[string]any)["invocations"].([]any)[0].(map[string]any)
	if invocation["executionSuccessful"] != false {
		t.Errorf("invocation = %v", invocation)
	}
}

func TestSARIFSaysWhatDidNotComplete(t *testing.T) {
	partial := withIncomplete(
		response(service.StatusClean, "finding", []service.Finding{}),
		1,
		service.IncompletePolicy{PolicyID: "acme.slow", Reason: "timed_out"},
	)
	unsupported := withIncomplete(
		response(service.StatusClean, "finding", []service.Finding{}),
		1,
		service.IncompletePolicy{PolicyID: "acme.taint", Reason: "unsupported", Detail: "type=taint_evaluation"},
	)
	cases := []struct {
		name     string
		response service.Response
		want     []any
	}{
		{"incomplete", partial, []any{
			map[string]any{
				"level":   "warning",
				"message": map[string]any{"text": "Analysis incomplete: 1 check(s) did not complete"},
			},
			map[string]any{
				"level":      "warning",
				"message":    map[string]any{"text": "acme.slow did not complete (timed_out)"},
				"descriptor": map[string]any{"id": "acme.slow"},
				"properties": map[string]any{"slopcop.reason": "timed_out"},
			},
		}},
		{"unsupported", unsupported, []any{
			map[string]any{
				"level":   "warning",
				"message": map[string]any{"text": "Analysis incomplete: 1 check(s) did not complete"},
			},
			map[string]any{
				"level":      "warning",
				"message":    map[string]any{"text": "acme.taint did not complete (unsupported: type=taint_evaluation)"},
				"descriptor": map[string]any{"id": "acme.taint"},
				"properties": map[string]any{"slopcop.reason": "unsupported", "slopcop.detail": "type=taint_evaluation"},
			},
		}},
		{"failed", failed(), []any{
			map[string]any{
				"level":   "error",
				"message": map[string]any{"text": "Analysis did not complete: The analysis could not be completed."},
			},
		}},
	}
	for _, tc := range cases {
		var log map[string]any
		if err := json.Unmarshal([]byte(render(t, tc.response, FormatSARIF)), &log); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		run := log["runs"].([]any)[0].(map[string]any)
		invocation := run["invocations"].([]any)[0].(map[string]any)
		if invocation["executionSuccessful"] != false {
			t.Errorf("%s: invocation = %v", tc.name, invocation)
		}
		if !reflect.DeepEqual(invocation["toolExecutionNotifications"], tc.want) {
			t.Errorf("%s: notifications = %v", tc.name, invocation["toolExecutionNotifications"])
		}
		if results := run["results"].([]any); len(results) != 0 {
			t.Errorf("%s: results = %v", tc.name, results)
		}
		if rules := run["tool"].(map[string]any)["driver"].(map[string]any)["rules"].([]any); len(rules) != 0 {
			t.Errorf("%s: rules = %v", tc.name, rules)
		}
		if run["properties"].(map[string]any)["slopcop.status"] != "incomplete" {
			t.Errorf("%s: properties = %v", tc.name, run["properties"])
		}
	}
}

func unproven() service.Response {
	r := response(service.StatusClean, "finding", []service.Finding{})
	r.Summary.Inconclusive = 6
	r.Summary.InconclusiveElsewhere = 2
	r.Inconclusive = []service.IncompletePolicy{
		{PolicyID: "acme.a", Reason: "inconclusive"},
		{PolicyID: "acme.b", Reason: "inconclusive"},
		{PolicyID: "acme.c", Reason: "inconclusive"},
	}
	r.InconclusiveElsewhere = []service.IncompletePolicy{{PolicyID: "acme.b", Reason: "inconclusive"}}
	return r
}

func TestHumanCountsInconclusiveChecksAndVerboseNamesThem(t *testing.T) {
	want := "No new findings | Analysis complete\n" +
		"Inconclusive: 6 check(s), 2 only about files the change does not write; " +
		"they never fail the check. --verbose names them.\n"
	if got := render(t, unproven(), FormatHuman); got != want {
		t.Errorf("human = %q, want %q", got, want)
	}

	want = "No new findings | Analysis complete\n" +
		"Inconclusive (never fail the check)\n" +
		"  acme.a\n" +
		"  acme.b (only files the change does not write)\n" +
		"  acme.c\n" +
		"  and 3 more\n"
	if got := renderWith(t, unproven(), FormatHuman, Options{Verbose: true}); got != want {
		t.Errorf("verbose human = %q, want %q", got, want)
	}

	// A service from before the lists only counts them.
	counted := response(service.StatusClean, "finding", []service.Finding{})
	counted.Summary.Inconclusive = 4
	want = "No new findings | Analysis complete\nInconclusive: 4 check(s); they never fail the check.\n"
	if got := render(t, counted, FormatHuman); got != want {
		t.Errorf("counted human = %q, want %q", got, want)
	}
	clean := response(service.StatusClean, "finding", []service.Finding{})
	if got := renderWith(t, clean, FormatHuman, Options{Verbose: true}); got != "No new findings | Analysis complete\n" {
		t.Errorf("clean verbose human = %q", got)
	}
}

func TestJSONNamesInconclusiveChecks(t *testing.T) {
	var document map[string]any
	if err := json.Unmarshal([]byte(render(t, unproven(), FormatJSON)), &document); err != nil {
		t.Fatalf("json: %v", err)
	}
	if document["status"] != "clean" || document["summary"].(map[string]any)["inconclusive"] != float64(6) {
		t.Errorf("json = %v", document)
	}
	want := []any{
		map[string]any{"policyId": "acme.a", "reason": "inconclusive", "elsewhere": false},
		map[string]any{"policyId": "acme.b", "reason": "inconclusive", "elsewhere": true},
		map[string]any{"policyId": "acme.c", "reason": "inconclusive", "elsewhere": false},
	}
	if !reflect.DeepEqual(document["inconclusive"], want) {
		t.Errorf("inconclusive = %v", document["inconclusive"])
	}

	// A service from before `inconclusive` sent only the elsewhere list.
	older := response(service.StatusClean, "finding", []service.Finding{})
	older.Summary.InconclusiveElsewhere = 1
	older.InconclusiveElsewhere = []service.IncompletePolicy{{PolicyID: "acme.b", Reason: "inconclusive"}}
	if err := json.Unmarshal([]byte(render(t, older, FormatJSON)), &document); err != nil {
		t.Fatalf("json: %v", err)
	}
	if !reflect.DeepEqual(document["inconclusive"], want[1:2]) {
		t.Errorf("older inconclusive = %v", document["inconclusive"])
	}
}

func TestSARIFNotesEachInconclusiveCheck(t *testing.T) {
	var log map[string]any
	if err := json.Unmarshal([]byte(render(t, unproven(), FormatSARIF)), &log); err != nil {
		t.Fatalf("sarif: %v", err)
	}
	run := log["runs"].([]any)[0].(map[string]any)
	invocation := run["invocations"].([]any)[0].(map[string]any)
	if invocation["executionSuccessful"] != true {
		t.Errorf("invocation = %v", invocation)
	}
	notifications := invocation["toolExecutionNotifications"].([]any)
	if len(notifications) != 3 {
		t.Fatalf("notifications = %v", notifications)
	}
	want := map[string]any{
		"level":      "note",
		"message":    map[string]any{"text": "acme.b could not prove there is nothing more to find (inconclusive)"},
		"descriptor": map[string]any{"id": "acme.b"},
		"properties": map[string]any{
			"slopcop.reason":       "inconclusive",
			"slopcop.inconclusive": true,
			"slopcop.elsewhere":    true,
		},
	}
	if !reflect.DeepEqual(notifications[1], want) {
		t.Errorf("notification = %v", notifications[1])
	}
	properties := run["properties"].(map[string]any)
	if properties["slopcop.inconclusive"] != float64(6) || properties["slopcop.inconclusiveElsewhere"] != float64(2) {
		t.Errorf("properties = %v", properties)
	}
}

func TestUnknownFormat(t *testing.T) {
	if err := Write(&bytes.Buffer{}, response(service.StatusClean, "finding", nil), "xml", Options{}); err == nil {
		t.Fatal("expected an error for an unknown format")
	}
}

func TestEveryFormatCarriesTheFindingID(t *testing.T) {
	const id = "0f8fad5b-d9cb-469f-a165-70867728950e"
	identified := finding("cli.py", 2, "warning", "acme.eval")
	identified.ID = id
	unidentified := finding("cli.py", 5, "warning", "acme.eval")
	check := response(service.StatusFindings, "finding", []service.Finding{identified, unidentified})

	want := "New findings\n" +
		"  [warning] acme.eval: acme.eval title\n" +
		"    Location: cli.py:2\n" +
		"    Certainty: definite\n" +
		"    ID: " + id + "\n" +
		"\n" +
		"  [warning] acme.eval: acme.eval title\n" +
		"    Location: cli.py:5\n" +
		"    Certainty: definite\n" +
		"\n" +
		"2 new findings across 1 file(s) | Analysis complete\n"
	if got := render(t, check, FormatHuman); got != want {
		t.Errorf("human = %q", got)
	}

	var document struct {
		Findings []map[string]any `json:"findings"`
	}
	if err := json.Unmarshal([]byte(render(t, check, FormatJSON)), &document); err != nil {
		t.Fatalf("json: %v", err)
	}
	if document.Findings[0]["id"] != id {
		t.Errorf("json id = %v", document.Findings[0]["id"])
	}
	if _, ok := document.Findings[1]["id"]; ok {
		t.Errorf("json invented an id: %v", document.Findings[1])
	}

	var log struct {
		Runs []struct {
			Results []map[string]any `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(render(t, check, FormatSARIF)), &log); err != nil {
		t.Fatalf("sarif: %v", err)
	}
	results := log.Runs[0].Results
	if properties, _ := results[0]["properties"].(map[string]any); properties["slopcop.findingId"] != id {
		t.Errorf("sarif properties = %v", results[0]["properties"])
	}
	if _, ok := results[1]["properties"]; ok {
		t.Errorf("sarif invented an id: %v", results[1])
	}
}

// remapped is a check where one new finding and one finding already in the
// code each match an ignore made before their code changed.
func remapped() service.Response {
	touched := finding("tests/test_inputs.py", 58, "warning", "acme.inheritance")
	touched.ID = "98a2d745-fa33-467b-8289-2a9f3d05a0c8"
	touched.PreviouslyIgnored = "f5a9bad1-80ab-4a38-9351-4131e05d1682"
	untouched := existing("tests/test_other.py", 9, "note", "acme.inheritance")
	untouched.ID = "1b4e28ba-2fa1-11d2-883f-0016d3cca427"
	untouched.PreviouslyIgnored = "6fa459ea-ee8a-3ca4-894e-db77e160355e"
	check := response(service.StatusFindings, "finding", []service.Finding{
		touched,
		untouched,
		existing("server/main.rs", 4, "note", "acme.length"),
	})
	check.Summary.RemappedIgnores = 2
	return check
}

func TestHumanSaysAFindingMatchesAnEarlierIgnore(t *testing.T) {
	want := "New findings\n" +
		"  [warning] acme.inheritance: acme.inheritance title\n" +
		"    Location: tests/test_inputs.py:58\n" +
		"    Certainty: definite\n" +
		"    ID: 98a2d745-fa33-467b-8289-2a9f3d05a0c8\n" +
		"    Matches ignored finding f5a9bad1-80ab-4a38-9351-4131e05d1682 (code changed); " +
		"`slopcop ignore 98a2d745-fa33-467b-8289-2a9f3d05a0c8` to keep ignoring it\n" +
		"\n" +
		"Previously ignored, code changed (never fail the check)\n" +
		"  [note] acme.inheritance: acme.inheritance title\n" +
		"    Location: tests/test_other.py:9\n" +
		"    Certainty: definite\n" +
		"    ID: 1b4e28ba-2fa1-11d2-883f-0016d3cca427\n" +
		"    Matches ignored finding 6fa459ea-ee8a-3ca4-894e-db77e160355e (code changed); " +
		"`slopcop ignore 1b4e28ba-2fa1-11d2-883f-0016d3cca427` to keep ignoring it\n" +
		"\n" +
		"1 new finding across 1 file(s); 2 already in the code; 2 match(es) an earlier ignore | Analysis complete\n"
	if got := render(t, remapped(), FormatHuman); got != want {
		t.Errorf("human = %q\nwant    %q", got, want)
	}

	// Without an id to ignore it by, the match is still said.
	anonymous := finding("cli.py", 2, "warning", "acme.eval")
	anonymous.PreviouslyIgnored = "f5a9bad1-80ab-4a38-9351-4131e05d1682"
	got := Findings([]service.Finding{anonymous})
	if !strings.Contains(got, "    Matches ignored finding f5a9bad1-80ab-4a38-9351-4131e05d1682 (code changed)\n") ||
		strings.Contains(got, "slopcop ignore") {
		t.Errorf("findings = %q", got)
	}

	// A service from before the summary count still has its matches counted.
	older := remapped()
	older.Summary.RemappedIgnores = 0
	if got := render(t, older, FormatHuman); !strings.Contains(got, "; 2 match(es) an earlier ignore |") {
		t.Errorf("older = %q", got)
	}
	if got := render(t, mixed(), FormatHuman); strings.Contains(got, "earlier ignore") {
		t.Errorf("unmatched = %q", got)
	}
}

func TestMachineReportsCarryTheEarlierIgnore(t *testing.T) {
	var document struct {
		Summary  map[string]any   `json:"summary"`
		Findings []map[string]any `json:"findings"`
	}
	if err := json.Unmarshal([]byte(render(t, remapped(), FormatJSON)), &document); err != nil {
		t.Fatalf("json: %v", err)
	}
	if document.Summary["remappedIgnores"] != float64(2) {
		t.Errorf("json summary = %v", document.Summary)
	}
	if len(document.Findings) != 2 ||
		document.Findings[0]["previouslyIgnored"] != "f5a9bad1-80ab-4a38-9351-4131e05d1682" ||
		document.Findings[1]["previouslyIgnored"] != "6fa459ea-ee8a-3ca4-894e-db77e160355e" {
		t.Errorf("json findings = %v", document.Findings)
	}
	document.Findings = nil
	if err := json.Unmarshal([]byte(render(t, mixed(), FormatJSON)), &document); err != nil {
		t.Fatalf("json: %v", err)
	}
	if _, ok := document.Findings[0]["previouslyIgnored"]; ok {
		t.Errorf("json invented a match: %v", document.Findings[0])
	}

	var log struct {
		Runs []struct {
			Results    []map[string]any `json:"results"`
			Properties map[string]any   `json:"properties"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(render(t, remapped(), FormatSARIF)), &log); err != nil {
		t.Fatalf("sarif: %v", err)
	}
	run := log.Runs[0]
	if run.Properties["slopcop.remappedIgnores"] != float64(2) {
		t.Errorf("sarif run properties = %v", run.Properties)
	}
	if len(run.Results) != 2 {
		t.Fatalf("sarif results = %v", run.Results)
	}
	for index, want := range []struct{ id, previous, state string }{
		{"98a2d745-fa33-467b-8289-2a9f3d05a0c8", "f5a9bad1-80ab-4a38-9351-4131e05d1682", "new"},
		{"1b4e28ba-2fa1-11d2-883f-0016d3cca427", "6fa459ea-ee8a-3ca4-894e-db77e160355e", "unchanged"},
	} {
		result := run.Results[index]
		properties, _ := result["properties"].(map[string]any)
		if properties["slopcop.findingId"] != want.id || properties["slopcop.previouslyIgnored"] != want.previous ||
			result["baselineState"] != want.state {
			t.Errorf("sarif result %d = %v", index, result)
		}
	}
}

func TestEmptyChangeIsMarkedInMachineReports(t *testing.T) {
	r := response(service.StatusClean, "finding", nil)
	for _, format := range []string{FormatJSON, FormatSARIF} {
		marked := renderWith(t, r, format, Options{EmptyChange: true})
		if !strings.Contains(marked, "mptyChange\": true") {
			t.Errorf("%s report does not mark the empty change:\n%s", format, marked)
		}
		if strings.Contains(render(t, r, format), "mptyChange") {
			t.Errorf("%s report marks a non-empty change as empty", format)
		}
	}
}

// A run that wrote the skill says so in every machine report, the failed and
// the pending alike, so an agent that reads only stdout rereads it.
func TestSkillUpdateIsInEveryMachineReport(t *testing.T) {
	options := Options{SkillUpdated: []SkillUpdate{{
		Dir:     "/home/me/.claude/skills/slopcop",
		From:    "v0.0.9",
		To:      "v0.0.10",
		Changed: []string{"SKILL.md"},
		Backup:  "/home/me/.config/slopcop/skill-backups/claude-20261102T101500Z",
	}}}
	want := []any{map[string]any{
		"dir":     "/home/me/.claude/skills/slopcop",
		"from":    "v0.0.9",
		"to":      "v0.0.10",
		"changed": []any{"SKILL.md"},
		"backup":  "/home/me/.config/slopcop/skill-backups/claude-20261102T101500Z",
	}}
	writers := map[string]func(io.Writer, string, Options) error{
		"report": func(w io.Writer, format string, options Options) error {
			return Write(w, response(service.StatusClean, "finding", nil), format, options)
		},
		"pending": func(w io.Writer, format string, options Options) error {
			return WritePending(w, service.Pending{RetryAfter: 5}, format, options)
		},
		"failure": func(w io.Writer, format string, options Options) error {
			return WriteFailure(w, &service.Error{StatusCode: 409, Body: service.ErrorBody{Code: "repo_renamed"}}, format, options)
		},
	}
	for name, write := range writers {
		var out bytes.Buffer
		if err := write(&out, FormatJSON, options); err != nil {
			t.Fatalf("%s json: %v", name, err)
		}
		var document map[string]any
		if err := json.Unmarshal(out.Bytes(), &document); err != nil {
			t.Fatalf("%s json: %v", name, err)
		}
		if !reflect.DeepEqual(document["skillUpdated"], want) {
			t.Errorf("%s json skillUpdated = %v, want %v", name, document["skillUpdated"], want)
		}

		out.Reset()
		if err := write(&out, FormatSARIF, options); err != nil {
			t.Fatalf("%s sarif: %v", name, err)
		}
		var log map[string]any
		if err := json.Unmarshal(out.Bytes(), &log); err != nil {
			t.Fatalf("%s sarif: %v", name, err)
		}
		run := log["runs"].([]any)[0].(map[string]any)
		if got := run["properties"].(map[string]any)["slopcop.skillUpdated"]; !reflect.DeepEqual(got, want) {
			t.Errorf("%s sarif slopcop.skillUpdated = %v, want %v", name, got, want)
		}

		out.Reset()
		if err := write(&out, FormatJSON, Options{}); err != nil {
			t.Fatalf("%s json: %v", name, err)
		}
		if strings.Contains(out.String(), "skillUpdated") {
			t.Errorf("%s json names a skill update that did not happen: %s", name, out.String())
		}
	}
}

func TestCombinedReviewFormats(t *testing.T) {
	verdict := review.Verdict{Status: review.StatusFindings, Capture: review.Capture{BaseTree: "base", Tree: "tree"}, Reviewers: []review.Reviewer{{Name: "1-terra", Family: review.Terra, Model: "gpt-6.10-terra", Effort: "high", Status: review.StatusFindings, Text: "defect"}}}
	options := Options{Review: &verdict, Status: "findings", EmptyChange: false}
	for _, format := range []string{FormatHuman, FormatJSON, FormatSARIF} {
		var out bytes.Buffer
		if err := WriteAI(&out, format, options); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "defect") {
			t.Fatalf("%s missing review: %s", format, out.String())
		}
		if format == FormatJSON {
			var doc map[string]any
			if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
				t.Fatal(err)
			}
			if doc["status"] != "findings" {
				t.Fatal(doc)
			}
		}
		if format == FormatSARIF {
			var doc map[string]any
			if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
				t.Fatal(err)
			}
			if len(doc["runs"].([]any)) != 2 {
				t.Fatal(doc)
			}
		}
	}
}

func TestStaticJSONFieldOrderIsUnchanged(t *testing.T) {
	response := service.Response{Status: service.StatusClean, BaseCommit: "base"}
	var out bytes.Buffer
	if err := Write(&out, response, FormatJSON, Options{}); err != nil {
		t.Fatal(err)
	}
	want := `{\n  "status": "clean",\n  "approved": false,\n  "baseCommit": "base",\n  "summary":`
	if !strings.HasPrefix(out.String(), strings.ReplaceAll(want, `\n`, "\n")) {
		t.Fatalf("field order changed: %s", out.String())
	}
}
func TestReviewHumanKeepsLines(t *testing.T) {
	verdict := review.Verdict{Status: review.StatusFindings, Reviewers: []review.Reviewer{{Name: "1-terra", Status: review.StatusFindings, Text: "first\nsecond"}}}
	var out bytes.Buffer
	writeReviewHuman(&out, verdict)
	if !strings.Contains(out.String(), "    first\n    second\n") {
		t.Fatalf("review lines: %q", out.String())
	}
}

func TestReviewJSONHasNoUnavailableField(t *testing.T) {
	data, err := json.Marshal(reviewValue(&review.Verdict{Status: review.StatusFailed}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"unavailable"`) {
		t.Fatal(string(data))
	}
}

// cut is a check whose incomplete list the service cut: it counted 10 and
// named 4, most of them for one reason.
func cut() service.Response {
	return withIncomplete(
		response(service.StatusClean, "finding", []service.Finding{}),
		10,
		service.IncompletePolicy{PolicyID: "acme.a", Reason: "unreliable"},
		service.IncompletePolicy{PolicyID: "acme.b", Reason: "unreliable"},
		service.IncompletePolicy{PolicyID: "acme.c", Reason: "timed_out"},
		service.IncompletePolicy{PolicyID: "acme.d", Reason: "unreliable"},
	)
}

func TestHumanSaysASharedIncompleteReasonOnce(t *testing.T) {
	want := "Incomplete (3 of 4 named: unreliable): acme.a, acme.b, acme.c (timed_out), acme.d, and 6 more\n"
	if got := render(t, cut(), FormatHuman); !strings.HasSuffix(got, want) {
		t.Errorf("most = %q", got)
	}

	all := withIncomplete(
		response(service.StatusClean, "finding", []service.Finding{}),
		2,
		service.IncompletePolicy{PolicyID: "acme.x", Reason: "unsupported", Detail: "type=taint\x1b[2J"},
		service.IncompletePolicy{PolicyID: "acme.y", Reason: "unsupported", Detail: "type=taint\x1b[2J"},
	)
	want = "Incomplete (all 2 named: unsupported: type=taint�[2J): acme.x, acme.y\n"
	if got := render(t, all, FormatHuman); !strings.HasSuffix(got, want) {
		t.Errorf("all = %q", got)
	}

	// The same reason with another detail is another reason, and half is not
	// most.
	split := withIncomplete(
		response(service.StatusClean, "finding", []service.Finding{}),
		4,
		service.IncompletePolicy{PolicyID: "acme.a", Reason: "unsupported", Detail: "type=a"},
		service.IncompletePolicy{PolicyID: "acme.b", Reason: "unsupported", Detail: "type=b"},
		service.IncompletePolicy{PolicyID: "acme.c", Reason: "timed_out"},
		service.IncompletePolicy{PolicyID: "acme.d", Reason: "timed_out"},
	)
	want = "Incomplete: acme.a (unsupported: type=a), acme.b (unsupported: type=b), acme.c (timed_out), acme.d (timed_out)\n"
	if got := render(t, split, FormatHuman); !strings.HasSuffix(got, want) {
		t.Errorf("split = %q", got)
	}
}

func TestJSONSaysTheIncompleteListWasCut(t *testing.T) {
	var document map[string]any
	if err := json.Unmarshal([]byte(render(t, cut(), FormatJSON)), &document); err != nil {
		t.Fatalf("json: %v", err)
	}
	if document["incompleteUnlisted"] != float64(6) {
		t.Errorf("incompleteUnlisted = %v", document["incompleteUnlisted"])
	}
	wantReasons := []any{
		map[string]any{"reason": "unreliable", "policies": float64(3)},
		map[string]any{"reason": "timed_out", "policies": float64(1)},
	}
	if !reflect.DeepEqual(document["incompleteReasons"], wantReasons) {
		t.Errorf("incompleteReasons = %v", document["incompleteReasons"])
	}

	whole := withIncomplete(
		response(service.StatusClean, "finding", []service.Finding{}),
		1,
		service.IncompletePolicy{PolicyID: "acme.x", Reason: "unsupported", Detail: "type=taint"},
	)
	document = map[string]any{}
	if err := json.Unmarshal([]byte(render(t, whole, FormatJSON)), &document); err != nil {
		t.Fatalf("json: %v", err)
	}
	if _, ok := document["incompleteUnlisted"]; ok {
		t.Errorf("a whole list said it was cut: %v", document)
	}
	wantReasons = []any{map[string]any{"reason": "unsupported", "detail": "type=taint", "policies": float64(1)}}
	if !reflect.DeepEqual(document["incompleteReasons"], wantReasons) {
		t.Errorf("whole incompleteReasons = %v", document["incompleteReasons"])
	}
}

func TestSARIFSaysTheIncompleteListWasCut(t *testing.T) {
	var log map[string]any
	if err := json.Unmarshal([]byte(render(t, cut(), FormatSARIF)), &log); err != nil {
		t.Fatalf("sarif: %v", err)
	}
	run := log["runs"].([]any)[0].(map[string]any)
	notifications := run["invocations"].([]any)[0].(map[string]any)["toolExecutionNotifications"].([]any)
	want := map[string]any{
		"level":      "warning",
		"message":    map[string]any{"text": "6 more check(s) did not complete and are not named"},
		"properties": map[string]any{"slopcop.unlisted": float64(6)},
	}
	// The overall notice, the four named checks, then the unnamed ones.
	if len(notifications) != 6 || !reflect.DeepEqual(notifications[5], want) {
		t.Errorf("notifications = %v", notifications)
	}
	properties := run["properties"].(map[string]any)
	if properties["slopcop.incompleteUnlisted"] != float64(6) {
		t.Errorf("properties = %v", properties)
	}
	wantReasons := []any{
		map[string]any{"reason": "unreliable", "policies": float64(3)},
		map[string]any{"reason": "timed_out", "policies": float64(1)},
	}
	if !reflect.DeepEqual(properties["slopcop.incompleteReasons"], wantReasons) {
		t.Errorf("incompleteReasons = %v", properties["slopcop.incompleteReasons"])
	}
}

func TestEveryFormatCarriesAnInconclusiveDetailAndTheCut(t *testing.T) {
	detailed := unproven()
	detailed.Inconclusive[0].Detail = "type=failed reasons=source_read_failed"

	if got := renderWith(t, detailed, FormatHuman, Options{Verbose: true}); !strings.Contains(got, "  acme.a (type=failed reasons=source_read_failed)\n") {
		t.Errorf("verbose human = %q", got)
	}

	var document map[string]any
	if err := json.Unmarshal([]byte(render(t, detailed, FormatJSON)), &document); err != nil {
		t.Fatalf("json: %v", err)
	}
	if document["inconclusiveUnlisted"] != float64(3) {
		t.Errorf("inconclusiveUnlisted = %v", document["inconclusiveUnlisted"])
	}
	if first := document["inconclusive"].([]any)[0].(map[string]any); first["detail"] != "type=failed reasons=source_read_failed" {
		t.Errorf("inconclusive detail = %v", first)
	}

	var log map[string]any
	if err := json.Unmarshal([]byte(render(t, detailed, FormatSARIF)), &log); err != nil {
		t.Fatalf("sarif: %v", err)
	}
	run := log["runs"].([]any)[0].(map[string]any)
	first := run["invocations"].([]any)[0].(map[string]any)["toolExecutionNotifications"].([]any)[0].(map[string]any)
	if first["properties"].(map[string]any)["slopcop.detail"] != "type=failed reasons=source_read_failed" {
		t.Errorf("sarif note = %v", first)
	}
	if run["properties"].(map[string]any)["slopcop.inconclusiveUnlisted"] != float64(3) {
		t.Errorf("properties = %v", run["properties"])
	}
}

// crashed is a check whose analyzer run failed: the service counted 207
// incomplete checks, named 3, and named the run's cause once.
func crashed() service.Response {
	const detail = "type=incomplete reasons=analyzer_process_failed exit_code=137"
	r := withIncomplete(
		response(service.StatusClean, "finding", []service.Finding{}),
		207,
		service.IncompletePolicy{PolicyID: "acme.a", Reason: "unreliable", Detail: detail, Scope: service.ScopeRun},
		service.IncompletePolicy{PolicyID: "acme.b", Reason: "unreliable", Detail: detail, Scope: service.ScopeRun},
		service.IncompletePolicy{PolicyID: "acme.c", Reason: "timed_out"},
	)
	r.RunFailures = []service.RunFailure{{Reason: "unreliable", Detail: detail, Policies: 206}}
	return r
}

func TestHumanSaysEachRunFailureOnce(t *testing.T) {
	want := "| Analysis incomplete: 207 check(s) did not complete\n" +
		"Analyzer run failed (unreliable: type=incomplete reasons=analyzer_process_failed exit_code=137): 206 check(s) did not complete\n" +
		"Incomplete: acme.a, acme.b, acme.c (timed_out), and 204 more\n"
	if got := render(t, crashed(), FormatHuman); !strings.HasSuffix(got, want) {
		t.Errorf("human = %q, want suffix %q", got, want)
	}

	hostile := crashed()
	hostile.RunFailures[0].Detail = "evil\x1b[2J"
	if got := render(t, hostile, FormatHuman); !strings.Contains(got, "Analyzer run failed (unreliable: evil�[2J)") {
		t.Errorf("hostile = %q", got)
	}
}

func TestJSONPassesRunFailuresThrough(t *testing.T) {
	var document map[string]any
	if err := json.Unmarshal([]byte(render(t, crashed(), FormatJSON)), &document); err != nil {
		t.Fatalf("json: %v", err)
	}
	want := []any{map[string]any{
		"reason":   "unreliable",
		"detail":   "type=incomplete reasons=analyzer_process_failed exit_code=137",
		"policies": float64(206),
	}}
	if !reflect.DeepEqual(document["runFailures"], want) {
		t.Errorf("runFailures = %v", document["runFailures"])
	}
	if first := document["incomplete"].([]any)[0].(map[string]any); first["scope"] != "run" {
		t.Errorf("incomplete scope = %v", first)
	}
	// A service from before run failures renders as before.
	document = map[string]any{}
	if err := json.Unmarshal([]byte(render(t, cut(), FormatJSON)), &document); err != nil {
		t.Fatalf("json: %v", err)
	}
	if _, ok := document["runFailures"]; ok {
		t.Errorf("older response = %v", document)
	}
	if first := document["incomplete"].([]any)[0].(map[string]any); first["scope"] != nil {
		t.Errorf("older scope = %v", first)
	}
}

func TestSARIFNotesEachRunFailure(t *testing.T) {
	var log map[string]any
	if err := json.Unmarshal([]byte(render(t, crashed(), FormatSARIF)), &log); err != nil {
		t.Fatalf("sarif: %v", err)
	}
	run := log["runs"].([]any)[0].(map[string]any)
	notifications := run["invocations"].([]any)[0].(map[string]any)["toolExecutionNotifications"].([]any)
	want := map[string]any{
		"level": "warning",
		"message": map[string]any{"text": "Analyzer run failed (unreliable: type=incomplete " +
			"reasons=analyzer_process_failed exit_code=137): 206 check(s) did not complete"},
		"properties": map[string]any{
			"slopcop.runFailure": true,
			"slopcop.reason":     "unreliable",
			"slopcop.detail":     "type=incomplete reasons=analyzer_process_failed exit_code=137",
			"slopcop.policies":   float64(206),
		},
	}
	// The overall notice, then the run failure, then each named check.
	if len(notifications) < 3 || !reflect.DeepEqual(notifications[1], want) {
		t.Fatalf("notifications = %v", notifications)
	}
	if scope := notifications[2].(map[string]any)["properties"].(map[string]any)["slopcop.scope"]; scope != "run" {
		t.Errorf("named check scope = %v", scope)
	}
	if failures, ok := run["properties"].(map[string]any)["slopcop.runFailures"].([]any); !ok || len(failures) != 1 {
		t.Errorf("properties = %v", run["properties"])
	}
}

// inlineIgnored is a clean check where inline `slopcop: ignore` comments
// accepted two findings, one past the listed bound, and two comments ignore
// nothing (BrokkAi/bifrost-service#1089).
func inlineIgnored() service.Response {
	r := response("clean", "warning", nil)
	reason := "the input is a constant"
	r.InlineSuppressed = []service.InlineSuppressedFinding{{
		PolicyID:  "bifrost.security.sql-injection",
		Severity:  "error",
		Certainty: "definite",
		Path:      "app.py",
		StartLine: line(12),
		EndLine:   line(12),
		Title:     "Query built from input",
		New:       true,
		Directive: service.InlineDirective{
			Line:     line(11),
			Policies: []string{"bifrost.security.sql-injection"},
			Reason:   &reason,
		},
	}}
	r.DirectiveWarnings = []service.DirectiveWarning{
		{Kind: service.DirectiveMissingReason, Path: "app.py", Line: line(20), Policies: []string{}, New: true},
		{Kind: service.DirectiveUnused, Path: "lib.py", Line: line(3), Policies: []string{"p.wrong"}},
	}
	r.Summary.InlineSuppressed = 2
	r.Summary.DirectiveWarnings = 2
	return r
}

func TestHumanListsInlineIgnoresAndCommentsThatIgnoreNothing(t *testing.T) {
	got := render(t, inlineIgnored(), FormatHuman)
	for _, want := range []string{
		"Ignored inline by slopcop: ignore (never fail the check)\n" +
			"  [error] bifrost.security.sql-injection: Query built from input\n" +
			"    Location: app.py:12\n" +
			"    Ignored by: comment for bifrost.security.sql-injection on line 11 -- the input is a constant\n" +
			"    In this change\n",
		"  and 1 more\n",
		"slopcop: ignore comments that ignore nothing (never fail the check)\n" +
			"  app.py:20: a blanket ignore needs a reason after `--`; it ignores nothing until it has one\n" +
			"  lib.py:3: names p.wrong, which matches no finding;",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("human report lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(render(t, response("clean", "warning", nil), FormatHuman), "slopcop: ignore") {
		t.Fatal("a check without comments mentions them")
	}
}

func TestHumanKeepsHostileReasonsOffTheTerminal(t *testing.T) {
	r := inlineIgnored()
	reason := "fine\x1b[2J\u202e"
	r.InlineSuppressed[0].Directive.Reason = &reason
	got := render(t, r, FormatHuman)
	if strings.ContainsAny(got, "\x1b\u202e") {
		t.Fatalf("control characters reached the terminal: %q", got)
	}
}

func TestJSONCarriesInlineIgnoresAsSent(t *testing.T) {
	var got map[string]any
	if err := json.Unmarshal([]byte(render(t, inlineIgnored(), FormatJSON)), &got); err != nil {
		t.Fatal(err)
	}
	accepted := got["inlineSuppressed"].([]any)[0].(map[string]any)
	directive := accepted["directive"].(map[string]any)
	if directive["line"] != float64(11) || directive["reason"] != "the input is a constant" {
		t.Fatalf("directive = %v", directive)
	}
	if kind := got["directiveWarnings"].([]any)[0].(map[string]any)["kind"]; kind != "missing_reason" {
		t.Fatalf("first warning kind = %v", kind)
	}
	summary := got["summary"].(map[string]any)
	if summary["inlineSuppressed"] != float64(2) || summary["directiveWarnings"] != float64(2) {
		t.Fatalf("summary = %v", summary)
	}
	var clean map[string]any
	if err := json.Unmarshal([]byte(render(t, response("clean", "warning", nil), FormatJSON)), &clean); err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"inlineSuppressed", "directiveWarnings"} {
		if _, ok := clean[absent]; ok {
			t.Fatalf("a check without comments carries %s", absent)
		}
	}
}

func TestSARIFSuppressesInlineIgnoresInSource(t *testing.T) {
	var log struct {
		Runs []struct {
			Results []struct {
				RuleID        string `json:"ruleId"`
				BaselineState string `json:"baselineState"`
				Suppressions  []struct {
					Kind          string `json:"kind"`
					Justification string `json:"justification"`
				} `json:"suppressions"`
			} `json:"results"`
			Invocations []struct {
				ExecutionSuccessful bool `json:"executionSuccessful"`
				Notifications       []struct {
					Level   string `json:"level"`
					Message struct {
						Text string `json:"text"`
					} `json:"message"`
				} `json:"toolExecutionNotifications"`
			} `json:"invocations"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(render(t, inlineIgnored(), FormatSARIF)), &log); err != nil {
		t.Fatal(err)
	}
	run := log.Runs[0]
	if len(run.Results) != 1 {
		t.Fatalf("results = %+v", run.Results)
	}
	result := run.Results[0]
	if result.RuleID != "bifrost.security.sql-injection" || result.BaselineState != "new" ||
		len(result.Suppressions) != 1 || result.Suppressions[0].Kind != "inSource" ||
		result.Suppressions[0].Justification != "the input is a constant" {
		t.Fatalf("result = %+v", result)
	}
	if !run.Invocations[0].ExecutionSuccessful {
		t.Fatal("comments that ignore nothing failed the execution")
	}
	warnings := 0
	for _, notification := range run.Invocations[0].Notifications {
		if notification.Level == "warning" && strings.Contains(notification.Message.Text, "slopcop: ignore comment") {
			warnings++
		}
	}
	if warnings != 2 {
		t.Fatalf("directive warnings = %d, want 2", warnings)
	}
}

func TestInlineIgnoresNeverChangeTheExitCode(t *testing.T) {
	if code := inlineIgnored().ExitCode(); code != service.ExitClean {
		t.Fatalf("exit code = %d", code)
	}
}

func excluded(only bool) service.Response {
	r := response(service.StatusClean, "finding", []service.Finding{})
	r.Summary.ExcludedFiles = 1
	r.Summary.ExcludedPairs = 2
	r.Summary.OnlyExcluded = only
	r.Exclusions = &service.Exclusions{
		Config: ".slopcop/config.toml",
		Scopes: []service.ExclusionScope{
			{Paths: []string{"vendor/**"}, AllRules: true, Files: []string{"vendor/a.js"}, FileCount: 1},
			{
				Paths:        []string{"fixtures/**"},
				RulePatterns: []string{"bifrost.java.*", "bifrost.renamed.rule"},
				Rules:        []string{"bifrost.java.x"},
				RuleCount:    1,
				Reason:       "Pinned\x1b fixtures",
				Files:        []string{"fixtures/A.java", "fixtures/B.java"},
				FileCount:    3,
				Pairs:        3,
				NotRun:       []string{"bifrost.java.x"},
			},
		},
		Warnings:      []service.ExclusionWarning{{Kind: "unmatched_rule", Rule: "bifrost.renamed.rule"}},
		ExcludedFiles: 1,
		ExcludedPairs: 2,
		OnlyExcluded:  only,
	}
	return r
}

func TestHumanListsExclusionsAndNeverCallsAnExcludedChangeClean(t *testing.T) {
	got := render(t, excluded(true), FormatHuman)
	want := "Nothing checked: every changed file is excluded by .slopcop/config.toml | Analysis complete\n" +
		"Excluded by .slopcop/config.toml: 1 changed file(s) for every rule, 2 (file, rule) pair(s) for named rules\n" +
		"  vendor/** for every rule: 1 changed file(s)\n" +
		"  fixtures/** for bifrost.java.*, bifrost.renamed.rule: 3 changed file(s) (Pinned\uFFFD fixtures)\n" +
		"Warning: [check] rule bifrost.renamed.rule matches no selected policy, so it excludes nothing\n" +
		"  --verbose lists the files and rules each one resolved to.\n"
	if got != want {
		t.Errorf("only excluded = %q", got)
	}
	got = renderWith(t, excluded(false), FormatHuman, Options{Verbose: true})
	for _, want := range []string{
		"No new findings | Analysis complete\n",
		"  vendor/** for every rule: 1 changed file(s)\n    Files: vendor/a.js\n",
		"    Rules: bifrost.java.x\n",
		"    Not run (excluded on every changed file): bifrost.java.x\n",
		"    Files: fixtures/A.java, fixtures/B.java and 1 more\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("verbose lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "--verbose lists") {
		t.Errorf("verbose still points at --verbose:\n%s", got)
	}
	if clean := render(t, response(service.StatusClean, "finding", []service.Finding{}), FormatHuman); strings.Contains(clean, "Excluded") {
		t.Errorf("a tree without [check] says nothing of it: %q", clean)
	}
}

func TestJSONAndSARIFCarryTheExclusions(t *testing.T) {
	var document map[string]any
	if err := json.Unmarshal([]byte(render(t, excluded(true), FormatJSON)), &document); err != nil {
		t.Fatal(err)
	}
	summary := document["summary"].(map[string]any)
	if summary["onlyExcluded"] != true || summary["excludedFiles"] != float64(1) || summary["excludedPairs"] != float64(2) {
		t.Errorf("summary = %v", summary)
	}
	exclusions := document["exclusions"].(map[string]any)
	if scopes := exclusions["scopes"].([]any); len(scopes) != 2 || scopes[1].(map[string]any)["reason"] != "Pinned\x1b fixtures" {
		t.Errorf("exclusions = %v", exclusions)
	}

	var log map[string]any
	if err := json.Unmarshal([]byte(render(t, excluded(true), FormatSARIF)), &log); err != nil {
		t.Fatal(err)
	}
	run := log["runs"].([]any)[0].(map[string]any)
	properties := run["properties"].(map[string]any)
	if properties["slopcop.onlyExcluded"] != true || properties["slopcop.exclusions"] == nil {
		t.Errorf("properties = %v", properties)
	}
	notifications := run["invocations"].([]any)[0].(map[string]any)["toolExecutionNotifications"].([]any)
	var texts []string
	for _, notification := range notifications {
		texts = append(texts, notification.(map[string]any)["message"].(map[string]any)["text"].(string))
	}
	joined := strings.Join(texts, "\n")
	for _, want := range []string{
		"[check] rule bifrost.renamed.rule matches no selected policy",
		"Nothing checked: every changed file is excluded by .slopcop/config.toml",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("notifications lack %q: %s", want, joined)
		}
	}

	var plain map[string]any
	if err := json.Unmarshal([]byte(render(t, response(service.StatusClean, "finding", []service.Finding{}), FormatJSON)), &plain); err != nil {
		t.Fatal(err)
	}
	if _, ok := plain["exclusions"]; ok {
		t.Errorf("a tree without [check] carries no exclusions: %v", plain)
	}
}

func TestSimilarityCandidatesAndReadinessNotices(t *testing.T) {
	match := finding("new.go", 3, "info", "slopcop.similarity")
	match.Certainty = "heuristic"
	match.RelatedLocations = []service.RelatedLocation{{
		Path: "old file.go", StartLine: 8, EndLine: 10, Message: "P(combine) = 0.87",
	}}
	r := response(service.StatusClean, "finding", []service.Finding{match})
	r.Checks = []service.CheckNotice{{PolicyID: "slopcop.similarity", Status: "future_status", Message: "Readiness detail."}}

	humanReport := render(t, r, FormatHuman)
	for _, want := range []string{"Candidate: old file.go:8 — P(combine) = 0.87", "Note: slopcop.similarity (future_status): Readiness detail."} {
		if !strings.Contains(humanReport, want) {
			t.Errorf("human report missing %q: %s", want, humanReport)
		}
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(render(t, r, FormatJSON)), &document); err != nil {
		t.Fatal(err)
	}
	findings := document["findings"].([]any)
	if len(findings) != 1 || findings[0].(map[string]any)["relatedLocations"].([]any)[0].(map[string]any)["message"] != "P(combine) = 0.87" {
		t.Fatalf("JSON omitted similarity candidate: %v", document)
	}
	checks := document["checks"].([]any)
	if len(checks) != 1 || checks[0].(map[string]any)["status"] != "future_status" {
		t.Fatalf("JSON omitted notice: %v", document)
	}

	if err := json.Unmarshal([]byte(render(t, r, FormatSARIF)), &document); err != nil {
		t.Fatal(err)
	}
	run := document["runs"].([]any)[0].(map[string]any)
	result := run["results"].([]any)[0].(map[string]any)
	if result["level"] != "note" || result["properties"].(map[string]any)["slopcop.severity"] != "info" {
		t.Fatalf("SARIF did not preserve info severity: %v", result)
	}
	related := result["relatedLocations"].([]any)[0].(map[string]any)
	physical := related["physicalLocation"].(map[string]any)
	if physical["artifactLocation"].(map[string]any)["uri"] != "old%20file.go" || related["message"].(map[string]any)["text"] != "P(combine) = 0.87" {
		t.Fatalf("SARIF candidate was malformed: %v", related)
	}
	notifications := run["invocations"].([]any)[0].(map[string]any)["toolExecutionNotifications"].([]any)
	foundNotice := false
	for _, value := range notifications {
		notice := value.(map[string]any)
		if notice["properties"].(map[string]any)["slopcop.checkStatus"] == "future_status" && notice["level"] == "note" {
			foundNotice = true
		}
	}
	if !foundNotice {
		t.Fatalf("SARIF notice missing: %v", notifications)
	}
}

func TestInfoOnlySummaryIsAdvisory(t *testing.T) {
	r := response(service.StatusClean, "finding", []service.Finding{finding("new.go", 3, "info", "slopcop.similarity")})
	r.Findings[0].Title = "Similar existing function"
	r.Findings[0].RelatedLocations = []service.RelatedLocation{{Path: "old.go", StartLine: 8, Message: "matching function"}}
	got := summaryLine(r)
	if strings.Contains(got, "1 new finding") || !strings.Contains(got, "No new findings; 1 note: Similar existing function") {
		t.Fatalf("summary = %q", got)
	}
	got = render(t, r, FormatHuman)
	if !strings.Contains(got, "Notes\n  [info] slopcop.similarity: Similar existing function\n") ||
		!strings.Contains(got, "Candidate: old.go:8 — matching function\n") || strings.Contains(got, "New findings\n") {
		t.Fatalf("human = %q", got)
	}
}

func TestPartialSimilarityNoticeRendersInAllFormats(t *testing.T) {
	r := response(service.StatusClean, "finding", nil)
	r.Checks = []service.CheckNotice{{PolicyID: "slopcop.similarity", Status: "partial", Message: "Compared 8 of 10 changed functions."}}
	if got := render(t, r, FormatHuman); !strings.Contains(got, "Note: slopcop.similarity (partial): Compared 8 of 10 changed functions.") || strings.Contains(got, "incomplete") {
		t.Fatalf("human = %q", got)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(render(t, r, FormatJSON)), &document); err != nil {
		t.Fatal(err)
	}
	checks := document["checks"].([]any)
	if checks[0].(map[string]any)["status"] != "partial" || document["status"] != service.StatusClean {
		t.Fatalf("JSON = %v", document)
	}
	if err := json.Unmarshal([]byte(render(t, r, FormatSARIF)), &document); err != nil {
		t.Fatal(err)
	}
	runs := document["runs"].([]any)
	notifications := runs[0].(map[string]any)["invocations"].([]any)[0].(map[string]any)["toolExecutionNotifications"].([]any)
	notice := notifications[0].(map[string]any)
	if notice["level"] != "note" || notice["message"].(map[string]any)["text"] != "Compared 8 of 10 changed functions." || notice["properties"].(map[string]any)["slopcop.checkStatus"] != "partial" {
		t.Fatalf("SARIF notice = %v", notice)
	}
}
