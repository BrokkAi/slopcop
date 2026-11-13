package cli

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/gitx"
	"github.com/BrokkAi/slopcop/internal/report"
	"github.com/BrokkAi/slopcop/internal/review"
)

func TestLargeDataIsOmittedBeforeUpload(t *testing.T) {
	for _, include := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "explicit-inclusion"}[include], func(t *testing.T) {
			isolateUserConfig(t)
			repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
			base := runGit(t, repo, "rev-parse", "HEAD")
			writeFile(t, repo, "evidence.json", `{"evidence":"`+strings.Repeat("q", 500001)+`"}`)
			writeFile(t, repo, "host.jsonl", strings.Repeat("{\"cpu\":1}\n", 50001))
			writeFile(t, repo, "bundle.gz", "\x00"+strings.Repeat("b", 500001))
			writeFile(t, repo, "config.json", "{\"enabled\":true}\n")
			writeFile(t, repo, "main.go", "package main\nfunc main() {}\n")
			var patch []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/check" {
					http.NotFound(w, r)
					return
				}
				var request struct {
					Change string `json:"change"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				var err error
				patch, err = base64.StdEncoding.DecodeString(request.Change)
				if err != nil {
					t.Error(err)
				}
				_, _ = w.Write([]byte(`{"status":"clean","applied":true,"baseCommit":"` + base + `","summary":{"files":2,"findings":0},"findings":[],"incomplete":[]}`))
			}))
			defer server.Close()
			args := []string{"check", "--root", repo, "--base", base, "--service", server.URL, "--format", "json"}
			if include {
				args = append(args, "--include", "evidence.json", "--include", "host.*", "--include", "bundle.gz")
			}
			code, out, stderr := runCLI(t, args...)
			if code != 0 {
				t.Fatalf("exit %d: %s\n%s", code, out, stderr)
			}
			for _, name := range []string{"evidence.json", "host.jsonl", "bundle.gz"} {
				if strings.Contains(string(patch), "b/"+name) != include {
					t.Errorf("upload scope for %s does not match include=%v", name, include)
				}
			}
			for _, name := range []string{"config.json", "main.go"} {
				if !strings.Contains(string(patch), "b/"+name) {
					t.Errorf("upload lost %s", name)
				}
			}
			var doc struct {
				Approved bool                 `json:"approved"`
				Omitted  []report.OmittedFile `json:"omittedFiles"`
			}
			if err := json.Unmarshal([]byte(out), &doc); err != nil {
				t.Fatal(err)
			}
			wantOmitted := 3
			if include {
				wantOmitted = 0
			}
			if !doc.Approved || len(doc.Omitted) != wantOmitted {
				t.Fatalf("scope approval/report: %s", out)
			}
			if !include && !strings.Contains(stderr, "contents were not analyzed") {
				t.Fatalf("no scope notice: %s", stderr)
			}
		})
	}
}

func TestOnlyLargeDataSkipsWithoutARequestOrApproval(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "evidence.json", strings.Repeat("x", 500001))
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; http.Error(w, "unexpected request", 500) }))
	defer server.Close()
	code, out, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL, "--format", "json")
	var doc struct {
		Status   string               `json:"status"`
		Code     string               `json:"code"`
		Approved bool                 `json:"approved"`
		Omitted  []report.OmittedFile `json:"omittedFiles"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("%v: %s %s", err, out, stderr)
	}
	if code != 0 || called || doc.Status != "skipped" || doc.Code != "all_files_omitted" || doc.Approved || len(doc.Omitted) != 1 {
		t.Fatalf("exit=%d, called=%v, report=%s, stderr=%s", code, called, out, stderr)
	}
	if strings.Contains(stderr, "working tree matches the base") || !strings.Contains(stderr, "nothing was uploaded or analyzed") {
		t.Fatalf("misleading empty change: %s", stderr)
	}
	snapshot, err := gitx.CaptureWorkingTree(repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := review.Reviewed(repo, snapshot.Tree)
	if err != nil || approved {
		t.Fatalf("artifact-only approval=%v err=%v", approved, err)
	}
}

func TestIncludeOnlyOptsMatchingDataBackIn(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "")
	base := runGit(t, repo, "rev-parse", "HEAD")
	writeFile(t, repo, "config/settings.json", strings.Repeat("c", 500001))
	writeFile(t, repo, "reports/evidence.json", strings.Repeat("e", 500001))
	writeFile(t, repo, "main.go", "package main\n")
	prepared, err := prepareCheck(&checkOptions{root: repo, base: base, mode: modeAI, include: []string{"config/*.json"}}, config.Config{}, "", "test", "finding")
	if err != nil {
		t.Fatal(err)
	}
	patch, err := base64.StdEncoding.DecodeString(prepared.request.Change)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patch), "b/config/settings.json") || !strings.Contains(string(patch), "b/main.go") || strings.Contains(string(patch), "b/reports/evidence.json") || len(prepared.omittedFiles) != 1 || prepared.omittedFiles[0].Path != "reports/evidence.json" {
		t.Fatal("inclusion broadened artifact scope or restricted source scope")
	}
}

func TestLocalReviewUsesFilteredTreeAndPreservesSnapshotIdentity(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "")
	writeFile(t, repo, "evidence.json", `{"artifact":"`+strings.Repeat("z", 500001)+`"}`)
	writeFile(t, repo, "config.json", "{\"enabled\":true}\n")
	t.Setenv("SLOPCOP_FAKE_ACP", "1")
	cfg := config.Config{Mode: "ai", Review: &config.Review{Reviewers: []string{"terra"}, Adapters: map[string]config.Adapter{"codex": {Command: []string{os.Args[0], "-test.run=^TestFakeAgent$", "--fake-mode=clean"}}}}}
	if _, err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runCLI(t, "check", "--root", repo, "--format", "json")
	if code != 0 {
		t.Fatalf("exit=%d: %s %s", code, out, stderr)
	}
	var doc struct {
		Approved bool `json:"approved"`
		Review   struct {
			Tree     string `json:"tree"`
			DiffTree string `json:"diffTree"`
			LogDir   string `json:"logDir"`
		} `json:"review"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	capture := review.Capture{Tree: doc.Review.Tree, DiffTree: doc.Review.DiffTree}
	snapshot, err := gitx.CaptureWorkingTree(repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !doc.Approved || capture.Tree != snapshot.Tree || capture.DiffTree == "" || capture.DiffTree == snapshot.Tree {
		t.Fatalf("capture identity/scope: %s", out)
	}
	prompt, err := os.ReadFile(filepath.Join(doc.Review.LogDir, "prompt.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(prompt), "evidence.json") || !strings.Contains(string(prompt), "config.json") || !strings.Contains(string(prompt), "were omitted") || !strings.Contains(string(prompt), capture.CheckedTree()) {
		t.Fatalf("review did not honor filtered scope: %.1500s", prompt)
	}
	approved, err := review.Reviewed(repo, snapshot.Tree)
	if err != nil || !approved {
		t.Fatalf("full snapshot approval=%v err=%v", approved, err)
	}
}

func TestHookUpdatesOmittedScopeWithoutResendingAnalysis(t *testing.T) {
	for _, sourceChange := range []bool{false, true} {
		t.Run(map[bool]string{false: "only-artifacts", true: "source-and-artifacts"}[sourceChange], func(t *testing.T) {
			isolateHook(t)
			repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
			if sourceChange {
				writeFile(t, repo, "main.go", "package main\n")
			}
			writeFile(t, repo, "first.json", strings.Repeat("j", 500001))
			writeSessionEdits(t, repo, "first.json", "main.go")
			fake := &fakeService{active: []string{"BrokkAi/slopcop"}}
			server := fake.start(t, repo)
			first := runTestHook(t, server.URL, stopPayload(repo, false))
			if !strings.Contains(first.SystemMessage, "first.json") {
				t.Fatalf("missing first omission: %+v", first)
			}
			checks := fake.checks
			if sourceChange && checks != 1 || !sourceChange && checks != 0 {
				t.Fatalf("first checks=%d", checks)
			}
			writeFile(t, repo, "second.json", strings.Repeat("k", 500001))
			writeSessionEdits(t, repo, "first.json", "second.json", "main.go")
			second := runTestHook(t, server.URL, stopPayload(repo, false))
			if !strings.Contains(second.SystemMessage, "second.json") || fake.checks != checks {
				t.Fatalf("new omission not disclosed independently of cached analysis: checks=%d, output=%+v", fake.checks, second)
			}
			third := runTestHook(t, server.URL, stopPayload(repo, false))
			if third.SystemMessage != "" || fake.checks != checks {
				t.Fatalf("unchanged omission repeated: checks=%d output=%+v", fake.checks, third)
			}
		})
	}
}

func TestHookOmissionNoticeIsBoundedAndDisclosesRemainder(t *testing.T) {
	files := make([]report.OmittedFile, 1000)
	for i := range files {
		files[i] = report.OmittedFile{Path: strings.Repeat("é", 100) + strconv.Itoa(i) + ".json", Bytes: 500001, Reason: "large-json"}
	}
	notice := hookOmittedFilesNotice(files)
	if utf8.RuneCountInString(notice) > maxHookOutputRunes/2 || !strings.Contains(notice, "1000 large JSON/binary file(s)") || !strings.Contains(notice, "additional omitted file(s)") || !strings.Contains(notice, "complete list") {
		t.Fatalf("unbounded or silent omission list: %d runes, %.1000s", utf8.RuneCountInString(notice), notice)
	}
}
