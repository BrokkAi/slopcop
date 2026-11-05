// Package reviewtest provides a re-exec ACP agent for review integration tests.
// A test process can call Main when SLOPCOP_FAKE_ACP=1, and point an adapter
// command at its test binary with that environment set.
package reviewtest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

type agent struct {
	acp.Agent
	conn                       *acp.AgentSideConnection
	mode, model, models, trace string
}

func (a *agent) Initialize(context.Context, acp.InitializeRequest) (acp.InitializeResponse, error) {
	a.record(fmt.Sprintf("pid=%d", os.Getpid()))
	a.record("initialize")
	return acp.InitializeResponse{ProtocolVersion: acp.ProtocolVersionNumber}, nil
}
func options(model, mode string) []acp.SessionConfigOption {
	m := acp.SessionConfigSelectOptionsUngrouped{}
	for _, value := range strings.Split(model, ",") {
		m = append(m, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(value), Name: value})
	}
	err := acp.SessionConfigSelectOptionsUngrouped{{Value: "high", Name: "High"}}
	if mode == "no-high" {
		err = acp.SessionConfigSelectOptionsUngrouped{{Value: "low", Name: "Low"}}
	}
	out := []acp.SessionConfigOption{{Select: &acp.SessionConfigOptionSelect{Type: "select", Id: "model", Name: "Model", CurrentValue: acp.SessionConfigValueId(model), Options: acp.SessionConfigSelectOptions{Ungrouped: &m}}}, {Select: &acp.SessionConfigOptionSelect{Type: "select", Id: "effort", Name: "Effort", CurrentValue: "high", Options: acp.SessionConfigSelectOptions{Ungrouped: &err}}}}
	if mode == "no-mode" {
		return out
	}
	// Each real adapter offers the restrictive mode slopcop selects: Codex
	// mode=read-only, Claude mode=default and dsh permission_mode=ask.
	modes := acp.SessionConfigSelectOptionsUngrouped{{Value: "read-only", Name: "Read Only"}, {Value: "default", Name: "Default"}, {Value: "full-access", Name: "Full Access"}}
	permission := acp.SessionConfigSelectOptionsUngrouped{{Value: "ask", Name: "Ask"}, {Value: "auto", Name: "Auto"}}
	return append(out, acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{Type: "select", Id: "mode", Name: "Mode", CurrentValue: "full-access", Options: acp.SessionConfigSelectOptions{Ungrouped: &modes}}}, acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{Type: "select", Id: "permission_mode", Name: "Permission mode", CurrentValue: "auto", Options: acp.SessionConfigSelectOptions{Ungrouped: &permission}}})
}
func (a *agent) NewSession(_ context.Context, p acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	if a.mode == "cwd" {
		dir, _ := os.Getwd()
		a.record("process-cwd=" + dir)
		a.record("session-cwd=" + p.Cwd)
	}
	if a.mode == "new-error" {
		return acp.NewSessionResponse{}, fmt.Errorf("fake session failure")
	}
	model := a.model
	if a.models != "" {
		model = a.models
	}
	if model == "" {
		model = "gpt-6.10-terra"
	}
	return acp.NewSessionResponse{SessionId: "fake", ConfigOptions: options(model, a.mode)}, nil
}
func (a *agent) SetSessionConfigOption(_ context.Context, p acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	model := a.model
	if a.models != "" {
		model = a.models
	}
	if model == "" {
		model = "gpt-6.10-terra"
	}
	if a.mode == "no-echo" {
		return acp.SetSessionConfigOptionResponse{ConfigOptions: []acp.SessionConfigOption{}}, nil
	}
	return acp.SetSessionConfigOptionResponse{ConfigOptions: options(model, a.mode)}, nil
}
func (a *agent) Prompt(ctx context.Context, p acp.PromptRequest) (acp.PromptResponse, error) {
	a.record("prompt")
	if a.mode == "auth-required" {
		return acp.PromptResponse{}, &acp.RequestError{Code: -32000, Message: "Authentication required"}
	}
	if a.mode == "prompt-error" {
		return acp.PromptResponse{}, &acp.RequestError{Code: -32603, Message: "Prompt failed"}
	}
	if a.mode == "barrier" {
		for {
			data, _ := os.ReadFile(a.trace)
			if strings.Count(string(data), "prompt\n") >= 2 {
				break
			}
			select {
			case <-ctx.Done():
				return acp.PromptResponse{}, ctx.Err()
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	text := "No findings."
	if len(p.Prompt) == 1 && p.Prompt[0].Text != nil && p.Prompt[0].Text.Text == "Reply with exactly the word ok and nothing else. Do not use any tools." {
		text = "ok"
	}
	if a.mode == "hang" {
		<-ctx.Done()
		return acp.PromptResponse{}, ctx.Err()
	}
	if a.mode == "timeout" {
		child := exec.Command("sh", "-c", `sleep 1; printf survived > "$1"`, "sh", a.trace+".survived")
		if err := child.Start(); err != nil {
			return acp.PromptResponse{}, err
		}
		a.record("grandchild-started")
		<-ctx.Done()
		return acp.PromptResponse{}, ctx.Err()
	}
	if a.mode == "no-allow" {
		kind := acp.ToolKindRead
		_, _ = a.conn.RequestPermission(ctx, acp.RequestPermissionRequest{SessionId: p.SessionId, ToolCall: acp.ToolCallUpdate{ToolCallId: "fake", Kind: &kind}, Options: []acp.PermissionOption{{Kind: acp.PermissionOptionKindRejectOnce, OptionId: "reject"}}})
	}
	if a.mode == "denied-then-fail" {
		kind := acp.ToolKindExecute
		title := "rm -rf ."
		_, _ = a.conn.RequestPermission(ctx, acp.RequestPermissionRequest{SessionId: p.SessionId, ToolCall: acp.ToolCallUpdate{ToolCallId: "fake", Kind: &kind, Title: &title}, Options: []acp.PermissionOption{{Kind: acp.PermissionOptionKindAllowOnce, OptionId: "allow"}, {Kind: acp.PermissionOptionKindRejectOnce, OptionId: "reject"}}})
		return acp.PromptResponse{}, &acp.RequestError{Code: -32603, Message: "Prompt failed"}
	}
	if a.mode == "findings" {
		text = "- [P1] fake defect"
	}
	if a.mode == "empty" {
		text = ""
	}
	if text != "" {
		if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: p.SessionId, Update: acp.UpdateAgentMessageText(text)}); err != nil {
			return acp.PromptResponse{}, err
		}
	}
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

// Main runs the fake agent and exits the process when SLOPCOP_FAKE_ACP=1.
// Call at the start of TestMain or a dedicated re-exec Test function.
func Main() {
	if os.Getenv("SLOPCOP_FAKE_ACP") != "1" {
		return
	}
	a := &agent{}
	for _, arg := range os.Args {
		if v, ok := strings.CutPrefix(arg, "--fake-mode="); ok {
			a.mode = v
		}
		if v, ok := strings.CutPrefix(arg, "--fake-models="); ok {
			a.models = v
		}
		if v, ok := strings.CutPrefix(arg, "--fake-model="); ok {
			a.model = v
		}
		if v, ok := strings.CutPrefix(arg, "--fake-trace="); ok {
			a.trace = v
		}
	}
	a.conn = acp.NewAgentSideConnection(a, os.Stdout, os.Stdin)
	<-a.conn.Done()
	os.Exit(0)
}

func (a *agent) record(event string) {
	if a.trace == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(a.trace), 0700)
	f, err := os.OpenFile(a.trace, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err == nil {
		_, _ = f.WriteString(event + "\n")
		_ = f.Close()
	}
}
