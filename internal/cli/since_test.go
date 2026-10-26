package cli

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSinceAndLastRunLifecycle(t *testing.T) {
	command := "check"
	for _, result := range []string{"clean", "findings", "incomplete", "rejected", "pending", "transport", "timeout", "detached"} {
		t.Run(command+"/"+result, func(t *testing.T) {
			isolateUserConfig(t)
			repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
			base := runGit(t, repo, "rev-parse", "HEAD")
			if result == "detached" {
				runGit(t, repo, "checkout", "--detach", "-q", "HEAD")
			}
			writeFile(t, repo, "new.go", "package demo\nfunc Added() {}\n")
			var captured map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
					t.Error(err)
				}
				switch result {
				case "rejected":
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]any{"code": "invalid_request", "message": "rejected"})
				case "pending":
					w.Header().Set("Retry-After", "60")
					w.WriteHeader(http.StatusConflict)
					code := "check_running"
					_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": "running", "retryable": true})
				case "timeout":
					<-r.Context().Done()
				case "transport":
					connection, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = connection.Close()
				default:
					status := result
					if result == "detached" {
						status = "clean"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "baseCommit": base, "summary": map[string]any{}, "findings": []any{}, "incomplete": []any{}})
				}
			}))
			defer server.Close()
			args := []string{command, "--root", repo, "--base", base, "--service", server.URL, "--timeout", "100ms"}
			_, _, stderr := runCLI(t, args...)
			if captured == nil {
				t.Fatalf("request not sent: %s", stderr)
			}
			if captured["sinceCommit"] != base {
				t.Errorf("first since = %v, want %s", captured["sinceCommit"], base)
			}
			passed := result == "clean"
			ref := "refs/slopcop/last-run/main"
			out, err := exec.Command("git", "-C", repo, "rev-parse", "--verify", ref).Output()
			if !passed {
				if err == nil {
					t.Fatalf("last-run ref updated for %s: %s", result, out)
				}
				return
			}
			now := captured["changeCommit"].(string)
			if err != nil || runGit(t, repo, "rev-parse", ref) != now {
				t.Fatalf("passing run did not update ref: %v", err)
			}
			writeFile(t, repo, "new.go", "package demo\nfunc Changed() {}\n")
			runCLI(t, args...)
			if captured["sinceCommit"] != now {
				t.Errorf("second since = %v, want last run %s", captured["sinceCommit"], now)
			}
			runCLI(t, append(args, "--since", "merge-base")...)
			if captured["sinceCommit"] != base {
				t.Errorf("--since merge-base = %v", captured["sinceCommit"])
			}
			runCLI(t, append(args, "--since", now)...)
			if captured["sinceCommit"] != now {
				t.Errorf("--since SHA = %v", captured["sinceCommit"])
			}
		})
	}
}

// An incompressible file proves the decoded pack limit runs before HTTP.
func TestOversizedPackFailsLocally(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	content := make([]byte, maxChangeBytes+1024*1024)
	if _, err := rand.Read(content); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "large.bin"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = w.Write([]byte(`{"status":"clean"}`))
	}))
	defer server.Close()
	code, stdout, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL)
	if code != 2 || stdout != "" || !strings.Contains(stderr, "maximum decoded size is 16 MiB") || called {
		t.Fatalf("exit=%d called=%v stdout=%s stderr=%s", code, called, stdout, stderr)
	}
	if out, err := exec.Command("git", "-C", repo, "show-ref", "--verify", "refs/slopcop/last-run/main").Output(); err == nil {
		t.Fatalf("oversized run advanced marker: %s", out)
	}
}

// Only a full default check that passed with no new finding advances the
// marker: a clean answer that --fail-on let new findings through, a narrower
// check, or a status the client does not know leaves it where it was (#19).
func TestOnlyAFullCleanCheckAdvancesTheMarker(t *testing.T) {
	cases := []struct {
		name   string
		status string
		newOne bool
		extra  []string
		exit   int
	}{
		{name: "fail-on never with a new finding", status: "clean", newOne: true, extra: []string{"--fail-on", "never"}},
		{name: "explicit since", status: "clean", extra: []string{"--since", "HEAD"}},
		{name: "no policy discovery", status: "clean", extra: []string{"--no-policy-discovery"}},
		{name: "rule selection", status: "clean", extra: []string{"--skip-rule", "demo.rule"}},
		{name: "unknown status", status: "mostly-fine", exit: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateUserConfig(t)
			repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
			writeFile(t, repo, "new.go", "package demo\nfunc Added() {}\n")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				newFindings := 0
				findings := []any{}
				if tc.newOne {
					newFindings = 1
					findings = append(findings, map[string]any{"policyId": "demo.rule", "severity": "warning", "title": "t", "path": "new.go", "startLine": 2, "new": true})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"status": tc.status, "summary": map[string]any{"newFindings": newFindings}, "findings": findings, "incomplete": []any{}})
			}))
			defer server.Close()
			args := append([]string{"check", "--root", repo, "--service", server.URL}, tc.extra...)
			if code, _, stderr := runCLI(t, args...); code != tc.exit {
				t.Fatalf("exit = %d, want %d: %s", code, tc.exit, stderr)
			}
			if out, err := exec.Command("git", "-C", repo, "rev-parse", "--verify", "refs/slopcop/last-run/main").Output(); err == nil {
				t.Fatalf("marker advanced: %s", out)
			}
		})
	}
}

// A tag named like the branch must not change the marker's name.
func TestMarkerKeyIgnoresATagNamedLikeTheBranch(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	runGit(t, repo, "tag", "main")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "clean", "summary": map[string]any{}, "findings": []any{}, "incomplete": []any{}})
	}))
	defer server.Close()
	if code, _, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL); code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	if _, err := exec.Command("git", "-C", repo, "rev-parse", "--verify", "refs/slopcop/last-run/main").Output(); err != nil {
		t.Fatalf("marker not recorded under the branch name: %v", err)
	}
}
