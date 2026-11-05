package review

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// reviewClient collects final agent text and permission failures across ACP callbacks.
type reviewClient struct {
	unsupportedClient
	mu              sync.Mutex
	text            strings.Builder
	permissionError error
	deniedEdit      string
	stderr          io.Writer
}

// SessionUpdate retains only agent message chunks after the last tool update.
func (c *reviewClient) SessionUpdate(_ context.Context, n acp.SessionNotification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	u := n.Update
	// Earlier commentary cannot become the verdict after the agent inspects a tool result.
	if u.ToolCall != nil || u.ToolCallUpdate != nil {
		c.text.Reset()
	}
	if u.AgentMessageChunk != nil && u.AgentMessageChunk.Content.Text != nil {
		c.text.WriteString(u.AgentMessageChunk.Content.Text.Text)
	}
	return nil
}

// RequestPermission allows only file reading, searching, and thinking.
func (c *reviewClient) RequestPermission(_ context.Context, p acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	kinds := []acp.PermissionOptionKind{acp.PermissionOptionKindRejectOnce, acp.PermissionOptionKindRejectAlways}
	if p.ToolCall.Kind != nil && (*p.ToolCall.Kind == acp.ToolKindRead || *p.ToolCall.Kind == acp.ToolKindSearch || *p.ToolCall.Kind == acp.ToolKindThink) {
		kinds = []acp.PermissionOptionKind{acp.PermissionOptionKindAllowOnce, acp.PermissionOptionKindAllowAlways}
	} else {
		kind := "missing"
		if p.ToolCall.Kind != nil {
			kind = string(*p.ToolCall.Kind)
		}
		title := ""
		if p.ToolCall.Title != nil {
			title = *p.ToolCall.Title
		}
		c.mu.Lock()
		c.deniedEdit = fmt.Sprintf("reviewer requested %s permission: %s", kind, title)
		if c.stderr != nil {
			fmt.Fprintln(c.stderr, c.deniedEdit)
		}
		c.mu.Unlock()
	}
	for _, kind := range kinds {
		for _, o := range p.Options {
			if o.Kind == kind {
				return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Selected: &acp.RequestPermissionOutcomeSelected{OptionId: o.OptionId}}}, nil
			}
		}
	}
	if len(kinds) > 0 && kinds[0] == acp.PermissionOptionKindRejectOnce {
		return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}}}, nil
	}
	err := fmt.Errorf("reviewer offered no allow permission option")
	c.mu.Lock()
	c.permissionError = err
	c.mu.Unlock()
	return acp.RequestPermissionResponse{}, err
}

// scrubEnvironment prevents nested session identity and service token leakage to reviewers.
func scrubEnvironment(env []string) []string {
	var out []string
	for _, v := range env {
		k, _, _ := strings.Cut(v, "=")
		switch k {
		case "CLAUDECODE", "CLAUDE_CODE_SESSION_ID", "CLAUDE_SESSION_ID", "CLAUDE_CODE_PROJECT_DIR_NAME", "CLAUDE_ENV_FILE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_CHILD_SESSION", "CLAUDE_CODE_SESSION_ATTENDED", "CLAUDE_CODE_CHROME_MCP_ORG_DENIED", "CLAUDE_CODE_EVAL_INTERVIEW_SESSION", "CLAUDE_CODE_BRIDGE_SESSION_ID", "CLAUDE_PID", "CHECKPOINT_TRANSCRIPT", "CODEX_SESSION_ID", "CODEX_THREAD_ID", "CODEX_TOOL_CALL_ID", "SLOPCOP_TOKEN":
			continue
		}
		out = append(out, v)
	}
	return out
}

// reviewerEnvironment gives dsh an absolute data directory beside review logs.
func reviewerEnvironment(f Family, root, data string, env []string) []string {
	env = scrubEnvironment(env)
	if f != Flash {
		return env
	}
	for i, v := range env {
		k, x, _ := strings.Cut(v, "=")
		if k == "DSH_ACP_DATA_DIR" {
			if x == "" {
				x = data
			}
			if !filepath.IsAbs(x) {
				x = filepath.Join(root, x)
			}
			env[i] = k + "=" + x
			return env
		}
	}
	return append(env, "DSH_ACP_DATA_DIR="+data)
}

// runner owns an adapter process, its ACP session, and the per-reviewer timeout.
type runner struct {
	ctx             context.Context
	cancel          context.CancelFunc
	cmd             *exec.Cmd
	conn            *acp.ClientSideConnection
	client          *reviewClient
	stdin           io.WriteCloser
	updates, stderr *os.File
	exited          chan struct{}
	stopWatch       func() bool
	session         acp.SessionId
	result          Reviewer
}

// startRunner initializes adapters serially so first-run npx installs never overlap.
func startRunner(ctx context.Context, root string, argv []string, f Family, name, dir string, timeout time.Duration) (*runner, error) {
	s := &runner{result: Reviewer{Name: name, Family: f, Log: filepath.Join(dir, name+".jsonl")}, exited: make(chan struct{})}
	s.ctx, s.cancel = context.WithTimeout(ctx, timeout)
	var err error
	s.updates, err = os.OpenFile(s.result.Log, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		s.cancel()
		return nil, err
	}
	s.stderr, err = os.OpenFile(filepath.Join(dir, name+".stderr.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		s.updates.Close()
		s.cancel()
		return nil, err
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		s.closeFiles()
		s.cancel()
		return nil, err
	}
	s.cmd = exec.Command(path, argv[1:]...)
	s.cmd.Dir = root
	s.cmd.Env = reviewerEnvironment(f, root, filepath.Join(dir, name+"-data"), os.Environ())
	configureProcess(s.cmd)
	s.cmd.Stderr = s.stderr
	s.stdin, err = s.cmd.StdinPipe()
	if err != nil {
		s.closeFiles()
		s.cancel()
		return nil, err
	}
	stdout, err := s.cmd.StdoutPipe()
	if err != nil {
		s.stdin.Close()
		s.closeFiles()
		s.cancel()
		return nil, err
	}
	if err = s.cmd.Start(); err != nil {
		s.stdin.Close()
		stdout.Close()
		s.closeFiles()
		s.cancel()
		return nil, err
	}
	go func() { _ = s.cmd.Wait(); close(s.exited) }()
	s.stopWatch = context.AfterFunc(s.ctx, func() { killProcessTree(s.cmd) })
	s.client = &reviewClient{stderr: s.stderr}
	s.conn = acp.NewClientSideConnection(s.client, s.stdin, io.TeeReader(stdout, s.updates))
	s.conn.SetLogger(slog.New(slog.NewTextHandler(s.stderr, nil)))
	_, err = s.conn.Initialize(s.ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber, ClientCapabilities: acp.ClientCapabilities{}})
	if err != nil {
		s.stop()
		return nil, fmt.Errorf("initialize: %w", err)
	}
	return s, nil
}
func (s *runner) closeFiles() {
	if s.updates != nil {
		s.updates.Close()
	}
	if s.stderr != nil {
		s.stderr.Close()
	}
}
func (s *runner) stop() {
	s.stopWatch()
	s.cancel()
	killProcessTree(s.cmd)
	s.stdin.Close()
	<-s.exited
	<-s.conn.Done()
	s.closeFiles()
}
func (s *runner) prepare(root string) error {
	session, err := s.conn.NewSession(s.ctx, acp.NewSessionRequest{Cwd: root, McpServers: []acp.McpServer{}})
	if err != nil {
		return fmt.Errorf("session/new: %w", err)
	}
	s.session = session.SessionId
	modelOpt := configOption(session.ConfigOptions, "model")
	model, ok := bestModel(s.result.Family, choices(modelOpt))
	if !ok {
		return fmt.Errorf("no model of family %s", s.result.Family)
	}
	response, err := s.conn.SetSessionConfigOption(s.ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: s.session, ConfigId: modelOpt.Id, Value: acp.SessionConfigValueId(model)}})
	if err != nil {
		return fmt.Errorf("select model: %w", err)
	}
	s.result.Model = model
	effortOptions := response.ConfigOptions
	if len(effortOptions) == 0 {
		effortOptions = session.ConfigOptions
	}
	effort := configOption(effortOptions, "effort")
	if effort == nil {
		return fmt.Errorf("no effort option")
	}
	found := false
	for _, v := range choices(effort) {
		if string(v.Value) == "high" {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("high effort unavailable")
	}
	response, err = s.conn.SetSessionConfigOption(s.ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: s.session, ConfigId: effort.Id, Value: acp.SessionConfigValueId("high")}})
	if err != nil {
		return fmt.Errorf("select high effort: %w", err)
	}
	s.result.Effort = "high"
	modeOptions := response.ConfigOptions
	key, value := reviewerMode(s.result.Family)
	option := configOption(modeOptions, key)
	if option == nil {
		option = configOption(effortOptions, key)
	}
	if option == nil {
		option = configOption(session.ConfigOptions, key)
	}
	// The adapter's default mode may let the reviewer edit files or run
	// commands without asking, so a reviewer that cannot be put in its
	// restrictive mode does not run.
	if !hasChoice(option, value) {
		return fmt.Errorf("adapter offers no %s=%s option, so the reviewer cannot be restricted; update the adapter", key, value)
	}
	_, err = s.conn.SetSessionConfigOption(s.ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: s.session, ConfigId: option.Id, Value: acp.SessionConfigValueId(value)}})
	if err != nil {
		return fmt.Errorf("select %s=%s: %w", key, value, err)
	}
	return nil
}
func (s *runner) prompt(prompt string) (Reviewer, error) {
	defer s.stop()
	r, err := s.conn.Prompt(s.ctx, acp.PromptRequest{SessionId: s.session, Prompt: []acp.ContentBlock{acp.TextBlock(prompt)}})
	if err == nil && r.StopReason != acp.StopReasonEndTurn {
		err = fmt.Errorf("incomplete turn: %s", r.StopReason)
	}
	s.client.mu.Lock()
	s.result.Text = strings.TrimSpace(s.client.text.String())
	if s.client.permissionError != nil {
		err = s.client.permissionError
	}
	denied := s.client.deniedEdit
	s.client.mu.Unlock()
	if s.ctx.Err() != nil {
		err = s.ctx.Err()
	}
	if err != nil {
		s.result.Error = err.Error()
		if denied != "" {
			s.result.Error += "; " + denied
		}
	}
	s.result.Status = parseVerdict(s.result)
	return s.result, err
}
