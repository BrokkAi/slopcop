package report

import (
	"encoding/json"
	"testing"

	"github.com/BrokkAi/slopcop/internal/service"
)

func TestJSONReportCarriesTheCommentKey(t *testing.T) {
	keyed := finding("src/a.go", 3, "warning", "rule.a")
	keyed.ID = "f-1"
	keyed.CommentKey = "static:7a92cae1ec0162202485"
	var document struct {
		Findings []map[string]any `json:"findings"`
	}
	checked := response(service.StatusFindings, "finding", []service.Finding{keyed})
	if err := json.Unmarshal([]byte(render(t, checked, FormatJSON)), &document); err != nil {
		t.Fatalf("json: %v", err)
	}
	// `slopcop comment` reads it back from this report.
	if len(document.Findings) != 1 || document.Findings[0]["commentKey"] != keyed.CommentKey {
		t.Errorf("json findings = %v", document.Findings)
	}
	document.Findings = nil
	if err := json.Unmarshal([]byte(render(t, mixed(), FormatJSON)), &document); err != nil {
		t.Fatalf("json: %v", err)
	}
	if _, ok := document.Findings[0]["commentKey"]; ok {
		t.Errorf("json invented a comment key: %v", document.Findings[0])
	}
}
