package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/BrokkAi/slopcop/internal/service"
)

// claudeHarness is the harness name Claude Code's attribution carries.
const claudeHarness = "claude-code"

// hookState is what the Stop hook remembers about one repository in one
// Claude Code session.
type hookState struct {
	// Active says the workspace checks this repository, so the hook may
	// send a check of it.
	Active bool `json:"active,omitempty"`
	// Off says the hook sends nothing more in this session: the workspace
	// does not check the repository, or the service refused in a way that
	// sending again will not change.
	Off bool `json:"off,omitempty"`
	// Fingerprint names the last check that completed in this session, by
	// the hook or by hand; Status and Findings are its result.
	Fingerprint string            `json:"fingerprint,omitempty"`
	Status      string            `json:"status,omitempty"`
	Findings    []service.Finding `json:"findings,omitempty"`
	// Handed says the hook already gave the agent or the user those
	// findings.
	Handed bool `json:"handed,omitempty"`
	// Others is how many new findings outside the session's files the user
	// was last told of, so the same count is not repeated every turn.
	Others int `json:"others,omitempty"`
}

// completed records the result of a check.
func (s *hookState) completed(fingerprint string, response service.Response) {
	s.Active = true
	s.Fingerprint, s.Status, s.Handed = fingerprint, response.Status, false
	s.Findings = nil
	for _, finding := range response.Findings {
		if finding.New {
			s.Findings = append(s.Findings, finding)
		}
	}
}

func fingerprintCheck(serviceURL, token, workspace string, request service.Request) string {
	credential := sha256.Sum256([]byte(token))
	body := struct {
		ServiceURL string             `json:"serviceUrl"`
		Credential string             `json:"credential"`
		Workspace  string             `json:"workspace,omitempty"`
		RepoURL    string             `json:"repoUrl"`
		BaseCommit string             `json:"baseCommit"`
		Change     string             `json:"change"`
		Selection  *service.Selection `json:"selection,omitempty"`
		FailOn     string             `json:"failOn,omitempty"`
	}{
		ServiceURL: serviceURL,
		Credential: hex.EncodeToString(credential[:]),
		Workspace:  workspace,
		RepoURL:    request.RepoURL,
		BaseCommit: request.BaseCommit,
		Change:     request.Change,
		Selection:  request.Selection,
		FailOn:     request.FailOn,
	}
	encoded, _ := json.Marshal(body)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// rememberCheck records a check run by hand in the Claude Code session that
// ran it, so that session's Stop hook does not send the same check again.
func rememberCheck(prepared preparedCheck, response service.Response) {
	attributed := prepared.request.Attribution
	if attributed == nil || attributed.Harness != claudeHarness {
		return
	}
	path := hookStatePath(attributed.SessionID, prepared.repoRoot)
	state := loadHookState(path)
	state.completed(prepared.fingerprint, response)
	saveHookState(path, state)
}

// hookStatePath is where the state of one repository in one session is
// kept, or empty when there is no cache directory to keep it in.
func hookStatePath(sessionID, repoRoot string) string {
	if sessionID == "" {
		return ""
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	identity := claudeHarness + "\x00" + sessionID + "\x00" + repoRoot
	digest := sha256.Sum256([]byte(identity))
	return filepath.Join(dir, "slopcop", "checks", hex.EncodeToString(digest[:])+".json")
}

// loadHookState reads the state at path. Missing or unreadable state is
// empty: the hook then checks as if it had not run before.
func loadHookState(path string) hookState {
	var state hookState
	if path == "" {
		return state
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &state) != nil {
		return hookState{}
	}
	return state
}

// saveHookState keeps state at path, best effort.
func saveHookState(path string, state hookState) {
	if path == "" {
		return
	}
	data, err := json.Marshal(state)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return
	}
	_ = os.Chmod(path, 0o600)
}
