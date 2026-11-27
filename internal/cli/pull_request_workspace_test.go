package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/service"
)

// The service's pullRequestChecks answers the tests use. A personal
// workspace has no name of its own to the service.
const (
	checkedInPersonal = `{"workspaceId": "` + personalID + `", "workspaceName": null, "member": true}`
	checkedInTeam     = `{"workspaceId": "` + teamID + `", "workspaceName": "Acme team", "member": true}`
	checkedNowhere    = `null`
	// strangerID is a workspace of someone else's that checks the pull
	// requests of a repository this account also has.
	strangerID        = "33333333-3333-4333-8333-333333333333"
	checkedByStranger = `{"workspaceId": "` + strangerID + `", "workspaceName": "Globex", "member": false}`
)

// realIgnoreElsewhereNotice makes ignore look up where the checkout's pull
// requests are checked, for tests about that notice.
func realIgnoreElsewhereNotice(t *testing.T) {
	t.Helper()
	original := ignoreElsewhereNotice
	ignoreElsewhereNotice = warnIgnoreElsewhere
	t.Cleanup(func() { ignoreElsewhereNotice = original })
}

func TestRepositoryPullRequestCheckWorkspace(t *testing.T) {
	for _, test := range []struct {
		name      string
		raw       string
		known     bool
		workspace *service.PullRequestCheckWorkspace
	}{
		{name: "not said", raw: "", known: false},
		{name: "nowhere", raw: "null", known: true},
		{name: "personal", raw: checkedInPersonal, known: true, workspace: &service.PullRequestCheckWorkspace{WorkspaceID: personalID, Member: true}},
		{name: "named", raw: checkedByStranger, known: true, workspace: &service.PullRequestCheckWorkspace{WorkspaceID: strangerID, WorkspaceName: "Globex"}},
		{name: "unreadable", raw: `"soon"`, known: false},
		{name: "no id", raw: `{}`, known: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var repository service.Repository
			body := `{"id": "r"}`
			if test.raw != "" {
				body = `{"id": "r", "pullRequestChecks": ` + test.raw + `}`
			}
			if err := json.Unmarshal([]byte(body), &repository); err != nil {
				t.Fatal(err)
			}
			workspace, known := repository.PullRequestCheckWorkspace()
			if known != test.known {
				t.Errorf("known = %v, want %v", known, test.known)
			}
			if (workspace == nil) != (test.workspace == nil) || (workspace != nil && *workspace != *test.workspace) {
				t.Errorf("workspace = %+v, want %+v", workspace, test.workspace)
			}
		})
	}
}

func TestStatusNamesTheWorkspaceThatChecksPullRequests(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/Acme/widgets.git")
	svc := &mapService{
		repositories:      map[string][]string{personalID: {"Acme/widgets"}, teamID: {"Acme/widgets"}},
		pullRequestChecks: checkedInPersonal,
	}
	server := svc.start(t, "")
	mapTo(t, server.URL, "https://github.com/Acme/widgets", teamID, "Acme team")

	code, stdout, stderr := runCLI(t, "status", "--root", repo, "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	for _, want := range []string{
		"Workspace:  Acme team (team, member)\n",
		"Also in:    \"Dave\" (active)\n",
		"PR checks:  \"Dave\", not the workspace above, so local findings and ignores are not theirs; `slopcop check -w \"Dave\"` gives the same answer as the pull request check\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	}

	code, stdout, _ = runCLI(t, "status", "--root", repo, "--format", "json", "--service", server.URL)
	var decoded struct {
		PullRequestChecks *statusPullRequestChecks `json:"pullRequestChecks"`
	}
	if err := json.Unmarshal([]byte(stdout), &decoded); code != 0 || err != nil {
		t.Fatalf("json: exit = %d, err = %v\n%s", code, err, stdout)
	}
	want := statusPullRequestChecks{On: true, WorkspaceID: personalID, WorkspaceName: "Dave", WorkspaceKind: "personal", Member: true, Selector: "Dave"}
	if decoded.PullRequestChecks == nil || *decoded.PullRequestChecks != want {
		t.Errorf("pullRequestChecks = %+v, want %+v", decoded.PullRequestChecks, want)
	}
}

func TestStatusSaysWhenPullRequestsAreCheckedInTheSameWorkspace(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/Acme/widgets.git")
	for _, test := range []struct{ checks, want string }{
		{checkedInPersonal, "PR checks:  the workspace above\n"},
		{checkedNowhere, "PR checks:  not checked: no workspace has Check pull requests on for this repository\n"},
	} {
		// The account's only workspace has the repository: the line is about
		// pull requests, not other workspaces, so it prints all the same.
		svc := &mapService{repositories: map[string][]string{personalID: {"Acme/widgets"}}, pullRequestChecks: test.checks}
		server := svc.start(t, "")
		code, stdout, stderr := runCLI(t, "status", "--root", repo, "--service", server.URL)
		if code != 0 || !strings.Contains(stdout, test.want) || strings.Contains(stdout, "Also in:") {
			t.Errorf("%s: exit = %d, stdout = %q, stderr = %s", test.checks, code, stdout, stderr)
		}
	}
}

func TestStatusAsksTheOtherWorkspacesWhenThisOneLacksTheRepository(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/Acme/widgets.git")
	svc := &mapService{repositories: map[string][]string{teamID: {"Acme/widgets"}}, pullRequestChecks: checkedInTeam}
	server := svc.start(t, "")

	code, stdout, stderr := runCLI(t, "status", "--root", repo, "--service", server.URL)
	want := "PR checks:  \"Acme team\", not the workspace above, so local findings and ignores are not theirs; `slopcop check -w \"Acme team\"` gives the same answer as the pull request check\n"
	if code != 0 || !strings.Contains(stdout, "State:      not added") || !strings.Contains(stdout, want) {
		t.Errorf("exit = %d, stdout = %q, stderr = %s", code, stdout, stderr)
	}
}

func TestStatusNamesAWorkspaceTheAccountIsNotIn(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/Acme/widgets.git")
	svc := &mapService{repositories: map[string][]string{personalID: {"Acme/widgets"}}, pullRequestChecks: checkedByStranger}
	server := svc.start(t, "")

	code, stdout, stderr := runCLI(t, "status", "--root", repo, "--service", server.URL)
	want := "PR checks:  \"Globex\" (" + strangerID + "), a workspace your account is not in, not the workspace above, so local findings and ignores are not theirs; no --workspace of yours reaches it"
	if code != 0 || !strings.Contains(stdout, want) {
		t.Errorf("exit = %d, stdout = %q, stderr = %s", code, stdout, stderr)
	}
}

func TestCheckWarnsWhenPullRequestsAreCheckedInAnotherWorkspace(t *testing.T) {
	isolateUserConfig(t)
	realRepositoryCheck(t)
	repo := newRepo(t, "https://github.com/Acme/widgets.git")
	svc := &mapService{
		repositories:      map[string][]string{personalID: {"Acme/widgets"}, teamID: {"Acme/widgets"}},
		pullRequestChecks: checkedInPersonal,
	}
	server := svc.start(t, "")

	code, _, stderr := runCLI(t, "check", "--root", repo, "-w", "Acme team", "--service", server.URL)
	want := "slopcop: pull requests of Acme/widgets are checked in \"Dave\", not in \"Acme team\", which this check ran in. Findings and ignores belong to one workspace, so the pull request check may report findings this check does not; `slopcop check -w \"Dave\"` gives the same answer as the pull request check.\n"
	if code != 0 || !strings.Contains(stderr, want) {
		t.Errorf("exit = %d, stderr = %q", code, stderr)
	}
	if len(svc.checks) != 1 || svc.checks[0] != teamID {
		t.Errorf("checks ran in %q, want the team workspace", svc.checks)
	}

	// The login's own workspace checks the pull requests too, so a check in
	// it says nothing.
	code, _, stderr = runCLI(t, "check", "--root", repo, "--service", server.URL)
	if code != 0 || strings.Contains(stderr, "pull requests of") {
		t.Errorf("same workspace: exit = %d, stderr = %q", code, stderr)
	}
}

func TestCheckSaysNothingWhenTheServiceDoesNotSay(t *testing.T) {
	isolateUserConfig(t)
	realRepositoryCheck(t)
	repo := newRepo(t, "https://github.com/Acme/widgets.git")
	for _, checks := range []string{"", checkedNowhere} {
		svc := &mapService{repositories: map[string][]string{teamID: {"Acme/widgets"}}, pullRequestChecks: checks}
		server := svc.start(t, "")
		code, _, stderr := runCLI(t, "check", "--root", repo, "-w", teamID, "--service", server.URL)
		if code != 0 || strings.Contains(stderr, "pull requests of") {
			t.Errorf("%q: exit = %d, stderr = %q", checks, code, stderr)
		}
		// Only a warning names the workspaces, so nothing asked for them.
		if svc.whoamis != 0 {
			t.Errorf("%q: whoami calls = %d", checks, svc.whoamis)
		}
	}
}

func TestIgnoreWarnsWhenPullRequestsAreCheckedInAnotherWorkspace(t *testing.T) {
	isolateUserConfig(t)
	realIgnoreElsewhereNotice(t)
	repo := newRepo(t, "https://github.com/Acme/widgets.git")
	svc := &mapService{
		repositories:      map[string][]string{personalID: {"Acme/widgets"}, teamID: {"Acme/widgets"}},
		pullRequestChecks: checkedInPersonal,
	}
	server := svc.start(t, "")
	t.Chdir(repo)

	code, stdout, stderr := runCLI(t, "ignore", findingID, "-w", teamID, "--service", server.URL)
	if code != 0 || !strings.Contains(stdout, "slopcop: ignored finding "+findingID) {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	want := "slopcop: if this finding is from Acme/widgets, the repository checked out here: this ignore applies in \"Acme team\", but pull requests of Acme/widgets are checked in \"Dave\", whose check will still report the finding; a finding has its own id in each workspace, so run `slopcop check -w \"Dave\"` and ignore the ids it prints with `slopcop ignore -w \"Dave\" <id>`.\n"
	if !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
	if len(svc.ignores) != 1 || svc.ignores[0] != teamID {
		t.Errorf("ignores went to %q", svc.ignores)
	}

	code, _, stderr = runCLI(t, "ignore", findingID, "--service", server.URL)
	if code != 0 || strings.Contains(stderr, "pull requests of") {
		t.Errorf("same workspace: exit = %d, stderr = %q", code, stderr)
	}
}

func TestIgnoreNamesAWorkspaceTheAccountIsNotIn(t *testing.T) {
	isolateUserConfig(t)
	realIgnoreElsewhereNotice(t)
	repo := newRepo(t, "https://github.com/Acme/widgets.git")
	svc := &mapService{repositories: map[string][]string{personalID: {"Acme/widgets"}}, pullRequestChecks: checkedByStranger}
	server := svc.start(t, "")
	t.Chdir(repo)

	code, _, stderr := runCLI(t, "ignore", findingID, "--service", server.URL)
	want := "are checked in \"Globex\" (" + strangerID + "), a workspace your account is not in, whose check will still report the finding; no --workspace of yours reaches it, so ask that workspace's owner to ignore it there.\n"
	if code != 0 || !strings.Contains(stderr, want) {
		t.Errorf("exit = %d, stderr = %q", code, stderr)
	}
}

func TestWorkspaceMapNamesTheWorkspaceThatChecksPullRequests(t *testing.T) {
	isolateUserConfig(t)
	repo := newRepo(t, "https://github.com/Acme/widgets.git")
	svc := &mapService{
		repositories:      map[string][]string{personalID: {"Acme/widgets"}, teamID: {"Acme/widgets"}},
		pullRequestChecks: checkedInPersonal,
	}
	server := svc.start(t, "")

	typed(t, "y\n")
	code, _, stderr := runCLI(t, "workspace", "map", "Acme team", "--root", repo, "--service", server.URL)
	want := "slopcop: the mapping affects local checks only; pull request checks are unaffected: pull requests of Acme/widgets are still checked in \"Dave\", with its ignores, not this workspace's\n"
	if code != 0 || !strings.Contains(stderr, want) {
		t.Errorf("exit = %d, stderr = %q", code, stderr)
	}
	_, stdout, _ := runCLI(t, "workspace", "map", "--help")
	if !strings.Contains(stdout, "It affects local checks only") {
		t.Errorf("help = %q", stdout)
	}
}

func TestTheCheckThatAddsARepositoryWarnsWhenPullRequestsAreCheckedElsewhere(t *testing.T) {
	isolateUserConfig(t)
	realRepositoryCheck(t)
	repo := newRepo(t, "https://github.com/Acme/widgets.git")
	// Only the personal workspace has the repository, and checks its pull
	// requests; the team workspace's listing says nothing of it.
	svc := &mapService{repositories: map[string][]string{personalID: {"Acme/widgets"}}, pullRequestChecks: checkedInPersonal}
	server := svc.start(t, "")

	code, _, stderr := runCLI(t, "check", "--root", repo, "-w", teamID, "--service", server.URL)
	want := "slopcop: pull requests of Acme/widgets are checked in \"Dave\", not in \"Acme team\", which this check ran in."
	if code != 0 || !strings.Contains(stderr, want) {
		t.Errorf("exit = %d, stderr = %q", code, stderr)
	}
}

func TestAMemberWorkspaceIsReachedByIDWhenTheWorkspacesCannotBeRead(t *testing.T) {
	owner := pullRequestOwner{known: true, workspace: &service.PullRequestCheckWorkspace{WorkspaceID: personalID, Member: true}}
	checks := resolvePullRequestChecks(owner, teamID, nil)
	if checks.Selector != personalID || !checks.elsewhere() {
		t.Fatalf("checks = %+v", checks)
	}
	want := "your personal workspace (" + personalID + "), not the workspace above, so local findings and ignores are not theirs; `slopcop check -w \"" + personalID + "\"` gives the same answer as the pull request check"
	if got := checks.statusLineText(); got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
}

func TestTheCheckThatAddsARepositoryAsksAgainOnceItIsAdded(t *testing.T) {
	isolateUserConfig(t)
	realRepositoryCheck(t)
	repo := newRepo(t, "https://github.com/Acme/widgets.git")
	// Someone else's workspace checks the pull requests, and no workspace of
	// this account has the repository, so only the listing after the add
	// can say so.
	svc := &mapService{repositories: map[string][]string{}, pullRequestChecks: checkedByStranger}
	server := svc.start(t, "")

	code, _, stderr := runCLI(t, "check", "--root", repo, "-w", teamID, "--service", server.URL)
	want := "slopcop: pull requests of Acme/widgets are checked in \"Globex\" (" + strangerID + "), a workspace your account is not in, not in \"Acme team\", which this check ran in."
	if code != 0 || !strings.Contains(stderr, "slopcop: added Acme/widgets") || strings.Count(stderr, want) != 1 {
		t.Errorf("exit = %d, stderr = %q", code, stderr)
	}
}
