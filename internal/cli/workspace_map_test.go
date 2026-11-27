package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/service"
)

// goneID is a workspace the account was once in: archived, or left.
const goneID = "99999999-9999-4999-8999-999999999999"

// mapService is an account with a personal workspace, which the login is
// bound to, and a team one. It answers each request for the workspace its
// header names and records which workspace every check ran in.
type mapService struct {
	// repositories are the active repositories of each workspace, by id.
	repositories map[string][]string
	// whoami replaces the two-workspace account when set.
	whoami string
	// pullRequestChecks is every listed repository's pullRequestChecks, as
	// the service sends it; empty leaves it out, as an older service does.
	pullRequestChecks string
	// ignores are the SlopCop-Workspace headers of the ignores sent.
	ignores []string
	// checks are the SlopCop-Workspace headers of the checks sent; empty is
	// the login's own workspace.
	checks  []string
	whoamis int
	lists   int
	base    string
}

func (s *mapService) start(t *testing.T, repo string) *httptest.Server {
	t.Helper()
	if repo != "" {
		s.base = runGit(t, repo, "rev-parse", "HEAD")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		workspace := r.Header.Get("SlopCop-Workspace")
		if workspace == goneID {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"workspace_not_found","message":"No such workspace.","remedy":"Name one of your workspaces.","retryable":false}`))
			return
		}
		if workspace == "" {
			workspace = personalID
		}
		switch r.URL.Path {
		case "/v1/whoami":
			s.whoamis++
			body := s.whoami
			if body == "" {
				body = whoamiBody
			}
			_, _ = w.Write([]byte(body))
		case "/v1/repositories":
			s.lists++
			listed := service.RepositoryList{WorkspaceID: workspace}
			for _, name := range s.repositories[workspace] {
				listed.Repositories = append(listed.Repositories, service.Repository{
					ID: name, Provider: "github", FullName: name, Enabled: true, Active: true,
					PullRequestChecks: json.RawMessage(s.pullRequestChecks),
				})
			}
			_ = json.NewEncoder(w).Encode(listed)
		case "/v1/ignore":
			s.ignores = append(s.ignores, r.Header.Get("SlopCop-Workspace"))
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			_ = json.NewEncoder(w).Encode(service.IgnoreResponse{FindingID: body["findingId"], Status: "ignored"})
		case "/v1/check":
			s.checks = append(s.checks, r.Header.Get("SlopCop-Workspace"))
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			base, _ := body["baseCommit"].(string)
			added := body["addRepository"] == true
			if added && s.repositories != nil {
				// The check adds the repository, so later listings name it.
				repoURL, _ := body["repoUrl"].(string)
				s.repositories[workspace] = append(s.repositories[workspace], repositoryName(repoURL))
			}
			_ = json.NewEncoder(w).Encode(service.Response{
				Status: service.StatusClean, BaseCommit: base, Applied: true,
				Findings: []service.Finding{}, Incomplete: []service.IncompletePolicy{}, RepositoryAdded: added,
			})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// mapTo writes a mapping straight into the stored configuration, as a
// confirmed 'workspace map' would.
func mapTo(t *testing.T, serviceURL, repoURL, workspaceID, name string) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.MapRepository(serviceURL, repoURL, config.RepositoryWorkspace{ID: workspaceID, Name: name})
	if _, err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
}

func mappedID(t *testing.T, serviceURL, repoURL string) string {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	mapped, _ := cfg.WorkspaceFor(serviceURL, repoURL)
	return mapped.ID
}

func TestTwoCheckoutsOnOneLoginCheckInTheirOwnWorkspaces(t *testing.T) {
	isolateUserConfig(t)
	realRepositoryCheck(t)
	work := newRepo(t, "git@github.com:Acme/widgets.git")
	personal := newRepo(t, "https://github.com/dave/dotfiles.git")
	svc := &mapService{repositories: map[string][]string{
		teamID:     {"Acme/widgets"},
		personalID: {"dave/dotfiles"},
	}}
	server := svc.start(t, "")

	typed(t, "y\n")
	code, _, stderr := runCLI(t, "workspace", "map", "Acme team", "--root", work, "--service", server.URL)
	if code != 0 || !strings.Contains(stderr, `checkouts of Acme/widgets on this machine now check in, and bill, "Acme team"`) {
		t.Fatalf("map: exit = %d, stderr = %q", code, stderr)
	}
	// The origin was written scp-style; the mapping is by its canonical URL,
	// whatever its case.
	if got := mappedID(t, server.URL, "https://github.com/acme/WIDGETS"); got != teamID {
		t.Fatalf("mapping = %q, want %s", got, teamID)
	}

	for _, repo := range []string{work, personal} {
		if code, _, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL); code != 0 {
			t.Fatalf("check %s: exit = %d, stderr = %s", repo, code, stderr)
		}
	}
	if len(svc.checks) != 2 || svc.checks[0] != teamID || svc.checks[1] != "" {
		t.Errorf("checks ran in %q, want the team workspace then the login's own", svc.checks)
	}
	// A mapping is an id, so the mapped check asks for no whoami.
	if svc.whoamis != 1 {
		t.Errorf("whoami calls = %d, want only the one 'workspace map' made", svc.whoamis)
	}
}

func TestWorkspacePrecedenceIsFlagThenEnvironmentThenMappingThenLogin(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/Acme/widgets.git")
	const other = "55555555-5555-4555-8555-555555555555"
	svc := &mapService{}
	server := svc.start(t, "")
	check := func(args ...string) string {
		t.Helper()
		before := len(svc.checks)
		args = append([]string{"check", "--root", repo, "--service", server.URL}, args...)
		if code, _, stderr := runCLI(t, args...); code != 0 {
			t.Fatalf("exit = %d, stderr = %s", code, stderr)
		}
		if len(svc.checks) != before+1 {
			t.Fatalf("checks = %v, want one more", svc.checks)
		}
		return svc.checks[before]
	}
	source := func(args ...string) string {
		t.Helper()
		args = append([]string{"status", "--root", repo, "--format", "json", "--service", server.URL}, args...)
		code, stdout, stderr := runCLI(t, args...)
		if code != 0 {
			t.Fatalf("status: exit = %d, stderr = %s", code, stderr)
		}
		var decoded struct {
			Source    string            `json:"workspaceSource"`
			Workspace service.Workspace `json:"workspace"`
		}
		if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
			t.Fatal(err)
		}
		return decoded.Source + " " + decoded.Workspace.ID
	}

	if got := check(); got != "" {
		t.Errorf("no mapping: ran in %q, want the login's workspace", got)
	}
	if got := source(); got != "login "+personalID {
		t.Errorf("no mapping: status = %q", got)
	}

	mapTo(t, server.URL, "https://github.com/Acme/widgets", teamID, "Acme team")
	if got := check(); got != teamID {
		t.Errorf("mapping: ran in %q, want %s", got, teamID)
	}
	if got := source(); got != "mapping "+teamID {
		t.Errorf("mapping: status = %q", got)
	}

	t.Setenv("SLOPCOP_WORKSPACE", other)
	if got := check(); got != other {
		t.Errorf("environment over mapping: ran in %q, want %s", got, other)
	}
	if got := check("--workspace", personalID); got != personalID {
		t.Errorf("flag over environment and mapping: ran in %q, want %s", got, personalID)
	}
	if got := source("--workspace", personalID); got != "flag "+personalID {
		t.Errorf("flag: status = %q", got)
	}
	t.Setenv("SLOPCOP_WORKSPACE", "Dave")
	if got := source(); got != "env "+personalID {
		t.Errorf("environment: status = %q", got)
	}
}

func TestStatusSaysWhyAWorkspaceWasChosen(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/Acme/widgets.git")
	svc := &mapService{repositories: map[string][]string{teamID: {"Acme/widgets"}}}
	server := svc.start(t, "")
	mapTo(t, server.URL, "https://github.com/Acme/widgets", teamID, "Acme team")

	code, stdout, stderr := runCLI(t, "status", "--root", repo, "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	for _, want := range []string{
		"State:      active\n",
		"Workspace:  Acme team (team, member)\n",
		"Chosen by:  this checkout's mapping (`slopcop workspace map`)\n",
		"Login:      Dave (personal, owner), Free plan\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	}
	// The service tells the login's plan only, which is not the mapped
	// workspace's: no Plan line stands beside "Acme team".
	if strings.Contains(stdout, "Plan:") {
		t.Errorf("stdout = %q, has a Plan line for a workspace whose plan is not known", stdout)
	}

	code, stdout, _ = runCLI(t, "status", "--root", repo, "--format", "json", "--service", server.URL)
	if code != 0 {
		t.Fatalf("json exit = %d", code)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("stdout is not json: %v\n%s", err, stdout)
	}
	if plan, ok := decoded["plan"]; ok {
		t.Errorf("json plan = %s beside the mapped workspace, want none", plan)
	}
	var login struct {
		Workspace struct {
			ID string `json:"id"`
		} `json:"workspace"`
		Plan string `json:"plan"`
	}
	if err := json.Unmarshal(decoded["login"], &login); err != nil {
		t.Fatalf("json login = %s: %v", decoded["login"], err)
	}
	if login.Workspace.ID != personalID || login.Plan != "free" {
		t.Errorf("json login = %s", decoded["login"])
	}

	t.Chdir(repo)
	code, stdout, _ = runCLI(t, "whoami", "--service", server.URL)
	if code != 0 || !strings.Contains(stdout, "Workspace: Dave (personal, owner)\n") ||
		!strings.Contains(stdout, "Checkout:  Acme/widgets checks in, and bills, \"Acme team\" instead") {
		t.Errorf("whoami: exit = %d, stdout = %q", code, stdout)
	}
	// The environment wins over the mapping, and whoami says so.
	t.Setenv("SLOPCOP_WORKSPACE", "Dave")
	if _, stdout, _ := runCLI(t, "whoami", "--service", server.URL); !strings.Contains(stdout, "but $SLOPCOP_WORKSPACE is set and wins") {
		t.Errorf("whoami under the environment: stdout = %q", stdout)
	}
	t.Setenv("SLOPCOP_WORKSPACE", "")
	code, stdout, _ = runCLI(t, "workspace", "mappings", "--service", server.URL)
	if code != 0 || !strings.Contains(stdout, "https://github.com/acme/widgets  Acme team  "+teamID) {
		t.Errorf("mappings: exit = %d, stdout = %q", code, stdout)
	}
}

func TestAStaleMappingFailsClosed(t *testing.T) {
	isolateHook(t)
	realRepositoryCheck(t)
	repo := newRepo(t, "https://github.com/Acme/widgets.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	writeSessionEdits(t, repo, "demo.go")
	// The login's own workspace has the repository too: a check that fell
	// through would run there, and bill it.
	svc := &mapService{repositories: map[string][]string{personalID: {"Acme/widgets"}}}
	server := svc.start(t, "")
	mapTo(t, server.URL, "https://github.com/Acme/widgets", goneID, "Old team")

	const refusal = `This checkout of Acme/widgets is mapped to workspace "Old team" (` + goneID + `), which this login's account cannot use`
	code, stdout, stderr := runCLI(t, "check", "--root", repo, "--format", "json", "--service", server.URL)
	if code == 0 || !strings.Contains(stderr, refusal) || !strings.Contains(stderr, "no other workspace was billed") ||
		!strings.Contains(stderr, "`slopcop workspace unmap`") {
		t.Errorf("check: exit = %d, stderr = %q", code, stderr)
	}
	var document struct {
		Failure struct {
			Code string `json:"code"`
		} `json:"failure"`
	}
	if err := json.Unmarshal([]byte(stdout), &document); err == nil && document.Failure.Code != "" && document.Failure.Code != "workspace_not_found" {
		t.Errorf("failure code = %q, want the service's workspace_not_found", document.Failure.Code)
	}

	code, _, stderr = runCLI(t, "status", "--root", repo, "--service", server.URL)
	if code == 0 || !strings.Contains(stderr, refusal) {
		t.Errorf("status: exit = %d, stderr = %q", code, stderr)
	}
	code, _, stderr = runCLI(t, "ignored", "--root", repo, "--service", server.URL)
	if code == 0 || !strings.Contains(stderr, refusal) {
		t.Errorf("ignored: exit = %d, stderr = %q", code, stderr)
	}

	// The Stop hook says so once and then stays off for the session.
	output := runTestHook(t, server.URL, stopPayload(repo, false))
	if output.Decision != "" || !strings.Contains(output.SystemMessage, "is mapped to workspace") {
		t.Errorf("hook output = %+v", output)
	}
	writeFile(t, repo, "demo.go", "package demo // again\n")
	if output := runTestHook(t, server.URL, stopPayload(repo, false)); output != (hookOutput{}) {
		t.Errorf("second hook output = %+v, want silence", output)
	}
	if len(svc.checks) != 0 {
		t.Errorf("checks ran in %q; a stale mapping must send none", svc.checks)
	}
}

func TestTheHookChecksInTheMappedWorkspace(t *testing.T) {
	isolateHook(t)
	repo := newRepo(t, "https://github.com/Acme/widgets.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	writeSessionEdits(t, repo, "demo.go")
	// Only the team workspace has it: unmapped, the hook would stay quiet.
	svc := &mapService{repositories: map[string][]string{teamID: {"Acme/widgets"}}}
	server := svc.start(t, "")
	mapTo(t, server.URL, "https://github.com/Acme/widgets", teamID, "Acme team")

	runTestHook(t, server.URL, stopPayload(repo, false))
	if len(svc.checks) != 1 || svc.checks[0] != teamID {
		t.Errorf("hook checks ran in %q, want %s", svc.checks, teamID)
	}
}

func TestTheHookSaysWhichWorkspaceItAddedARepositoryTo(t *testing.T) {
	isolateHook(t)
	repo := newRepo(t, "https://github.com/Acme/widgets.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	writeSessionEdits(t, repo, "demo.go")
	// No workspace has it yet, so the hook's first check adds it.
	svc := &mapService{}
	server := svc.start(t, "")

	output := runTestHook(t, server.URL, stopPayload(repo, false))
	if len(svc.checks) != 1 || svc.checks[0] != "" {
		t.Fatalf("checks ran in %q, want one in the login's workspace", svc.checks)
	}
	for _, want := range []string{
		"slopcop: added Acme/widgets to \"Dave\" (the automatic check)",
		"it was chosen by this login's workspace",
		"the user runs `slopcop workspace map <name>` in the checkout at a terminal",
	} {
		if !strings.Contains(output.SystemMessage, want) {
			t.Errorf("system message = %q, want %q", output.SystemMessage, want)
		}
	}
	// It is said in the turn the repository was added, and not again.
	svc.repositories = map[string][]string{personalID: {"Acme/widgets"}}
	writeFile(t, repo, "demo.go", "package demo // again\n")
	if output := runTestHook(t, server.URL, stopPayload(repo, false)); strings.Contains(output.SystemMessage, "added Acme/widgets") {
		t.Errorf("second output = %+v, want the add said once", output)
	}
}

func TestOnlyAPersonAtATerminalChangesAMapping(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/Acme/widgets.git")
	svc := &mapService{repositories: map[string][]string{teamID: {"Acme/widgets"}}}
	server := svc.start(t, "")
	const repoURL = "https://github.com/Acme/widgets"

	// An agent's shell: a pipe that even answers yes, and the null device.
	for _, stdin := range []func(){func() { pipedStdin(t, "y\n") }, func() { nullStdin(t) }} {
		stdin()
		code, _, stderr := runCLI(t, "workspace", "map", "Acme team", "--root", repo, "--service", server.URL)
		if code != 2 || !strings.Contains(stderr, "must be confirmed at a terminal") ||
			!strings.Contains(stderr, `pass --workspace "Acme team"`) {
			t.Errorf("map without a terminal: exit = %d, stderr = %q", code, stderr)
		}
		// 'enable' of a repository the workspace already checks only says
		// how to map it.
		code, _, stderr = runCLI(t, "enable", "--root", repo, "-w", "Acme team", "--service", server.URL)
		if code != 0 || !strings.Contains(stderr, "run `slopcop workspace map \"Acme team\"` at a terminal") {
			t.Errorf("enable without a terminal: exit = %d, stderr = %q", code, stderr)
		}
		if got := mappedID(t, server.URL, repoURL); got != "" {
			t.Fatalf("mapping = %q, want none without a terminal", got)
		}
	}
	// A check that names the workspace does not map the checkout either.
	if code, _, stderr := runCLI(t, "check", "--root", repo, "-w", teamID, "--service", server.URL); code != 0 {
		t.Fatalf("check: exit = %d, stderr = %s", code, stderr)
	}
	if got := mappedID(t, server.URL, repoURL); got != "" {
		t.Fatalf("mapping = %q after a check with --workspace, want none", got)
	}

	// At a terminal, anything but yes maps nothing.
	t.Run("a no maps nothing", func(t *testing.T) {
		typed(t, "n\n")
		if code, _, _ := runCLI(t, "workspace", "map", "Acme team", "--root", repo, "--service", server.URL); code != 2 || mappedID(t, server.URL, repoURL) != "" {
			t.Errorf("a no still mapped: exit = %d", code)
		}
	})

	mapTo(t, server.URL, repoURL, teamID, "Acme team")
	pipedStdin(t, "y\n")
	code, _, stderr := runCLI(t, "workspace", "unmap", "--root", repo, "--service", server.URL)
	if code != 2 || !strings.Contains(stderr, "must be confirmed at a terminal") || mappedID(t, server.URL, repoURL) != teamID {
		t.Errorf("unmap without a terminal: exit = %d, stderr = %q", code, stderr)
	}
	typed(t, "yes\n")
	code, stdout, _ := runCLI(t, "workspace", "unmap", "--root", repo, "--service", server.URL)
	if code != 0 || !strings.Contains(stdout, "now checks in the workspace this login is bound to") || mappedID(t, server.URL, repoURL) != "" {
		t.Errorf("unmap: exit = %d, stdout = %q", code, stdout)
	}
}

func TestACheckThatAddsARepositorySaysWhereAndHowToChangeIt(t *testing.T) {
	isolateUserConfig(t)
	realRepositoryCheck(t)
	repo := newRepo(t, "https://github.com/Acme/widgets.git")
	svc := &mapService{}
	server := svc.start(t, "")
	mapTo(t, server.URL, "https://github.com/Acme/widgets", teamID, "Acme team")
	// An agent's shell: nothing is asked, and the check adds the repository.
	pipedStdin(t, "")

	code, stdout, stderr := runCLI(t, "check", "--root", repo, "--format", "json", "--service", server.URL)
	if code != 0 || len(svc.checks) != 1 || svc.checks[0] != teamID {
		t.Fatalf("exit = %d, checks = %q, stderr = %s", code, svc.checks, stderr)
	}
	for _, want := range []string{
		"slopcop: added Acme/widgets to \"Acme team\"\n",
		"it was chosen by this checkout's mapping",
		"the user runs `slopcop workspace map <name>` in the checkout at a terminal",
		"`slopcop repo disable Acme/widgets --workspace " + teamID + "`",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	}
	var document struct {
		RepositoryAdded bool `json:"repositoryAdded"`
		AddedTo         struct {
			ID, Name, Source, Change string
		} `json:"addedToWorkspace"`
	}
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if !document.RepositoryAdded || document.AddedTo.ID != teamID || document.AddedTo.Name != "Acme team" ||
		document.AddedTo.Source != sourceMapping || !strings.Contains(document.AddedTo.Change, "slopcop workspace map") {
		t.Errorf("addedToWorkspace = %+v", document.AddedTo)
	}
}

func TestEnableMapsTheCheckoutToTheWorkspaceItAddsTo(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/Acme/widgets.git")
	svc := &mapService{}
	server := svc.start(t, "")
	pipedStdin(t, "")

	code, _, stderr := runCLI(t, "enable", "--root", repo, "-w", "Acme team", "--service", server.URL)
	if code != 0 {
		t.Fatalf("enable: exit = %d, stderr = %s", code, stderr)
	}
	if got := mappedID(t, server.URL, "https://github.com/Acme/widgets"); got != teamID {
		t.Fatalf("mapping = %q, want %s", got, teamID)
	}
	// A later check names no workspace and still runs where it was enabled.
	if code, _, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL); code != 0 {
		t.Fatalf("check: exit = %d, stderr = %s", code, stderr)
	}
	if len(svc.checks) != 2 || svc.checks[0] != teamID || svc.checks[1] != teamID {
		t.Errorf("checks ran in %q, want both in %s", svc.checks, teamID)
	}
}

func TestAnUnmappedCheckoutIsNotedOnce(t *testing.T) {
	isolateUserConfig(t)
	realUnmappedNotice(t)
	repo := newRepo(t, "https://github.com/Acme/widgets.git")
	other := newRepo(t, "https://github.com/dave/dotfiles.git")
	svc := &mapService{repositories: map[string][]string{
		personalID: {"Acme/widgets", "dave/dotfiles"},
		teamID:     {"Acme/widgets"},
	}}
	server := svc.start(t, "")

	// In another of the account's workspaces too: the check still runs.
	code, _, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL)
	if code != 0 || len(svc.checks) != 1 || svc.checks[0] != "" {
		t.Fatalf("exit = %d, checks = %q, stderr = %s", code, svc.checks, stderr)
	}
	for _, want := range []string{
		`note: Acme/widgets is also in your workspace "Acme team", and this check runs in, and bills, "Dave"`,
		"run `slopcop workspace map <name>` at a terminal",
		"This note is shown once.",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	}
	asked := svc.whoamis + svc.lists
	code, _, stderr = runCLI(t, "check", "--root", repo, "--service", server.URL)
	if code != 0 || strings.Contains(stderr, "note:") || svc.whoamis+svc.lists != asked {
		t.Errorf("second check: exit = %d, lookups %d -> %d, stderr = %q", code, asked, svc.whoamis+svc.lists, stderr)
	}

	// Nowhere else, but the account has more than one workspace.
	code, _, stderr = runCLI(t, "check", "--root", other, "--service", server.URL)
	if code != 0 || !strings.Contains(stderr, `note: dave/dotfiles is not mapped to a workspace, so it checks in, and bills, "Dave"`) ||
		!strings.Contains(stderr, "your account has 2 workspaces") {
		t.Errorf("unmapped: exit = %d, stderr = %q", code, stderr)
	}

	// A mapped checkout, or a workspace named for the command, is a choice
	// already made.
	mapTo(t, server.URL, "https://github.com/dave/dotfiles", personalID, "Dave")
	forgetWorkspaceNotices()
	_, _, stderr = runCLI(t, "check", "--root", other, "--service", server.URL)
	_, _, flagged := runCLI(t, "check", "--root", repo, "-w", teamID, "--service", server.URL)
	if strings.Contains(stderr, "note:") || strings.Contains(flagged, "note:") {
		t.Errorf("a chosen workspace was still noted: %q %q", stderr, flagged)
	}
}

func TestAnAccountWithOneWorkspaceIsToldNothing(t *testing.T) {
	isolateUserConfig(t)
	realUnmappedNotice(t)
	repo := newRepo(t, "https://github.com/dave/dotfiles.git")
	svc := &mapService{whoami: `{
		"account": {"id": "a", "username": "dave", "displayName": "Dave", "email": "dave@example.com"},
		"workspace": {"id": "` + personalID + `", "name": "Dave", "kind": "personal", "role": "owner", "current": true},
		"plan": "free",
		"workspaces": [{"id": "` + personalID + `", "name": "Dave", "kind": "personal", "role": "owner", "current": true}]
	}`}
	server := svc.start(t, "")

	for range 2 {
		code, _, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL)
		if code != 0 || strings.Contains(stderr, "note:") {
			t.Errorf("exit = %d, stderr = %q", code, stderr)
		}
	}
	if svc.whoamis != 1 {
		t.Errorf("whoami calls = %d, want the account asked once", svc.whoamis)
	}
}

func TestStatusNamesTheOtherWorkspacesThatHaveTheRepository(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/Acme/widgets.git")
	svc := &mapService{repositories: map[string][]string{personalID: {"Acme/widgets"}, teamID: {"Acme/widgets"}}}
	server := svc.start(t, "")
	mapTo(t, server.URL, "https://github.com/Acme/widgets", teamID, "Acme team")

	code, stdout, stderr := runCLI(t, "status", "--root", repo, "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	for _, want := range []string{
		"Workspace:  Acme team (team, member)\n",
		"Also in:    \"Dave\" (active)\n",
		"PR checks:  turned on per workspace in the web app, not by the workspace above; a pull request's SlopCop check names the workspace that ran it\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	}

	code, stdout, _ = runCLI(t, "status", "--root", repo, "--format", "json", "--service", server.URL)
	var decoded struct {
		OtherWorkspaces []statusElsewhere `json:"otherWorkspaces"`
	}
	if err := json.Unmarshal([]byte(stdout), &decoded); code != 0 || err != nil {
		t.Fatalf("json: exit = %d, err = %v\n%s", code, err, stdout)
	}
	want := statusElsewhere{ID: personalID, Name: "Dave", Kind: "personal", Role: "owner", State: statusActive}
	if len(decoded.OtherWorkspaces) != 1 || decoded.OtherWorkspaces[0] != want {
		t.Errorf("otherWorkspaces = %+v, want %+v", decoded.OtherWorkspaces, want)
	}
}

func TestStatusSaysNothingOfWorkspacesWithoutTheRepository(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/Acme/widgets.git")
	svc := &mapService{repositories: map[string][]string{personalID: {"Acme/widgets"}}}
	server := svc.start(t, "")

	code, stdout, stderr := runCLI(t, "status", "--root", repo, "--service", server.URL)
	if code != 0 || strings.Contains(stdout, "Also in:") || strings.Contains(stdout, "PR checks:") {
		t.Errorf("exit = %d, stdout = %q, stderr = %s", code, stdout, stderr)
	}
	code, stdout, _ = runCLI(t, "status", "--root", repo, "--format", "json", "--service", server.URL)
	if code != 0 || strings.Contains(stdout, "otherWorkspaces") {
		t.Errorf("json: exit = %d, stdout = %q", code, stdout)
	}
}

func TestStatusNamesAWorkspaceThatCouldNotBeAsked(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/Acme/widgets.git")
	svc := &mapService{
		repositories: map[string][]string{personalID: {"Acme/widgets"}},
		whoami:       strings.Replace(whoamiBody, teamID, goneID, 1),
	}
	server := svc.start(t, "")

	code, stdout, stderr := runCLI(t, "status", "--root", repo, "--service", server.URL)
	if code != 0 || !strings.Contains(stdout, "Also in:    \"Acme team\" (could not be asked)\n") {
		t.Errorf("exit = %d, stdout = %q, stderr = %s", code, stdout, stderr)
	}
}

func TestWorkspaceMapSaysPullRequestChecksAreUnaffected(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/Acme/widgets.git")
	svc := &mapService{repositories: map[string][]string{teamID: {"Acme/widgets"}}}
	server := svc.start(t, "")

	typed(t, "y\n")
	code, _, stderr := runCLI(t, "workspace", "map", "Acme team", "--root", repo, "--service", server.URL)
	if code != 0 || !strings.Contains(stderr, "pull request checks are unaffected: they run in whichever workspace has Check pull requests on for Acme/widgets in the web app") {
		t.Errorf("exit = %d, stderr = %q", code, stderr)
	}
	_, stdout, _ := runCLI(t, "workspace", "map", "--help")
	if !strings.Contains(stdout, "Pull request checks are unaffected.") {
		t.Errorf("help = %q", stdout)
	}

	// Pull request checks are GitHub's, so a GitLab repository is told nothing.
	gitlab := newRepo(t, "https://gitlab.com/Acme/widgets.git")
	typed(t, "y\n")
	code, _, stderr = runCLI(t, "workspace", "map", "Acme team", "--root", gitlab, "--service", server.URL)
	if code != 0 || strings.Contains(stderr, "pull request checks") {
		t.Errorf("gitlab: exit = %d, stderr = %q", code, stderr)
	}
}
