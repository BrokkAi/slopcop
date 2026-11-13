package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// dictionaryServer answers the dictionary routes and records each request.
type dictionaryServer struct {
	method, path, workspace string
	body                    map[string]any
	calls                   int
}

func (d *dictionaryServer) start(t *testing.T, answer string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.calls++
		d.method, d.path, d.workspace = r.Method, r.URL.EscapedPath(), r.Header.Get("SlopCop-Workspace")
		d.body = nil
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&d.body); err != nil {
				t.Errorf("decode request: %v", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestDictionaryAddSendsTheFindingID(t *testing.T) {
	isolateUserConfig(t)
	fake := &dictionaryServer{}
	server := fake.start(t, `{"word": "parsr", "status": "added"}`)

	code, stdout, stderr := runCLI(t, "dictionary", "add", findingID, "-w", teamID, "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if fake.method != http.MethodPost || fake.path != "/v1/dictionary" || fake.workspace != teamID {
		t.Errorf("request = %s %s in %q", fake.method, fake.path, fake.workspace)
	}
	if len(fake.body) != 1 || fake.body["findingId"] != findingID {
		t.Errorf("body = %v", fake.body)
	}
	if want := "slopcop: added \"parsr\" to the workspace dictionary; later checks will not report it as misspelled\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestDictionaryAddSendsAWord(t *testing.T) {
	isolateUserConfig(t)
	fake := &dictionaryServer{}
	// A service that does not echo the word still gets the one sent named.
	server := fake.start(t, `{"status": "added"}`)

	code, stdout, stderr := runCLI(t, "dictionary", "add", "--word", "Kubectl", "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if len(fake.body) != 1 || fake.body["word"] != "Kubectl" {
		t.Errorf("body = %v", fake.body)
	}
	if !strings.Contains(stdout, `added "Kubectl" to the workspace dictionary`) {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestDictionaryAddRefusesBadArguments(t *testing.T) {
	isolateUserConfig(t)
	fake := &dictionaryServer{}
	server := fake.start(t, `{}`)

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{}, "pass the finding id a check printed, or --word"},
		{[]string{findingID, "--word", "parsr"}, "not both"},
		{[]string{"not-a-uuid"}, "is not a UUID"},
		{[]string{"--word", "parse_r"}, "is one identifier part of ASCII letters"},
		{[]string{"--word", "camelCase"}, "is one identifier part of ASCII letters"},
		{[]string{"--word", "naïve"}, "is one identifier part of ASCII letters"},
		{[]string{"--word", strings.Repeat("a", 65)}, "at most 64 letters"},
	} {
		args := append([]string{"dictionary", "add"}, tc.args...)
		code, stdout, stderr := runCLI(t, append(args, "--service", server.URL)...)
		if code != 2 || stdout != "" || !strings.Contains(stderr, tc.want) {
			t.Errorf("%v: exit = %d, stdout = %q, stderr = %q", tc.args, code, stdout, stderr)
		}
	}
	if fake.calls != 0 {
		t.Errorf("a refused argument reached the service %d time(s)", fake.calls)
	}
}

func TestDictionaryAddPrintsTheServiceRemedy(t *testing.T) {
	isolateUserConfig(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"code": "no_suggested_word", "message": "That finding has no word to add.", "remedy": "Use a misspelled-identifier finding.", "retryable": false}`))
	}))
	defer server.Close()

	code, stdout, stderr := runCLI(t, "dictionary", "add", findingID, "--service", server.URL)
	if code != 2 || stdout != "" ||
		!strings.HasSuffix(stderr, "That finding has no word to add.\nUse a misspelled-identifier finding.\n") {
		t.Errorf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
}

func TestDictionaryListPrintsTheWords(t *testing.T) {
	isolateUserConfig(t)
	fake := &dictionaryServer{}
	server := fake.start(t, `{"words": [
		{"word": "parsr", "addedBy": {"username": "dave", "displayName": "Dave"}, "sourceFindingId": "`+findingID+`", "createdAt": "2026-11-12T08:30:00Z"},
		{"word": "kubectl\u001b[31m", "addedBy": null, "sourceFindingId": null, "createdAt": "2026-11-13T09:00:00Z"}
	]}`)

	code, stdout, stderr := runCLI(t, "dictionary", "list", "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if fake.method != http.MethodGet || fake.path != "/v1/dictionary" {
		t.Errorf("request = %s %s", fake.method, fake.path)
	}
	want := "slopcop: 2 words in the workspace dictionary\n" +
		"  parsr (added 2026-11-12 08:30 UTC by Dave (@dave) from finding " + findingID + ")\n" +
		"  kubectl\ufffd[31m (added 2026-11-13 09:00 UTC by an unrecorded account)\n" +
		"slopcop: remove one with `slopcop dictionary remove <word>`\n"
	if stdout != want {
		t.Errorf("stdout = %q\nwant     %q", stdout, want)
	}

	code, stdout, _ = runCLI(t, "dictionary", "list", "--format", "json", "--service", server.URL)
	var listed struct {
		Words []map[string]any `json:"words"`
	}
	if err := json.Unmarshal([]byte(stdout), &listed); code != 0 || err != nil || len(listed.Words) != 2 || listed.Words[0]["word"] != "parsr" {
		t.Errorf("json exit = %d, err = %v, stdout = %q", code, err, stdout)
	}
}

func TestDictionaryListSaysWhenEmpty(t *testing.T) {
	isolateUserConfig(t)
	server := (&dictionaryServer{}).start(t, `{"words": []}`)

	code, stdout, _ := runCLI(t, "dictionary", "list", "--service", server.URL)
	if code != 0 || stdout != "slopcop: the workspace dictionary is empty\n" {
		t.Errorf("exit = %d, stdout = %q", code, stdout)
	}
}

func TestDictionaryRemoveDeletesTheWord(t *testing.T) {
	isolateUserConfig(t)
	fake := &dictionaryServer{}
	server := fake.start(t, ``)

	code, stdout, stderr := runCLI(t, "dictionary", "remove", "parsr", "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if fake.method != http.MethodDelete || fake.path != "/v1/dictionary/parsr" {
		t.Errorf("request = %s %s", fake.method, fake.path)
	}
	if want := "slopcop: removed \"parsr\" from the workspace dictionary; later checks report it as misspelled again\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	// A word that is not one never reaches the service, so it cannot name
	// another path.
	calls := fake.calls
	for _, word := range []string{"../ignore", "a/b", "par sr"} {
		if code, _, stderr := runCLI(t, "dictionary", "remove", word, "--service", server.URL); code != 2 ||
			!strings.Contains(stderr, "is one identifier part of ASCII letters") {
			t.Errorf("%q: exit = %d, stderr = %q", word, code, stderr)
		}
	}
	if fake.calls != calls {
		t.Error("an invalid word reached the service")
	}
}
