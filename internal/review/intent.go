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
			return intent{Text: t, Source: "actual user messages from the identified Claude Code transcript; a line starting `[agent asked: …]` is the agent's question that the message after it answers, not user words"}
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

// claudeRow is the part of a Claude Code transcript row that intent reads.
type claudeRow struct {
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

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
}

// askedLabel marks text the agent wrote, so reviewers don't take it for the
// user's words. A short reply such as "Ship rules, models later" only makes
// sense next to the question it answers.
const askedLabel = "[agent asked: "

// questionLimit bounds one quoted agent question, in runes.
const questionLimit = 600

// readClaudeIntent returns the user's messages from a Claude Code transcript.
// Each reply to a question the agent asked follows that question, labelled
// with askedLabel; answers given through AskUserQuestion are kept the same way.
func readClaudeIntent(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	var out []string
	// ask is the question the current agent turn put to the user through a
	// wrap_up box; lastText is the turn's latest text, a fallback for asks
	// made in prose. A user message ends the turn and resets both.
	var ask, lastText string
	// asked holds the ids of AskUserQuestion calls, so that only their
	// results, and no other tool's output, count as the user's answers.
	asked := map[string]bool{}
	for {
		line, er := r.ReadBytes('\n')
		var v claudeRow
		if len(bytes.TrimSpace(line)) > 0 && json.Unmarshal(line, &v) == nil && !v.IsSidechain && (len(v.ParentToolUseID) == 0 || string(v.ParentToolUseID) == "null") {
			switch {
			case v.IsCompactSummary:
				ask, lastText = "", ""
			case v.Type == "assistant" && v.Message.Role == "assistant":
				var blocks []claudeBlock
				if json.Unmarshal(v.Message.Content, &blocks) == nil {
					for _, b := range blocks {
						// The latest ask wins: a later wrap_up, a question in
						// later text, or more work after it supersedes it.
						switch {
						case b.Type == "text" && strings.TrimSpace(b.Text) != "":
							lastText = b.Text
							if proseQuestion(b.Text) != "" {
								ask = ""
							}
						case b.Type == "tool_use" && b.Name == "AskUserQuestion":
							asked[b.ID] = true
							ask, lastText = "", ""
						case b.Type == "tool_use" && strings.HasSuffix(b.Name, "ccd_turn__wrap_up"):
							ask = wrapUpAsk(b.Input)
						case b.Type == "tool_use":
							ask, lastText = "", ""
						}
					}
				}
			case v.Type == "user" && v.Message.Role == "user" && !v.IsMeta:
				msg := ""
				if len(v.ToolUseResult) > 0 {
					if asked[toolResultID(v.Message.Content)] {
						msg = askUserAnswers(v.ToolUseResult)
					}
				} else if msg = userText(v.Message.Content); msg != "" {
					q := ask
					if q == "" {
						q = proseQuestion(lastText)
					}
					if q != "" {
						msg = askedLabel + clip(q) + "]\n" + msg
					}
				}
				if msg != "" {
					out = append(out, msg)
					ask, lastText = "", ""
				}
			}
		}
		if er != nil {
			break
		}
	}
	return strings.Join(out, "\n\n"), nil
}

// userText returns a user row's typed text without harness-injected
// system reminders, or "" when the row is a tool result or holds no text.
func userText(content json.RawMessage) string {
	var plain string
	if json.Unmarshal(content, &plain) != nil {
		var blocks []claudeBlock
		if json.Unmarshal(content, &blocks) != nil {
			return ""
		}
		var texts []string
		for _, b := range blocks {
			if b.Type == "tool_result" {
				return ""
			}
			if t := stripReminders(b.Text); b.Type == "text" && t != "" {
				texts = append(texts, t)
			}
		}
		return strings.Join(texts, "\n")
	}
	return stripReminders(plain)
}

// stripReminders removes the system reminders the harness puts at the start
// of a user's text. Transcripts show the harness puts them nowhere else, so a
// reminder elsewhere is the user's own text and is kept.
func stripReminders(text string) string {
	const open, closing = "<system-reminder>", "</system-reminder>"
	text = strings.TrimSpace(text)
	for strings.HasPrefix(text, open) {
		i := strings.Index(text, closing)
		if i < 0 {
			break
		}
		text = strings.TrimSpace(text[i+len(closing):])
	}
	return text
}

// toolResultID returns the id of the call a tool-result row answers.
func toolResultID(content json.RawMessage) string {
	var blocks []claudeBlock
	if json.Unmarshal(content, &blocks) == nil {
		for _, b := range blocks {
			if b.Type == "tool_result" && b.ToolUseID != "" {
				return b.ToolUseID
			}
		}
	}
	return ""
}

// wrapUpAsk returns a wrap_up call's question with its reply buttons.
func wrapUpAsk(input json.RawMessage) string {
	var in struct {
		Ask     string `json:"ask"`
		Actions []struct {
			Label string `json:"label"`
		} `json:"actions"`
	}
	if json.Unmarshal(input, &in) != nil || strings.TrimSpace(in.Ask) == "" {
		return ""
	}
	var labels []string
	for _, a := range in.Actions {
		if l := strings.TrimSpace(a.Label); l != "" {
			labels = append(labels, l)
		}
	}
	q := strings.TrimSpace(in.Ask)
	if len(labels) > 0 {
		q += " (choices: " + strings.Join(labels, " / ") + ")"
	}
	return q
}

// askUserAnswers renders an AskUserQuestion result as each question, labelled,
// followed by the user's answer: a chosen option or their own text.
func askUserAnswers(result json.RawMessage) string {
	var res struct {
		Questions []struct {
			Question string `json:"question"`
		} `json:"questions"`
		Answers map[string]string `json:"answers"`
	}
	if json.Unmarshal(result, &res) != nil || len(res.Answers) == 0 {
		return ""
	}
	var out []string
	for _, q := range res.Questions {
		a, ok := res.Answers[q.Question]
		if !ok || strings.TrimSpace(a) == "" {
			continue
		}
		out = append(out, askedLabel+clip(strings.TrimSpace(q.Question))+"]\n"+strings.TrimSpace(a))
	}
	return strings.Join(out, "\n\n")
}

// proseQuestion returns the last paragraph of an agent's text when it is a
// question, else "".
func proseQuestion(text string) string {
	text = strings.TrimSpace(text)
	if i := strings.LastIndex(text, "\n\n"); i >= 0 {
		text = strings.TrimSpace(text[i+2:])
	}
	if !strings.HasSuffix(text, "?") {
		return ""
	}
	return strings.Join(strings.Fields(text), " ")
}

// clip keeps the end of a long question, where the question itself is.
func clip(q string) string {
	if utf8.RuneCountInString(q) <= questionLimit {
		return q
	}
	r := []rune(q)
	return "…" + string(r[len(r)-questionLimit:])
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
