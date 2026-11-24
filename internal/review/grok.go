package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	acp "github.com/coder/acp-go-sdk"
)

// Grok Build's ACP server as an opt-in reviewer adapter for xAI's Grok models.
// Checked live against 1.0.92 on macOS. Its behavior shapes how it runs:
//
//   - It loads hooks, MCP servers, skills and rules from the repository, from
//     its home directory, and from Claude Code and Cursor settings. So the
//     reviewer gets an empty workspace with an export one level down, its own
//     HOME and GROK_HOME, and those compatibility surfaces turned off.
//   - A built-in sandbox profile that fails to apply only logs a warning, but
//     a custom profile refuses to start. So the reviewer uses a custom one.
//   - Read-only tools, web search and subagent control run without asking,
//     and Grok's strict sandbox can still read every temporary directory. So
//     a profile keeps only read_file, and SlopCop serves each read itself over
//     ACP, which refuses a path outside the workspace before anything is read.
//     toolGuard stops the reviewer on any other tool, on a path outside the
//     workspace, and on a read that did not come through SlopCop.
//   - The sandbox leaves child network open on macOS, and in-process requests
//     open everywhere. Nothing here depends on it to stop network use.
const grokAdapter = "grok"

// grokDefaultCommand is the pinned CLI. startGrok adds the restrictions.
var grokDefaultCommand = []string{"npx", "--yes", "@xai-official/grok@1.0.92"}

// grokSandboxProfile names the custom sandbox profile in the reviewer's home.
const grokSandboxProfile = "slopcop-review"

// grokReadTool is the only tool a Grok reviewer may call. Its grep and list_dir
// read the disk directly, where SlopCop could not refuse a path first.
const grokReadTool = "read_file"

// grokMaxRead bounds one file served to the reviewer. It is above the export's
// per-file limit so that a large files.txt index can still be read.
const grokMaxRead = 16 << 20

// grokRules tells the reviewer what it may use. The profile and the guard
// enforce it; this keeps an honest reviewer from ending its own turn.
const grokRules = "The snapshot's files are in the `repo` folder of your workspace, and `files.txt` in the workspace lists them. Read them only with the read_file tool, using paths inside the workspace. Any other tool, or a path outside the workspace, stops your review.\n"

// grokProfile is the agent profile that removes every other tool. The MCP
// meta-tools survive an allowlist, so they are denied by name.
const grokProfile = `---
name: slopcop-review
description: Read-only code reviewer
tools:
  - read_file
disallowedTools:
  - search_tool
  - use_tool
  - Agent
---

You review a code change. Only read files in your workspace.
`

// errGrokNoKey is worded so that failureCause reads it as a missing login,
// which is not retried.
var errGrokNoKey = errors.New("api key not set: export XAI_API_KEY in the shell that runs slopcop. A Grok reviewer does not use a browser login, because its home directory is empty")

// grokFixedArgs are arguments SlopCop sets itself. A command override may not
// set them, since each one could widen what the reviewer can reach.
var grokFixedArgs = []string{"agent", "--always-approve", "--yolo", "--permission-mode", "--sandbox", "--trust", "--trust-folder", "--plugin-dir", "--agent-profile", "--cwd", "--leader", "--xai-api-base-url", "--cli-chat-proxy-base-url"}

func checkGrokCommand(base []string) error {
	for _, arg := range base[1:] {
		name, _, _ := strings.Cut(arg, "=")
		if slices.Contains(grokFixedArgs, name) {
			return fmt.Errorf("review.adapters.grok.command sets %s, so the reviewer cannot be restricted; give only the command that starts the Grok CLI", name)
		}
	}
	return nil
}

// startGrok exports the checked tree into a fresh workspace and starts a
// reviewer there. On a failure after the adapter started it returns the
// stopped runner, whose result names the log.
func startGrok(ctx context.Context, root string, trees reviewTrees, base []string, f Family, name, dir string, timeout time.Duration) (*runner, error) {
	if err := checkGrokCommand(base); err != nil {
		return nil, err
	}
	if runtime.GOOS == "windows" {
		return nil, errors.New("a Grok reviewer needs Grok's macOS or Linux sandbox, so it does not run on Windows")
	}
	if strings.TrimSpace(os.Getenv("XAI_API_KEY")) == "" {
		return nil, errGrokNoKey
	}
	realHome, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("find the home directory to keep the Grok reviewer out of: %w", err)
	}
	tmp, workspace, err := isolatedWorkspace(ctx, root, trees, "slopcop-grok-")
	if err != nil {
		return nil, fmt.Errorf("prepare the Grok workspace: %w", err)
	}
	home := filepath.Join(tmp, "home")
	profile, err := writeGrokHome(home, tmp, realHome)
	if err != nil {
		_ = os.RemoveAll(tmp)
		return nil, fmt.Errorf("prepare the Grok home: %w", err)
	}
	argv, startDir, err := grokLaunch(base, []string{"--sandbox", grokSandboxProfile, "--disable-web-search", "--no-auto-update", "agent", "--agent-profile", profile, "--no-leader", "stdio"}, workspace)
	if err != nil {
		_ = os.RemoveAll(tmp)
		return nil, err
	}
	check := newGrokToolCheck(workspace)
	s, err := startRunnerWith(ctx, root, argv, f, name, dir, timeout, runnerOptions{
		guard:       &toolGuard{check: check.check},
		refuseAll:   true,
		readRoot:    workspace,
		onRead:      check.served,
		dir:         startDir,
		environment: func(env []string) []string { return grokEnvironment(env, home, realHome) },
	})
	if err != nil {
		_ = os.RemoveAll(tmp)
		return nil, err
	}
	s.tempDir = tmp
	s.result.Adapter = grokAdapter
	if err = s.prepare(workspace); err != nil {
		err = startError(s.ctx, err)
		s.stop()
		return s, err
	}
	return s, nil
}

// grokLaunch returns the command that runs Grok in workspace, where it roots
// its sandbox, and the directory to start that command in. The workspace cannot
// be under the user's home, which the sandbox denies, so its parents can be a
// shared temp directory. npm reads config, packages and binaries from every
// parent of where it starts, so npx starts in the private adapter directory
// (an empty result) and the command it runs enters the workspace.
func grokLaunch(base, args []string, workspace string) ([]string, string, error) {
	return launchInWorkspace(base, args, workspace, "grok", "review.adapters.grok.command", "Grok")
}

// writeGrokHome fills the reviewer's GROK_HOME and returns the agent profile's
// path. The sandbox denies the user's real home, which holds their logins.
func writeGrokHome(home, tmp, realHome string) (string, error) {
	deny := []string{realHome}
	if resolved, err := filepath.EvalSymlinks(realHome); err == nil && resolved != realHome {
		deny = append(deny, resolved)
	}
	for _, d := range deny {
		if pathInside(d, tmp) {
			return "", fmt.Errorf("the temporary directory %s is inside your home directory, which the reviewer's sandbox denies; set TMPDIR to a directory outside it", tmp)
		}
	}
	grokHome := filepath.Join(home, ".grok")
	if err := os.MkdirAll(grokHome, 0700); err != nil {
		return "", err
	}
	config := map[string]any{
		"features":  map[string]any{"telemetry": "off"},
		"memory":    map[string]any{"enabled": false},
		"relay":     map[string]any{"enabled": false},
		"subagents": map[string]any{"enabled": false},
		"telemetry": map[string]any{"mixpanel_enabled": false, "trace_upload": false, "otel_enabled": false},
	}
	// On macOS restrict_network blocks nothing and only adds Grok's masks for
	// container sockets, which refuse to start when Docker Desktop's socket
	// links into the real home. The deny list already covers that socket.
	sandbox := map[string]any{"profiles": map[string]any{grokSandboxProfile: map[string]any{
		"extends":          "strict",
		"restrict_network": runtime.GOOS != "darwin",
		"deny":             deny,
	}}}
	for file, value := range map[string]any{"config.toml": config, "sandbox.toml": sandbox} {
		var b bytes.Buffer
		if err := toml.NewEncoder(&b).Encode(value); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(grokHome, file), b.Bytes(), 0600); err != nil {
			return "", err
		}
	}
	profile := filepath.Join(grokHome, grokSandboxProfile+".md")
	return profile, os.WriteFile(profile, []byte(grokProfile), 0600)
}

// grokEnvironment replaces everything that tells Grok where to load settings
// or send data. Environment settings outrank remote ones, so the compatibility
// surfaces are turned off here as well as by the empty HOME. The API key is
// the only credential kept.
func grokEnvironment(env []string, home, realHome string) []string {
	out := make([]string, 0, len(env)+40)
	seen := map[string]bool{}
	for _, v := range env {
		k, _, _ := strings.Cut(v, "=")
		upper := strings.ToUpper(k)
		if k == "HOME" || upper == "NPM_CONFIG_IGNORE_SCRIPTS" || strings.HasPrefix(upper, "GROK_") || strings.HasPrefix(upper, "OTEL_") || strings.HasPrefix(upper, "XDG_") || strings.HasPrefix(upper, "XAI_") && upper != "XAI_API_KEY" {
			continue
		}
		seen[strings.ToLower(k)] = true
		out = append(out, v)
	}
	out = append(out,
		"HOME="+home,
		"GROK_HOME="+filepath.Join(home, ".grok"),
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
		"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
		"XDG_STATE_HOME="+filepath.Join(home, ".local", "state"),
		"GROK_DISABLE_AUTOUPDATER=1",
		"GROK_FOLDER_TRUST=1",
		"GROK_SUBAGENTS=0",
		"GROK_MEMORY=0",
		"GROK_WEB_FETCH=0",
		// A .gitignore in the change must not hide files from the reviewer.
		"GROK_RESPECT_GITIGNORE=0",
		"GROK_FEEDBACK_ENABLED=0",
		"GROK_EXTERNAL_OTEL=0",
		"GROK_TELEMETRY_MIXPANEL_ENABLED=0",
		"GROK_TELEMETRY_TRACE_UPLOAD=0",
		"GROK_CODEX_SESSIONS_ENABLED=0",
	)
	for _, vendor := range []string{"CLAUDE", "CURSOR"} {
		for _, surface := range []string{"SKILLS", "RULES", "AGENTS", "MCPS", "HOOKS", "SESSIONS"} {
			out = append(out, "GROK_"+vendor+"_"+surface+"_ENABLED=0")
		}
	}
	// The package's launcher unpacks the binary itself, so npx need not run
	// its install script.
	out = append(out, "npm_config_ignore_scripts=true")
	// npx keeps its cache and registry settings under HOME.
	if !seen["npm_config_cache"] {
		out = append(out, "npm_config_cache="+filepath.Join(realHome, ".npm"))
	}
	if !seen["npm_config_userconfig"] {
		out = append(out, "npm_config_userconfig="+filepath.Join(realHome, ".npmrc"))
	}
	return out
}

// pathInside reports whether path is dir or below it.
func pathInside(dir, path string) bool {
	dir, path = filepath.Clean(dir), filepath.Clean(path)
	return path == dir || strings.HasPrefix(path, strings.TrimSuffix(dir, string(filepath.Separator))+string(filepath.Separator))
}

// errSnapshotUnreadable marks a file in the snapshot that could not be served.
// The reviewer fails, since it could otherwise approve code it did not read. A
// path that does not exist is only a wrong guess: every changed file is
// already known to be in the export.
var errSnapshotUnreadable = errors.New("a file in the reviewed snapshot could not be read")

// readWorkspaceFile serves one fs/read_text_file request from the workspace.
// os.OpenInRoot refuses a path that leaves it, including through a symlink.
func readWorkspaceFile(root string, p acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	if !filepath.IsAbs(p.Path) || !pathInside(root, p.Path) {
		return acp.ReadTextFileResponse{}, fmt.Errorf("%q is outside the reviewed snapshot", p.Path)
	}
	rel, err := filepath.Rel(root, filepath.Clean(p.Path))
	if err != nil {
		return acp.ReadTextFileResponse{}, fmt.Errorf("%q is outside the reviewed snapshot", p.Path)
	}
	f, err := os.OpenInRoot(root, rel)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return acp.ReadTextFileResponse{}, fmt.Errorf("read %s: no such file", rel)
	}
	if err != nil {
		return acp.ReadTextFileResponse{}, fmt.Errorf("%w: %s: %v", errSnapshotUnreadable, rel, errors.Unwrap(err))
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return acp.ReadTextFileResponse{}, fmt.Errorf("%w: %s: %v", errSnapshotUnreadable, rel, errors.Unwrap(err))
	}
	if !info.Mode().IsRegular() {
		return acp.ReadTextFileResponse{}, fmt.Errorf("read %s: not a regular file", rel)
	}
	if info.Size() > grokMaxRead {
		return acp.ReadTextFileResponse{}, fmt.Errorf("%w: %s is over the %d byte limit", errSnapshotUnreadable, rel, grokMaxRead)
	}
	data, err := io.ReadAll(io.LimitReader(f, grokMaxRead+1))
	if err != nil {
		return acp.ReadTextFileResponse{}, fmt.Errorf("%w: %s: %v", errSnapshotUnreadable, rel, errors.Unwrap(err))
	}
	if len(data) > grokMaxRead {
		return acp.ReadTextFileResponse{}, fmt.Errorf("%w: %s is over the %d byte limit", errSnapshotUnreadable, rel, grokMaxRead)
	}
	content := string(data)
	if p.Line != nil || p.Limit != nil {
		lines := strings.SplitAfter(content, "\n")
		start := 0
		if p.Line != nil && *p.Line > 1 {
			start = min(*p.Line-1, len(lines))
		}
		end := len(lines)
		if p.Limit != nil && *p.Limit >= 0 && *p.Limit < end-start {
			end = start + *p.Limit
		}
		content = strings.Join(lines[start:end], "")
	}
	return acp.ReadTextFileResponse{Content: content}, nil
}

// grokToolCheck allows only read_file, inside the workspace and served by
// SlopCop. Grok names each tool in its x.ai/tool metadata, which the ACP SDK
// does not expose.
type grokToolCheck struct {
	workspace string
	calls     map[string]*grokToolCall
	// mu guards servedReads, which the client's read handler adds to.
	mu sync.Mutex
	// servedReads counts, by path, the reads SlopCop served that no finished
	// read_file has accounted for yet.
	servedReads map[string]int
}

type grokToolCall struct {
	name string
	// path is the file the call reported, inside the workspace.
	path string
	// finished records a completed or failed call, which accounts for the
	// read SlopCop served it, if any.
	finished bool
	// failed records that the call finished by failing.
	failed bool
}

func newGrokToolCheck(workspace string) *grokToolCheck {
	return &grokToolCheck{workspace: workspace, calls: map[string]*grokToolCall{}, servedReads: map[string]int{}}
}

// served records a read the client served. The client calls it before it
// replies, so the agent cannot report that read finished before it is counted.
func (c *grokToolCheck) served(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.servedReads[c.absolute(path)]++
}

// takeServed uses up one served read of path and reports whether there was one.
func (c *grokToolCheck) takeServed(path string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.servedReads[path]
	if n == 0 {
		return false
	}
	if n == 1 {
		delete(c.servedReads, path)
	} else {
		c.servedReads[path] = n - 1
	}
	return true
}

// anyServed reports whether a served read is still unaccounted for.
func (c *grokToolCheck) anyServed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.servedReads) > 0
}

// check returns what the reviewer did wrong in one ACP message, or the zero
// toolStop.
func (c *grokToolCheck) check(line []byte) toolStop {
	var m struct {
		Method string `json:"method"`
		Params struct {
			Path   string `json:"path"`
			Update struct {
				SessionUpdate string `json:"sessionUpdate"`
				ToolCallID    string `json:"toolCallId"`
				Status        string `json:"status"`
				Locations     []struct {
					Path string `json:"path"`
				} `json:"locations"`
				RawInput json.RawMessage `json:"rawInput"`
				Meta     struct {
					Tool struct {
						Name  string `json:"name"`
						Input struct {
							Path      string `json:"path"`
							Directory string `json:"directory"`
						} `json:"input"`
					} `json:"x.ai/tool"`
				} `json:"_meta"`
			} `json:"update"`
		} `json:"params"`
	}
	if json.Unmarshal(line, &m) != nil {
		return toolStop{}
	}
	// The client refuses each of these requests itself.
	if m.Method == "fs/read_text_file" {
		if m.Params.Path == "" || !c.inside(m.Params.Path) {
			return toolStop{what: fmt.Sprintf("a read outside the reviewed snapshot (%q)", m.Params.Path), notServed: true}
		}
		return toolStop{}
	}
	if strings.HasPrefix(m.Method, "fs/") || strings.HasPrefix(m.Method, "terminal/") {
		return toolStop{what: m.Method, notServed: true}
	}
	if m.Method != "session/update" {
		return toolStop{}
	}
	u := m.Params.Update
	if u.SessionUpdate != "tool_call" && u.SessionUpdate != "tool_call_update" {
		return toolStop{}
	}
	call := c.calls[u.ToolCallID]
	if call == nil {
		if u.SessionUpdate == "tool_call_update" {
			return toolStop{what: "a tool call it never announced"}
		}
		call = &grokToolCall{}
		c.calls[u.ToolCallID] = call
	}
	if name := u.Meta.Tool.Name; name != "" {
		if call.name != "" && call.name != name {
			return toolStop{what: "a tool call that changed from " + call.name + " to " + name}
		}
		call.name = name
	}
	if call.name == "" {
		return toolStop{what: "an unnamed tool"}
	}
	if call.name != grokReadTool {
		return toolStop{what: call.name}
	}
	paths := []string{u.Meta.Tool.Input.Path, u.Meta.Tool.Input.Directory}
	for _, l := range u.Locations {
		paths = append(paths, l.Path)
	}
	var raw map[string]any
	if json.Unmarshal(u.RawInput, &raw) == nil {
		for _, key := range []string{"target_file", "target_directory", "path", "file_path", "directory"} {
			if p, ok := raw[key].(string); ok {
				paths = append(paths, p)
			}
		}
	}
	for _, p := range paths {
		if p == "" {
			continue
		}
		if !c.inside(p) {
			// SlopCop never serves this path, so a read reported as finished
			// got it some other way. An unknown status counts as finished.
			unfinished := !call.finished && slices.Contains([]string{"", "pending", "in_progress", "failed"}, u.Status)
			return toolStop{what: fmt.Sprintf("%s outside the reviewed snapshot (%q)", call.name, p), notServed: unfinished}
		}
		full := c.absolute(p)
		if call.path != "" && call.path != full {
			return toolStop{what: call.name + " that reported two different paths"}
		}
		call.path = full
	}
	if u.Status == "completed" && call.failed {
		return toolStop{what: call.name + " that finished after it failed"}
	}
	if (u.Status == "completed" || u.Status == "failed") && !call.finished {
		call.finished = true
		call.failed = u.Status == "failed"
		// With no path, the guard cannot tell which served read this call used,
		// and the one it leaves behind could vouch for a later read from disk.
		if call.failed && call.path == "" && c.anyServed() {
			return toolStop{what: call.name + " that failed without a path SlopCop could check"}
		}
		served := c.takeServed(call.path)
		if u.Status == "completed" {
			if call.path == "" {
				return toolStop{what: call.name + " without a path SlopCop could check"}
			}
			// A finished read that SlopCop did not serve came straight from disk.
			if !served {
				return toolStop{what: call.name + " that did not go through SlopCop"}
			}
		}
	}
	return toolStop{}
}

func (c *grokToolCheck) inside(p string) bool {
	if strings.HasPrefix(p, "~") {
		return false
	}
	return pathInside(c.workspace, c.absolute(p))
}

// absolute resolves a reported path against the workspace.
func (c *grokToolCheck) absolute(p string) string {
	if !filepath.IsAbs(p) {
		p = filepath.Join(c.workspace, p)
	}
	return filepath.Clean(p)
}
