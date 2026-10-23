package report

import (
	"bytes"
	"encoding/json"
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
	fresh, dismissedCount := 0, 0
	for _, finding := range findings {
		switch {
		case finding.Dismissed:
			dismissedCount++
		case finding.New:
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
			ExistingFindings:    len(findings) - fresh - dismissedCount,
			DismissedFindings:   dismissedCount,
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
		"1 new finding across 1 file(s); 3 already in the code (--show-existing lists them) | Analysis complete\n"
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
	if got := render(t, onlyExisting, FormatHuman); got != "No new findings; 1 already in the code (--show-existing lists them) | Analysis complete\n" {
		t.Errorf("only existing = %q", got)
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
		service.IncompletePolicy{PolicyID: "z.last", Reason: "other"},
	)
	got = render(t, partial, FormatHuman)
	want := "| Analysis incomplete: 3 check(s) did not complete\n" +
		"Incomplete: a.first (timed_out), evil�[2J (base_comparison_failed), z.last (other)\n"
	if !strings.HasSuffix(got, want) {
		t.Errorf("incomplete = %q", got)
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
	notice := "45 more finding(s) were not reported because a check found more than it " +
		"may report; the most severe are listed"

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
	if want := []string{"baseCommit", "checkId", "findings", "incomplete", "inconclusiveElsewhere", "status", "summary"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
	wantSummary := map[string]any{
		"files":                 float64(3),
		"findings":              float64(3),
		"newFindings":           float64(3),
		"existingFindings":      float64(0),
		"dismissedFindings":     float64(0),
		"incompletePolicies":    float64(0),
		"incompletePolicyIds":   []any{},
		"inconclusiveElsewhere": float64(0),
		"omittedFindings":       float64(0),
		"failOn":                "error",
	}
	if !reflect.DeepEqual(document["summary"], wantSummary) {
		t.Errorf("summary = %v", document["summary"])
	}
	// A service from before findings had ids sends none, so the id is empty.
	wantFinding := map[string]any{
		"id":        "",
		"policyId":  "bifrost.maintainability.complexity",
		"severity":  "note",
		"certainty": "definite",
		"path":      "new file.py",
		"startLine": float64(1),
		"endLine":   float64(1),
		"title":     "bifrost.maintainability.complexity title",
		"new":       true,
		"dismissed": false,
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
		if err := WritePending(&out, pending, format); err != nil {
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
	if err := WritePending(&bytes.Buffer{}, pending, "xml"); err == nil {
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

func TestInconclusiveChecksOutsideTheChangeAreListedNotFailed(t *testing.T) {
	elsewhere := response(service.StatusClean, "finding", []service.Finding{})
	elsewhere.Summary.InconclusiveElsewhere = 3
	elsewhere.InconclusiveElsewhere = []service.IncompletePolicy{
		{PolicyID: "acme.flow", Reason: "inconclusive"},
		{PolicyID: "evil\x1b[2J", Reason: "inconclusive"},
	}
	got := render(t, elsewhere, FormatHuman)
	want := "No new findings | Analysis complete\n" +
		"Inconclusive outside the change (never fail the check): acme.flow, evil�[2J, and 1 more\n"
	if got != want {
		t.Errorf("human = %q", got)
	}

	var document map[string]any
	if err := json.Unmarshal([]byte(render(t, elsewhere, FormatJSON)), &document); err != nil {
		t.Fatalf("json: %v", err)
	}
	if document["summary"].(map[string]any)["inconclusiveElsewhere"] != float64(3) ||
		len(document["inconclusiveElsewhere"].([]any)) != 2 {
		t.Errorf("json = %v", document)
	}

	var log map[string]any
	if err := json.Unmarshal([]byte(render(t, elsewhere, FormatSARIF)), &log); err != nil {
		t.Fatalf("sarif: %v", err)
	}
	run := log["runs"].([]any)[0].(map[string]any)
	invocation := run["invocations"].([]any)[0].(map[string]any)
	notifications := invocation["toolExecutionNotifications"].([]any)
	first := notifications[0].(map[string]any)
	if invocation["executionSuccessful"] != true || len(notifications) != 2 || first["level"] != "note" ||
		first["properties"].(map[string]any)["slopcop.outsideChange"] != true {
		t.Errorf("invocation = %v", invocation)
	}
	if run["properties"].(map[string]any)["slopcop.inconclusiveElsewhere"] != float64(3) {
		t.Errorf("properties = %v", run["properties"])
	}
}

func TestUnknownFormat(t *testing.T) {
	if err := Write(&bytes.Buffer{}, response(service.StatusClean, "finding", nil), "xml", Options{}); err == nil {
		t.Fatal("expected an error for an unknown format")
	}
}

const (
	checkID     = "2b3c4d5e-6f70-4812-9a3b-4c5d6e7f8091"
	freshID     = "6f1c2a9e-3b4d-4e5f-8a7b-0c1d2e3f4a5b"
	existingID  = "9e8d7c6b-5a49-4382-a716-151413121110"
	xssID       = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	unwrapID    = "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"
	hintLine    = `False positive? slopcop report <id> --reason "why"` + "\n"
	dismissedAs = "reported as false positive"
)

func identified(found service.Finding, id string) service.Finding {
	found.ID = id
	return found
}

// dismissed is a finding reported as a false positive, as the service sends
// it: not new, and dismissed.
func dismissed(path string, at int, severity, policy, id string) service.Finding {
	found := identified(existing(path, at, severity, policy), id)
	found.Dismissed = true
	return found
}

// reported is a check from a service that names checks and findings: one new
// finding, one already in the code, and two reported as false positives.
func reported() service.Response {
	r := response(service.StatusFindings, "finding", []service.Finding{
		dismissed("server/domain.rs", 40, "warning", "bifrost.security.xss", xssID),
		identified(finding("server/domain.rs", 703, "warning", "bifrost.performance.regex-compile-in-loop"), freshID),
		identified(existing("server/main.rs", 4, "note", "acme.length"), existingID),
		dismissed("server/main.rs", 12, "error", "acme.unwrap", unwrapID),
	})
	r.CheckID = checkID
	return r
}

func TestHumanShowsIDsAndHowToReport(t *testing.T) {
	newBlock := "New findings\n" +
		"  [warning] bifrost.performance.regex-compile-in-loop: bifrost.performance.regex-compile-in-loop title\n" +
		"    Location: server/domain.rs:703\n" +
		"    Certainty: definite\n" +
		"    Id: " + freshID + "\n" +
		"\n" +
		hintLine +
		"\n"
	cases := []struct {
		name    string
		options Options
		want    string
	}{
		{
			name: "default",
			want: newBlock +
				"1 new finding across 1 file(s); 1 already in the code; 2 reported as false positive (--show-existing lists them) | Analysis complete\n",
		},
		{
			name:    "show existing",
			options: Options{ShowExisting: true},
			want: newBlock +
				"Already in the code (never fail the check)\n" +
				"  [note] acme.length: acme.length title\n" +
				"    Location: server/main.rs:4\n" +
				"    Certainty: definite\n" +
				"    Id: " + existingID + "\n" +
				"\n" +
				"Reported as false positive (never fail the check)\n" +
				"  [warning] bifrost.security.xss: bifrost.security.xss title\n" +
				"    Location: server/domain.rs:40\n" +
				"    Certainty: definite\n" +
				"    Id: " + xssID + "\n" +
				"\n" +
				"  [error] acme.unwrap: acme.unwrap title\n" +
				"    Location: server/main.rs:12\n" +
				"    Certainty: definite\n" +
				"    Id: " + unwrapID + "\n" +
				"\n" +
				"1 new finding across 1 file(s); 1 already in the code; 2 reported as false positive | Analysis complete\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderWith(t, reported(), FormatHuman, tc.options); got != tc.want {
				t.Errorf("human =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

func TestHumanSummaryCountsDismissedFindings(t *testing.T) {
	cases := []struct {
		name     string
		findings []service.Finding
		options  Options
		want     string
	}{
		{
			name:     "only dismissed",
			findings: []service.Finding{dismissed("a.go", 1, "error", "p", xssID)},
			want:     "No new findings; 1 reported as false positive (--show-existing lists them) | Analysis complete\n",
		},
		{
			name: "existing and dismissed",
			findings: []service.Finding{
				identified(existing("a.go", 1, "error", "p"), existingID),
				dismissed("a.go", 2, "error", "q", xssID),
				dismissed("a.go", 3, "error", "q", unwrapID),
			},
			want: "No new findings; 1 already in the code; 2 reported as false positive (--show-existing lists them) | Analysis complete\n",
		},
		{
			name: "only existing, as before dismissals",
			findings: []service.Finding{
				identified(existing("a.go", 1, "error", "p"), existingID),
			},
			want: "No new findings; 1 already in the code (--show-existing lists them) | Analysis complete\n",
		},
		{
			name:     "dismissed and listed",
			findings: []service.Finding{dismissed("a.go", 1, "error", "p", xssID)},
			options:  Options{ShowExisting: true},
			want: "Reported as false positive (never fail the check)\n" +
				"  [error] p: p title\n" +
				"    Location: a.go:1\n" +
				"    Certainty: definite\n" +
				"    Id: " + xssID + "\n" +
				"\n" +
				"No new findings; 1 reported as false positive | Analysis complete\n",
		},
		{
			// A dismissed finding is never new, even if a service says so.
			name: "dismissed and marked new",
			findings: []service.Finding{func() service.Finding {
				found := dismissed("a.go", 1, "error", "p", xssID)
				found.New = true
				return found
			}()},
			want: "No new findings; 1 reported as false positive (--show-existing lists them) | Analysis complete\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderWith(t, response(service.StatusClean, "finding", tc.findings), FormatHuman, tc.options)
			if got != tc.want {
				t.Errorf("human = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHumanHintNeedsANewFindingWithAnID(t *testing.T) {
	cases := []struct {
		name     string
		findings []service.Finding
		options  Options
		want     bool
	}{
		{"a new finding with an id", []service.Finding{identified(finding("a.go", 1, "warning", "p"), freshID)}, Options{}, true},
		{"a service from before ids", []service.Finding{finding("a.go", 1, "warning", "p")}, Options{}, false},
		{"no new findings", []service.Finding{identified(existing("a.go", 1, "warning", "p"), existingID)}, Options{ShowExisting: true}, false},
		{"only dismissed", []service.Finding{dismissed("a.go", 1, "warning", "p", xssID)}, Options{ShowExisting: true}, false},
		{"clean", []service.Finding{}, Options{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderWith(t, response(service.StatusFindings, "finding", tc.findings), FormatHuman, tc.options)
			if strings.Contains(got, "False positive?") != tc.want {
				t.Errorf("hint shown = %v, want %v:\n%s", !tc.want, tc.want, got)
			}
			if tc.want && strings.Count(got, hintLine) != 1 {
				t.Errorf("want exactly one hint line:\n%s", got)
			}
		})
	}
}

func TestJSONCarriesIDsAndDismissedFindings(t *testing.T) {
	cases := []struct {
		name    string
		options Options
		want    [][2]any // id and dismissed of each listed finding, in order
	}{
		{"default", Options{}, [][2]any{{freshID, false}}},
		{"show existing", Options{ShowExisting: true}, [][2]any{
			{freshID, false}, {existingID, false}, {xssID, true}, {unwrapID, true},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var document map[string]any
			if err := json.Unmarshal([]byte(renderWith(t, reported(), FormatJSON, tc.options)), &document); err != nil {
				t.Fatalf("json: %v", err)
			}
			if document["checkId"] != checkID {
				t.Errorf("checkId = %v", document["checkId"])
			}
			summary := document["summary"].(map[string]any)
			if summary["findings"] != float64(4) || summary["newFindings"] != float64(1) ||
				summary["existingFindings"] != float64(1) || summary["dismissedFindings"] != float64(2) {
				t.Errorf("the counts are always present: %v", summary)
			}
			var got [][2]any
			for _, raw := range document["findings"].([]any) {
				found := raw.(map[string]any)
				got = append(got, [2]any{found["id"], found["dismissed"]})
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("findings = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSARIFCarriesIDsAndSuppressesDismissedFindings(t *testing.T) {
	type result struct {
		findingID   any
		state       any
		suppression any
	}
	suppressed := []any{map[string]any{"kind": "external", "justification": dismissedAs}}
	cases := []struct {
		name        string
		response    service.Response
		options     Options
		wantCheckID any
		want        []result
	}{
		{
			name:        "default",
			response:    reported(),
			wantCheckID: checkID,
			want:        []result{{freshID, "new", nil}},
		},
		{
			name:        "show existing",
			response:    reported(),
			options:     Options{ShowExisting: true},
			wantCheckID: checkID,
			want: []result{
				{freshID, "new", nil},
				{existingID, "unchanged", nil},
				{xssID, "new", suppressed},
				{unwrapID, "new", suppressed},
			},
		},
		{
			name:     "a service from before ids",
			response: mixed(),
			options:  Options{ShowExisting: true},
			want: []result{
				{nil, "new", nil}, {nil, "unchanged", nil}, {nil, "unchanged", nil}, {nil, "unchanged", nil},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var log map[string]any
			if err := json.Unmarshal([]byte(renderWith(t, tc.response, FormatSARIF, tc.options)), &log); err != nil {
				t.Fatalf("sarif: %v", err)
			}
			run := log["runs"].([]any)[0].(map[string]any)
			properties := run["properties"].(map[string]any)
			if properties["slopcop.checkId"] != tc.wantCheckID {
				t.Errorf("run checkId = %v, want %v", properties["slopcop.checkId"], tc.wantCheckID)
			}
			if properties["slopcop.dismissedFindings"] != float64(tc.response.Summary.DismissedFindings) {
				t.Errorf("run properties = %v", properties)
			}
			var got []result
			for _, raw := range run["results"].([]any) {
				entry := raw.(map[string]any)
				var id any
				if bag, ok := entry["properties"].(map[string]any); ok {
					id = bag["slopcop.findingId"]
				}
				got = append(got, result{id, entry["baselineState"], entry["suppressions"]})
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("results = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFalsePositiveSaysWhatTheReportDid(t *testing.T) {
	answer := func(status, policy string) service.ReportResponse {
		return service.ReportResponse{
			ReportID: "11111111-2222-4333-8444-555555555555", FindingID: freshID, CheckID: checkID,
			PolicyID: policy, Kind: service.ReportFalsePositive, Status: status,
		}
	}
	cases := []struct {
		name     string
		response service.ReportResponse
		want     string
	}{
		{
			name:     "recorded",
			response: answer(service.ReportRecorded, "bifrost.security.xss"),
			want: "slopcop: reported finding " + freshID + " (bifrost.security.xss) as a false positive\n" +
				"It no longer fails checks of this repository in the same workspace (or with the same operator token), and the team sees the report and its reason on the SlopCop dashboard.\n",
		},
		{
			name:     "already reported",
			response: answer(service.ReportAlreadyReported, "bifrost.security.xss"),
			want: "slopcop: finding " + freshID + " (bifrost.security.xss) was already reported as a false positive\n" +
				"It no longer fails checks of this repository in the same workspace (or with the same operator token), and the team sees the earlier report on the SlopCop dashboard.\n",
		},
		{
			name:     "hostile rule id",
			response: answer(service.ReportRecorded, "evil\x1b[2J"),
			want: "slopcop: reported finding " + freshID + " (evil�[2J) as a false positive\n" +
				"It no longer fails checks of this repository in the same workspace (or with the same operator token), and the team sees the report and its reason on the SlopCop dashboard.\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := WriteFalsePositive(&out, tc.response, FormatHuman); err != nil {
				t.Fatalf("human: %v", err)
			}
			if out.String() != tc.want {
				t.Errorf("human = %q, want %q", out.String(), tc.want)
			}

			out.Reset()
			if err := WriteFalsePositive(&out, tc.response, FormatJSON); err != nil {
				t.Fatalf("json: %v", err)
			}
			var document map[string]any
			if err := json.Unmarshal(out.Bytes(), &document); err != nil {
				t.Fatalf("json: %v", err)
			}
			want := map[string]any{
				"reportId": tc.response.ReportID, "findingId": tc.response.FindingID, "checkId": tc.response.CheckID,
				"policyId": tc.response.PolicyID, "kind": tc.response.Kind, "status": tc.response.Status,
			}
			if !reflect.DeepEqual(document, want) {
				t.Errorf("json = %v, want %v", document, want)
			}
		})
	}
	if err := WriteFalsePositive(&bytes.Buffer{}, answer(service.ReportRecorded, "p"), FormatSARIF); err == nil {
		t.Error("a report has no sarif form")
	}
}
