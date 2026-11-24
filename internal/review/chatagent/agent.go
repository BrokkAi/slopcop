// Package chatagent is a read-only ACP agent for OpenAI-compatible chat
// services. SlopCop starts it as a child process, like any other reviewer
// adapter. It exists because a general coding agent loads configuration and
// plugins from the repository it runs in: OpenCode 1.18 ran a repository's
// plugin even with --pure and project config off.
//
// The model gets three tools that answer from one git tree. The agent has no
// shell, writes no files, keeps no history, loads no configuration from the
// repository, and sends requests only to its provider's fixed URL.
package chatagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"

	acp "github.com/coder/acp-go-sdk"
)

const (
	// maxRounds bounds the model requests of one review.
	maxRounds = 40
	// maxSentBytes bounds what one review sends in all its requests. Each
	// request carries the whole conversation so far, so this, not
	// maxToolBytes, is what bounds the input a repository can make it pay for.
	maxSentBytes = 32 << 20
	// maxToolBytes bounds what tools return in one review, and so the cost a
	// repository can cause by asking the reviewer to read everything.
	maxToolBytes = 8 << 20
	// maxResponse bounds one model response.
	maxResponse = 32 << 20
	// maxErrorText bounds provider error text kept in logs.
	maxErrorText = 500
	// authRequired is the ACP error code for a missing or rejected login.
	authRequired = -32000
	internalErr  = -32603
)

// Config is one agent's fixed setup.
type Config struct {
	Provider Provider
	Model    string
	Key      string
	// Tree is the git tree the tools read. Without one, as in a probe, the
	// model is offered no tools.
	Tree string
	// Base is the tree the change is compared with, which tells the agent
	// which files changed.
	Base string
	// ReadBudget overrides maxToolBytes, and SendBudget maxSentBytes, when
	// positive.
	ReadBudget, SendBudget int
}

// limitPrefix starts the error of a reviewer stopped at one of its limits.
const limitPrefix = "the reviewer reached its limit of "

var (
	errReadLimit  = errors.New(limitPrefix + "8 MiB of file reads before a verdict, so it did not finish the review; configure a fallback on another provider or check a smaller change")
	errSendLimit  = errors.New(limitPrefix + "32 MiB sent to the model before a verdict, so it did not finish the review; configure a fallback on another provider or check a smaller change")
	errRoundLimit = fmt.Errorf(limitPrefix+"%d model requests before a verdict, so it did not finish the review; configure a fallback on another provider or check a smaller change", maxRounds)
)

// IsLimitError reports whether error text is a reviewer's limit. A retry
// would spend the same again, at a repository's prompting.
func IsLimitError(text string) bool {
	return strings.Contains(text, limitPrefix)
}

var treeID = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

// Main runs the agent for the hidden review-agent command. It takes the API
// key out of the environment, so the git commands it runs never hold it, and
// turns off git's on-demand fetch, so a read stays on this machine.
func Main(ctx context.Context, provider, model, tree, base string, in io.Reader, out io.Writer) error {
	p, ok := Lookup(provider)
	if !ok {
		return fmt.Errorf("unknown review provider %q", provider)
	}
	if err := p.CheckModel(model); err != nil {
		return err
	}
	for _, id := range []string{tree, base} {
		if id != "" && !treeID.MatchString(id) {
			return fmt.Errorf("invalid tree id %q", id)
		}
	}
	key := ""
	if p.KeyEnv != "" {
		key = strings.TrimSpace(os.Getenv(p.KeyEnv))
		if err := os.Unsetenv(p.KeyEnv); err != nil {
			return err
		}
	}
	for name, value := range map[string]string{"GIT_NO_LAZY_FETCH": "1", "GIT_TERMINAL_PROMPT": "0", "GIT_OPTIONAL_LOCKS": "0"} {
		if err := os.Setenv(name, value); err != nil {
			return err
		}
	}
	return Serve(ctx, Config{Provider: p, Model: model, Key: key, Tree: tree, Base: base}, in, out)
}

// Serve speaks ACP on in and out until the client disconnects.
func Serve(ctx context.Context, cfg Config, in io.Reader, out io.Writer) error {
	a := &agent{cfg: cfg, http: &http.Client{
		// A redirect could carry the prompt somewhere else.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	// The connection starts reading at once, and its handlers use a.conn.
	ready := make(chan struct{})
	a.conn = acp.NewAgentSideConnection(a, out, gatedReader{in, ready})
	close(ready)
	select {
	case <-a.conn.Done():
	case <-ctx.Done():
	}
	return nil
}

// gatedReader holds reads back until open is closed.
type gatedReader struct {
	io.Reader
	open <-chan struct{}
}

func (g gatedReader) Read(p []byte) (int, error) {
	<-g.open
	return g.Reader.Read(p)
}

type agent struct {
	acp.Agent
	conn *acp.AgentSideConnection
	cfg  Config
	http *http.Client

	mu     sync.Mutex
	tree   *snapshot
	cancel context.CancelFunc
}

func (a *agent) Initialize(context.Context, acp.InitializeRequest) (acp.InitializeResponse, error) {
	return acp.InitializeResponse{ProtocolVersion: acp.ProtocolVersionNumber}, nil
}

// Cancel ends the running prompt.
func (a *agent) Cancel(context.Context, acp.CancelNotification) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
	}
	return nil
}

// NewSession checks the key and loads the tree. The one model option tells
// the client which model will answer.
func (a *agent) NewSession(ctx context.Context, p acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	if a.cfg.Provider.KeyEnv != "" && a.cfg.Key == "" {
		return acp.NewSessionResponse{}, &acp.RequestError{Code: authRequired, Message: "authentication required: set " + a.cfg.Provider.KeyEnv}
	}
	if a.cfg.Tree != "" {
		tree, err := loadSnapshot(ctx, p.Cwd, a.cfg.Tree, a.cfg.Base)
		if err != nil {
			return acp.NewSessionResponse{}, &acp.RequestError{Code: internalErr, Message: "read the reviewed tree: " + err.Error()}
		}
		a.mu.Lock()
		a.tree = tree
		a.mu.Unlock()
	}
	value := acp.SessionConfigValueId(a.cfg.Model)
	models := acp.SessionConfigSelectOptionsUngrouped{{Value: value, Name: a.cfg.Model}}
	return acp.NewSessionResponse{SessionId: "review", ConfigOptions: []acp.SessionConfigOption{{Select: &acp.SessionConfigOptionSelect{
		Type: "select", Id: "model", Name: "Model", CurrentValue: value, Options: acp.SessionConfigSelectOptions{Ungrouped: &models},
	}}}}, nil
}

// Prompt runs the review and sends only its final reply.
func (a *agent) Prompt(ctx context.Context, p acp.PromptRequest) (acp.PromptResponse, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.mu.Lock()
	a.cancel = cancel
	tree := a.tree
	a.mu.Unlock()
	var prompt strings.Builder
	for _, block := range p.Prompt {
		if block.Text != nil {
			prompt.WriteString(block.Text.Text)
		}
	}
	reply, err := a.converse(ctx, p.SessionId, tree, prompt.String())
	if err != nil {
		var requestErr *acp.RequestError
		if errors.As(err, &requestErr) {
			return acp.PromptResponse{}, requestErr
		}
		return acp.PromptResponse{}, &acp.RequestError{Code: internalErr, Message: a.scrub(err.Error())}
	}
	if err = a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: p.SessionId, Update: acp.UpdateAgentMessageText(reply)}); err != nil {
		return acp.PromptResponse{}, err
	}
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// converse loops between the model and the tools until the model replies. Any
// reply that was cut short or filtered is an error, never a verdict.
func (a *agent) converse(ctx context.Context, session acp.SessionId, tree *snapshot, prompt string) (string, error) {
	messages := []any{map[string]any{"role": "user", "content": prompt}}
	var tools []map[string]any
	if tree != nil {
		tools = toolDefinitions()
	}
	budget, sendBudget := maxToolBytes, maxSentBytes
	if a.cfg.ReadBudget > 0 {
		budget = a.cfg.ReadBudget
	}
	if a.cfg.SendBudget > 0 {
		sendBudget = a.cfg.SendBudget
	}
	for round := range maxRounds {
		raw, err := a.complete(ctx, messages, tools, &sendBudget)
		if err != nil {
			return "", err
		}
		if a.cfg.Provider.ServerToolUse != nil {
			if what := a.cfg.Provider.ServerToolUse(raw); what != "" {
				return "", a.reportServerTool(ctx, session, what)
			}
		}
		message, finish, err := a.parse(raw)
		if err != nil {
			return "", err
		}
		var m struct {
			Content   json.RawMessage `json:"content"`
			ToolCalls []toolCall      `json:"tool_calls"`
		}
		if err = json.Unmarshal(message, &m); err != nil {
			return "", fmt.Errorf("%s sent an unreadable message", a.cfg.Provider.Label)
		}
		switch finish {
		case "stop", "tool_calls", "":
		case "length":
			return "", fmt.Errorf("%s cut the reply off at the model's output limit, so it is not a verdict", a.cfg.Provider.Label)
		default:
			return "", fmt.Errorf("%s ended the reply with finish reason %q, so it is not a verdict", a.cfg.Provider.Label, finish)
		}
		if len(m.ToolCalls) == 0 {
			text, err := messageText(m.Content)
			if err != nil {
				return "", fmt.Errorf("%s sent a reply in an unknown format", a.cfg.Provider.Label)
			}
			if strings.TrimSpace(text) == "" {
				return "", fmt.Errorf("%s returned an empty reply", a.cfg.Provider.Label)
			}
			if tree != nil {
				if path := tree.unreadChange(); path != "" {
					return "", fmt.Errorf("the reviewer replied after a read of changed file %q failed, so the reply is not a verdict", path)
				}
			}
			return a.scrub(text), nil
		}
		if tree == nil {
			return "", fmt.Errorf("the model called a tool during a probe")
		}
		// The message goes back unchanged: some models need the reasoning
		// fields it carries to continue after a tool call.
		messages = append(messages, message)
		for i, call := range m.ToolCalls {
			if call.Type != "" && call.Type != "function" {
				return "", a.reportServerTool(ctx, session, call.Type)
			}
			result, err := a.runTool(ctx, session, tree, fmt.Sprintf("%d-%d", round, i), call, &budget)
			if err != nil {
				return "", err
			}
			if err = ctx.Err(); err != nil {
				return "", err
			}
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": call.ID, "content": result})
		}
	}
	return "", errRoundLimit
}

// runTool runs one call, logs it over ACP, and returns what the model sees. A
// tool's own failure goes to the model. Running out of budget ends the review:
// a verdict without what the model asked to read would not be one.
func (a *agent) runTool(ctx context.Context, session acp.SessionId, tree *snapshot, id string, call toolCall, budget *int) (string, error) {
	title := call.Function.Name
	if len(title) > 64 {
		title = title[:64]
	}
	var input map[string]any
	if json.Unmarshal([]byte(call.Function.Arguments), &input) != nil {
		input = nil
	}
	// A tool the agent does not offer is logged as what it is, and as failed,
	// since nothing runs it.
	kind, known := toolKind(call.Function.Name)
	status := acp.ToolCallStatusCompleted
	if !known {
		status = acp.ToolCallStatusFailed
	}
	_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: session, Update: acp.StartToolCall(acp.ToolCallId(id), title,
		acp.WithStartKind(kind), acp.WithStartStatus(status), acp.WithStartRawInput(input))})
	result, err := tree.call(ctx, call.Function.Name, call.Function.Arguments)
	if err != nil {
		// An error can echo what the model sent, so it counts too.
		result = "Error: " + err.Error()
	}
	if len(result) > *budget {
		return "", errReadLimit
	}
	*budget -= len(result)
	return result, nil
}

// reportServerTool tells the client a tool ran that it could not refuse. It is
// reported as a fetch, which SlopCop's guard stops without a retry.
func (a *agent) reportServerTool(ctx context.Context, session acp.SessionId, what string) error {
	_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: session, Update: acp.StartToolCall("server-tool", what,
		acp.WithStartKind(acp.ToolKindFetch), acp.WithStartStatus(acp.ToolCallStatusCompleted))})
	return fmt.Errorf("%s ran %s by itself", a.cfg.Provider.Label, what)
}

// complete sends one chat request, charging its size to sendBudget, and
// returns the response body.
func (a *agent) complete(ctx context.Context, messages []any, tools []map[string]any, sendBudget *int) ([]byte, error) {
	body := make(map[string]any, len(a.cfg.Provider.Request)+4)
	for k, v := range a.cfg.Provider.Request {
		body[k] = v
	}
	body["model"], body["messages"], body["stream"] = a.cfg.Model, messages, false
	if len(tools) > 0 {
		body["tools"], body["tool_choice"] = tools, "auto"
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	if len(data) > *sendBudget {
		return nil, errSendLimit
	}
	*sendBudget -= len(data)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(a.cfg.Provider.BaseURL, "/")+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if a.cfg.Key != "" {
		req.Header.Set("Authorization", "Bearer "+a.cfg.Key)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) && ctx.Err() == nil {
			return nil, fmt.Errorf("could not reach %s: %v", a.cfg.Provider.Label, urlErr.Err)
		}
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return nil, fmt.Errorf("could not reach %s: reading the response: %v", a.cfg.Provider.Label, err)
	}
	if len(raw) > maxResponse {
		return nil, fmt.Errorf("%s sent a response over the size limit", a.cfg.Provider.Label)
	}
	// Before anything parses or shortens it, so an echoed key cannot survive
	// in part.
	if a.cfg.Key != "" {
		raw = bytes.ReplaceAll(raw, []byte(a.cfg.Key), []byte("[api key]"))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, a.statusError(resp.StatusCode, raw)
	}
	return raw, nil
}

// statusError turns a failed request into text failureCause can classify.
func (a *agent) statusError(status int, raw []byte) error {
	detail := a.apiErrorText(raw)
	label := a.cfg.Provider.Label
	switch status {
	case http.StatusUnauthorized:
		return &acp.RequestError{Code: authRequired, Message: a.scrub(fmt.Sprintf("authentication required: %s rejected %s (status %d): %s", label, a.cfg.Provider.KeyEnv, status, detail))}
	case http.StatusForbidden:
		// The key was accepted: a 403 is a budget, a key limit or moderation.
		lower := strings.ToLower(detail)
		if strings.Contains(lower, "budget") || strings.Contains(lower, "limit") {
			return fmt.Errorf("%s usage limit reached (status 403): %s", label, detail)
		}
		return fmt.Errorf("%s refused the request (status 403): %s", label, detail)
	case http.StatusPaymentRequired:
		return fmt.Errorf("%s payment required, the account is out of credits: %s", label, detail)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%s rate limit (status 429): %s", label, detail)
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return fmt.Errorf("%s service unavailable (status %d), or no endpoint for this model meets the reviewer's privacy and tool requirements: %s", label, status, detail)
	}
	return fmt.Errorf("%s returned status %d: %s", label, status, detail)
}

// parse takes the first choice from a response, treating an error sent with
// status 200 as a failure.
func (a *agent) parse(raw []byte) (json.RawMessage, string, error) {
	var r struct {
		Error   json.RawMessage `json:"error"`
		Choices []struct {
			Message      json.RawMessage `json:"message"`
			FinishReason *string         `json:"finish_reason"`
			Error        json.RawMessage `json:"error"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, "", fmt.Errorf("%s sent an unreadable response", a.cfg.Provider.Label)
	}
	if len(r.Error) > 0 && string(r.Error) != "null" {
		return nil, "", fmt.Errorf("%s reported an error: %s", a.cfg.Provider.Label, a.errorText(r.Error))
	}
	if len(r.Choices) == 0 || len(r.Choices[0].Message) == 0 {
		return nil, "", fmt.Errorf("%s sent a response with no message", a.cfg.Provider.Label)
	}
	c := r.Choices[0]
	if len(c.Error) > 0 && string(c.Error) != "null" {
		return nil, "", fmt.Errorf("%s reported an error: %s", a.cfg.Provider.Label, a.errorText(c.Error))
	}
	finish := ""
	if c.FinishReason != nil {
		finish = *c.FinishReason
	}
	return c.Message, finish, nil
}

// messageText reads a message's content, a string or a list of text parts.
func messageText(content json.RawMessage) (string, error) {
	if len(content) == 0 || string(content) == "null" {
		return "", nil
	}
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &parts); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String(), nil
}

// apiErrorText and errorText scrub decoded text before shortening it: the raw
// scrub misses a key written with JSON escapes, which decoding restores.
func (a *agent) apiErrorText(raw []byte) string {
	var r struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &r) == nil && len(r.Error) > 0 {
		return a.errorText(r.Error)
	}
	return a.plainText(raw)
}

func (a *agent) errorText(raw json.RawMessage) string {
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Message != "" {
		return clip(a.scrub(e.Message))
	}
	return a.plainText(raw)
}

// plainText shortens raw response text for an error. JSON is decoded and
// written again without escapes first, so a key hidden behind escapes such as
// \u0074 is in plain form when the scrub looks for it.
func (a *agent) plainText(raw []byte) string {
	text := string(raw)
	var v any
	if json.Unmarshal(raw, &v) == nil {
		var b strings.Builder
		e := json.NewEncoder(&b)
		e.SetEscapeHTML(false)
		if e.Encode(v) == nil {
			text = b.String()
		}
	}
	return clip(a.scrub(text))
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxErrorText {
		return s[:maxErrorText] + "..."
	}
	return s
}

// scrub removes the API key from text that reaches logs, in case a service
// echoes it in an error.
func (a *agent) scrub(s string) string {
	if a.cfg.Key == "" {
		return s
	}
	return strings.ReplaceAll(s, a.cfg.Key, "[api key]")
}
