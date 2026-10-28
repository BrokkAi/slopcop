package attribution

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/BrokkAi/slopcop/internal/service"
)

const codexHarness = "codex"

var (
	turnContextMarker = []byte(`"turn_context"`)
	fileChangeMarker  = []byte(`"FileChange"`)
)

// codexLine is the part of a Codex rollout line this package reads: the model
// and effort each turn ran with, and the files each completed file change
// wrote.
type codexLine struct {
	Type    string `json:"type"`
	Payload struct {
		Type   string `json:"type"`
		TurnID string `json:"turn_id"`
		Model  string `json:"model"`
		// Effort is where Codex releases before collaboration modes kept it.
		Effort            string `json:"effort"`
		CollaborationMode struct {
			Settings struct {
				ReasoningEffort string `json:"reasoning_effort"`
			} `json:"settings"`
		} `json:"collaboration_mode"`
		Item struct {
			Type    string `json:"type"`
			Status  string `json:"status"`
			Changes map[string]struct {
				Type     string `json:"type"`
				MovePath string `json:"move_path"`
			} `json:"changes"`
		} `json:"item"`
	} `json:"payload"`
}

// codex reads the attribution of a check run inside a Codex session. Codex
// puts CODEX_THREAD_ID in the commands the agent runs, and the thread's
// rollout log records the model and effort of every turn and every file
// change. A failed edit records no file change.
func codex(getenv func(string) string, repoRoot string) (*service.Attribution, []string) {
	threadID := strings.TrimSpace(getenv("CODEX_THREAD_ID"))
	if !sessionIDPattern.MatchString(threadID) || strings.Trim(threadID, ".") == "" {
		return nil, nil
	}
	attribution := &service.Attribution{
		Harness:        codexHarness,
		HarnessVersion: strings.TrimSpace(getenv("CODEX_VERSION")),
		SessionID:      threadID,
	}
	codexHome := strings.TrimSpace(getenv("CODEX_HOME"))
	if codexHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return attribution, nil
		}
		codexHome = filepath.Join(home, ".codex")
	}
	rollouts, _ := filepath.Glob(filepath.Join(codexHome, "sessions", "*", "*", "*", "rollout-*-"+threadID+".jsonl"))
	var edits []fileEdit
	for _, rollout := range rollouts {
		edits = append(edits, codexEdits(rollout)...)
	}
	// A turn at the model's default effort records none: an effort the user
	// chose is always recorded, so the default is the one that ran.
	var defaults map[string]string
	for i := range edits {
		if edits[i].effort == "" {
			if defaults == nil {
				defaults = codexDefaultEfforts(codexHome)
			}
			edits[i].effort = defaults[edits[i].model]
		}
	}
	attribution.Models = models(repoRoot, edits)
	return attribution, rollouts
}

// codexDefaultEfforts reads each model's default effort from the model list
// Codex caches. It is today's default, which is close enough for a session
// still on disk.
func codexDefaultEfforts(codexHome string) map[string]string {
	defaults := map[string]string{}
	data, err := os.ReadFile(filepath.Join(codexHome, "models_cache.json"))
	if err != nil {
		return defaults
	}
	var cache struct {
		Models []struct {
			Slug                  string `json:"slug"`
			DefaultReasoningLevel string `json:"default_reasoning_level"`
		} `json:"models"`
	}
	if json.Unmarshal(data, &cache) != nil {
		return defaults
	}
	for _, model := range cache.Models {
		if model.Slug != "" && model.DefaultReasoningLevel != "" {
			defaults[model.Slug] = model.DefaultReasoningLevel
		}
	}
	return defaults
}

// codexEdits returns the files the completed file changes in one rollout
// wrote, each with the model and effort of its turn. A move writes its
// destination; a delete writes nothing.
func codexEdits(path string) []fileEdit {
	type change struct{ turn, path string }
	turns := map[string]modelKey{}
	var changes []change
	readRecord(path, func(line []byte) {
		if !bytes.Contains(line, turnContextMarker) && !bytes.Contains(line, fileChangeMarker) {
			return
		}
		var entry codexLine
		if json.Unmarshal(line, &entry) != nil {
			return
		}
		payload := entry.Payload
		switch {
		case entry.Type == "turn_context" && payload.TurnID != "":
			effort := payload.CollaborationMode.Settings.ReasoningEffort
			if effort == "" {
				effort = payload.Effort
			}
			turns[payload.TurnID] = modelKey{payload.Model, effort}
		case entry.Type == "event_msg" && payload.Type == "item_completed" &&
			payload.Item.Type == "FileChange" && payload.Item.Status == "completed":
			for target, changed := range payload.Item.Changes {
				switch {
				case changed.Type == "delete":
				case changed.MovePath != "":
					changes = append(changes, change{payload.TurnID, changed.MovePath})
				default:
					changes = append(changes, change{payload.TurnID, target})
				}
			}
		}
	})
	var edits []fileEdit
	for _, change := range changes {
		if turn, ok := turns[change.turn]; ok && turn.model != "" {
			edits = append(edits, fileEdit{turn.model, turn.effort, change.path})
		}
	}
	return edits
}
