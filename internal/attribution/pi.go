package attribution

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/BrokkAi/slopcop/internal/service"
)

// PiHarness is the harness name a Pi session's attribution carries.
const PiHarness = "pi"

// The schema read here is Pi's session format as its source defines it
// (earendil-works/pi, formerly badlogic/pi-mono, at commit 136a7b2d7d46:
// packages/coding-agent/docs/session-format.md, docs/message-types.md,
// docs/environment-variables.md, src/core/session-manager.ts,
// src/core/tools/{edit,write,bash}.ts). It has not yet been checked against
// a captured session.

// piEditTools are Pi's built-in tools that write a file. Each names it in
// its path argument, relative to the session's directory or absolute.
var piEditTools = map[string]bool{"edit": true, "write": true}

var (
	piToolCallMarker      = []byte(`"toolCall"`)
	piToolResultMarker    = []byte(`"toolResult"`)
	piThinkingLevelMarker = []byte(`"thinking_level_change"`)
)

// piEntry is the part of a session line this package reads: the header's
// directory, the thinking level a change sets, the model, thinking level and
// tool calls of an assistant message, and whether a tool result failed.
type piEntry struct {
	Type          string `json:"type"`
	ID            string `json:"id"`
	Cwd           string `json:"cwd"`
	ThinkingLevel string `json:"thinkingLevel"`
	Message       struct {
		Role          string          `json:"role"`
		Model         string          `json:"model"`
		ThinkingLevel string          `json:"thinkingLevel"`
		Content       json.RawMessage `json:"content"`
		ToolCallID    string          `json:"toolCallId"`
		IsError       bool            `json:"isError"`
		NestedCalls   struct {
			Calls []piNestedCall `json:"calls"`
		} `json:"nestedCalls"`
	} `json:"message"`
}

// piToolCall is one block of an assistant message's content.
type piToolCall struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments struct {
		Path string `json:"path"`
	} `json:"arguments"`
}

// piNestedCall is a tool call another tool made, as codemode's scripts do.
// Its arguments are left out when they are too large.
type piNestedCall struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	Arguments struct {
		Path string `json:"path"`
	} `json:"arguments"`
}

// pi reads the attribution of a check run inside a Pi session. Pi puts
// PI_SESSION_ID and PI_SESSION_FILE in the commands its bash tool runs, and
// the session file records the model and thinking level of every assistant
// message, its tool calls, and whether each tool result failed. Pi has no
// shell-command hook, so there is no Stop hook path.
func pi(getenv func(string) string, repoRoot string) (*service.Attribution, []string) {
	sessionID := strings.TrimSpace(getenv("PI_SESSION_ID"))
	if !sessionIDPattern.MatchString(sessionID) || strings.Trim(sessionID, ".") == "" {
		return nil, nil
	}
	// Pi records no version of itself in the session or the environment.
	attribution := &service.Attribution{Harness: PiHarness, SessionID: sessionID}
	session := piSessionFile(getenv, sessionID)
	if session == "" {
		return attribution, nil
	}
	attribution.Models = models(repoRoot, piEdits(session))
	return attribution, []string{session}
}

// piSessionFile is the session file of sessionID: PI_SESSION_FILE when its
// header names the session, or else the one named for it under the session
// directory. It is empty for an ephemeral session, which keeps no file.
func piSessionFile(getenv func(string) string, sessionID string) string {
	if file := strings.TrimSpace(getenv("PI_SESSION_FILE")); filepath.IsAbs(file) &&
		filepath.Ext(file) == ".jsonl" && piHeader(file).ID == sessionID {
		return file
	}
	dir := strings.TrimSpace(getenv("PI_CODING_AGENT_SESSION_DIR"))
	if dir == "" {
		agentDir := strings.TrimSpace(getenv("PI_CODING_AGENT_DIR"))
		if agentDir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return ""
			}
			agentDir = filepath.Join(home, ".pi", "agent")
		}
		dir = filepath.Join(agentDir, "sessions")
	}
	// Files are <timestamp>_<session id>.jsonl, in a folder per directory.
	for _, pattern := range []string{
		filepath.Join(dir, "*", "*_"+sessionID+".jsonl"),
		filepath.Join(dir, "*_"+sessionID+".jsonl"),
	} {
		files, _ := filepath.Glob(pattern)
		for _, file := range files {
			if piHeader(file).ID == sessionID {
				return file
			}
		}
	}
	return ""
}

// piHeader reads the session line that opens the file at path.
func piHeader(path string) piEntry {
	var header piEntry
	readFirst(path, func(line []byte) {
		if json.Unmarshal(line, &header) != nil || header.Type != "session" {
			header = piEntry{}
		}
	})
	return header
}

// piEdits returns the files the successful edit and write calls in one
// session file wrote, each with the model and thinking level of the
// assistant message that called it. A call codemode made counts when it
// succeeded and its arguments were kept.
func piEdits(path string) []fileEdit {
	type call struct {
		edit fileEdit
		name string
	}
	header := piHeader(path)
	calls := map[string]call{}
	var edits []fileEdit
	// level is the thinking level last set, for messages from releases that
	// did not record their own. It is read in file order, which a branched
	// session only approximates.
	level := ""
	readRecord(path, func(line []byte) {
		if !bytes.Contains(line, piToolCallMarker) && !bytes.Contains(line, piToolResultMarker) &&
			!bytes.Contains(line, piThinkingLevelMarker) {
			return
		}
		var entry piEntry
		if json.Unmarshal(line, &entry) != nil {
			return
		}
		message := entry.Message
		switch {
		case entry.Type == "thinking_level_change":
			level = entry.ThinkingLevel
		case entry.Type == "message" && message.Role == "assistant" && message.Model != "":
			effort := message.ThinkingLevel
			if effort == "" {
				effort = level
			}
			var blocks []piToolCall
			if json.Unmarshal(message.Content, &blocks) != nil {
				return
			}
			for _, block := range blocks {
				if block.Type == "toolCall" && block.ID != "" {
					target := piPath(header.Cwd, block.Arguments.Path)
					calls[block.ID] = call{fileEdit{message.Model, effort, target}, block.Name}
				}
			}
		case entry.Type == "message" && message.Role == "toolResult":
			called, ok := calls[message.ToolCallID]
			if !ok {
				return
			}
			if !message.IsError && piEditTools[called.name] && called.edit.path != "" {
				edits = append(edits, called.edit)
			}
			// A script's calls are real even when the script failed.
			for _, nested := range message.NestedCalls.Calls {
				if nested.Status == "ok" && piEditTools[nested.Name] {
					if target := piPath(header.Cwd, nested.Arguments.Path); target != "" {
						edits = append(edits, fileEdit{called.edit.model, called.edit.effort, target})
					}
				}
			}
		}
	})
	return edits
}

// piPath is the absolute path Pi's tools resolve path to from the session
// directory cwd: without a leading @, with ~ expanded. It is empty when the
// path cannot be resolved.
func piPath(cwd, path string) string {
	path = strings.TrimPrefix(path, "@")
	switch {
	case path == "":
		return ""
	case path == "~" || strings.HasPrefix(path, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		return filepath.Join(home, path[1:])
	case filepath.IsAbs(cwd):
		return resolvePath(cwd, path)
	}
	return resolvePath("", path)
}
