package review

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"strings"

	"github.com/BrokkAi/slopcop/internal/attribution"
)

type intent struct{ Text, Source string }

func discoverIntent(fallback string) intent {
	if p, ok := attribution.ClaudeTranscript(os.Getenv); ok {
		if t, err := readClaudeIntent(p); err == nil && t != "" {
			return intent{t, "actual user messages from the identified Claude Code transcript"}
		}
	}
	if p, ok := attribution.CodexRollout(os.Getenv); ok {
		if t, err := readCodexIntent(p); err == nil && t != "" {
			return intent{t, "actual user messages from the identified Codex rollout"}
		}
	}
	if strings.TrimSpace(fallback) != "" {
		return intent{fallback, "agent supplied via --intent; not independently verified user messages"}
	}
	return intent{"", "not available"}
}
func readClaudeIntent(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	var out []string
	for {
		line, er := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var v struct {
				Type             string          `json:"type"`
				IsMeta           bool            `json:"isMeta"`
				IsSidechain      bool            `json:"isSidechain"`
				IsCompactSummary bool            `json:"isCompactSummary"`
				ToolUseResult    json.RawMessage `json:"toolUseResult"`
				ParentToolUseID  json.RawMessage `json:"parent_tool_use_id"`
				Message          struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &v) == nil && v.Type == "user" && v.Message.Role == "user" && !v.IsMeta && !v.IsSidechain && !v.IsCompactSummary && len(v.ToolUseResult) == 0 && (len(v.ParentToolUseID) == 0 || string(v.ParentToolUseID) == "null") {
				var plain string
				if json.Unmarshal(v.Message.Content, &plain) == nil {
					if strings.TrimSpace(plain) != "" {
						out = append(out, plain)
					}
				} else {
					var blocks []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					}
					if json.Unmarshal(v.Message.Content, &blocks) == nil {
						var texts []string
						tool := false
						for _, b := range blocks {
							if b.Type == "tool_result" {
								tool = true
							}
							if b.Type == "text" {
								texts = append(texts, b.Text)
							}
						}
						if !tool && strings.TrimSpace(strings.Join(texts, "\n")) != "" {
							out = append(out, strings.Join(texts, "\n"))
						}
					}
				}
			}
		}
		if er != nil {
			break
		}
	}
	return strings.Join(out, "\n\n"), nil
}
func readCodexIntent(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var out []string
	for scan.Scan() {
		var v struct {
			Type    string `json:"type"`
			Payload struct {
				Type    string `json:"type"`
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"payload"`
		}
		if json.Unmarshal(scan.Bytes(), &v) != nil || v.Type != "response_item" || v.Payload.Type != "message" || v.Payload.Role != "user" {
			continue
		}
		for _, b := range v.Payload.Content {
			t := strings.TrimSpace(b.Text)
			if b.Type != "input_text" && b.Type != "text" || t == "" || strings.Contains(t, "<environment_context>") || strings.Contains(t, "# AGENTS.md instructions") || strings.Contains(t, "mj://prompt-context") || strings.Contains(t, "<context ref=") || strings.HasPrefix(t, "<system_reminder>") {
				continue
			}
			out = append(out, t)
		}
	}
	return strings.Join(out, "\n\n"), scan.Err()
}
