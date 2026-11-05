package report

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"sort"
	"strings"
	"testing"

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
	return []service.Finding{
		finding("cli.py", 2, "warning", "bifrost.correctness.dynamic-evaluation"),
		finding("cli.py", 5, "warning", "bifrost.correctness.dynamic-evaluation"),
		finding("new file.py", 1, "note", "bifrost.maintainability.complexity"),
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
		"  [note] bifrost.maintainability.complexity: bifrost.maintainability.complexity title\n" +
		"    Location: new file.py:1\n" +
		"    Certainty: definite\n" +
		"\n" +
		"3 new findings across 2 file(s) | Analysis complete\n"
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
	want := "\n3 new findings across 2 file(s); +45 not reported | Analysis complete\n" +
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
	if want := []string{"baseCommit", "findings", "incomplete", "inconclusive", "status", "summary"}; !reflect.DeepEqual(keys, want) {
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
		"severity":  "note",
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
