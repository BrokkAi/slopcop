package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/BrokkAi/slopcop/internal/report"
	"github.com/BrokkAi/slopcop/internal/service"
)

func TestRealSimilarityServiceResponsesDecodeAndRender(t *testing.T) {
	files, err := filepath.Glob("testdata/similarity-contract/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no captured service responses")
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var response service.Response
			if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&response); err != nil {
				t.Fatal(err)
			}
			for _, format := range []string{report.FormatHuman, report.FormatJSON, report.FormatSARIF} {
				var out bytes.Buffer
				if err := report.Write(&out, response, format, report.Options{}); err != nil {
					t.Fatalf("%s: %v", format, err)
				}
				if format != report.FormatHuman && !json.Valid(out.Bytes()) {
					t.Fatalf("%s output is invalid JSON", format)
				}
			}
		})
	}
}
