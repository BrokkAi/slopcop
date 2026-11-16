package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/service"
)

// sizedRepo is a repository whose base holds a large records directory and a
// vendored one .bifrostignore already leaves out, with an unpushed change.
func sizedRepo(t *testing.T) string {
	t.Helper()
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "records/a.json", strings.Repeat("x", 6000))
	writeFile(t, repo, "records/b.json", strings.Repeat("x", 6000))
	writeFile(t, repo, "vendor/lib.js", strings.Repeat("x", 20000))
	writeFile(t, repo, ".bifrostignore", "vendor/\n")
	writeFile(t, repo, "src/main.go", "package main\n")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-qm", "data")
	writeFile(t, repo, "src/main.go", "package main\n\nfunc main() {}\n")
	return repo
}

const nearLimitAnswer = `{
	"status": "clean",
	"baseCommit": %q,
	"applied": true,
	"summary": {"files": 1, "findings": 0, "newFindings": 0, "incompletePolicies": 0, "failOn": "finding"},
	"findings": [],
	"incomplete": [],
	"repositorySize": {"files": 41000, "bytes": 1000, "maxFiles": 50000, "maxBytes": 536870912, "excludedFiles": 1, "excludedBytes": 20000, "nearLimit": %t}
}`

func TestCheckNearTheSizeLimitNamesTheLargestPaths(t *testing.T) {
	isolateUserConfig(t)
	repo := sizedRepo(t)
	base := runGit(t, repo, "rev-parse", "HEAD")
	near := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, nearLimitAnswer, base, near)
	}))
	defer server.Close()

	code, stdout, stderr := runCLI(t, "check", "--mode", "static", "--root", repo, "--service", server.URL, "--base", "HEAD")
	if code != 0 {
		t.Fatalf("exit = %d stderr = %s", code, stderr)
	}
	for _, want := range []string{
		"Repository near the size limit: the check's checkout held 41,000 of 50,000 files and 1000 B of 512.0 MiB.\n",
		"after .bifrostignore and [check] exclude leave out 1 file(s), 19.5 KiB. Largest paths by file count:\n",
		"  records/          11.7 KiB  2 file(s)\n",
		"  [check]\n  exclude = [\"records/**\"]\n",
		"enterprise agreement for higher limits",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %s\nwant %q", stdout, want)
		}
	}
	if strings.Contains(stdout, "vendor/") {
		t.Errorf("an excluded path was listed: %s", stdout)
	}

	code, stdout, stderr = runCLI(t, "check", "--mode", "static", "--root", repo, "--service", server.URL, "--base", "HEAD", "--format", "json")
	if code != 0 {
		t.Fatalf("json exit = %d stderr = %s", code, stderr)
	}
	var document struct {
		RepositorySize *service.RepositorySize `json:"repositorySize"`
		SizeBreakdown  *struct {
			Largest []struct {
				Path    string `json:"path"`
				Pattern string `json:"pattern"`
			} `json:"largest"`
		} `json:"sizeBreakdown"`
	}
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("stdout is not json: %v\n%s", err, stdout)
	}
	if document.RepositorySize == nil || !document.RepositorySize.NearLimit || document.RepositorySize.Files != 41000 {
		t.Fatalf("repositorySize = %+v", document.RepositorySize)
	}
	if document.SizeBreakdown == nil || document.SizeBreakdown.Largest[0].Pattern != "records/**" {
		t.Fatalf("sizeBreakdown = %+v", document.SizeBreakdown)
	}
	if strings.Contains(stderr, "size limit") {
		t.Fatalf("json mode wrote the advice to stderr: %s", stderr)
	}

	near = false
	code, stdout, stderr = runCLI(t, "check", "--mode", "static", "--root", repo, "--service", server.URL, "--base", "HEAD", "--format", "json")
	if code != 0 || !strings.Contains(stdout, `"repositorySize"`) || strings.Contains(stdout, "sizeBreakdown") {
		t.Fatalf("far from the limit: exit = %d stdout = %s stderr = %s", code, stdout, stderr)
	}
}

func TestCheckRefusedAsTooLargeNamesTheLargestPaths(t *testing.T) {
	isolateUserConfig(t)
	repo := sizedRepo(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"code": "repository_too_large", "message": "The repository is over the size limit.", "remedy": "Exclude bulky paths in [check].", "retryable": false}`))
	}))
	defer server.Close()

	code, stdout, stderr := runCLI(t, "check", "--mode", "static", "--root", repo, "--service", server.URL, "--base", "HEAD")
	if code != service.ExitIncomplete || stdout != "" {
		t.Fatalf("exit = %d stdout = %q", code, stdout)
	}
	for _, want := range []string{
		"slopcop: repository_too_large: The repository is over the size limit.\nExclude bulky paths in [check].\nBase tree ",
		"  records/          11.7 KiB  2 file(s)\n",
		"  exclude = [\"records/**\"]\n",
		"enterprise agreement",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %s\nwant %q", stderr, want)
		}
	}

	code, stdout, _ = runCLI(t, "check", "--mode", "static", "--root", repo, "--service", server.URL, "--base", "HEAD", "--format", "json")
	var document struct {
		Code          string `json:"code"`
		SizeBreakdown *struct {
			Files   int `json:"files"`
			Largest []struct {
				Path string `json:"path"`
			} `json:"largest"`
		} `json:"sizeBreakdown"`
	}
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("stdout is not json: %v\n%s", err, stdout)
	}
	if code != service.ExitIncomplete || document.Code != "repository_too_large" || document.SizeBreakdown == nil || document.SizeBreakdown.Largest[0].Path != "records" {
		t.Fatalf("exit = %d json = %s", code, stdout)
	}
}
