package review

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// GitHub Copilot CLI as a reviewer adapter. Copilot fixes the model and the
// reasoning effort when it starts, so each reviewer gets its own process
// started with --model and --reasoning-effort, and a short session/new
// without a prompt first lists the models the account may use. The reviewer
// is restricted twice: --available-tools gives the model only read tools, and
// the client still refuses every other permission request.

const copilotAdapter = "copilot"

// copilotDefaultCommand is the pinned Copilot CLI in ACP server mode over
// stdio.
var copilotDefaultCommand = []string{"npx", "--yes", "@github/copilot@1.0.114", "--acp", "--stdio"}

// copilotReadOnlyTools are the only tools a Copilot reviewer may use. Without
// a write or shell tool the model cannot ask for one, and a refused request
// would otherwise end Copilot's whole turn.
var copilotReadOnlyTools = []string{"view", "grep", "glob"}

// copilotAuto is Copilot's own model choice, the only one Copilot Free
// offers.
const copilotAuto = "auto"

// copilotEffort is the reasoning effort a Copilot reviewer asks for first.
const copilotEffort = "high"

// copilotDefaultEffort records a reviewer that runs at its model's default
// effort, after Copilot refused copilotEffort.
const copilotDefaultEffort = "default"

// copilotFamilies are the families Copilot offers models of. It has no
// DeepSeek models, so flash keeps its own adapter.
var copilotFamilies = []Family{Terra, Sol, Haiku, Sonnet, Opus}

// CopilotServes reports whether Copilot offers models of a family.
func CopilotServes(f Family) bool { return slices.Contains(copilotFamilies, f) }

// copilotArgs adds the model, the effort and the read-only tool list to the
// base command. --available-tools takes every argument after it, so it goes
// last.
func copilotArgs(base []string, model, effort string) []string {
	args := slices.Clone(base)
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort != "" {
		args = append(args, "--reasoning-effort", effort)
	}
	args = append(args, "--available-tools")
	return append(args, copilotReadOnlyTools...)
}

// copilotCatalog lists the account's models once per review or probe and
// hands Auto to at most one family, and only on an account with no model of
// any family Copilot serves: Auto could pick the model another reviewer
// runs, which is not an independent review.
type copilotCatalog struct {
	base []string

	// loadMu guards models, which hold the first listing that succeeded.
	loadMu sync.Mutex
	models []acp.SessionConfigSelectOption

	mu      sync.Mutex
	autoFor Family
}

func newCopilotCatalog(base []string) *copilotCatalog {
	return &copilotCatalog{base: slices.Clone(base)}
}

// load lists the models until a listing succeeds and returns that one after,
// so a reviewer's retry lists them again after a failure.
func (c *copilotCatalog) load(ctx context.Context, root, dir string, timeout time.Duration) ([]acp.SessionConfigSelectOption, error) {
	c.loadMu.Lock()
	defer c.loadMu.Unlock()
	if c.models != nil {
		return c.models, nil
	}
	models, err := listCopilotModels(ctx, root, c.base, dir, timeout)
	if err != nil {
		return nil, err
	}
	c.models = models
	return models, nil
}

// listCopilotModels opens a session without prompting it, which costs no
// request, and reads its model option. Copilot has been seen to leave the
// option out of a session started without --model, so a second start names
// Auto.
func listCopilotModels(ctx context.Context, root string, base []string, dir string, timeout time.Duration) ([]acp.SessionConfigSelectOption, error) {
	for i, model := range []string{"", copilotAuto} {
		s, err := startRunner(ctx, root, copilotArgs(base, model, ""), "", fmt.Sprintf("copilot-models-%s-%d", time.Now().UTC().Format("150405.000000"), i+1), dir, timeout)
		if err != nil {
			return nil, fmt.Errorf("list Copilot models: %w", err)
		}
		session, err := s.conn.NewSession(s.ctx, acp.NewSessionRequest{Cwd: root, McpServers: []acp.McpServer{}})
		if err == nil {
			s.setSession(session.SessionId)
		}
		err = startError(s.ctx, err)
		s.stop()
		if err != nil {
			return nil, fmt.Errorf("list Copilot models: session/new: %w", err)
		}
		if o := configOption(session.ConfigOptions, "model"); o != nil {
			return choices(o), nil
		}
	}
	return nil, errors.New("list Copilot models: Copilot offered no model option")
}

// copilotEnablement is a model's enablement for the account, when Copilot
// says.
func copilotEnablement(o acp.SessionConfigSelectOption) string {
	v, _ := o.Meta["copilotEnablement"].(string)
	return v
}

// pick chooses the model a family reviews with, and reports whether it is
// Auto.
func (c *copilotCatalog) pick(f Family, models []acp.SessionConfigSelectOption) (string, bool, error) {
	var enabled []acp.SessionConfigSelectOption
	for _, o := range models {
		if e := copilotEnablement(o); e == "" || e == "enabled" {
			enabled = append(enabled, o)
		}
	}
	if m, ok := bestModel(f, enabled); ok {
		return m, false, nil
	}
	if m, ok := bestModel(f, models); ok {
		for _, o := range models {
			if string(o.Value) == m {
				return "", false, fmt.Errorf("Copilot lists %s for this account but it is not enabled (%s): enable it in your Copilot model settings, then run the check again", m, copilotEnablement(o))
			}
		}
	}
	named := slices.ContainsFunc(copilotFamilies, func(g Family) bool { _, ok := bestModel(g, enabled); return ok })
	if named || !slices.ContainsFunc(enabled, func(o acp.SessionConfigSelectOption) bool { return string(o.Value) == copilotAuto }) {
		names := make([]string, 0, len(models))
		for _, o := range models {
			names = append(names, string(o.Value))
		}
		return "", false, fmt.Errorf("Copilot offers this account no %s model (it offers %s)", f, strings.Join(names, ", "))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.autoFor != "" && c.autoFor != f {
		return "", false, fmt.Errorf("Copilot offers this account only Auto, and %s already reviews on it; a second Auto reviewer could get the same model, so %s does not run. Copilot Pro unlocks the terra, sol, haiku, sonnet and opus models", c.autoFor, f)
	}
	c.autoFor = f
	return copilotAuto, true, nil
}

// release frees Auto from a family whose reviewer failed, so a fallback or a
// later probe may run on it.
func (c *copilotCatalog) release(f Family) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.autoFor == f {
		c.autoFor = ""
	}
}

// startCopilot starts and prepares a Copilot reviewer for a family. It asks
// for copilotEffort first; if Copilot will not start or open a session with
// it, which it does for a model without effort settings or a CLI that does
// not know the flag, the reviewer starts again without it. On a failure it
// returns the stopped runner when there is one, so the result names its log.
func startCopilot(ctx context.Context, root string, catalog *copilotCatalog, f Family, name, dir string, timeout time.Duration, efforts ...string) (*runner, error) {
	models, err := catalog.load(ctx, root, dir, timeout)
	if err != nil {
		return nil, err
	}
	model, auto, err := catalog.pick(f, models)
	if err != nil {
		return nil, err
	}
	s, err := startCopilotModel(ctx, root, catalog.base, f, model, auto, name, dir, timeout, efforts...)
	if err != nil && auto {
		catalog.release(f)
	}
	return s, err
}

// startCopilotModel starts a reviewer on a chosen model, falling back to the
// model's default effort as startCopilot describes.
func startCopilotModel(ctx context.Context, root string, base []string, f Family, model string, auto bool, name, dir string, timeout time.Duration, efforts ...string) (*runner, error) {
	try := func(effort, attempt string) (*runner, error) {
		s, err := startRunner(ctx, root, copilotArgs(base, model, effort), f, attempt, dir, timeout)
		if err != nil {
			return nil, err
		}
		s.copilot = true
		s.result.Name = name
		s.result.Adapter = copilotAdapter
		if err = s.prepareCopilot(root, model); err != nil {
			err = startError(s.ctx, err)
			s.stop()
			return s, err
		}
		s.result.Effort = effort
		return s, nil
	}
	// Copilot refuses any reasoning effort on Auto.
	if auto {
		if slotEffort(efforts, 0) != "" {
			return nil, fmt.Errorf("%w: Copilot auto does not support configuring reasoning effort", errEffort)
		}
		s, err := try("", name)
		if err != nil {
			return s, err
		}
		s.result.Effort = copilotDefaultEffort
		s.result.Warning = copilotWarning(f, model, auto, nil)
		return s, nil
	}
	s, err := try(effectiveEffort(slotEffort(efforts, 0)), name)
	if err == nil {
		s.result.Warning = copilotWarning(f, model, auto, nil)
		return s, nil
	}
	if slotEffort(efforts, 0) != "" {
		return s, fmt.Errorf("%w: Copilot refused %s effort: %v", errEffort, efforts[0], err)
	}
	// A timeout would only double the wait.
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return s, err
	}
	refused := err
	if s, err = try("", name+"-default-effort"); err != nil {
		return s, fmt.Errorf("%w (with reasoning effort %s it failed first: %v)", err, copilotEffort, refused)
	}
	s.result.Effort = copilotDefaultEffort
	s.result.Warning = copilotWarning(f, model, auto, refused)
	return s, nil
}

// prepareCopilot opens the session and checks that Copilot will ask before
// any tool and runs the model asked for.
func (s *runner) prepareCopilot(root, model string) error {
	session, err := s.conn.NewSession(s.ctx, acp.NewSessionRequest{Cwd: root, McpServers: []acp.McpServer{}})
	if err != nil {
		return fmt.Errorf("session/new: %w", err)
	}
	s.setSession(session.SessionId)
	options := session.ConfigOptions
	set := func(o *acp.SessionConfigOptionSelect, value string) error {
		r, err := s.conn.SetSessionConfigOption(s.ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: session.SessionId, ConfigId: o.Id, Value: acp.SessionConfigValueId(value)}})
		if err != nil {
			return fmt.Errorf("select %s=%s: %w", o.Id, value, err)
		}
		if len(r.ConfigOptions) > 0 {
			options = r.ConfigOptions
		}
		return nil
	}
	// Autopilot turns on allow_all; the reviewer stays in the agent mode.
	if o := configOption(options, "mode"); o != nil && strings.HasSuffix(string(o.CurrentValue), "#autopilot") {
		agent := ""
		for _, c := range choices(o) {
			if strings.HasSuffix(string(c.Value), "#agent") {
				agent = string(c.Value)
			}
		}
		if agent == "" {
			return errors.New("Copilot started in autopilot and offers no agent mode, so the reviewer cannot be restricted")
		}
		if err := set(o, agent); err != nil {
			return err
		}
	}
	// With allow_all on, Copilot would approve its own tool requests.
	if o := configOption(options, "allow_all"); o != nil && o.CurrentValue != "off" {
		if !hasChoice(o, "off") {
			return errors.New("Copilot offers no allow_all=off option, so the reviewer cannot be restricted; update Copilot CLI")
		}
		if err := set(o, "off"); err != nil {
			return err
		}
	}
	if o := configOption(options, "model"); o != nil && string(o.CurrentValue) != model {
		if !hasChoice(o, model) {
			return fmt.Errorf("Copilot started on model %s, not %s", o.CurrentValue, model)
		}
		if err := set(o, model); err != nil {
			return err
		}
	}
	s.result.Model = model
	return nil
}

// copilotWarning says how a Copilot reviewer runs differently from what its
// family asks for.
func copilotWarning(f Family, model string, auto bool, refused error) string {
	var parts []string
	if auto {
		parts = append(parts, fmt.Sprintf("Copilot offers this account only Auto, so this reviewer ran on a model Copilot picked instead of a %s model. Copilot Free also has a small monthly request allowance that every check draws on; Copilot Pro unlocks the terra, sol, haiku, sonnet and opus models.", f))
	}
	if refused != nil {
		why := refused.Error()
		if len(why) > 300 {
			why = why[:300] + "..."
		}
		parts = append(parts, fmt.Sprintf("Copilot would not start %s at reasoning effort %s, so this reviewer ran at the model's default effort (%s).", model, copilotEffort, why))
	}
	return strings.Join(parts, " ")
}

// copilotNotice matches the first line of a reply that is Copilot's own
// status line, such as "Error: Execution failed: ..." or "Info: Operation
// cancelled by user", which Copilot sends as the reply of a normal turn.
var copilotNotice = regexp.MustCompile(`^(Error|Info|Warning): `)

// copilotErrorReply reports whether a Copilot reply is its error rather than
// a review. A review either approves or tags its findings.
func copilotErrorReply(text string) bool {
	return text != CleanReply && !priorityTag.MatchString(text) && copilotNotice.MatchString(text)
}

// launch starts a family's adapter and prepares its session. On a prepare
// failure it returns the stopped runner, whose result names the log.
func launch(ctx context.Context, root string, trees reviewTrees, adapter string, argv []string, catalog *copilotCatalog, ollamaModel string, f Family, name, dir string, timeout time.Duration, efforts ...string) (*runner, error) {
	if adapter == copilotAdapter {
		if catalog == nil {
			catalog = newCopilotCatalog(argv)
		}
		return startCopilot(ctx, root, catalog, f, name, dir, timeout, efforts...)
	}
	if adapter == antigravityAdapter {
		return startAntigravity(ctx, root, trees, argv, f, name, dir, timeout, efforts...)
	}
	if adapter == kimiAdapter {
		return startKimi(ctx, root, trees, argv, f, name, dir, timeout, efforts...)
	}
	if adapter == grokAdapter {
		return startGrok(ctx, root, trees, argv, f, name, dir, timeout, efforts...)
	}
	if adapter == ollamaAdapter && slotEffort(efforts, 0) != "" {
		return nil, fmt.Errorf("%w: ollama does not support configuring reasoning effort", errEffort)
	}
	if adapter == ollamaAdapter {
		return startOllama(ctx, root, trees, argv, ollamaModel, f, name, dir, timeout)
	}
	if IsChatAdapter(adapter) {
		return startChat(ctx, root, trees, argv, f, name, dir, timeout, efforts...)
	}
	s, err := startRunner(ctx, root, argv, f, name, dir, timeout)
	if err != nil {
		return nil, err
	}
	s.result.Adapter = adapter
	if err = s.prepare(root, efforts...); err != nil {
		err = startError(s.ctx, err)
		s.stop()
		return s, err
	}
	return s, nil
}

// CopilotMayBeLoggedIn reports whether Copilot CLI may find a GitHub login:
// a token in the environment, its own saved login, or a GitHub CLI login,
// which it also uses. Like the other checks it looks only for what is
// certainly absent.
func CopilotMayBeLoggedIn() bool {
	if anyEnv("COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN") {
		return true
	}
	user, err := os.UserHomeDir()
	if err != nil {
		return true
	}
	home := strings.TrimSpace(os.Getenv("COPILOT_HOME"))
	if home == "" {
		home = filepath.Join(user, ".copilot")
	}
	if exists(filepath.Join(home, "config.json")) {
		return true
	}
	gh := strings.TrimSpace(os.Getenv("GH_CONFIG_DIR"))
	if gh == "" {
		config := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
		if config == "" {
			config = filepath.Join(user, ".config")
		}
		gh = filepath.Join(config, "gh")
	}
	return exists(filepath.Join(gh, "hosts.yml"))
}
