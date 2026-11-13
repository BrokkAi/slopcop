package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BrokkAi/slopcop/internal/attribution"
	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/gitx"
	"github.com/BrokkAi/slopcop/internal/report"
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
	Fingerprint string `json:"fingerprint,omitempty"`
	// OmittedScope tracks disclosure separately from the hosted request:
	// artifact-only changes must update the notice without resending analysis.
	OmittedScope string            `json:"omittedScope,omitempty"`
	Status       string            `json:"status,omitempty"`
	Findings     []service.Finding `json:"findings,omitempty"`
	// Handed says the hook already gave the agent or the user those
	// findings.
	Handed bool `json:"handed,omitempty"`
	// Holds counts the stops in a row the hook held the agent back at. A
	// stop that follows no hold ends the run.
	Holds int `json:"holds,omitempty"`
	// Others is how many new findings outside the session's files the user
	// was last told of, so the same count is not repeated every turn.
	Others int `json:"others,omitempty"`
	// ServiceNamed is the service, other than the hosted one, the user was
	// last told the hook's checks go to, so it is said once and not every
	// turn.
	ServiceNamed string `json:"serviceNamed,omitempty"`
}

// namingService adds to output, once in a session for a given service, that
// the hook's check went to a service other than the hosted one. The hook
// sends the change with no one watching, so this is where a default that was
// moved would otherwise go unseen. The user reads the system message; a
// held agent is told too, in the reason, which is all some harnesses pass
// on.
func (s *hookState) namingService(serviceURL string, output hookOutput) hookOutput {
	origin := config.Origin(serviceURL)
	if origin == config.DefaultServiceURL || origin == s.ServiceNamed {
		return output
	}
	s.ServiceNamed = origin
	notice := fmt.Sprintf("slopcop: the automatic check uses service %s, not %s.", origin, config.DefaultServiceURL)
	if output.Decision == "block" {
		output.Reason = boundedHookText(notice + "\n" + output.Reason)
	}
	output.SystemMessage = boundedHookText(notice + "\n" + output.SystemMessage)
	return output
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
		if finding.New && finding.Severity != "info" {
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
		Certainty  string             `json:"certainty,omitempty"`
	}{
		ServiceURL: serviceURL,
		Credential: hex.EncodeToString(credential[:]),
		Workspace:  workspace,
		RepoURL:    request.RepoURL,
		BaseCommit: request.BaseCommit,
		Change:     request.Change,
		Selection:  request.Selection,
		FailOn:     request.FailOn,
		Certainty:  request.Certainty,
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
	session := attribution.HookSession(attributed.Harness, attributed.SessionID)
	path := hookStatePath(attributed.Harness, session, prepared.repoRoot)
	state, _ := loadHookState(path)
	state.completed(prepared.workspace, prepared.fingerprint, response)
	state.OmittedScope = omittedScopeDigest(prepared.omittedFiles)
	saveHookState(path, state)
}

func omittedScopeDigest(files []report.OmittedFile) string {
	if len(files) == 0 {
		return ""
	}
	encoded, _ := json.Marshal(files)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// forgetFinding drops an ignored finding from the state of the session that
// ignored it, in the repository at dir, so its Stop hook does not hand the
// finding back.
func forgetFinding(dir, findingID string) {
	forgetFindings(dir, func(_ string, finding service.Finding) bool {
		return strings.EqualFold(finding.ID, findingID)
	})
}

// forgetWord drops, as forgetFinding does, the finding findingID names and,
// from a check in the same workspace, every finding whose suggested word that
// workspace's dictionary now holds. workspace is the name or id the command
// was given, empty for the login's own; a check named differently keeps its
// findings until it runs again. findingID may be empty when a word was added
// on its own.
func forgetWord(dir, workspace, findingID, word string) {
	forgetFindings(dir, func(stateWorkspace string, finding service.Finding) bool {
		if findingID != "" && strings.EqualFold(finding.ID, findingID) {
			return true
		}
		return word != "" && strings.EqualFold(stateWorkspace, workspace) &&
			strings.EqualFold(finding.SuggestedWord, word)
	})
}

// forgetFindings drops the findings drop picks, given the workspace their
// check ran in, from the state of the session that runs this command, in the
// repository at dir.
func forgetFindings(dir string, drop func(workspace string, finding service.Finding) bool) {
	sessions := attribution.HookSessions()
	if len(sessions) == 0 {
		return
	}
	repoRoot, err := gitx.Toplevel(dir)
	if err != nil {
		return
	}
	for harness, session := range sessions {
		path := hookStatePath(harness, session, repoRoot)
		state, loaded := loadHookState(path)
		if !loaded {
			continue
		}
		kept := make([]service.Finding, 0, len(state.Findings))
		for _, finding := range state.Findings {
			if !drop(state.Workspace, finding) {
				kept = append(kept, finding)
			}
		}
		if len(kept) != len(state.Findings) {
			state.Findings = kept
			saveHookState(path, state)
		}
	}
}

// forgetCheck drops the last completed check from the state of the session
// that unignored a finding, in the repository at dir, so its next Stop hook
// checks again and hands the unignored finding back instead of answering from
// a state that no longer lists it.
func forgetCheck(dir string) {
	sessions := attribution.HookSessions()
	if len(sessions) == 0 {
		return
	}
	repoRoot, err := gitx.Toplevel(dir)
	if err != nil {
		return
	}
	for harness, session := range sessions {
		path := hookStatePath(harness, session, repoRoot)
		state, loaded := loadHookState(path)
		if !loaded || state.Fingerprint == "" {
			continue
		}
		state.Fingerprint = ""
		state.Handed = false
		saveHookState(path, state)
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

// pruneHookStates drops the state of sessions that are over, at most once a
// day, which a marker file's time keeps.
func pruneHookStates(dir string) {
	marker := filepath.Join(dir, ".pruned")
	if info, err := os.Stat(marker); err == nil && time.Since(info.ModTime()) < 24*time.Hour {
		return
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err == nil && info.Mode().IsRegular() && entry.Name() != ".pruned" && time.Since(info.ModTime()) > hookStateLifetime {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}
