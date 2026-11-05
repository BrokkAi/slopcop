package attribution

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/BrokkAi/slopcop/internal/service"
)

// CopilotHarness is the harness name a GitHub Copilot CLI session's
// attribution carries.
const CopilotHarness = "copilot-cli"

// maxJournalFiles bounds how many journal files of one session are read when
// a journal links to the one before it.
const maxJournalFiles = 16

var (
	copilotEditMarker  = []byte(`"fileEdits"`)
	copilotStartMarker = []byte(`"tool.execution_start"`)
	copilotStateMarker = []byte(`"session.`)
	copilotAgentMarker = []byte(`"subagent.configured"`)
)

// copilotEvent is the part of a session event this package reads. Copilot
// CLI is not open source, so the schema is the one its SDK publishes:
// github/copilot-sdk nodejs/src/generated/session-events.ts at
// 99d90edf504f824c77dcdf0faa978f4b35848a87, generated from
// session-events.schema.json for Copilot CLI 1.0.106-1. That schema says
// which events are persisted to events.jsonl; that a persisted line has the
// same shape is not yet checked against a live session. fileEdits is marked
// experimental there and appeared in 1.0.92. A field is a pointer where
// absent and null mean different things.
type copilotEvent struct {
	Type string `json:"type"`
	// AgentID names the subagent that emitted the event; it is absent for
	// the main agent.
	AgentID string `json:"agentId"`
	Data    struct {
		// session.start
		CopilotVersion string `json:"copilotVersion"`
		// session.journal_link
		PreviousFile string `json:"previousFile"`
		// session.start, session.resume and session.model_change: the
		// effort of later model calls, and the model it is bound to when
		// it came with a model selection.
		ReasoningEffort      *string `json:"reasoningEffort"`
		ReasoningEffortModel string  `json:"reasoningEffortModel"`
		// tool.execution_start, tool.execution_complete and
		// subagent.configured.
		Model      string `json:"model"`
		ToolCallID string `json:"toolCallId"`
		// tool.execution_complete: the files a built-in edit tool
		// committed, with absolute paths, even when a later mutation of
		// the same call failed.
		FileEdits []struct {
			Kind string `json:"kind"`
			Path string `json:"path"`
		} `json:"fileEdits"`
	} `json:"data"`
}

// copilot reads the attribution of a check run inside a GitHub Copilot CLI
// session. Copilot CLI puts COPILOT_AGENT_SESSION_ID in the commands the
// agent runs, and the session's events.jsonl records the model of every tool
// call and the files each built-in edit committed.
func copilot(getenv func(string) string, repoRoot string) (*service.Attribution, []string) {
	sessionID := strings.TrimSpace(getenv("COPILOT_AGENT_SESSION_ID"))
	if !sessionIDPattern.MatchString(sessionID) || strings.Trim(sessionID, ".") == "" {
		return nil, nil
	}
	attribution := &service.Attribution{Harness: CopilotHarness, SessionID: sessionID}
	home := strings.TrimSpace(getenv("COPILOT_HOME"))
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return attribution, nil
		}
		home = filepath.Join(userHome, ".copilot")
	}
	dir := filepath.Join(home, "session-state", sessionID)
	journals := copilotJournals(dir)
	var edits []fileEdit
	for _, journal := range journals {
		found, version := copilotEdits(journal)
		edits = append(edits, found...)
		if attribution.HarnessVersion == "" {
			attribution.HarnessVersion = version
		}
	}
	attribution.Models = models(repoRoot, edits)
	return attribution, journals
}

// copilotJournals are the session's journal files that exist, oldest first:
// events.jsonl, and the numbered archives a session.journal_link at its start
// names, once Copilot CLI has split the journal.
func copilotJournals(dir string) []string {
	var journals []string
	seen := map[string]bool{}
	name := "events.jsonl"
	for len(journals) < maxJournalFiles && name != "" && !seen[name] {
		seen[name] = true
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			break
		}
		journals = append([]string{path}, journals...)
		name = ""
		readFirst(path, func(line []byte) {
			var event copilotEvent
			if json.Unmarshal(line, &event) != nil || event.Type != "session.journal_link" {
				return
			}
			// The archive is in the same directory; a name that leaves it
			// is not followed.
			previous := event.Data.PreviousFile
			if previous != "" && filepath.Base(previous) == previous && previous != "." && previous != ".." {
				name = previous
			}
		})
	}
	return journals
}

// copilotEdits returns the files the edit tool calls in one journal
// committed, each with the model that made the call and the effort it ran
// at, and the Copilot CLI version the journal's session.start names. A
// delete writes nothing. Effort is the session's, as session.start,
// session.resume and session.model_change last set it, for the main agent,
// and the one subagent.configured resolved for a subagent; it is left out
// when it is bound to another model than the call's.
func copilotEdits(path string) ([]fileEdit, string) {
	type effortState struct{ effort, model string }
	var main effortState
	subagents := map[string]effortState{}
	started := map[string]string{}
	version := ""
	var edits []fileEdit
	readRecord(path, func(line []byte) {
		if !bytes.Contains(line, copilotEditMarker) && !bytes.Contains(line, copilotStartMarker) &&
			!bytes.Contains(line, copilotStateMarker) && !bytes.Contains(line, copilotAgentMarker) {
			return
		}
		var event copilotEvent
		if json.Unmarshal(line, &event) != nil {
			return
		}
		data := event.Data
		switch event.Type {
		case "session.start", "session.resume", "session.model_change":
			if event.AgentID != "" {
				return
			}
			if event.Type == "session.start" && version == "" {
				version = data.CopilotVersion
			}
			// A model change that names no effort leaves it as it was;
			// one that names null clears it.
			if data.ReasoningEffort != nil || event.Type != "session.model_change" {
				main = effortState{"", data.ReasoningEffortModel}
				if data.ReasoningEffort != nil {
					main.effort = *data.ReasoningEffort
				}
			}
		case "subagent.configured":
			if event.AgentID != "" && data.ReasoningEffort != nil {
				subagents[event.AgentID] = effortState{*data.ReasoningEffort, data.Model}
			}
		case "tool.execution_start":
			if data.ToolCallID != "" && data.Model != "" {
				started[data.ToolCallID] = data.Model
			}
		case "tool.execution_complete":
			model := data.Model
			if model == "" {
				model = started[data.ToolCallID]
			}
			if model == "" {
				return
			}
			state := main
			if event.AgentID != "" {
				state = subagents[event.AgentID]
			}
			effort := state.effort
			if state.model != "" && state.model != model {
				effort = ""
			}
			for _, edit := range data.FileEdits {
				if edit.Kind != "delete" && edit.Path != "" {
					edits = append(edits, fileEdit{model, effort, edit.Path})
				}
			}
		}
	})
	return edits, version
}
