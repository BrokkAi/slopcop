package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// atTerminal stands in for a person at a terminal who types typed, or for no
// terminal at all when attached is false.
func atTerminal(t *testing.T, attached bool, typed string) {
	t.Helper()
	previous := confirmInput
	confirmInput = func(*cobra.Command) (io.Reader, bool) {
		if !attached {
			return nil, false
		}
		return strings.NewReader(typed), true
	}
	t.Cleanup(func() { confirmInput = previous })
}

// repoServer answers every request with answer and records the last request's
// path, body, and authorization.
type repoServer struct {
	path          string
	body          map[string]any
	authorization string
}

func (s *repoServer) start(t *testing.T, status int, answer string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		s.path = r.URL.Path
		s.authorization = r.Header.Get("Authorization")
		s.body = nil
		if err := json.NewDecoder(r.Body).Decode(&s.body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestRepoEnableAndDisableSendTheRepositoryAndState(t *testing.T) {
	for _, tc := range []struct {
		command string
		enabled bool
		answer  string
		want    string
	}{
		{
			command: "disable",
			enabled: false,
			answer:  `{"repository": "Fixture/public", "enabled": false, "active": false}`,
			want:    "slopcop: disabled Fixture/public; it is not checked, and costs nothing from the next UTC day\n",
		},
		{
			command: "enable",
			enabled: true,
			answer:  `{"repository": "Fixture/public", "enabled": true, "active": true}`,
			want:    "slopcop: enabled Fixture/public; it is checked again\n",
		},
	} {
		t.Run(tc.command, func(t *testing.T) {
			isolateUserConfig(t)
			t.Setenv("SLOPCOP_TOKEN", "secret")
			atTerminal(t, true, "Fixture/public\n")
			repo := newRepo(t, "git@github.com:Fixture/public.git")
			var recorded repoServer
			server := recorded.start(t, http.StatusOK, tc.answer)

			code, stdout, stderr := runCLI(t, "repo", tc.command, "--root", repo, "--service", server.URL)
			if code != 0 {
				t.Fatalf("exit = %d, stderr = %s", code, stderr)
			}
			if recorded.path != "/v1/repository/activation" {
				t.Errorf("path = %q", recorded.path)
			}
			// enabled is sent even when false: the service requires it.
			if len(recorded.body) != 2 || recorded.body["repoUrl"] != "https://github.com/Fixture/public" || recorded.body["enabled"] != tc.enabled {
				t.Errorf("body = %v", recorded.body)
			}
			if recorded.authorization != "Bearer secret" {
				t.Errorf("authorization = %q", recorded.authorization)
			}
			if stdout != tc.want {
				t.Errorf("stdout = %q, want %q", stdout, tc.want)
			}
			if prompt := "Type Fixture/public to " + tc.command + " it: "; stderr != prompt {
				t.Errorf("stderr = %q, want %q", stderr, prompt)
			}
		})
	}
}

func TestRepoChangesOnlyWhatThePersonConfirms(t *testing.T) {
	for _, tc := range []struct {
		name     string
		attached bool
		typed    string
		want     string
	}{
		{name: "no terminal", attached: false, want: "slopcop: repo disable must be confirmed at a terminal; run it yourself, or change it on the Repositories page in SlopCop\n"},
		{name: "another name", attached: true, typed: "Fixture/other\n", want: "Type Fixture/public to disable it: slopcop: that is not Fixture/public, so nothing was changed\n"},
		{name: "nothing typed", attached: true, typed: "", want: "Type Fixture/public to disable it: slopcop: that is not Fixture/public, so nothing was changed\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateUserConfig(t)
			atTerminal(t, tc.attached, tc.typed)
			repo := newRepo(t, "https://github.com/Fixture/public.git")
			called := false
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				called = true
			}))
			defer server.Close()

			code, stdout, stderr := runCLI(t, "repo", "disable", "--root", repo, "--service", server.URL)
			if code != 2 || stdout != "" {
				t.Errorf("exit = %d, stdout = %q", code, stdout)
			}
			if stderr != tc.want {
				t.Errorf("stderr = %q, want %q", stderr, tc.want)
			}
			if called {
				t.Error("an unconfirmed change reached the service")
			}
		})
	}
}

func TestRepoStatusSendsOnlyTheRepository(t *testing.T) {
	for _, tc := range []struct {
		answer string
		want   string
	}{
		{
			answer: `{"repository": "Fixture/public", "enabled": true, "active": true}`,
			want:   "slopcop: Fixture/public is enabled and checked\n",
		},
		{
			answer: `{"repository": "Fixture/public", "enabled": false, "active": false}`,
			want:   "slopcop: Fixture/public is disabled; it is not checked\n",
		},
		{
			answer: `{"repository": "Fixture/public", "enabled": true, "active": false}`,
			want:   "slopcop: Fixture/public is enabled but over your workspace's active-repository limit, so it is not checked; choose which repositories are active on the Repositories page in SlopCop\n",
		},
	} {
		isolateUserConfig(t)
		repo := newRepo(t, "https://github.com/Fixture/public.git")
		var recorded repoServer
		server := recorded.start(t, http.StatusOK, tc.answer)

		code, stdout, stderr := runCLI(t, "repo", "status", "--root", repo, "--service", server.URL)
		if code != 0 {
			t.Fatalf("exit = %d, stderr = %s", code, stderr)
		}
		if recorded.path != "/v1/repository" {
			t.Errorf("path = %q", recorded.path)
		}
		if len(recorded.body) != 1 || recorded.body["repoUrl"] != "https://github.com/Fixture/public" {
			t.Errorf("body = %v", recorded.body)
		}
		if stdout != tc.want {
			t.Errorf("stdout = %q, want %q", stdout, tc.want)
		}
	}
}

func TestRepoPrintsTheServiceRemedy(t *testing.T) {
	isolateUserConfig(t)
	atTerminal(t, true, "Fixture/public\n")
	repo := newRepo(t, "https://github.com/Fixture/public.git")
	var recorded repoServer
	server := recorded.start(t, http.StatusForbidden, `{
		"code": "owner_required",
		"message": "Only your workspace's owner can enable or disable a repository.",
		"remedy": "Ask your workspace's owner to change it.",
		"retryable": false
	}`)

	code, stdout, stderr := runCLI(t, "repo", "disable", "--root", repo, "--service", server.URL)
	if code != 2 {
		t.Fatalf("exit = %d", code)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q", stdout)
	}
	want := "Type Fixture/public to disable it: Only your workspace's owner can enable or disable a repository.\nAsk your workspace's owner to change it.\n"
	if stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
}

func TestRepoNeedsAnOrigin(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "")
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	defer server.Close()

	code, stdout, _ := runCLI(t, "repo", "disable", "--root", repo, "--service", server.URL)
	if code != 2 || stdout != "" {
		t.Errorf("exit = %d, stdout = %q", code, stdout)
	}
	if called {
		t.Error("a checkout without an origin reached the service")
	}
}
