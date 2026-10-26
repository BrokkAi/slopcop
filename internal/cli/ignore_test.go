package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const findingID = "0f8fad5b-d9cb-469f-a165-70867728950e"

func TestIgnoreSendsTheFindingID(t *testing.T) {
	isolateUserConfig(t)
	t.Setenv("SLOPCOP_TOKEN", "secret")
	var captured map[string]any
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/ignore" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		authorization = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"findingId": "` + findingID + `", "status": "ignored"}`))
	}))
	defer server.Close()

	code, stdout, stderr := runCLI(t, "ignore", findingID, "--service", server.URL)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if len(captured) != 1 || captured["findingId"] != findingID {
		t.Errorf("body = %v", captured)
	}
	if authorization != "Bearer secret" {
		t.Errorf("authorization = %q", authorization)
	}
	if want := "slopcop: ignored finding " + findingID + "; later checks of this repository will not report it\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestIgnorePrintsTheServiceRemedy(t *testing.T) {
	isolateUserConfig(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{
			"code": "finding_not_found",
			"message": "SlopCop has no such finding for this login.",
			"remedy": "Use a finding id from a check you ran with this login.",
			"retryable": false
		}`))
	}))
	defer server.Close()

	code, stdout, stderr := runCLI(t, "ignore", findingID, "--service", server.URL)
	if code != 2 {
		t.Fatalf("exit = %d", code)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q", stdout)
	}
	want := "SlopCop has no such finding for this login.\nUse a finding id from a check you ran with this login.\n"
	if stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
}

func TestIgnoreAddsTheLoginHint(t *testing.T) {
	isolateUserConfig(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code": "auth_required", "message": "This needs a SlopCop login.", "remedy": "Sign in, then retry.", "retryable": false}`))
	}))
	defer server.Close()

	code, _, stderr := runCLI(t, "ignore", findingID, "--service", server.URL)
	if code != 2 || !strings.Contains(stderr, "Sign in with `slopcop login`.") {
		t.Errorf("exit = %d, stderr = %q", code, stderr)
	}
}

func TestIgnoreRejectsAnIDThatIsNotAUUID(t *testing.T) {
	isolateUserConfig(t)
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	defer server.Close()

	for _, id := range []string{"not-a-uuid", "0f8fad5b-d9cb-469f-a165-70867728950", "../0f8fad5b-d9cb-469f-a165-70867728950e"} {
		code, stdout, stderr := runCLI(t, "ignore", id, "--service", server.URL)
		if code != 2 || stdout != "" || !strings.Contains(stderr, "is not a UUID") {
			t.Errorf("%s: exit = %d, stdout = %q, stderr = %q", id, code, stdout, stderr)
		}
	}
	if code, _, _ := runCLI(t, "ignore", "--service", server.URL); code != 2 {
		t.Errorf("no id: exit = %d", code)
	}
	if called {
		t.Error("an invalid id reached the service")
	}
}
