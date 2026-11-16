package report

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/service"
)

// spanned is a misspelled-identifier finding on `segmnet`, columns 11 to 18
// of line 2, with the service's observation.
func spanned() service.Finding {
	found := finding("lib/feed.ts", 2, "warning", "bifrost.javascript-codeql-quality.misspelled-identifier")
	found.StartColumn, found.EndColumn = line(11), line(18)
	found.Message = "Bifrost computed a score of 32; the threshold is 15."
	return found
}

const feedSource = "const a = 1;\nconst x = segmnet.length;\n"

func source(files map[string]string) Source {
	return func(path string) ([]byte, bool) {
		content, ok := files[path]
		return []byte(content), ok
	}
}

func TestHumanSaysTheMessageAndQuotesTheSpan(t *testing.T) {
	r := response(service.StatusFindings, "finding", []service.Finding{spanned()})
	got := renderWith(t, r, FormatHuman, Options{Source: source(map[string]string{"lib/feed.ts": feedSource})})
	want := "  [warning] bifrost.javascript-codeql-quality.misspelled-identifier: " +
		"bifrost.javascript-codeql-quality.misspelled-identifier title\n" +
		"    Message: Bifrost computed a score of 32; the threshold is 15.\n" +
		"    Location: lib/feed.ts:2:11\n" +
		"    Code: `segmnet`\n" +
		"    Certainty: definite\n"
	if !strings.Contains(got, want) {
		t.Errorf("human = %q\nwant it to contain %q", got, want)
	}

	// Without a source, as the Stop hook renders findings, it says where
	// the span is but quotes nothing.
	if got := Findings(r.Findings); strings.Contains(got, "Code:") || !strings.Contains(got, "Location: lib/feed.ts:2:11\n") {
		t.Errorf("findings = %q", got)
	}

	// A finding without columns or a message reads as before.
	plain := response(service.StatusFindings, "finding", []service.Finding{finding("cli.py", 2, "warning", "acme.rule")})
	if got := render(t, plain, FormatHuman); strings.Contains(got, "Message:") || strings.Contains(got, "Code:") ||
		!strings.Contains(got, "    Location: cli.py:2\n") {
		t.Errorf("plain = %q", got)
	}

	// The message and the quote are repository data, printed safely.
	hostile := spanned()
	hostile.Message = "evil\x1b[31m"
	r = response(service.StatusFindings, "finding", []service.Finding{hostile})
	got = renderWith(t, r, FormatHuman, Options{Source: source(map[string]string{"lib/feed.ts": "\nconst x = seg\x1bnet.length;\n"})})
	if strings.Contains(got, "\x1b") {
		t.Errorf("human printed a control character: %q", got)
	}
}

func TestASpanIsQuotedOnlyWhenItIsExact(t *testing.T) {
	for name, tc := range map[string]struct {
		content          string
		line, start, end int
		want             string
	}{
		"one line":                  {feedSource, 2, 11, 18, "segmnet"},
		"last line, no newline":     {"a\nb segmnet", 2, 3, 10, "segmnet"},
		"past the line's end":       {feedSource, 2, 11, 40, ""},
		"past the file's end":       {feedSource, 5, 1, 3, ""},
		"non-ASCII before the span": {"\nconst é = segmnet;\n", 2, 11, 18, ""},
		"a line break in the span":  {"\nconst x = segmnet\r\n", 2, 11, 19, ""},
	} {
		if got := spanText([]byte(tc.content), tc.line, tc.start, tc.end); got != tc.want {
			t.Errorf("%s: spanText = %q, want %q", name, got, tc.want)
		}
	}

	quote := spanQuoter(source(map[string]string{"lib/feed.ts": feedSource}))
	for name, edit := range map[string]func(*service.Finding){
		"no columns":      func(f *service.Finding) { f.StartColumn, f.EndColumn = nil, nil },
		"several lines":   func(f *service.Finding) { f.EndLine = line(3) },
		"empty span":      func(f *service.Finding) { f.EndColumn = line(11) },
		"column zero":     func(f *service.Finding) { f.StartColumn = line(0) },
		"too long":        func(f *service.Finding) { f.EndColumn = line(11 + maxQuotedSpan + 1) },
		"unreadable file": func(f *service.Finding) { f.Path = "missing.ts" },
	} {
		found := spanned()
		edit(&found)
		if got := quote(found); got != "" {
			t.Errorf("%s: quoted %q", name, got)
		}
	}
	if got := quote(spanned()); got != "segmnet" {
		t.Errorf("quote = %q", got)
	}
}

func TestMachineReportsCarryTheMessageAndColumns(t *testing.T) {
	r := response(service.StatusFindings, "finding", []service.Finding{spanned(), finding("cli.py", 2, "warning", "acme.rule")})
	r.CheckID = "6f1c2a7e-3b4d-4e5f-8a9b-0c1d2e3f4a5b"
	var document struct {
		CheckID  string           `json:"checkId"`
		Findings []map[string]any `json:"findings"`
	}
	if err := json.Unmarshal([]byte(render(t, r, FormatJSON)), &document); err != nil {
		t.Fatalf("json: %v", err)
	}
	if document.CheckID != r.CheckID {
		t.Errorf("checkId = %q", document.CheckID)
	}
	first := document.Findings[0]
	if first["startColumn"] != 11.0 || first["endColumn"] != 18.0 || first["message"] != spanned().Message {
		t.Errorf("json finding = %v", first)
	}
	for _, field := range []string{"startColumn", "endColumn", "message"} {
		if _, ok := document.Findings[1][field]; ok {
			t.Errorf("json invented %s: %v", field, document.Findings[1])
		}
	}

	var log struct {
		Runs []struct {
			Results []struct {
				Message   struct{ Text string } `json:"message"`
				Locations []struct {
					PhysicalLocation struct {
						Region map[string]int `json:"region"`
					} `json:"physicalLocation"`
				} `json:"locations"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(render(t, r, FormatSARIF)), &log); err != nil {
		t.Fatalf("sarif: %v", err)
	}
	results := log.Runs[0].Results
	region := results[0].Locations[0].PhysicalLocation.Region
	if region["startLine"] != 2 || region["endLine"] != 2 || region["startColumn"] != 11 || region["endColumn"] != 18 {
		t.Errorf("sarif region = %v", region)
	}
	if want := spanned().Title + "\n" + spanned().Message; results[0].Message.Text != want {
		t.Errorf("sarif message = %q, want %q", results[0].Message.Text, want)
	}
	if _, ok := results[1].Locations[0].PhysicalLocation.Region["startColumn"]; ok {
		t.Errorf("sarif invented a column: %v", results[1].Locations[0].PhysicalLocation.Region)
	}

	// A span that measures nothing leaves the region to its lines.
	empty := spanned()
	empty.EndColumn = line(11)
	if region := findingRegion(empty); region.StartColumn != nil || region.EndColumn != nil {
		t.Errorf("empty span region = %+v", region)
	}
}
