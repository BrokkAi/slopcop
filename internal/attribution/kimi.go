package attribution

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/BrokkAi/slopcop/internal/service"
)

// KimiHarness is the harness name a Kimi Code CLI session's attribution
// carries.
const KimiHarness = "kimi-code"

// kimiSessionVariable is not a variable Kimi Code sets: its agent's commands
// carry nothing that names the session, so only its Stop hook, whose payload
// has session_id, can find it. DetectHook passes that id under this name.
const kimiSessionVariable = "SLOPCOP_KIMI_SESSION_ID"

// The Kimi Code CLI session format below is read from its source,
// github.com/MoonshotAI/kimi-code at 131c21a (2026-11-05, Kimi Code 2.5.1,
// wire protocol 1.5), and has not yet been checked against a live session:
//
//   - Data lives in $KIMI_CODE_HOME, ~/.kimi-code by default
//     (agent-core-v2/src/app/bootstrap/bootstrap.ts).
//   - A session lives in sessions/<workspace id>/<session id>/, and
//     session_index.jsonl maps each sessionId to its workDir
//     (workspace/sessionLifecycle/sessionLifecycleService.ts).
//   - Each agent, main and every subagent, writes
//     agents/<agent id>/wire.jsonl, one durable event per line:
//     {"type":…,"agentId":…,…payload,"time":…} (app/event/event2.ts).
//   - profile.bind and config.update carry the agent's modelAlias and
//     thinkingEffort (config.update's legacy name is thinkingLevel) when they
//     set or change them (agent/profile/profileOps.ts).
//   - context.append_loop_event carries each tool call as
//     {"type":"tool.call","toolCallId":…,"name":…,"args":{…}} and its outcome
//     as {"type":"tool.result","toolCallId":…,"result":{"isError":…}}
//     (agent/loop/loopService.ts, agent/contextMemory/loopEventFold.ts).
//   - The file-writing tools are Edit and Write, whose args.path is absolute
//     or relative to the working directory (agent/tools/edit,
//     agent/tools/os/write).
//
// The model is the alias the user's config.toml names it by, such as
// kimi-code/k3, and the effort is Kimi's thinking effort as recorded: a level,
// or "on" or "off". A session migrated from the older Kimi CLI records its
// history differently and is not read.

var (
	kimiProfileMarker = []byte(`"modelAlias"`)
	kimiThinkMarker   = []byte(`"thinking`)
	kimiLoopMarker    = []byte(`"context.append_loop_event"`)
)

// kimiEditTools are the tools that write a file.
var kimiEditTools = map[string]bool{"Edit": true, "Write": true}

// kimiLine is the part of a wire.jsonl line this package reads.
type kimiLine struct {
	Type           string `json:"type"`
	ModelAlias     string `json:"modelAlias"`
	ThinkingEffort string `json:"thinkingEffort"`
	ThinkingLevel  string `json:"thinkingLevel"`
	Event          struct {
		Type       string          `json:"type"`
		ToolCallID string          `json:"toolCallId"`
		Name       string          `json:"name"`
		Args       json.RawMessage `json:"args"`
		Result     struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	} `json:"event"`
}

// kimiFrom reads the attribution of the Kimi Code session its Stop hook
// names. The payload carries no transcript path, so the session is looked
// for under the Kimi Code home.
func kimiFrom(getenv func(string) string, repoRoot, _ string) (*service.Attribution, []string) {
	sessionID := strings.TrimSpace(getenv(kimiSessionVariable))
	if !sessionIDPattern.MatchString(sessionID) || strings.Trim(sessionID, ".") == "" {
		return nil, nil
	}
	attribution := &service.Attribution{Harness: KimiHarness, SessionID: sessionID}
	home := strings.TrimSpace(getenv("KIMI_CODE_HOME"))
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return attribution, nil
		}
		home = filepath.Join(userHome, ".kimi-code")
	}
	wires, _ := filepath.Glob(filepath.Join(home, "sessions", "*", sessionID, "agents", "*", "wire.jsonl"))
	workDir := kimiWorkDir(home, sessionID)
	var edits []fileEdit
	for _, wire := range wires {
		edits = append(edits, kimiEdits(wire, workDir)...)
	}
	attribution.Models = models(repoRoot, edits)
	return attribution, wires
}

// kimiWorkDir is the working directory session_index.jsonl records for the
// session, or empty.
func kimiWorkDir(home, sessionID string) string {
	workDir := ""
	readRecord(filepath.Join(home, "session_index.jsonl"), func(line []byte) {
		var entry struct {
			SessionID string `json:"sessionId"`
			WorkDir   string `json:"workDir"`
		}
		if json.Unmarshal(line, &entry) == nil && entry.SessionID == sessionID && filepath.IsAbs(entry.WorkDir) {
			workDir = entry.WorkDir
		}
	})
	return workDir
}

// kimiEdits returns the files the successful Edit and Write calls in one
// agent's wire wrote, each with the model and effort the agent ran with when
// it made the call.
func kimiEdits(path, workDir string) []fileEdit {
	var current modelKey
	pending := map[string]fileEdit{}
	var edits []fileEdit
	readRecord(path, func(line []byte) {
		if !bytes.Contains(line, kimiProfileMarker) && !bytes.Contains(line, kimiThinkMarker) &&
			!bytes.Contains(line, kimiLoopMarker) {
			return
		}
		var entry kimiLine
		if json.Unmarshal(line, &entry) != nil {
			return
		}
		switch entry.Type {
		case "profile.bind", "config.update":
			if entry.ModelAlias != "" {
				current.model = entry.ModelAlias
			}
			if entry.ThinkingEffort != "" {
				current.effort = entry.ThinkingEffort
			} else if entry.ThinkingLevel != "" {
				current.effort = entry.ThinkingLevel
			}
		case "context.append_loop_event":
			event := entry.Event
			switch {
			case event.Type == "tool.call" && kimiEditTools[event.Name] && event.ToolCallID != "":
				target := kimiPath(event.Args, workDir)
				if target != "" && current.model != "" {
					pending[event.ToolCallID] = fileEdit{current.model, current.effort, target}
				}
			case event.Type == "tool.result":
				if edit, ok := pending[event.ToolCallID]; ok {
					delete(pending, event.ToolCallID)
					if !event.Result.IsError {
						edits = append(edits, edit)
					}
				}
			}
		}
	})
	return edits
}

// kimiPath is the absolute path an Edit or Write call's args name, or empty.
func kimiPath(args json.RawMessage, workDir string) string {
	var input struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(args, &input) != nil {
		// Arguments recorded as the model's JSON text.
		var text string
		if json.Unmarshal(args, &text) != nil || json.Unmarshal([]byte(text), &input) != nil {
			return ""
		}
	}
	return resolvePath(workDir, input.Path)
}
