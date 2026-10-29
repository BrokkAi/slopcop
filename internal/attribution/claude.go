package attribution

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BrokkAi/slopcop/internal/service"
)

// ClaudeHarness is the harness name a Claude Code session's attribution
// carries.
const ClaudeHarness = "claude-code"

// maxTranscriptBytes bounds the read of one transcript file.
const maxTranscriptBytes = 1 << 30

// sessionIDPattern is the session id shape the service accepts. It also
// keeps the id from naming a path or a glob.
var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// agentVersionPattern reads the version from Claude Code's AI_AGENT marker,
// claude-code_2-1-312_agent in the agent's commands.
var agentVersionPattern = regexp.MustCompile(`^claude-code_([0-9]+(?:-[0-9]+)*)_`)

// claudeEditTools are the tools that write a file, and so name its author.
var claudeEditTools = map[string]bool{
	"Write":        true,
	"Edit":         true,
	"MultiEdit":    true,
	"NotebookEdit": true,
}

// claudeCode reads the attribution of a check run inside a Claude Code
// session. Claude Code puts CLAUDE_CODE_SESSION_ID in the commands the agent
// runs, and by the time the agent runs a check its transcript holds every edit
// it made: the model that wrote it, the effort, the file and whether it
// succeeded. Subagents keep their own transcripts beside the session's.
func claudeCode(getenv func(string) string, repoRoot string) (*service.Attribution, []string) {
	return claudeCodeFrom(getenv, repoRoot, "")
}

// claudeCodeFrom is claudeCode reading the session transcript at transcript,
// as a hook payload names it. Without one, or with one that is not the
// session's, the transcript is looked for under the configuration directory.
func claudeCodeFrom(getenv func(string) string, repoRoot, transcript string) (*service.Attribution, []string) {
	sessionID := strings.TrimSpace(getenv("CLAUDE_CODE_SESSION_ID"))
	if !sessionIDPattern.MatchString(sessionID) || strings.Trim(sessionID, ".") == "" {
		return nil, nil
	}
	attribution := &service.Attribution{Harness: ClaudeHarness, SessionID: sessionID}
	if match := agentVersionPattern.FindStringSubmatch(getenv("AI_AGENT")); match != nil {
		attribution.HarnessVersion = strings.ReplaceAll(match[1], "-", ".")
	}
	var transcripts []string
	if filepath.IsAbs(transcript) && filepath.Base(transcript) == sessionID+".jsonl" {
		transcripts = []string{transcript}
	} else {
		configDir := strings.TrimSpace(getenv("CLAUDE_CONFIG_DIR"))
		if configDir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return attribution, nil
			}
			configDir = filepath.Join(home, ".claude")
		}
		transcripts, _ = filepath.Glob(filepath.Join(configDir, "projects", "*", sessionID+".jsonl"))
	}
	var files []string
	for _, transcript := range transcripts {
		files = append(files, transcript)
		subagents, _ := filepath.Glob(filepath.Join(strings.TrimSuffix(transcript, ".jsonl"), "subagents", "*.jsonl"))
		files = append(files, subagents...)
	}
	var edits []fileEdit
	for _, file := range files {
		edits = append(edits, claudeEdits(file)...)
	}
	attribution.Models = models(repoRoot, edits)
	return attribution, files
}

// claudeEntry is the part of a transcript line this package reads: the model
// and effort of an assistant turn, and the file each edit tool call names.
type claudeEntry struct {
	Type    string `json:"type"`
	Effort  string `json:"effort"`
	Message struct {
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type claudeBlock struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
	} `json:"input"`
	ToolUseID string `json:"tool_use_id"`
	IsError   bool   `json:"is_error"`
}

var (
	toolUseMarker    = []byte(`"tool_use"`)
	toolResultMarker = []byte(`"tool_result"`)
)

// claudeEdits returns the edits in one transcript file whose tool call
// succeeded, in transcript order. Lines that name no tool call are skipped
// before they are decoded, and a line that does not parse is skipped.
func claudeEdits(path string) []fileEdit {
	type call struct {
		edit fileEdit
		id   string
	}
	var calls []call
	succeeded := map[string]bool{}
	readRecord(path, func(line []byte) {
		if !bytes.Contains(line, toolUseMarker) && !bytes.Contains(line, toolResultMarker) {
			return
		}
		var entry claudeEntry
		var blocks []claudeBlock
		if json.Unmarshal(line, &entry) != nil || json.Unmarshal(entry.Message.Content, &blocks) != nil {
			return
		}
		for _, block := range blocks {
			switch {
			case entry.Type == "assistant" && block.Type == "tool_use" && claudeEditTools[block.Name]:
				target := block.Input.FilePath
				if target == "" {
					target = block.Input.NotebookPath
				}
				if target != "" && block.ID != "" {
					calls = append(calls, call{fileEdit{entry.Message.Model, entry.Effort, target}, block.ID})
				}
			case entry.Type == "user" && block.Type == "tool_result" && !block.IsError:
				succeeded[block.ToolUseID] = true
			}
		}
	})
	var edits []fileEdit
	for _, call := range calls {
		// A model Claude Code made up for its own messages names no model.
		if succeeded[call.id] && call.edit.model != "" && !strings.HasPrefix(call.edit.model, "<") {
			edits = append(edits, call.edit)
		}
	}
	return edits
}
