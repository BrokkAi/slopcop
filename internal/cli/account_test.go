package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/service"
)

const (
	personalID = "11111111-1111-4111-8111-111111111111"
	teamID     = "22222222-2222-4222-8222-222222222222"
	widgetsID  = "33333333-3333-4333-8333-333333333333"
	gadgetsID  = "44444444-4444-4444-8444-444444444444"
)

const whoamiBody = `{
	"account": {"id": "a", "username": "dave", "displayName": "Dave", "email": "dave@example.com"},
	"workspace": {"id": "` + personalID + `", "name": "Dave", "kind": "personal", "role": "owner", "current": true},
	"plan": "free",
	"workspaces": [
		{"id": "` + personalID + `", "name": "Dave", "kind": "personal", "role": "owner", "current": true},
		{"id": "` + teamID + `", "name": "Acme team", "kind": "team", "role": "member", "current": false}
	]
}`

const repositoriesBody = `{
	"workspaceId": "` + personalID + `",
	"activeRepositoryLimit": 1,
	"activeRepositories": 1,
	"canManageRepositories": true,
	"repositories": [
		{"id": "` + widgetsID + `", "provider": "github", "fullName": "Acme/widgets", "enabled": true, "active": true},
		{"id": "` + gadgetsID + `", "provider": "github", "fullName": "Acme/gadgets", "enabled": false, "active": false}
	]
}`

type accountCall struct {
	method        string
	path          string
	authorization string
	body          map[string]any
}

// accountService answers the account routes from fixed bodies and records
// every call. answer, when set, replaces the answer to a POST.
func accountService(t *testing.T, answer func(w http.ResponseWriter, r *http.Request, body map[string]any)) (*httptest.Server, *[]accountCall) {
	t.Helper()
	calls := &[]accountCall{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := accountCall{method: r.Method, path: r.URL.Path, authorization: r.Header.Get("Authorization")}
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&call.body); err != nil {
				t.Errorf("decode request: %v", err)
			}
		}
		*calls = append(*calls, call)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/whoami":
			_, _ = w.Write([]byte(whoamiBody))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/repositories":
			_, _ = w.Write([]byte(repositoriesBody))
		case r.Method == http.MethodPost && answer != nil:
			answer(w, r, call.body)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/workspace":
			_, _ = w.Write([]byte(`{"workspaceId": "` + call.body["workspaceId"].(string) + `"}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server, calls
}

func TestWhoamiPrintsTheWorkspaceThatBills(t *testing.T) {
	isolateUserConfig(t)
	t.Setenv("SLOPCOP_TOKEN", "secret")
	server, calls := accountService(t, nil)

	code, stdout, stderr := runCLI(t, "whoami", "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	want := "Account:   dave <dave@example.com>\n" +
		"Workspace: Dave (personal, owner)\n" +
		"Plan:      Free\n" +
		"Service:   " + server.URL + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if len(*calls) != 1 || (*calls)[0].authorization != "Bearer secret" {
		t.Errorf("calls = %+v", *calls)
	}

	code, stdout, _ = runCLI(t, "whoami", "--format", "json", "--service", server.URL)
	var decoded map[string]any
	if code != 0 || json.Unmarshal([]byte(stdout), &decoded) != nil {
		t.Fatalf("exit = %d, stdout = %q", code, stdout)
	}
	if decoded["service"] != server.URL || decoded["plan"] != "free" {
		t.Errorf("json = %v", decoded)
	}
	if workspace := decoded["workspace"].(map[string]any); workspace["id"] != personalID {
		t.Errorf("workspace = %v", workspace)
	}
	if _, listed := decoded["workspaces"]; listed {
		t.Error("whoami json lists every workspace; that is workspace list's job")
	}
}

func TestWorkspaceListMarksThisLogin(t *testing.T) {
	isolateUserConfig(t)
	server, _ := accountService(t, nil)

	code, stdout, stderr := runCLI(t, "workspace", "list", "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "* Dave ") || !strings.HasPrefix(lines[1], "  Acme team ") {
		t.Fatalf("stdout = %q", stdout)
	}
	if !strings.Contains(lines[1], "member") || !strings.HasSuffix(lines[1], teamID) {
		t.Errorf("team line = %q", lines[1])
	}
}

func TestWorkspaceUseBindsTheLoginByName(t *testing.T) {
	isolateUserConfig(t)
	server, calls := accountService(t, nil)

	code, stdout, stderr := runCLI(t, "workspace", "use", "acme TEAM", "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if want := "slopcop: this login now checks in, and bills, Acme team (team)\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	last := (*calls)[len(*calls)-1]
	if last.method != http.MethodPost || last.path != "/v1/workspace" || last.body["workspaceId"] != teamID || len(last.body) != 1 {
		t.Errorf("last call = %+v", last)
	}

	// By id too.
	code, _, _ = runCLI(t, "workspace", "use", personalID, "--service", server.URL)
	if code != 0 || (*calls)[len(*calls)-1].body["workspaceId"] != personalID {
		t.Errorf("by id: exit = %d, calls = %+v", code, *calls)
	}
}

func TestWorkspaceUseRefusesAnUnknownName(t *testing.T) {
	isolateUserConfig(t)
	server, calls := accountService(t, nil)

	code, stdout, stderr := runCLI(t, "workspace", "use", "Nope", "--service", server.URL)
	if code != 2 || stdout != "" || !strings.Contains(stderr, `no workspace of your account is named "Nope"`) {
		t.Errorf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	for _, call := range *calls {
		if call.method == http.MethodPost {
			t.Errorf("an unknown name reached the service: %+v", call)
		}
	}
}

func TestFindWorkspaceNeedsAnIDForADuplicateName(t *testing.T) {
	_, err := findWorkspace([]service.Workspace{
		{ID: personalID, Name: "Acme"},
		{ID: teamID, Name: "acme"},
	}, "ACME")
	if err == nil || !strings.Contains(err.Error(), personalID) || !strings.Contains(err.Error(), teamID) {
		t.Errorf("err = %v", err)
	}
}

func TestRepoListShowsEachState(t *testing.T) {
	isolateUserConfig(t)
	server, _ := accountService(t, nil)

	code, stdout, stderr := runCLI(t, "repo", "list", "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("stdout = %q", stdout)
	}
	if !strings.HasPrefix(lines[0], "Acme/widgets") || !strings.Contains(lines[0], "active") {
		t.Errorf("widgets = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "Acme/gadgets") || !strings.Contains(lines[1], "disabled") {
		t.Errorf("gadgets = %q", lines[1])
	}
	if lines[2] != "1 of 1 active." {
		t.Errorf("limit = %q", lines[2])
	}

	code, stdout, _ = runCLI(t, "repo", "list", "--format", "json", "--service", server.URL)
	var decoded map[string]any
	if code != 0 || json.Unmarshal([]byte(stdout), &decoded) != nil || len(decoded["repositories"].([]any)) != 2 {
		t.Errorf("json: exit = %d, stdout = %q", code, stdout)
	}
}

func TestRepoDisableAndEnableSendTheRepositoryID(t *testing.T) {
	isolateUserConfig(t)
	// Enabling answers not active: the workspace's one active slot is taken.
	server, calls := accountService(t, func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
		enabled, _ := json.Marshal(body["enabled"])
		_, _ = w.Write([]byte(`{"repositoryId": "` + body["repositoryId"].(string) +
			`", "fullName": "Acme/gadgets", "enabled": ` + string(enabled) + `, "active": false}`))
	})

	code, stdout, stderr := runCLI(t, "repo", "disable", "acme/GADGETS", "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if stdout != "slopcop: disabled Acme/gadgets\n" {
		t.Errorf("stdout = %q", stdout)
	}
	last := (*calls)[len(*calls)-1]
	if last.path != "/v1/repositories/state" || last.body["repositoryId"] != gadgetsID || last.body["enabled"] != false {
		t.Errorf("last call = %+v", last)
	}

	code, stdout, _ = runCLI(t, "repo", "enable", gadgetsID, "--service", server.URL)
	if code != 0 || stdout != "slopcop: enabled Acme/gadgets, but it is past the workspace's active limit\n" {
		t.Errorf("exit = %d, stdout = %q", code, stdout)
	}
	if last := (*calls)[len(*calls)-1]; last.body["enabled"] != true {
		t.Errorf("last call = %+v", last)
	}
}

func TestRepoEnablePrintsTheServiceRemedy(t *testing.T) {
	isolateUserConfig(t)
	server, _ := accountService(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code": "owner_required", "message": "Only the workspace's owner can enable or disable its repositories.", "remedy": "Ask the workspace's owner, or switch to a workspace you own.", "retryable": false}`))
	})

	code, stdout, stderr := runCLI(t, "repo", "enable", "Acme/widgets", "--service", server.URL)
	if code != 2 || stdout != "" {
		t.Fatalf("exit = %d, stdout = %q", code, stdout)
	}
	want := "Only the workspace's owner can enable or disable its repositories.\n" +
		"Ask the workspace's owner, or switch to a workspace you own.\n" +
		"Switch with `slopcop workspace use <name>`.\n"
	if stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
}

func TestRepoRefusesAnUnknownRepository(t *testing.T) {
	isolateUserConfig(t)
	server, calls := accountService(t, nil)

	code, _, stderr := runCLI(t, "repo", "disable", "Acme/nope", "--service", server.URL)
	if code != 2 || !strings.Contains(stderr, `this workspace has no repository "Acme/nope"`) {
		t.Errorf("exit = %d, stderr = %q", code, stderr)
	}
	for _, call := range *calls {
		if call.method == http.MethodPost {
			t.Errorf("an unknown repository reached the service: %+v", call)
		}
	}
}

func TestAccountCommandsRejectAnUnknownFormat(t *testing.T) {
	isolateUserConfig(t)
	for _, args := range [][]string{{"whoami"}, {"workspace", "list"}, {"repo", "list"}} {
		code, _, stderr := runCLI(t, append(args, "--format", "sarif", "--service", "https://example.invalid")...)
		if code != 2 || !strings.Contains(stderr, "use human or json") {
			t.Errorf("%v: exit = %d, stderr = %q", args, code, stderr)
		}
	}
}
