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
	}
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
	return service.Response{
		Status:     status,
		BaseCommit: baseCommit,
		Applied:    true,
		Summary: service.Summary{
			Files:               3,
			Findings:            len(findings),
			IncompletePolicyIDs: []string{},
			FailOn:              failOn,
		},
		Findings:   findings,
		Incomplete: []service.IncompletePolicy{},
	}
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
	var out bytes.Buffer
	if err := Write(&out, r, format); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return out.String()
}

func TestHumanListsEachFindingThenTheSummary(t *testing.T) {
	got := render(t, response(service.StatusFindings, "finding", sampleFindings()), FormatHuman)
	want := "Findings\n" +
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
		"3 findings across 2 file(s) | Analysis complete\n"
	if got != want {
		t.Errorf("the file count is files with findings, not changed files:\n%s", got)
	}

	clean := render(t, response(service.StatusClean, "finding", []service.Finding{}), FormatHuman)
	if clean != "0 findings | Analysis complete\n" {
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
	if !strings.HasSuffix(got, "\n1 finding across 1 file(s) | Analysis complete\n") {
		t.Errorf("summary = %q", got)
	}
}

func TestHumanSaysWhyTheAnalysisDidNotComplete(t *testing.T) {
	got := render(t, failed(), FormatHuman)
	if got != "0 findings | Analysis did not complete: The analysis could not be completed.\n" {
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
	want := "\n3 findings (+45 not reported) across 2 file(s) | Analysis complete\n" +
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
	if want := []string{"baseCommit", "findings", "incomplete", "status", "summary"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
	wantSummary := map[string]any{
		"files":               float64(3),
		"findings":            float64(3),
		"incompletePolicies":  float64(0),
		"incompletePolicyIds": []any{},
		"omittedFindings":     float64(0),
		"failOn":              "error",
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
		if _, ok := result["baselineState"]; ok {
			t.Errorf("results are not all new against the base: %v", result)
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
		properties["slopcop.baseCommit"] != baseCommit {
		t.Errorf("properties = %v", properties)
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

func TestUnknownFormat(t *testing.T) {
	if err := Write(&bytes.Buffer{}, response(service.StatusClean, "finding", nil), "xml"); err == nil {
		t.Fatal("expected an error for an unknown format")
	}
}
