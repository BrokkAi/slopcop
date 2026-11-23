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
	// cliModel, cliEffort and tools are Copilot CLI's --model,
	// --reasoning-effort and --available-tools, for modes named copilot*.
	cliModel, cliEffort, tools string
	// cwd is the session's working directory, for Grok's client reads.
	cwd string
}

func (a *agent) copilot() bool { return strings.HasPrefix(a.mode, "copilot") }

// copilotOptions mirrors the options Copilot CLI 1.0.114 returns from
// session/new: its modes, the account's models, and allow_all.
func (a *agent) copilotOptions() []acp.SessionConfigOption {
	modes := acp.SessionConfigSelectOptionsUngrouped{{Value: "https://agentclientprotocol.com/protocol/session-modes#agent", Name: "Agent"}, {Value: "https://agentclientprotocol.com/protocol/session-modes#autopilot", Name: "Autopilot"}}
	models := acp.SessionConfigSelectOptionsUngrouped{}
	for _, value := range strings.Split(a.models, ",") {
		o := acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(value), Name: value}
		if v, ok := strings.CutSuffix(value, "!"); ok {
			o = acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(v), Name: v, Meta: map[string]any{"copilotEnablement": "disabled"}}
		}
		models = append(models, o)
	}
	current := a.cliModel
	if current == "" {
		current = "auto"
	}
	allow := acp.SessionConfigSelectOptionsUngrouped{{Value: "on", Name: "On"}, {Value: "off", Name: "Off"}}
	allowAll := "off"
	if a.mode == "copilot-allow-all" {
		allowAll = "on"
	}
	return []acp.SessionConfigOption{
		{Select: &acp.SessionConfigOptionSelect{Type: "select", Id: "mode", Name: "Mode", CurrentValue: "https://agentclientprotocol.com/protocol/session-modes#agent", Options: acp.SessionConfigSelectOptions{Ungrouped: &modes}}},
		{Select: &acp.SessionConfigOptionSelect{Type: "select", Id: "model", Name: "Model", CurrentValue: acp.SessionConfigValueId(current), Options: acp.SessionConfigSelectOptions{Ungrouped: &models}}},
		{Select: &acp.SessionConfigOptionSelect{Type: "select", Id: "allow_all", Name: "Allow All", CurrentValue: acp.SessionConfigValueId(allowAll), Options: acp.SessionConfigSelectOptions{Ungrouped: &allow}}},
	}
}

func (a *agent) Initialize(_ context.Context, p acp.InitializeRequest) (acp.InitializeResponse, error) {
	a.record(fmt.Sprintf("pid=%d", os.Getpid()))
	a.record("initialize")
	if strings.HasPrefix(a.mode, "grok-") {
		a.record(fmt.Sprintf("client-fs read=%v write=%v", p.ClientCapabilities.Fs.ReadTextFile, p.ClientCapabilities.Fs.WriteTextFile))
	}
	// Codex and Claude adapters advertise session/delete; mode no-delete
	// stands for an adapter that does not.
	caps := acp.AgentCapabilities{}
	if a.copilot() {
		a.record(fmt.Sprintf("copilot model=%s effort=%s tools=%s", a.cliModel, a.cliEffort, a.tools))
		// Copilot offers session/close, not session/delete.
		caps.SessionCapabilities.Close = &acp.SessionCloseCapabilities{}
		return acp.InitializeResponse{ProtocolVersion: acp.ProtocolVersionNumber, AgentCapabilities: caps}, nil
	}
	if a.mode != "no-delete" {
		caps.SessionCapabilities.Delete = &acp.SessionDeleteCapabilities{}
	}
	return acp.InitializeResponse{ProtocolVersion: acp.ProtocolVersionNumber, AgentCapabilities: caps}, nil
}

// Cancel accepts the client's session/cancel; a hanging prompt ends with its
// own context.
func (a *agent) Cancel(context.Context, acp.CancelNotification) error { return nil }

// CloseSession records the session slopcop closes.
func (a *agent) CloseSession(_ context.Context, p acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	a.record("close=" + string(p.SessionId))
	return acp.CloseSessionResponse{}, nil
}

// UnstableDeleteSession records the session slopcop cleans up.
func (a *agent) UnstableDeleteSession(_ context.Context, p acp.UnstableDeleteSessionRequest) (acp.UnstableDeleteSessionResponse, error) {
	a.record("delete=" + string(p.SessionId))
	return acp.UnstableDeleteSessionResponse{}, nil
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
func (a *agent) NewSession(ctx context.Context, p acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	a.cwd = p.Cwd
	if a.mode == "cwd" {
		dir, _ := os.Getwd()
		a.record("process-cwd=" + dir)
		a.record("session-cwd=" + p.Cwd)
	}
	// An Antigravity reviewer's workspace: what it holds, and how it started.
	if a.mode == "workspace" {
		a.record("session-cwd=" + p.Cwd)
		a.record("args=" + strings.Join(os.Args, " "))
		for _, rel := range []string{"repo/change.txt", "repo/.agents/hooks.json", "repo/link", ".agents"} {
			_, err := os.Lstat(filepath.Join(p.Cwd, rel))
			a.record(fmt.Sprintf("exists %s=%v", rel, err == nil))
		}
		index, _ := os.ReadFile(filepath.Join(p.Cwd, "files.txt"))
		a.record("index=" + strings.ReplaceAll(strings.TrimSpace(string(index)), "\n", ","))
	}
	// A Kimi reviewer's environment and generated config. It records whether
	// the key arrived, never the key.
	if a.mode == "kimi-env" {
		a.record("session-cwd=" + p.Cwd)
		a.record("args=" + strings.Join(os.Args, " "))
		a.record(fmt.Sprintf("key-set=%v", os.Getenv("KIMI_API_KEY") != ""))
		for _, k := range []string{"HOME", "USERPROFILE", "KIMI_CODE_HOME", "KIMI_DISABLE_TELEMETRY", "KIMI_CODE_NO_AUTO_UPDATE", "KIMI_CODE_TRUST_WORKSPACE", "KIMI_MODEL_NAME", "npm_config_cache", "npm_config_userconfig"} {
			a.record(k + "=" + os.Getenv(k))
		}
		for _, rel := range []string{"repo/change.txt", "repo/.kimi-code/mcp.json", "repo/link", ".kimi-code", ".agents"} {
			_, err := os.Lstat(filepath.Join(p.Cwd, rel))
			a.record(fmt.Sprintf("exists %s=%v", rel, err == nil))
		}
		config, _ := os.ReadFile(filepath.Join(os.Getenv("KIMI_CODE_HOME"), "config.toml"))
		a.record("config:\n" + string(config))
	}
	// A Grok reviewer's workspace and the environment it started with. Key
	// values stay out of the trace.
	if a.mode == "grok-workspace" {
		dir, _ := os.Getwd()
		a.record("process-cwd=" + dir)
		a.record("session-cwd=" + p.Cwd)
		a.record("args=" + strings.Join(os.Args, " "))
		for _, rel := range []string{"repo/change.txt", "repo/.grok/hooks.json", "repo/link", ".grok"} {
			_, err := os.Lstat(filepath.Join(p.Cwd, rel))
			a.record(fmt.Sprintf("exists %s=%v", rel, err == nil))
		}
		for _, v := range os.Environ() {
			k, value, _ := strings.Cut(v, "=")
			if k == "XAI_API_KEY" {
				a.record("key-set=" + fmt.Sprint(value != ""))
			} else if k == "HOME" || strings.HasPrefix(k, "GROK_") || strings.HasPrefix(k, "XAI_") {
				a.record("env " + v)
			}
		}
		grokHome := os.Getenv("GROK_HOME")
		for _, file := range []string{"sandbox.toml", "config.toml"} {
			data, _ := os.ReadFile(filepath.Join(grokHome, file))
			a.record(file + "=" + strings.ReplaceAll(string(data), "\n", " "))
		}
	}
	if a.mode == "hang-session" {
		a.record("new-session")
		<-ctx.Done()
		return acp.NewSessionResponse{}, ctx.Err()
	}
	if a.mode == "new-error" {
		return acp.NewSessionResponse{}, fmt.Errorf("fake session failure")
	}
	if a.copilot() {
		if a.mode == "copilot-no-effort" && a.cliEffort != "" {
			return acp.NewSessionResponse{}, fmt.Errorf("Request sessions.open failed with message: session construction failed: Model '%s' does not support reasoning effort configuration.", a.cliModel)
		}
		return acp.NewSessionResponse{SessionId: "fake", ConfigOptions: a.copilotOptions()}, nil
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
	if a.copilot() {
		a.record(fmt.Sprintf("set %s=%s", p.ValueId.ConfigId, p.ValueId.Value))
		return acp.SetSessionConfigOptionResponse{}, nil
	}
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
	// A held reviewer replies once the test creates the release file, so the
	// test can change the checkout while the review is running.
	if strings.HasPrefix(a.mode, "held") {
		for {
			if _, err := os.Stat(a.trace + ".release"); err == nil {
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
	// A tool that runs without asking, as Antigravity's web search does.
	if a.mode == "ungated-tool" {
		if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: p.SessionId, Update: acp.StartToolCall("web", "query on Web")}); err != nil {
			return acp.PromptResponse{}, err
		}
		<-ctx.Done()
		return acp.PromptResponse{}, ctx.Err()
	}
	// A read, reported the way Kimi Code reports one, then a clean reply.
	if a.mode == "kimi-read" || a.mode == "kimi-read-fails" || a.mode == "kimi-read-fails-findings" || a.mode == "kimi-read-missing" {
		path, status := "repo/change.txt", acp.ToolCallStatusCompleted
		if a.mode != "kimi-read" {
			status = acp.ToolCallStatusFailed
		}
		if a.mode == "kimi-read-missing" {
			path = "repo/not-in-the-change.txt"
		}
		if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: p.SessionId, Update: acp.StartToolCall("read", "Reading "+path, acp.WithStartKind(acp.ToolKindRead), acp.WithStartStatus(acp.ToolCallStatusInProgress), acp.WithStartRawInput(map[string]any{"path": path}))}); err != nil {
			return acp.PromptResponse{}, err
		}
		if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: p.SessionId, Update: acp.UpdateToolCall("read", acp.WithUpdateStatus(status))}); err != nil {
			return acp.PromptResponse{}, err
		}
	}
	// A web fetch, which Kimi Code would run without asking if its allowlist
	// did not apply.
	if a.mode == "kimi-fetch" {
		if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: p.SessionId, Update: acp.StartToolCall("web", "Fetching https://example.test/", acp.WithStartKind(acp.ToolKindOther), acp.WithStartStatus(acp.ToolCallStatusInProgress))}); err != nil {
			return acp.PromptResponse{}, err
		}
		<-ctx.Done()
		return acp.PromptResponse{}, ctx.Err()
	}
	// Grok names its tools in x.ai/tool metadata and reads through the client.
	// grok-tool-<name>:<path> calls one tool; grok-direct-<name>:<path> reports
	// the same call without asking the client, as a read from disk would.
	tool, viaClient := strings.CutPrefix(a.mode, "grok-tool-")
	direct, isDirect := strings.CutPrefix(a.mode, "grok-direct-")
	if isDirect {
		tool = direct
	}
	if viaClient || isDirect {
		name, path, _ := strings.Cut(tool, ":")
		call := acp.StartToolCall("call-1", name, acp.WithStartStatus(acp.ToolCallStatusInProgress))
		call.ToolCall.Meta = map[string]any{"x.ai/tool": map[string]any{"name": name, "input": map[string]any{"path": path}}}
		if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: p.SessionId, Update: call}); err != nil {
			return acp.PromptResponse{}, err
		}
		if viaClient && name == "read_file" {
			full := path
			if !filepath.IsAbs(full) {
				full = filepath.Join(a.cwd, path)
			}
			got, err := a.conn.ReadTextFile(ctx, acp.ReadTextFileRequest{SessionId: p.SessionId, Path: full})
			if err != nil {
				a.record("read-refused")
				<-ctx.Done()
				return acp.PromptResponse{}, ctx.Err()
			}
			a.record(fmt.Sprintf("read-bytes=%d", len(got.Content)))
		}
		done := acp.UpdateToolCall("call-1", acp.WithUpdateStatus(acp.ToolCallStatusCompleted))
		if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: p.SessionId, Update: done}); err != nil {
			return acp.PromptResponse{}, err
		}
		if name != "read_file" || isDirect || filepath.IsAbs(path) {
			<-ctx.Done()
			return acp.PromptResponse{}, ctx.Err()
		}
	}
	// Antigravity asks only about a read outside the workspace, and ends its
	// turn with no reply when refused.
	if a.mode == "outside-read" {
		kind := acp.ToolKindRead
		title := "/outside/secret"
		got, err := a.conn.RequestPermission(ctx, acp.RequestPermissionRequest{SessionId: p.SessionId, ToolCall: acp.ToolCallUpdate{ToolCallId: "fake", Kind: &kind, Title: &title}, Options: []acp.PermissionOption{{Kind: acp.PermissionOptionKindAllowOnce, OptionId: "allow"}, {Kind: acp.PermissionOptionKindRejectOnce, OptionId: "deny"}}})
		if err != nil {
			return acp.PromptResponse{}, err
		}
		if got.Outcome.Selected != nil {
			a.record("permission=" + string(got.Outcome.Selected.OptionId))
		}
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	}
	if a.mode == "denied-then-fail" {
		kind := acp.ToolKindExecute
		title := "rm -rf ."
		_, _ = a.conn.RequestPermission(ctx, acp.RequestPermissionRequest{SessionId: p.SessionId, ToolCall: acp.ToolCallUpdate{ToolCallId: "fake", Kind: &kind, Title: &title}, Options: []acp.PermissionOption{{Kind: acp.PermissionOptionKindAllowOnce, OptionId: "allow"}, {Kind: acp.PermissionOptionKindRejectOnce, OptionId: "reject"}}})
		return acp.PromptResponse{}, &acp.RequestError{Code: -32603, Message: "Prompt failed"}
	}
	if strings.HasPrefix(a.mode, "escalate") {
		// Codex in read-only mode asks before running a command outside its
		// sandbox and ends the turn as cancelled when the answer is "cancel".
		retry := strings.Contains(promptText(p), "## Retry")
		if retry {
			a.record("retry-prompt")
		}
		if !retry || a.mode == "escalate-always" {
			kind := acp.ToolKindExecute
			title := "Run command"
			options := []acp.PermissionOption{{Kind: acp.PermissionOptionKindAllowOnce, OptionId: "allow_once"}, {Kind: acp.PermissionOptionKindAllowAlways, OptionId: "accept_execpolicy_amendment"}}
			if a.mode == "escalate-decline" {
				options = append(options, acp.PermissionOption{Kind: acp.PermissionOptionKindRejectOnce, OptionId: "decline"})
			}
			options = append(options, acp.PermissionOption{Kind: acp.PermissionOptionKindRejectOnce, OptionId: "cancel"})
			got, err := a.conn.RequestPermission(ctx, acp.RequestPermissionRequest{SessionId: p.SessionId, ToolCall: acp.ToolCallUpdate{ToolCallId: "fake", Kind: &kind, Title: &title, RawInput: map[string]any{"command": "go test ./..."}}, Options: options})
			if err != nil {
				return acp.PromptResponse{}, err
			}
			chosen := "none"
			if got.Outcome.Selected != nil {
				chosen = string(got.Outcome.Selected.OptionId)
			}
			a.record("permission=" + chosen)
			if chosen != "decline" {
				return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
			}
		}
	}
	if a.mode == "copilot-error-reply" {
		text = fmt.Sprintf("Error: Execution failed: Model %q is not available.", a.cliModel)
	}
	if a.mode == "findings" || a.mode == "held-findings" || a.mode == "kimi-read-fails-findings" {
		text = "- [P1] fake defect"
	}
	// One false positive, then the same point as another review words it.
	if a.mode == "release-race" {
		text = "- [P2] npm can publish a newer version than the latest GitHub release `change.txt:3-4`\n" +
			"The setup script compares the installed npm package with the latest GitHub release, so when the npm job publishes before goreleaser creates the release, the check reports a newer version that does not exist yet."
	}
	if a.mode == "release-race-reworded" {
		text = "- [P2] Check the installed package's channel for updates `change.txt:6-7`\n" +
			"When the npm package is published before goreleaser finishes the GitHub release, the setup script sees a newer npm version than the latest release and reports an update that is not available yet."
	}
	if a.mode == "empty" {
		text = ""
	}
	if a.mode == "offline" {
		// Codex ends its turn normally with its connection error as the reply.
		text = "Codex can't reach chatgpt.com to look up your ChatGPT workspace. Check your network connection and any proxy, VPN, or firewall that blocks chatgpt.com, then try again. (workspace routing discovery failed)"
	}
	if text != "" {
		if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: p.SessionId, Update: acp.UpdateAgentMessageText(text)}); err != nil {
			return acp.PromptResponse{}, err
		}
	}
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

func promptText(p acp.PromptRequest) string {
	var b strings.Builder
	for _, block := range p.Prompt {
		if block.Text != nil {
			b.WriteString(block.Text.Text)
		}
	}
	return b.String()
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
	for i, arg := range os.Args {
		switch arg {
		case "--model":
			if i+1 < len(os.Args) {
				a.cliModel = os.Args[i+1]
			}
		case "--reasoning-effort":
			if i+1 < len(os.Args) {
				a.cliEffort = os.Args[i+1]
			}
		case "--available-tools":
			a.tools = strings.Join(os.Args[i+1:], ",")
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
