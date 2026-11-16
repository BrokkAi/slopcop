package report

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/service"
)

// misspelled is a check with one misspelled-identifier finding that carries
// its suggested word.
func misspelled() service.Response {
	flagged := finding("src/parse.js", 12, "note", "bifrost.javascript-codeql-quality.misspelled-identifier")
	flagged.ID = "98a2d745-fa33-467b-8289-2a9f3d05a0c8"
	flagged.SuggestedWord = "parsr"
	return response(service.StatusFindings, "finding", []service.Finding{flagged})
}

func TestHumanNamesTheSuggestedWordAndHowToAddIt(t *testing.T) {
	want := "    Word: parsr; add it to the workspace dictionary with " +
		"`slopcop dictionary add 98a2d745-fa33-467b-8289-2a9f3d05a0c8`\n"
	if got := render(t, misspelled(), FormatHuman); !strings.Contains(got, want) {
		t.Errorf("human = %q\nwant it to contain %q", got, want)
	}

	// Without an id to add it by, the word is still named.
	anonymous := finding("cli.js", 2, "note", "acme.misspelled")
	anonymous.SuggestedWord = "Kubectl"
	got := Findings([]service.Finding{anonymous})
	if !strings.Contains(got, "    Word: Kubectl\n") || strings.Contains(got, "dictionary add") {
		t.Errorf("findings = %q", got)
	}

	// A hostile word is printed safely.
	hostile := misspelled()
	hostile.Findings[0].SuggestedWord = "evil\x1b[31m"
	if got := render(t, hostile, FormatHuman); strings.Contains(got, "\x1b") {
		t.Errorf("human printed a control character: %q", got)
	}

	if got := render(t, mixed(), FormatHuman); strings.Contains(got, "Word:") {
		t.Errorf("unsuggested = %q", got)
	}
}

func TestMachineReportsCarryTheSuggestedWord(t *testing.T) {
	var document struct {
		Findings []map[string]any `json:"findings"`
	}
	if err := json.Unmarshal([]byte(render(t, misspelled(), FormatJSON)), &document); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(document.Findings) != 1 || document.Findings[0]["suggestedWord"] != "parsr" {
		t.Errorf("json findings = %v", document.Findings)
	}
	document.Findings = nil
	if err := json.Unmarshal([]byte(render(t, mixed(), FormatJSON)), &document); err != nil {
		t.Fatalf("json: %v", err)
	}
	if _, ok := document.Findings[0]["suggestedWord"]; ok {
		t.Errorf("json invented a word: %v", document.Findings[0])
	}

	var log struct {
		Runs []struct {
			Results []map[string]any `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(render(t, misspelled(), FormatSARIF)), &log); err != nil {
		t.Fatalf("sarif: %v", err)
	}
	properties, _ := log.Runs[0].Results[0]["properties"].(map[string]any)
	if properties["slopcop.suggestedWord"] != "parsr" {
		t.Errorf("sarif result = %v", log.Runs[0].Results[0])
	}
}
