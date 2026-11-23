package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/review"
)

const (
	trackedCheckID  = "11111111-2222-3333-4444-555555555555"
	answerWorkspace = "aaaaaaaa-0000-4000-8000-000000000001"
	whoamiWorkspace = "bbbbbbbb-0000-4000-8000-000000000002"
)

// trackingService answers checks and records what reaches the review routes.
type trackingService struct {
	*httptest.Server
	mu       sync.Mutex
	reports  []map[string]any
	disposed []map[string]any
	paths    []string
	// headers are the workspace each review request named.
	headers []string
	// answerWorkspace is the check answer's workspaceId; "" leaves it out.
	answerWorkspace string
	// whoamiWorkspace is the login's workspace; "" names none.
	whoamiWorkspace string
}

// newTrackingService answers a check with the given reviewTracking ("" leaves
// it out); answer, when set, handles the review routes.
func newTrackingService(t *testing.T, tracking string, answer http.HandlerFunc) *trackingService {
	t.Helper()
	s := &trackingService{answerWorkspace: answerWorkspace}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/whoami" && s.whoamiWorkspace != "" {
			_, _ = w.Write([]byte(`{"workspace":{"id":"` + s.whoamiWorkspace + `"},"workspaces":[]}`))
			return
		}
		if strings.Contains(r.URL.Path, "/review") {
			body, _ := io.ReadAll(r.Body)
			var decoded map[string]any
			_ = json.Unmarshal(body, &decoded)
			s.mu.Lock()
			s.paths = append(s.paths, r.URL.Path)
			s.headers = append(s.headers, r.Header.Get("SlopCop-Workspace"))
			if strings.HasSuffix(r.URL.Path, "/dispositions") {
				s.disposed = append(s.disposed, decoded)
			} else {
				s.reports = append(s.reports, decoded)
			}
			s.mu.Unlock()
			if answer != nil {
				answer(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"checkId":"` + trackedCheckID + `","status":"recorded"}`))
			return
		}
		field := ""
		if tracking != "" {
			field = `"reviewTracking":"` + tracking + `",`
		}
		if s.answerWorkspace != "" {
			field += `"workspaceId":"` + s.answerWorkspace + `",`
		}
		_, _ = w.Write([]byte(`{"checkId":"` + trackedCheckID + `",` + field + `"status":"clean","baseCommit":"0123456789abcdef0123456789abcdef01234567","applied":true,"summary":{"files":1,"newFindings":0},"findings":[],"incomplete":[]}`))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *trackingService) counts() (reports, dispositions int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reports), len(s.disposed)
}

func trackedRepo(t *testing.T, agent string) string {
	t.Helper()
	isolateUserConfig(t)
	t.Setenv("SLOPCOP_TOKEN", "test-token")
	t.Setenv("SLOPCOP_FAKE_ACP", "1")
	cfg := config.Config{Mode: "both", Review: &config.Review{Reviewers: []string{"terra"}, Adapters: map[string]config.Adapter{
		"codex": {Command: []string{os.Args[0], "-test.run=^TestFakeAgent$", "--fake-mode=" + agent}},
	}}}
	if _, err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "README.md", "changed\n")
	return repo
}

// volatile are the parts of a JSON report that differ between two runs.
var volatile = regexp.MustCompile(`"logDir":\s*"[^"]*"|/review-[0-9T]+-[0-9]+|"reused":\s*(true|false),?`)

func TestReportSentOnlyWhenTheAnswerSaysFindings(t *testing.T) {
	for _, tc := range []struct {
		tracking string
		want     int
	}{{"findings", 1}, {"off", 0}, {"", 0}, {"everything", 0}} {
		t.Run("tracking="+tc.tracking, func(t *testing.T) {
			repo := trackedRepo(t, "findings")
			server := newTrackingService(t, tc.tracking, nil)
			code, _, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL, "--format", "json")
			if code != 1 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			if got, _ := server.counts(); got != tc.want {
				t.Fatalf("reports = %d, want %d", got, tc.want)
			}
			if tc.want == 1 {
				if server.paths[0] != "/v1/check/"+trackedCheckID+"/review" {
					t.Fatalf("path = %s", server.paths[0])
				}
				row := server.reports[0]["reviewers"].([]any)[0].(map[string]any)
				if row["family"] != "terra" || row["status"] != "findings" || row["findingCount"] != float64(1) {
					t.Fatalf("reviewer = %v", row)
				}
			}
		})
	}
}

func TestAIModeSendsNothing(t *testing.T) {
	repo := trackedRepo(t, "findings")
	server := newTrackingService(t, "findings", nil)
	runCLI(t, "check", "--root", repo, "--service", server.URL, "--mode", "ai", "--format", "json")
	if got, _ := server.counts(); got != 0 {
		t.Fatalf("reports = %d", got)
	}
}

// A report that goes wrong in any way leaves the check as it was.
func TestAFailedReportChangesNothing(t *testing.T) {
	repo := trackedRepo(t, "findings")
	quiet := newTrackingService(t, "off", nil)
	wantCode, wantOut, _ := runCLI(t, "check", "--root", repo, "--service", quiet.URL, "--format", "json")
	want := volatile.ReplaceAllString(wantOut, "")

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	for name, answer := range map[string]http.HandlerFunc{
		"refuses": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":"check_not_found","message":"no"}`))
		},
		"server error": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) },
		"stalls": func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		},
		"redirects": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://127.0.0.1:1/elsewhere", http.StatusTemporaryRedirect)
		},
		"unknown fields": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"status":"recorded","surprise":{"a":1}}`))
		},
		"unknown status": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"mystery"}`)) },
		"not json":       func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<html>`)) },
		"off":            func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"off"}`)) },
		"already":        func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"already_recorded"}`)) },
	} {
		t.Run(name, func(t *testing.T) {
			server := newTrackingService(t, "findings", answer)
			started := time.Now()
			code, out, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL, "--format", "json")
			if elapsed := time.Since(started); elapsed > reviewReportTimeout+5*time.Second {
				t.Fatalf("took %s", elapsed)
			}
			if got, _ := server.counts(); got != 1 {
				t.Fatalf("reports = %d", got)
			}
			if code != wantCode || volatile.ReplaceAllString(out, "") != want {
				t.Fatalf("exit %d (want %d)\nout %s\nwant %s", code, wantCode, out, want)
			}
			if strings.Contains(stderr, "not reported") {
				t.Fatalf("said without --verbose: %s", stderr)
			}
		})
	}
}

func TestVerboseSaysWhyAReportFailed(t *testing.T) {
	repo := trackedRepo(t, "findings")
	server := newTrackingService(t, "findings", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	_, _, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL, "--format", "json", "--verbose")
	if !strings.Contains(stderr, "slopcop: review not reported") {
		t.Fatalf("stderr = %s", stderr)
	}
}

func checkedFindingID(t *testing.T, repo string) string {
	t.Helper()
	record, err := review.LastRecord(repo)
	if err != nil || record == nil || len(record.Findings()) == 0 {
		t.Fatalf("record = %+v, %v", record, err)
	}
	return record.Findings()[0].ID
}

func TestFindingOverrideAndUndoReachATrackedCheck(t *testing.T) {
	repo := trackedRepo(t, "findings")
	server := newTrackingService(t, "findings", nil)
	runCLI(t, "check", "--root", repo, "--service", server.URL, "--format", "json")
	id := checkedFindingID(t, repo)

	if code, _, stderr := runCLI(t, "override", "--root", repo, "--service", server.URL, id, "--reason", "does  not\nhold"); code != 0 {
		t.Fatalf("override: %d %s", code, stderr)
	}
	if code, _, stderr := runCLI(t, "override", "--root", repo, "--service", server.URL, "--undo", id); code != 0 {
		t.Fatalf("undo: %d %s", code, stderr)
	}
	if _, got := server.counts(); got != 2 {
		t.Fatalf("dispositions = %d", got)
	}
	first, second := server.disposed[0], server.disposed[1]
	if first["kind"] != "finding_overridden" || first["key"] != id || first["reason"] != "does not hold" {
		t.Fatalf("override = %v", first)
	}
	if second["kind"] != "finding_override_undone" || second["key"] != id {
		t.Fatalf("undo = %v", second)
	}
	if !strings.HasSuffix(server.paths[len(server.paths)-1], "/v1/check/"+trackedCheckID+"/review/dispositions") {
		t.Fatalf("paths = %v", server.paths)
	}
}

func TestWholeReviewOverrideReachesATrackedCheck(t *testing.T) {
	repo := trackedRepo(t, "findings")
	server := newTrackingService(t, "findings", nil)
	runCLI(t, "check", "--root", repo, "--service", server.URL, "--format", "json")
	if code, out, stderr := runCLI(t, "override", "--root", repo, "--service", server.URL, "--reason", "accepted for now"); code != 0 {
		t.Fatalf("override: %d %s %s", code, out, stderr)
	}
	if _, got := server.counts(); got != 1 || server.disposed[0]["kind"] != "overridden" || server.disposed[0]["reason"] != "accepted for now" {
		t.Fatalf("dispositions = %v", server.disposed)
	}
}

func TestOverridesSendNothingWithTrackingOff(t *testing.T) {
	repo := trackedRepo(t, "findings")
	server := newTrackingService(t, "off", nil)
	runCLI(t, "check", "--root", repo, "--service", server.URL, "--format", "json")
	id := checkedFindingID(t, repo)
	for _, args := range [][]string{{id, "--reason", "wrong"}, {"--undo", id}, {"--reason", "accepted"}} {
		runCLI(t, append([]string{"override", "--root", repo, "--service", server.URL}, args...)...)
	}
	if reports, dispositions := server.counts(); reports != 0 || dispositions != 0 {
		t.Fatalf("reports = %d, dispositions = %d", reports, dispositions)
	}
}

func TestOverrideSucceedsWhenTheServiceFails(t *testing.T) {
	repo := trackedRepo(t, "findings")
	server := newTrackingService(t, "findings", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/dispositions") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"status":"recorded"}`))
	})
	runCLI(t, "check", "--root", repo, "--service", server.URL, "--format", "json")
	id := checkedFindingID(t, repo)
	code, out, stderr := runCLI(t, "override", "--root", repo, "--service", server.URL, id, "--reason", "wrong")
	if code != 0 || !strings.Contains(out, "overrode review finding") || stderr != "" {
		t.Fatalf("exit %d out %q err %q", code, out, stderr)
	}
}

// An undo also reaches a later check that left the finding out because of it.
func TestUndoReachesChecksThatReliedOnTheOverride(t *testing.T) {
	repo := trackedRepo(t, "findings")
	server := newTrackingService(t, "findings", nil)
	runCLI(t, "check", "--root", repo, "--service", server.URL, "--format", "json")
	id := checkedFindingID(t, repo)
	runCLI(t, "override", "--root", repo, "--service", server.URL, id, "--reason", "wrong")

	later := review.CheckRef{ID: "99999999-2222-3333-4444-555555555555", Service: server.URL, Workspace: answerWorkspace, Tracked: true}
	record, err := review.LastRecord(repo)
	if err != nil {
		t.Fatal(err)
	}
	verdict, err := review.ApplyFindingOverrides(repo, record.Verdict)
	if err != nil || len(verdict.Overridden) != 1 {
		t.Fatalf("verdict = %+v, %v", verdict.Overridden, err)
	}
	if err := review.RememberCheck(repo, later, verdict, true); err != nil {
		t.Fatal(err)
	}
	runCLI(t, "override", "--root", repo, "--service", server.URL, "--undo", id)
	joined := strings.Join(server.paths, " ")
	if !strings.Contains(joined, later.ID+"/review/dispositions") || !strings.Contains(joined, trackedCheckID+"/review/dispositions") {
		t.Fatalf("paths = %v", server.paths)
	}
}

// A later check of a workspace that does not track reviews stops an override
// of the same finding from reaching the older tracked check.
func TestOverrideOfAFindingInAnUntrackedCheckSendsNothing(t *testing.T) {
	repo := trackedRepo(t, "findings")
	tracked := newTrackingService(t, "findings", nil)
	runCLI(t, "check", "--root", repo, "--service", tracked.URL, "--format", "json")
	id := checkedFindingID(t, repo)
	store, err := review.OpenStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	last, err := store.Last()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetCheck(review.CheckRef{ID: "77777777-2222-3333-4444-555555555555", Service: tracked.URL}, last.Verdict); err != nil {
		t.Fatal(err)
	}
	store.Close()
	runCLI(t, "override", "--root", repo, "--service", tracked.URL, id, "--reason", "wrong")
	if _, got := tracked.counts(); got != 0 {
		t.Fatalf("dispositions = %v", tracked.disposed)
	}
}

// The service keeps a check's first report, so a second review of the same
// check id does not change which findings an override can name.
func TestAlreadyRecordedKeepsTheFirstReportsFindings(t *testing.T) {
	repo := trackedRepo(t, "findings")
	server := newTrackingService(t, "findings", nil)
	runCLI(t, "check", "--root", repo, "--service", server.URL, "--format", "json")
	id := checkedFindingID(t, repo)
	ref, ok := review.TrackedCheckOf(repo, id)
	if !ok {
		t.Fatal("first report not remembered")
	}
	if err := review.RememberCheck(repo, ref, review.Verdict{}, false); err != nil {
		t.Fatal(err)
	}
	if got := review.TrackedChecksRelyingOn(repo, id); len(got) != 1 || got[0].ID != ref.ID {
		t.Fatalf("checks = %+v", got)
	}
}

// An AI-only review that reported the finding after a tracked check is the
// newest to hold it, so an override of it stays local.
func TestOverrideOfAFindingFromAnAIReviewSendsNothing(t *testing.T) {
	repo := trackedRepo(t, "findings")
	server := newTrackingService(t, "findings", nil)
	runCLI(t, "check", "--root", repo, "--service", server.URL, "--format", "json")
	id := checkedFindingID(t, repo)
	record, err := review.LastRecord(repo)
	if err != nil {
		t.Fatal(err)
	}
	local := record.Verdict
	local.Time = local.Time.Add(time.Second)
	if err := review.RememberLocalReview(repo, local); err != nil {
		t.Fatal(err)
	}
	store, err := review.OpenStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	other := review.Verdict{Status: review.StatusFailed, Capture: record.Capture}
	if err := store.Save(review.Record{Mode: "ai", Verdict: other}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	if _, ok := review.TrackedCheckOf(repo, id); ok {
		t.Fatal("the override would reach the older tracked check")
	}
}

// Another worktree's tracked check does not receive this worktree's reason.
func TestOverrideReasonStaysOutOfAnotherWorktreesCheck(t *testing.T) {
	repo := trackedRepo(t, "findings")
	server := newTrackingService(t, "findings", nil)
	runCLI(t, "check", "--root", repo, "--service", server.URL, "--format", "json")
	id := checkedFindingID(t, repo)
	other := t.TempDir() + "/other"
	runGit(t, repo, "worktree", "add", "-q", "--detach", other)
	other = runGit(t, other, "rev-parse", "--show-toplevel")
	if _, ok := review.TrackedCheckOf(other, id); ok {
		t.Fatal("the reason would reach another worktree's check")
	}
	if _, ok := review.TrackedCheckOf(repo, id); !ok {
		t.Fatal("this worktree's check was not found")
	}
}

// Every request about a check names the workspace it ran in: the answer's,
// else the login's at the time, even after the login has moved on.
func TestReviewRequestsNameTheChecksWorkspace(t *testing.T) {
	for _, tc := range []struct{ name, answer, whoami, want string }{
		{"from the check answer", answerWorkspace, whoamiWorkspace, answerWorkspace},
		{"from whoami for an older service", "", whoamiWorkspace, whoamiWorkspace},
		{"a malformed answer falls back", "not-a-uuid", whoamiWorkspace, whoamiWorkspace},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := trackedRepo(t, "findings")
			server := newTrackingService(t, "findings", nil)
			server.answerWorkspace, server.whoamiWorkspace = tc.answer, tc.whoami
			runCLI(t, "check", "--root", repo, "--service", server.URL, "--format", "json")
			id := checkedFindingID(t, repo)
			server.whoamiWorkspace = "cccccccc-0000-4000-8000-000000000003"
			runCLI(t, "override", "--root", repo, "--service", server.URL, id, "--reason", "wrong")
			runCLI(t, "override", "--root", repo, "--service", server.URL, "--undo", id)
			if len(server.headers) != 3 {
				t.Fatalf("requests = %v", server.paths)
			}
			for _, got := range server.headers {
				if got != tc.want {
					t.Fatalf("workspaces = %v, want %s", server.headers, tc.want)
				}
			}
		})
	}
}

// With no workspace id from the answer or the login, nothing is sent.
func TestNoWorkspaceIDSendsNothing(t *testing.T) {
	repo := trackedRepo(t, "findings")
	server := newTrackingService(t, "findings", nil)
	server.answerWorkspace = ""
	code, _, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL, "--format", "json", "--verbose")
	if code != 1 || !strings.Contains(stderr, "workspace is not known") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	id := checkedFindingID(t, repo)
	server.whoamiWorkspace = whoamiWorkspace
	runCLI(t, "override", "--root", repo, "--service", server.URL, id, "--reason", "wrong")
	runCLI(t, "override", "--root", repo, "--service", server.URL, "--undo", id)
	runCLI(t, "override", "--root", repo, "--service", server.URL, "--reason", "accepted")
	if reports, dispositions := server.counts(); reports != 0 || dispositions != 0 {
		t.Fatalf("reports = %d, dispositions = %d", reports, dispositions)
	}
}
