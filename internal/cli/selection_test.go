package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestSelectionFlags(t *testing.T) {
	for _, test := range []struct{ flag, field string }{
		{"rule", "policyIds"}, {"skip-rule", "skipPolicyIds"},
		{"category", "categories"}, {"skip-category", "skipCategories"},
	} {
		t.Run(test.flag, func(t *testing.T) {
			isolateUserConfig(t)
			repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
			var captured map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
					t.Error(err)
				}
				_, _ = w.Write([]byte(`{"status":"clean","summary":{},"findings":[]}`))
			}))
			defer server.Close()
			code, _, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL,
				"--"+test.flag, "first", "--"+test.flag, "second", "--"+test.flag, "first")
			if code != 0 {
				t.Fatalf("exit=%d: %s", code, stderr)
			}
			want := map[string]any{test.field: []any{"first", "second"}}
			if !reflect.DeepEqual(captured["selection"], want) {
				t.Fatalf("selection=%v want=%v", captured["selection"], want)
			}
			if captured["contractVersion"] != "check-v2" {
				t.Fatalf("contractVersion=%v", captured["contractVersion"])
			}
		})
	}
}

func TestCombinedSelectorsPreservePolicyFiles(t *testing.T) {
	repo := newRepo(t, "")
	writeFile(t, repo, ".slopcop/policies/auto.rqlp", "auto")
	selection, err := resolveSelection(repo, &checkOptions{
		policies: []string{"extra.rqlp"}, rules: []string{"r"}, skipRules: []string{"s"},
		categories: []string{"c"}, skipCategories: []string{"d"},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(selection)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"files":[".slopcop/policies/auto.rqlp","extra.rqlp"],"policyIds":["r"],"skipPolicyIds":["s"],"categories":["c"],"skipCategories":["d"]}`
	if string(body) != want {
		t.Fatalf("selection=%s want=%s", body, want)
	}
}

func TestInfoFailOnThresholds(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	for _, threshold := range []string{"", "finding", "info", "note", "warning", "error", "never"} {
		t.Run("threshold="+threshold, func(t *testing.T) {
			for _, format := range []string{"human", "json"} {
				var sent string
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var request struct{ FailOn string }
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
					}
					sent = request.FailOn
					// Threshold evaluation belongs to the service. Verify forwarding and exit status.
					status := "clean"
					if sent == "info" {
						status = "findings"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{
						"status": status, "summary": map[string]any{"newFindings": 1, "failOn": sent},
						"findings": []any{map[string]any{"policyId": "demo.info", "severity": "info", "title": "Informational finding", "path": "new.go", "startLine": 2, "new": true}},
					})
				}))
				args := []string{"check", "--root", repo, "--service", server.URL, "--since", "merge-base", "--format", format}
				if threshold != "" {
					args = append(args, "--fail-on", threshold)
				}
				code, stdout, stderr := runCLI(t, args...)
				server.Close()
				want, wantThreshold := 0, threshold
				if threshold == "info" {
					want = 1
				}
				if threshold == "" {
					wantThreshold = "finding"
				}
				if code != want || sent != wantThreshold || !strings.Contains(stdout, "demo.info") || !strings.Contains(stdout, "info") {
					t.Fatalf("exit=%d sent=%q stdout=%s stderr=%s", code, sent, stdout, stderr)
				}
			}
		})
	}
}
