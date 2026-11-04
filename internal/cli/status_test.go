package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStatusShowsAnActiveRepository(t *testing.T) {
	isolateUserConfig(t)
	t.Setenv("SLOPCOP_TOKEN", "secret")
	server, calls := accountService(t, nil)
	repo := newRepo(t, "https://github.com/Acme/widgets.git")

	code, stdout, stderr := runCLI(t, "status", "--root", repo, "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	want := "Repository: Acme/widgets (github)\n" +
		"State:      active\n" +
		"Account:    dave <dave@example.com>\n" +
		"Workspace:  Dave (personal, owner)\n" +
		"Plan:       Free\n" +
		"Service:    " + server.URL + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if len(*calls) != 2 || (*calls)[0].path != "/v1/whoami" || (*calls)[1].path != "/v1/repositories" {
		t.Errorf("calls = %+v", *calls)
	}

	code, stdout, _ = runCLI(t, "status", "--root", repo, "--format", "json", "--service", server.URL)
	if code != 0 {
		t.Fatalf("json exit = %d", code)
	}
	var decoded struct {
		Service    string `json:"service"`
		Plan       string `json:"plan"`
		Repository struct {
			URL      string `json:"url"`
			FullName string `json:"fullName"`
			Provider string `json:"provider"`
			State    string `json:"state"`
			ID       string `json:"id"`
			Enabled  bool   `json:"enabled"`
			Active   bool   `json:"active"`
		} `json:"repository"`
	}
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("stdout is not json: %v\n%s", err, stdout)
	}
	if decoded.Service != server.URL || decoded.Plan != "free" {
		t.Errorf("json = %+v", decoded)
	}
	if decoded.Repository.URL != "https://github.com/Acme/widgets" ||
		decoded.Repository.FullName != "Acme/widgets" ||
		decoded.Repository.Provider != "github" ||
		decoded.Repository.State != statusActive ||
		decoded.Repository.ID != widgetsID ||
		!decoded.Repository.Enabled || !decoded.Repository.Active {
		t.Errorf("repository = %+v", decoded.Repository)
	}
}

func TestStatusShowsADisabledRepository(t *testing.T) {
	isolateUserConfig(t)
	t.Setenv("SLOPCOP_TOKEN", "secret")
	server, _ := accountService(t, nil)
	repo := newRepo(t, "https://github.com/Acme/gadgets.git")

	code, stdout, stderr := runCLI(t, "status", "--root", repo, "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "State:      disabled\n") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestStatusShowsAnEnabledRepositoryOverTheLimit(t *testing.T) {
	isolateUserConfig(t)
	t.Setenv("SLOPCOP_TOKEN", "secret")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/whoami":
			_, _ = w.Write([]byte(whoamiBody))
		case "/v1/repositories":
			_, _ = w.Write([]byte(`{"repositories": [
				{"id": "` + gadgetsID + `", "provider": "github", "fullName": "Acme/gadgets", "enabled": true, "active": false}
			]}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	repo := newRepo(t, "https://github.com/Acme/gadgets.git")

	code, stdout, stderr := runCLI(t, "status", "--root", repo, "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "State:      enabled, over the limit\n") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestStatusShowsARepositoryThatIsNotAdded(t *testing.T) {
	isolateUserConfig(t)
	t.Setenv("SLOPCOP_TOKEN", "secret")
	server, _ := accountService(t, nil)
	repo := newRepo(t, "https://github.com/Acme/newthing.git")

	code, stdout, stderr := runCLI(t, "status", "--root", repo, "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "State:      not added (run 'slopcop enable' from the checkout to add it)\n") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestStatusShowsAnUnsupportedHost(t *testing.T) {
	isolateUserConfig(t)
	t.Setenv("SLOPCOP_TOKEN", "secret")
	server, calls := accountService(t, nil)
	repo := newRepo(t, "https://example.com/Acme/widgets.git")

	code, stdout, stderr := runCLI(t, "status", "--root", repo, "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "Repository: Acme/widgets\n") {
		t.Errorf("stdout = %q", stdout)
	}
	if !strings.Contains(stdout, "SlopCop checks github.com, gitlab.com and bitbucket.org") {
		t.Errorf("stdout = %q", stdout)
	}
	if len(*calls) != 1 || (*calls)[0].path != "/v1/whoami" {
		t.Errorf("status listed repositories for an unsupported host: %+v", *calls)
	}
}

func TestStatusUsesTheWorkspaceFlag(t *testing.T) {
	isolateUserConfig(t)
	t.Setenv("SLOPCOP_TOKEN", "secret")
	server, calls := accountService(t, nil)
	repo := newRepo(t, "https://github.com/Acme/widgets.git")

	code, stdout, stderr := runCLI(t, "status", "--root", repo, "-w", "Acme team", "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "Workspace:  Acme team (team, member)\n") {
		t.Errorf("stdout = %q", stdout)
	}
	last := (*calls)[len(*calls)-1]
	if last.path != "/v1/repositories" || last.workspace != teamID {
		t.Errorf("last call = %+v", last)
	}
}

func TestStatusNeedsAnOrigin(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "")

	code, _, stderr := runCLI(t, "status", "--root", repo, "--service", "https://example.invalid")
	if code != 2 || !strings.Contains(stderr, "no origin remote") {
		t.Errorf("exit = %d, stderr = %q", code, stderr)
	}
}

func TestStatusPrintsTheServiceRemedy(t *testing.T) {
	isolateUserConfig(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code": "auth_required", "message": "Sign in to check a change.", "remedy": "Approve this login in the browser.", "retryable": false}`))
	}))
	t.Cleanup(server.Close)
	repo := newRepo(t, "https://github.com/Acme/widgets.git")

	code, stdout, stderr := runCLI(t, "status", "--root", repo, "--service", server.URL)
	if code != 2 || stdout != "" {
		t.Fatalf("exit = %d, stdout = %q", code, stdout)
	}
	for _, want := range []string{"Sign in to check a change.", "Approve this login in the browser.", "Sign in with `slopcop login`."} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	}
}
