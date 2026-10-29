package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BrokkAi/slopcop/internal/attribution"
	"github.com/BrokkAi/slopcop/internal/gitx"
	"github.com/BrokkAi/slopcop/internal/service"
)

// hookStateLifetime is how long the state of a session is kept after it was
// last written. A session idle that long is over.
const hookStateLifetime = 7 * 24 * time.Hour

// hookState is what the Stop hook remembers about one repository in one
// Claude Code or Codex session.
type hookState struct {
	// Workspace is the workspace Active and Off are about, as a check names
	// it: --workspace or $SLOPCOP_WORKSPACE, or empty for the login's own.
	Workspace string `json:"workspace,omitempty"`
	// Active says the workspace checks this repository, so the hook may
	// send a check of it.
	Active bool `json:"active,omitempty"`
	// Off says the hook sends nothing more in this session: the workspace
	// does not check the repository, or the service refused in a way that
	// sending again will not change. A check that completes turns it back
	// on.
	Off bool `json:"off,omitempty"`
	// Fingerprint names the last check that completed in this session, by
	// the hook or by hand; Status and Findings are its result.
	Fingerprint string            `json:"fingerprint,omitempty"`
	Status      string            `json:"status,omitempty"`
	Findings    []service.Finding `json:"findings,omitempty"`
	// Handed says the hook already gave the agent or the user those
	// findings.
	Handed bool `json:"handed,omitempty"`
	// Holds counts the stops in a row the hook held the agent back at. A
	// stop that follows no hold ends the run.
	Holds int `json:"holds,omitempty"`
	// Others is how many new findings outside the session's files the user
	// was last told of, so the same count is not repeated every turn.
	Others int `json:"others,omitempty"`
}

// inWorkspace points the state at the workspace the next check runs in.
// What it knew of another workspace's repositories says nothing of this one.
func (s *hookState) inWorkspace(workspace string) {
	if s.Workspace != workspace {
		s.Workspace, s.Active, s.Off = workspace, false, false
	}
}

// completed records the result of a check in workspace. The repository is
// evidently checked there, and findings already handed over for the same
// tree are not handed over again.
func (s *hookState) completed(workspace, fingerprint string, response service.Response) {
	s.Workspace, s.Active, s.Off = workspace, true, false
	if fingerprint != s.Fingerprint {
		s.Handed = false
	}
	s.Fingerprint, s.Status = fingerprint, response.Status
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

// rememberCheck records a check run by hand in the session that ran it, so
// that session's Stop hook does not send the same check again.
func rememberCheck(prepared preparedCheck, response service.Response) {
	attributed := prepared.request.Attribution
	if attributed == nil {
		return
	}
	path := hookStatePath(attributed.Harness, attributed.SessionID, prepared.repoRoot)
	state, _ := loadHookState(path)
	state.completed(prepared.workspace, prepared.fingerprint, response)
	saveHookState(path, state)
}

// sessionVariables name the variable each harness puts its session id in,
// in the commands its agent runs.
var sessionVariables = map[string]string{
	attribution.ClaudeHarness: "CLAUDE_CODE_SESSION_ID",
	attribution.CodexHarness:  "CODEX_THREAD_ID",
}

// forgetFinding drops an ignored finding from the state of the session that
// ignored it, in the repository at dir, so its Stop hook does not hand the
// finding back.
func forgetFinding(dir, findingID string) {
	repoRoot, err := gitx.Toplevel(dir)
	if err != nil {
		return
	}
	for harness, variable := range sessionVariables {
		path := hookStatePath(harness, strings.TrimSpace(os.Getenv(variable)), repoRoot)
		state, loaded := loadHookState(path)
		if !loaded {
			continue
		}
		kept := make([]service.Finding, 0, len(state.Findings))
		for _, finding := range state.Findings {
			if !strings.EqualFold(finding.ID, findingID) {
				kept = append(kept, finding)
			}
		}
		if len(kept) != len(state.Findings) {
			state.Findings = kept
			saveHookState(path, state)
		}
	}
}

// hookStatePath is where the state of one repository in one session is
// kept, or empty when there is no cache directory to keep it in.
func hookStatePath(harness, sessionID, repoRoot string) string {
	if harness == "" || sessionID == "" {
		return ""
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	identity := harness + "\x00" + sessionID + "\x00" + repoRoot
	digest := sha256.Sum256([]byte(identity))
	return filepath.Join(dir, "slopcop", "checks", hex.EncodeToString(digest[:])+".json")
}

// loadHookState reads the state at path, and says whether there was one.
// Missing or unreadable state is empty: the hook then checks as if it had
// not run before.
func loadHookState(path string) (hookState, bool) {
	var state hookState
	if path == "" {
		return state, false
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &state) != nil {
		return hookState{}, false
	}
	return state, true
}

// saveHookState keeps state at path, best effort, and drops the state of
// sessions that are over.
func saveHookState(path string, state hookState) {
	if path == "" {
		return
	}
	data, err := json.Marshal(state)
	if err != nil {
		return
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return
	}
	_ = os.Chmod(path, 0o600)
	pruneHookStates(dir)
}

func pruneHookStates(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err == nil && info.Mode().IsRegular() && time.Since(info.ModTime()) > hookStateLifetime {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}
