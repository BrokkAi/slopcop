package review

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/BrokkAi/slopcop/internal/attribution"
)

// intent is the user's request as given to reviewers. Truncated says that
// earlier text was dropped to keep the prompt within intentLimit.
type intent struct {
	Text, Source string
	Truncated    bool
}

// intentLimit bounds the intent text in a review prompt. A long session's
// transcript can hold megabytes of user messages; the most recent ones say
// best what the change under review is for.
const intentLimit = 64 * 1024

func discoverIntent(fallback string) intent {
	return capIntent(findIntent(fallback), intentLimit)
}
func findIntent(fallback string) intent {
	if p, ok := attribution.ClaudeTranscript(os.Getenv); ok {
		if t, err := readClaudeIntent(p); err == nil && t != "" {
			return intent{Text: t, Source: "actual user messages from the identified Claude Code transcript"}
		}
	}
	if p, ok := attribution.CodexRollout(os.Getenv); ok {
		if t, err := readCodexIntent(p); err == nil && t != "" {
			return intent{Text: t, Source: "actual user messages from the identified Codex rollout"}
		}
	}
	if strings.TrimSpace(fallback) != "" {
		return intent{Text: fallback, Source: "agent supplied via --intent; not independently verified user messages"}
	}
	return intent{Text: "", Source: "not available"}
}

// capIntent keeps at most limit bytes from the end of the intent text. It
// starts the kept text at a message boundary (a blank line) when there is
// one, else at a line boundary, else at a character boundary.
func capIntent(in intent, limit int) intent {
	if len(in.Text) <= limit {
		return in
	}
	tail := in.Text[len(in.Text)-limit:]
	if i := strings.Index(tail, "\n\n"); i >= 0 {
		tail = tail[i+2:]
	} else if i := strings.IndexByte(tail, '\n'); i >= 0 {
		tail = tail[i+1:]
	} else {
		for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
			tail = tail[1:]
		}
	}
	in.Text = tail
	in.Truncated = true
	return in
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
