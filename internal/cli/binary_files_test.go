package cli

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/report"
)

func TestCheckFlagsUnverifiedBinaryFiles(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	base := runGit(t, repo, "rev-parse", "HEAD")
	var logo bytes.Buffer
	if err := png.Encode(&logo, image.NewGray(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "logo.png", logo.String())
	writeFile(t, repo, "run.dat", "#!/bin/sh\n# \x00\nid\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/check" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"status":"clean","applied":true,"baseCommit":"` + base + `","summary":{"files":2,"findings":0},"findings":[],"incomplete":[]}`))
	}))
	defer server.Close()
	code, out, stderr := runCLI(t, "check", "--root", repo, "--base", base, "--service", server.URL, "--format", "json")
	if code != 0 {
		t.Fatalf("the flag must not fail the check: exit %d: %s\n%s", code, out, stderr)
	}
	var doc struct {
		Approved    bool                `json:"approved"`
		BinaryFiles []report.BinaryFile `json:"binaryFiles"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	got := map[string]report.BinaryFile{}
	for _, file := range doc.BinaryFiles {
		got[file.Path] = file
	}
	if !doc.Approved || len(got) != 2 || !got["logo.png"].Verified || got["logo.png"].Format != "png" || got["run.dat"].Verified {
		t.Fatalf("report: %s", out)
	}
	if !strings.Contains(stderr, "1 changed binary file(s) could not be verified") {
		t.Fatalf("no notice: %s", stderr)
	}
}
