package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
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
			completed := result == "clean" || result == "findings" || result == "incomplete"
			ref := "refs/slopcop/last-run/main"
			out, err := exec.Command("git", "-C", repo, "rev-parse", "--verify", ref).Output()
			if !completed {
				if err == nil {
					t.Fatalf("last-run ref updated for %s: %s", result, out)
				}
				return
			}
			now := captured["changeCommit"].(string)
			if err != nil || runGit(t, repo, "rev-parse", ref) != now {
				t.Fatalf("completed run did not update ref: %v", err)
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
