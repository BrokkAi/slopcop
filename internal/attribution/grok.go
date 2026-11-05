package attribution

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/BrokkAi/slopcop/internal/service"
)

// GrokHarness is the harness name a Grok Build session's attribution carries.
const GrokHarness = "grok"

// The Grok Build session format below is read from its source,
// github.com/xai-org/grok-build at 2bdd1d6 (2026-09-29), and has not yet been
// checked against a live session:
//
//   - The agent's commands, and hooks, get GROK_SESSION_ID
//     (xai-grok-shell/src/session/agent_rebuild.rs).
//   - A session lives in $GROK_HOME/sessions/<encoded cwd>/<session id>/, with
//     GROK_HOME defaulting to ~/.grok (xai-dirs, xai-grok-config/src/paths.rs).
//   - updates.jsonl holds every ACP session/update the session sent:
//     {"method":"session/update","params":{"sessionId":…,"update":{…}}}. A file
//     edit is a tool_call or tool_call_update with "kind":"edit", the file in
//     "locations", and a final "status" of "completed" or "failed"
//     (session/acp_session_impl/tool_calls.rs, session/acp_conversion.rs). The
//     user_message_chunk that opens each turn carries the turn's model in
//     _meta.modelId (acp_session_impl/turn.rs).
//   - chat_history.jsonl holds the model's conversation, one item per line;
//     each assistant item carries the model_id and reasoning_effort of its
//     response and the ids of its tool calls, the same ids updates.jsonl uses
//     (xai-grok-sampling-types/src/conversation.rs). Compaction rewrites it,
//     so an edit from before a compaction falls back to the turn's model with
//     no effort.
//   - summary.json holds info.cwd, the directory relative paths resolve
//     against.
//
// Grok's subagents run in sessions of their own, which are not read yet.

var (
	grokToolCallMarker  = []byte(`"tool_call`)
	grokUserChunkMarker = []byte(`"user_message_chunk"`)
	grokAssistantMarker = []byte(`"assistant"`)
)

// grokUpdate is the part of an updates.jsonl line this package reads.
type grokUpdate struct {
	Method string `json:"method"`
	Params struct {
		Update struct {
			SessionUpdate string `json:"sessionUpdate"`
			ToolCallID    string `json:"toolCallId"`
			Kind          string `json:"kind"`
			Status        string `json:"status"`
			Locations     []struct {
				Path string `json:"path"`
			} `json:"locations"`
			Meta struct {
				ModelID string `json:"modelId"`
			} `json:"_meta"`
		} `json:"update"`
	} `json:"params"`
}

// grokAssistant is the part of a chat_history.jsonl line this package reads.
type grokAssistant struct {
	Type            string `json:"type"`
	ModelID         string `json:"model_id"`
	ReasoningEffort string `json:"reasoning_effort"`
	ToolCalls       []struct {
		ID string `json:"id"`
	} `json:"tool_calls"`
}

// grok reads the attribution of a check run inside a Grok Build session,
// which puts GROK_SESSION_ID in the commands its agent runs.
func grok(getenv func(string) string, repoRoot string) (*service.Attribution, []string) {
	return grokFrom(getenv, repoRoot, "")
}

// grokFrom is grok reading the session whose updates.jsonl is at transcript,
// as a hook payload names it; without one, the session is looked for under
// the Grok home.
func grokFrom(getenv func(string) string, repoRoot, transcript string) (*service.Attribution, []string) {
	sessionID := strings.TrimSpace(getenv("GROK_SESSION_ID"))
	if !sessionIDPattern.MatchString(sessionID) || strings.Trim(sessionID, ".") == "" {
		return nil, nil
	}
	attribution := &service.Attribution{Harness: GrokHarness, SessionID: sessionID}
	var dir string
	if filepath.IsAbs(transcript) && filepath.Base(transcript) == "updates.jsonl" &&
		filepath.Base(filepath.Dir(transcript)) == sessionID {
		dir = filepath.Dir(transcript)
	} else {
		home := strings.TrimSpace(getenv("GROK_HOME"))
		if home == "" {
			userHome, err := os.UserHomeDir()
			if err != nil {
				return attribution, nil
			}
			home = filepath.Join(userHome, ".grok")
		}
		dirs, _ := filepath.Glob(filepath.Join(home, "sessions", "*", sessionID))
		if len(dirs) == 0 {
			return attribution, nil
		}
		dir = dirs[0]
	}
	updates := filepath.Join(dir, "updates.jsonl")
	history := filepath.Join(dir, "chat_history.jsonl")
	attribution.Models = models(repoRoot, grokEdits(updates, history, grokCwd(dir)))
	return attribution, []string{updates, history}
}

// grokCwd is the working directory summary.json in dir records, or empty.
func grokCwd(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		return ""
	}
	var summary struct {
		Info struct {
			Cwd string `json:"cwd"`
		} `json:"info"`
	}
	if json.Unmarshal(data, &summary) != nil || !filepath.IsAbs(summary.Info.Cwd) {
		return ""
	}
	return summary.Info.Cwd
}

// grokEdits returns the files the completed edit tool calls of one session
// wrote, each with the model and effort of the response that made the call.
// A path relative to the session's cwd is resolved against it.
func grokEdits(updates, history, cwd string) []fileEdit {
	responses := map[string]modelKey{}
	readRecord(history, func(line []byte) {
		if !bytes.Contains(line, grokAssistantMarker) {
			return
		}
		var item grokAssistant
		if json.Unmarshal(line, &item) != nil || item.Type != "assistant" || item.ModelID == "" {
			return
		}
		for _, call := range item.ToolCalls {
			responses[call.ID] = modelKey{item.ModelID, item.ReasoningEffort}
		}
	})

	type call struct {
		turnModel, kind, status string
		paths                   []string
	}
	calls := map[string]*call{}
	var order []string
	turnModel := ""
	readRecord(updates, func(line []byte) {
		if !bytes.Contains(line, grokToolCallMarker) && !bytes.Contains(line, grokUserChunkMarker) {
			return
		}
		var entry grokUpdate
		if json.Unmarshal(line, &entry) != nil || entry.Method != "session/update" {
			return
		}
		update := entry.Params.Update
		switch update.SessionUpdate {
		case "user_message_chunk":
			if update.Meta.ModelID != "" {
				turnModel = update.Meta.ModelID
			}
		case "tool_call", "tool_call_update":
			if update.ToolCallID == "" {
				return
			}
			c := calls[update.ToolCallID]
			if c == nil {
				c = &call{turnModel: turnModel}
				calls[update.ToolCallID] = c
				order = append(order, update.ToolCallID)
			}
			if update.Kind != "" {
				c.kind = update.Kind
			}
			if update.Status != "" {
				c.status = update.Status
			}
			if len(update.Locations) > 0 {
				c.paths = c.paths[:0]
				for _, location := range update.Locations {
					c.paths = append(c.paths, location.Path)
				}
			}
		}
	})

	var edits []fileEdit
	for _, id := range order {
		c := calls[id]
		if c.kind != "edit" || c.status != "completed" {
			continue
		}
		key, ok := responses[id]
		if !ok {
			key = modelKey{model: c.turnModel}
		}
		if key.model == "" {
			continue
		}
		for _, path := range c.paths {
			if path = resolvePath(cwd, path); path == "" {
				continue
			}
			edits = append(edits, fileEdit{key.model, key.effort, path})
		}
	}
	return edits
}
