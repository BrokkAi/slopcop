package attribution

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/BrokkAi/slopcop/internal/service"
)

// CodexHarness is the harness name a Codex session's attribution carries.
const CodexHarness = "codex"

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
	return codexFrom(getenv, repoRoot, "")
}

// codexFrom is codex reading the thread's rollout at rollout, as a hook
// payload names it; without one, the rollout is looked for under the Codex
// home. The rollouts of the session's subagents, which keep their own, are
// read too.
func codexFrom(getenv func(string) string, repoRoot, rollout string) (*service.Attribution, []string) {
	threadID := strings.TrimSpace(getenv("CODEX_THREAD_ID"))
	if !sessionIDPattern.MatchString(threadID) || strings.Trim(threadID, ".") == "" {
		return nil, nil
	}
	attribution := &service.Attribution{
		Harness:        CodexHarness,
		HarnessVersion: strings.TrimSpace(getenv("CODEX_VERSION")),
		SessionID:      threadID,
	}
	var rollouts []string
	codexHome := rolloutHome(rollout)
	if codexHome != "" {
		rollouts = []string{rollout}
	} else {
		codexHome = codexHomeDir(getenv)
		if codexHome == "" {
			return attribution, nil
		}
		rollouts, _ = filepath.Glob(filepath.Join(codexHome, "sessions", "*", "*", "*", "rollout-*-"+threadID+".jsonl"))
	}
	if len(rollouts) > 0 {
		rollouts = append(rollouts, codexSubagentRollouts(rollouts[0])...)
	}
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

// codexHomeDir is CODEX_HOME, or ~/.codex, or empty when neither is known.
func codexHomeDir(getenv func(string) string) string {
	if dir := strings.TrimSpace(getenv("CODEX_HOME")); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex")
}

// rolloutHome is the Codex home that keeps the rollout at path, under
// sessions/YYYY/MM/DD, or empty when path is not such a rollout.
func rolloutHome(path string) string {
	if !filepath.IsAbs(path) || !strings.HasPrefix(filepath.Base(path), "rollout-") || filepath.Ext(path) != ".jsonl" {
		return ""
	}
	sessions := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(path))))
	if filepath.Base(sessions) != "sessions" {
		return ""
	}
	return filepath.Dir(sessions)
}

// codexMeta is the line that opens a rollout. SessionID names the session
// the thread belongs to: a subagent's thread has an id of its own, but keeps
// its parent's session.
type codexMeta struct {
	Type    string `json:"type"`
	Payload struct {
		SessionID string `json:"session_id"`
		ID        string `json:"id"`
	} `json:"payload"`
}

// rolloutMeta reads the session_meta line that opens the rollout at path.
func rolloutMeta(path string) (codexMeta, bool) {
	var meta codexMeta
	found := false
	readFirst(path, func(line []byte) {
		found = json.Unmarshal(line, &meta) == nil && meta.Type == "session_meta" && meta.Payload.SessionID != ""
	})
	return meta, found
}

// codexSubagentRollouts are the rollouts of the other threads of the session
// root's rollout belongs to: its subagents'. They are looked for in root's
// day and the days after it, since a session can run past midnight.
func codexSubagentRollouts(root string) []string {
	meta, ok := rolloutMeta(root)
	if !ok {
		return nil
	}
	day := filepath.Dir(root)
	sessions := filepath.Dir(filepath.Dir(filepath.Dir(day)))
	days, _ := filepath.Glob(filepath.Join(sessions, "*", "*", "*"))
	var found []string
	for _, dir := range days {
		if dir < day {
			continue
		}
		candidates, _ := filepath.Glob(filepath.Join(dir, "rollout-*.jsonl"))
		for _, candidate := range candidates {
			if candidate == root {
				continue
			}
			if other, ok := rolloutMeta(candidate); ok && other.Payload.SessionID == meta.Payload.SessionID {
				found = append(found, candidate)
			}
		}
	}
	return found
}

// codexSession is the session the thread threadID belongs to, as its
// rollout records it, or threadID itself when that cannot be read.
func codexSession(getenv func(string) string, threadID string) string {
	codexHome := codexHomeDir(getenv)
	if codexHome == "" {
		return threadID
	}
	rollouts, _ := filepath.Glob(filepath.Join(codexHome, "sessions", "*", "*", "*", "rollout-*-"+threadID+".jsonl"))
	for _, rollout := range rollouts {
		if meta, ok := rolloutMeta(rollout); ok {
			return meta.Payload.SessionID
		}
	}
	return threadID
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
