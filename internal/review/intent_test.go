package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestClaudeIntentKeepsTheQuestionAReplyAnswers checks that a short reply
// keeps the question it answers, labelled as the agent's words.
func TestClaudeIntentKeepsTheQuestionAReplyAnswers(t *testing.T) {
	rows := []string{
		`{"type":"user","message":{"role":"user","content":"<system-reminder>\nYou are operating in a git worktree.\n</system-reminder>Cut a rules release and a packs release"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Both are ready."}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"mcp__ccd_turn__wrap_up","input":{"recap":"r","ask":"How should the rules release handle model-dependent rules?","actions":[{"label":"Ship rules, models later"},{"label":"Wait for models"}]}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"Shown in the next-step box."}]},"toolUseResult":[{"type":"text","text":"Shown"}]}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Closing message."}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Ship rules, models later"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"AskUserQuestion","input":{"questions":[{"question":"Which branch?","options":[{"label":"main"},{"label":"dev"}]}]}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"answered"}]},"toolUseResult":{"questions":[{"question":"Which branch?"}],"answers":{"Which branch?":"dev, then main"}}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t7","name":"mcp__other__fetch","input":{}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t7","content":"x"}]},"toolUseResult":{"questions":[{"question":"Delete prod?"}],"answers":{"Delete prod?":"yes"}}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Done.\n\nShould I open a PR?"}]}}`,
		`{"type":"user","message":{"role":"user","content":"yes"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Is it fine? It is open."}]}}`,
		`{"type":"user","isSidechain":true,"message":{"role":"user","content":"subagent prompt"}}`,
		`{"type":"user","message":{"role":"user","content":"<system-reminder>only a reminder</system-reminder>"}}`,
		`{"type":"user","message":{"role":"user","content":"thanks"}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Parse blocks like <system-reminder>x</system-reminder>"},{"type":"text","text":"<system-reminder>separate block</system-reminder>"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t3","name":"mcp__ccd_turn__wrap_up","input":{"recap":"r","ask":"Merge now?"}}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Actually, CI failed.\n\nRetry CI first?"}]}}`,
		`{"type":"user","message":{"role":"user","content":"retry"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t4","name":"mcp__ccd_turn__wrap_up","input":{"recap":"r","ask":"Deploy?"}}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t5","name":"Bash","input":{"command":"ls"}}]}}`,
		`{"type":"user","message":{"role":"user","content":"stop"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Should I deploy?"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t6","name":"Bash","input":{"command":"ls"}}]}}`,
		`{"type":"user","message":{"role":"user","content":"wait"}}`,
	}
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(rows, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	messages, err := readClaudeIntent(p)
	got := strings.Join(messages, "\n\n")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"Cut a rules release and a packs release",
		"[agent asked: How should the rules release handle model-dependent rules? (choices: Ship rules, models later / Wait for models)]\nShip rules, models later",
		"[agent asked: Which branch?]\ndev, then main",
		"[agent asked: Should I open a PR?]\nyes",
		"thanks",
		"Parse blocks like <system-reminder>x</system-reminder>",
		"[agent asked: Retry CI first?]\nretry",
		"stop",
		"wait",
	}, "\n\n")
	if got != want {
		t.Fatalf("intent:\n%s\n\nwant:\n%s", got, want)
	}
}

// TestClaudeIntentSkipsHarnessRows checks that rows Claude Code writes as
// the user, such as a background command's result, are not intent, so they
// neither reach reviewers nor change the reuse key between two checks.
func TestClaudeIntentSkipsHarnessRows(t *testing.T) {
	rows := []string{
		`{"type":"user","origin":{"kind":"human"},"message":{"role":"user","content":"Fix the parser"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Should I also add a test?"}]}}`,
		`{"type":"user","origin":{"kind":"task-notification","producer":"session-task"},"message":{"role":"user","content":"<task-notification>\n<status>completed</status>\n</task-notification>"}}`,
		`{"type":"user","origin":{"kind":"something-new"},"message":{"role":"user","content":"not typed by the person"}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"<system-reminder>r</system-reminder>\n<task-notification>\nold transcript\n</task-notification>"}]}}`,
		`{"type":"user","message":{"role":"user","content":"<ci-monitor-event>checks failed</ci-monitor-event>"}}`,
		`{"type":"user","message":{"role":"user","content":"yes, and mention <task-notification> in the docs"}}`,
		`{"type":"user","origin":{"kind":"human"},"message":{"role":"user","content":"<ci-monitor-event>pasted</ci-monitor-event> fix this"}}`,
		`{"type":"user","message":{"role":"user","content":"<task-notification>a</task-notification>\n<ci-monitor-event>b</ci-monitor-event>\nfix what these say"}}`,
	}
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(rows, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	messages, err := readClaudeIntent(p)
	got := strings.Join(messages, "\n\n")
	if err != nil {
		t.Fatal(err)
	}
	want := "Fix the parser\n\n[agent asked: Should I also add a test?]\nyes, and mention <task-notification> in the docs\n\n<ci-monitor-event>pasted</ci-monitor-event> fix this\n\n<task-notification>a</task-notification>\n<ci-monitor-event>b</ci-monitor-event>\nfix what these say"
	if got != want {
		t.Fatalf("intent:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestClipKeepsTheEndOfALongQuestion(t *testing.T) {
	q := strings.Repeat("é", questionLimit) + " why?"
	got := clip(q)
	if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, " why?") || len([]rune(got)) != questionLimit+1 {
		t.Fatalf("clip = %q", got)
	}
}
