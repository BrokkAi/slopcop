package cli

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/BrokkAi/slopcop/internal/service"
)

// hostRecorder is a transport that records the host of every request the
// process sends, so a test can show the client reached nothing but the fake
// service.
type hostRecorder struct {
	next  http.RoundTripper
	mu    sync.Mutex
	hosts map[string]bool
}

func (r *hostRecorder) RoundTrip(request *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.hosts[request.URL.Host] = true
	r.mu.Unlock()
	return r.next.RoundTrip(request)
}

func recordHosts(t *testing.T) *hostRecorder {
	t.Helper()
	recorder := &hostRecorder{next: http.DefaultTransport, hosts: map[string]bool{}}
	http.DefaultTransport = recorder
	t.Cleanup(func() { http.DefaultTransport = recorder.next })
	return recorder
}

// stubPackageManagers puts package managers first on PATH that each leave a
// file in the returned directory when run.
func stubPackageManagers(t *testing.T) string {
	t.Helper()
	bin, ran := t.TempDir(), t.TempDir()
	for _, name := range []string{"npm", "npx", "pnpm", "yarn", "cargo", "uv", "pip", "poetry", "go"} {
		marker := filepath.Join(ran, name)
		script, content := filepath.Join(bin, name), "#!/bin/sh\necho ran > '"+marker+"'\n"
		if runtime.GOOS == "windows" {
			script, content = script+".bat", "@echo ran> \""+marker+"\"\r\n"
		}
		if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return ran
}

func TestHookChecksADependencyChangeMadeThroughTheShell(t *testing.T) {
	isolateHook(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "package.json", `{"name":"demo","dependencies":{}}`+"\n")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-qm", "manifest")
	// What `npm install left-pad` leaves behind, with no edit-tool write in
	// the session. The lockfile is not even JSON: the client never reads it.
	writeFile(t, repo, "package.json", `{"name":"demo","dependencies":{"left-pad":"1.3.0"}}`+"\n")
	writeFile(t, repo, "package-lock.json", "not json {\n")
	ran := stubPackageManagers(t)
	hosts := recordHosts(t)
	fake := &fakeService{active: []string{"BrokkAi/slopcop"}, findings: []string{"package-lock.json"}}
	server := fake.start(t, repo)

	output := runTestHook(t, server.URL, stopPayload(repo, false))
	if output.Decision != "block" || !strings.Contains(output.Reason, "Fix package-lock.json") {
		t.Fatalf("output = %+v", output)
	}
	// Stopping again on the same tree sends nothing and does not hold again.
	output = runTestHook(t, server.URL, stopPayload(repo, true))
	if output.Decision != "" || !strings.Contains(output.SystemMessage, "remain") {
		t.Fatalf("held output = %+v", output)
	}
	if output := runTestHook(t, server.URL, stopPayload(repo, false)); output != (hookOutput{}) {
		t.Fatalf("unchanged output = %+v", output)
	}
	if fake.checks != 1 || fake.lists != 1 {
		t.Fatalf("checks = %d, lists = %d, want 1 and 1", fake.checks, fake.lists)
	}

	if entries, err := os.ReadDir(ran); err != nil || len(entries) != 0 {
		t.Fatalf("package managers ran: %v %v", entries, err)
	}
	service, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(hosts.hosts, map[string]bool{service.Host: true}) {
		t.Fatalf("hosts = %v, want only the service at %s", hosts.hosts, service.Host)
	}
}

func TestHookSendsNothingForOtherChangedFiles(t *testing.T) {
	isolateHook(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, ".gitignore", "build/\n")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-qm", "ignore")
	writeFile(t, repo, "README.md", "changed\n")
	writeFile(t, repo, "notes/package.json.bak", "{}\n")
	writeFile(t, repo, "node_modules/left-pad/package.json", "{}\n")
	writeFile(t, repo, "build/package.json", "{}\n")
	fake := &fakeService{active: []string{"BrokkAi/slopcop"}}
	server := fake.start(t, repo)

	if output := runTestHook(t, server.URL, stopPayload(repo, false)); output != (hookOutput{}) {
		t.Fatalf("output = %+v", output)
	}
	if fake.checks != 0 || fake.lists != 0 {
		t.Fatalf("checks = %d, lists = %d, want none", fake.checks, fake.lists)
	}
}

func TestChangedDependencyFilesInAnyDirectory(t *testing.T) {
	repo := newRepo(t, "")
	writeFile(t, repo, "Cargo.toml", "[package]\n")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-qm", "crate")
	writeFile(t, repo, "Cargo.toml", "[package]\nname = \"demo\"\n")
	for _, name := range []string{
		"web/pnpm-lock.yaml", "services/api/go.sum", "requirements.txt", "requirements-dev.txt",
		"py/uv.lock", "py/pyproject.toml", "Cargo.lock", "yarn.lock",
		"requirements.md", "src/main.go", "go.mod.orig",
	} {
		writeFile(t, repo, name, "x\n")
	}
	var got []string
	for path := range changedDependencyFiles(repo) {
		got = append(got, path)
	}
	sort.Strings(got)
	want := []string{
		"Cargo.lock", "Cargo.toml", "py/pyproject.toml", "py/uv.lock", "requirements-dev.txt",
		"requirements.txt", "services/api/go.sum", "web/pnpm-lock.yaml", "yarn.lock",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changed = %v, want %v", got, want)
	}
}

func TestHookHandsBackDependencyAdvisoryEvidence(t *testing.T) {
	isolateHook(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "package.json", `{"name":"demo","dependencies":{}}`+"\n")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-qm", "manifest")
	writeFile(t, repo, "package.json", `{"name":"demo","dependencies":{"lodash":"4.17.20"}}`+"\n")
	direct := true
	fake := &fakeService{
		active:   []string{"BrokkAi/slopcop"},
		findings: []string{"package.json"},
		dependency: &service.Dependency{
			Ecosystem: "npm", Package: "lodash", Version: "4.17.20", Direct: &direct, ManifestPath: "package.json",
			AdvisoryID: "GHSA-29mw-wpgm-hmr9", FixedVersions: []string{"4.17.21"},
			UpgradeCommand: "npm install lodash@4.17.21 --save-exact",
		},
	}
	server := fake.start(t, repo)

	output := runTestHook(t, server.URL, stopPayload(repo, false))
	for _, want := range []string{
		"Package: npm lodash@4.17.20 (direct, in package.json)",
		"Advisory: GHSA-29mw-wpgm-hmr9",
		"Fixed in: 4.17.21",
		"Upgrade: npm install lodash@4.17.21 --save-exact",
	} {
		if output.Decision != "block" || !strings.Contains(output.Reason, want) {
			t.Fatalf("output lacks %q: %+v", want, output)
		}
	}

	// The upgrade to the fixed version is a new tree, and the service no
	// longer reports the advisory: the hook lets the agent go.
	writeFile(t, repo, "package.json", `{"name":"demo","dependencies":{"lodash":"4.17.21"}}`+"\n")
	fake.findings = nil
	if output := runTestHook(t, server.URL, stopPayload(repo, true)); output != (hookOutput{}) {
		t.Fatalf("output after the upgrade = %+v", output)
	}
	if fake.checks != 2 {
		t.Fatalf("checks = %d, want 2", fake.checks)
	}
}

func TestAnIgnoredDependencyFindingIsNotHandedBack(t *testing.T) {
	isolateHook(t)
	// `slopcop ignore` runs inside the agent's session, which names it.
	t.Setenv("CLAUDE_CODE_SESSION_ID", hookSession)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "package.json", `{"name":"demo","dependencies":{}}`+"\n")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-qm", "manifest")
	writeFile(t, repo, "package.json", `{"name":"demo","dependencies":{"lodash":"4.17.20"}}`+"\n")
	fake := &fakeService{
		active:     []string{"BrokkAi/slopcop"},
		findings:   []string{"package.json"},
		dependency: &service.Dependency{Ecosystem: "npm", Package: "lodash", Version: "4.17.20", AdvisoryID: "GHSA-29mw-wpgm-hmr9"},
	}
	server := fake.start(t, repo)

	if output := runTestHook(t, server.URL, stopPayload(repo, false)); output.Decision != "block" {
		t.Fatalf("first output = %+v", output)
	}
	t.Chdir(repo)
	if code, _, stderr := runCLI(t, "ignore", fakeFindingID(0), "--service", server.URL); code != 0 {
		t.Fatalf("ignore exit = %d, stderr = %s", code, stderr)
	}
	if output := runTestHook(t, server.URL, stopPayload(repo, true)); output != (hookOutput{}) {
		t.Fatalf("output after the ignore = %+v", output)
	}
}
