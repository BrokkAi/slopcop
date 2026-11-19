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

const creditsAnswer = `{
	"status": "clean",
	"baseCommit": %q,
	"applied": true,
	"summary": {"files": 1, "findings": 0, "newFindings": 0, "incompletePolicies": 0, "failOn": "finding"},
	"findings": [],
	"incomplete": []%s
}`

const lowCredits = `,
	"credits": {"freeChecksLeft": 0, "freeChecksPerPeriod": 1000, "period": "month", "resetsAt": "2026-12-01T00:00:00Z",
		"availableCredits": 42.5, "checkPriceCredits": 1, "checksLeft": 42, "low": true, "topUpUrl": "https://slopcop.test/credits"}`

const plentyOfCredits = `,
	"credits": {"freeChecksLeft": 900, "freeChecksPerPeriod": 1000, "period": "month", "resetsAt": "2026-12-01T00:00:00Z",
		"availableCredits": 0, "checkPriceCredits": 1, "checksLeft": 900, "low": false, "topUpUrl": "https://slopcop.test/credits"}`

const lowCreditsLine = "slopcop: 42 checks left on this workspace's free checks and credits; add credits at https://slopcop.test/credits\n"

func TestCheckWarnsOnceWhenTheWorkspaceIsLowOnCredits(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "src/main.go", "package main\n")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-qm", "base")
	writeFile(t, repo, "src/main.go", "package main\n\nfunc main() {}\n")
	base := runGit(t, repo, "rev-parse", "HEAD")
	block := lowCredits
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, creditsAnswer, base, block)
	}))
	defer server.Close()
	check := func(extra ...string) (int, string, string) {
		t.Helper()
		args := append([]string{"check", "--mode", "static", "--root", repo, "--service", server.URL, "--base", "HEAD"}, extra...)
		return runCLI(t, args...)
	}

	// Low: one line on stderr, after the report.
	code, stdout, stderr := check()
	if code != 0 {
		t.Fatalf("exit = %d stderr = %s", code, stderr)
	}
	if strings.Count(stderr, lowCreditsLine) != 1 || !strings.HasSuffix(stderr, lowCreditsLine) {
		t.Errorf("stderr = %q, want the low-credits line once", stderr)
	}
	if strings.Contains(stdout, "checks left") {
		t.Errorf("stdout carries the warning: %s", stdout)
	}

	// JSON carries the figures and leaves stderr alone.
	code, stdout, stderr = check("--format", "json")
	if code != 0 {
		t.Fatalf("json exit = %d stderr = %s", code, stderr)
	}
	var document struct {
		Credits *service.Credits `json:"credits"`
	}
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("stdout is not json: %v\n%s", err, stdout)
	}
	want := service.Credits{
		FreeChecksPerPeriod: 1000, Period: "month", ResetsAt: "2026-12-01T00:00:00Z",
		AvailableCredits: 42.5, CheckPriceCredits: 1, ChecksLeft: 42, Low: true, TopUpURL: "https://slopcop.test/credits",
	}
	if document.Credits == nil || *document.Credits != want {
		t.Errorf("credits = %+v, want %+v", document.Credits, want)
	}
	if strings.Contains(stderr, "checks left") {
		t.Errorf("json mode wrote the warning to stderr: %s", stderr)
	}

	// Not low, or a service that sends no block: quiet.
	for name, answer := range map[string]string{"not low": plentyOfCredits, "absent": ""} {
		block = answer
		code, stdout, stderr = check()
		if code != 0 || strings.Contains(stderr, "checks left") {
			t.Errorf("%s: exit = %d stderr = %q", name, code, stderr)
		}
		code, stdout, _ = check("--format", "json")
		if code != 0 || strings.Contains(stdout, `"credits"`) != (answer != "") {
			t.Errorf("%s: json exit = %d stdout = %s", name, code, stdout)
		}
	}
}

func TestHookSaysOnceInASessionThatCreditsAreLow(t *testing.T) {
	isolateHook(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	writeSessionEdits(t, repo, "demo.go")
	fake := &fakeService{active: []string{"BrokkAi/slopcop"}, credits: &service.Credits{ChecksLeft: 1, Low: true}}
	server := fake.start(t, repo)

	want := "slopcop: 1 check left on this workspace's free checks and credits; add credits on the Credits page in SlopCop"
	if output := runTestHook(t, server.URL, stopPayload(repo, false)); !strings.HasPrefix(output.SystemMessage, want) {
		t.Fatalf("output = %+v, want the low-credits line", output)
	}
	// A later check in the session, still low, does not repeat it.
	writeFile(t, repo, "demo.go", "package demo // changed\n")
	if output := runTestHook(t, server.URL, stopPayload(repo, false)); strings.Contains(output.SystemMessage, "check left") || fake.checks != 2 {
		t.Fatalf("second output = %+v, checks = %d", output, fake.checks)
	}
}

func TestHookTellsAHeldAgentThatCreditsAreLow(t *testing.T) {
	isolateHook(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	writeSessionEdits(t, repo, "demo.go")
	fake := &fakeService{active: []string{"BrokkAi/slopcop"}, findings: []string{"demo.go"},
		credits: &service.Credits{ChecksLeft: 3, Low: true}}
	server := fake.start(t, repo)

	// Held, the agent reads the reason, which some harnesses pass on alone;
	// the line leads both so a long findings list cannot cut it off.
	output := runTestHook(t, server.URL, stopPayload(repo, false))
	want := "slopcop: 3 checks left on this workspace's free checks and credits;"
	if output.Decision != "block" || !strings.HasPrefix(output.Reason, want) || !strings.HasPrefix(output.SystemMessage, want) {
		t.Fatalf("output = %+v, want the low-credits line first in both", output)
	}
}
