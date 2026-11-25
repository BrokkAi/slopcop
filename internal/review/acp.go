package review

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/BrokkAi/slopcop/internal/review/chatagent"
)

// reviewClient collects final agent text and permission failures across ACP callbacks.
type reviewClient struct {
	unsupportedClient
	mu              sync.Mutex
	text            strings.Builder
	permissionError error
	deniedEdit      string
	stderr          io.Writer
	// messageID names the message text belongs to, when the adapter names
	// messages.
	messageID string
	// toolSinceText records a tool update after the last message chunk, and
	// callSinceText a tool call started after it.
	toolSinceText, callSinceText bool
	// refuseAll refuses reads too, for an adapter that asks only about a read
	// outside the reviewer's workspace.
	refuseAll bool
	// violation is why a toolGuard stopped the reviewer.
	violation error
	// readRoot, when set, is the only directory ReadTextFile serves.
	readRoot string
	// onRead, when set, hears of each read served, before the reply goes out.
	onRead func(path string)
}

// ReadTextFile serves a read from readRoot for an adapter that reads through
// the client, so a path outside it is refused before anything is read.
func (c *reviewClient) ReadTextFile(ctx context.Context, p acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	if c.readRoot == "" {
		return c.unsupportedClient.ReadTextFile(ctx, p)
	}
	r, err := readWorkspaceFile(c.readRoot, p)
	if err == nil && c.onRead != nil {
		c.onRead(p.Path)
	}
	if errors.Is(err, errSnapshotUnreadable) {
		c.mu.Lock()
		if c.permissionError == nil {
			c.permissionError = err
		}
		c.mu.Unlock()
	}
	return r, err
}

// reply is the reviewer's final reply so far: none when it started a tool
// call after its last message, since text before a tool call is progress.
func (c *reviewClient) reply() string {
	if c.callSinceText {
		return ""
	}
	return strings.TrimSpace(c.text.String())
}

// SessionUpdate retains only the agent's last message after its last tool
// update, so the verdict is the reviewer's final reply and not progress notes
// sent before it. Messages are told apart by their ids.
//
// A tool update ends the message before it, unless that same named message
// goes on after the update: Codex streams updates for earlier calls, such as
// a background command's output or a subagent finishing, into the middle of
// its final reply, and resetting there kept only the reply's tail.
func (c *reviewClient) SessionUpdate(_ context.Context, n acp.SessionNotification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	u := n.Update
	if u.ToolCall != nil || u.ToolCallUpdate != nil {
		c.toolSinceText = true
	}
	if u.ToolCall != nil {
		c.callSinceText = true
	}
	if u.AgentMessageChunk != nil && u.AgentMessageChunk.Content.Text != nil {
		id := ""
		if u.AgentMessageChunk.MessageId != nil {
			id = *u.AgentMessageChunk.MessageId
		}
		// A new message replaces the one before it. Only the adapter can say
		// where a message starts; chunks without an id stay one message
		// until a tool update, so a reply is never cut short.
		resumed := id != "" && id == c.messageID
		if (id != "" && id != c.messageID) || (c.toolSinceText && !resumed) {
			c.text.Reset()
			c.messageID = id
		}
		c.toolSinceText, c.callSinceText = false, false
		c.text.WriteString(u.AgentMessageChunk.Content.Text.Text)
	}
	return nil
}

// declineOptionID is the Codex adapter's refusal that lets the turn go on
// ("No, continue without running it"). Its other refusal, "cancel", ends the
// turn, so a reviewer that asks to leave its sandbox would fail its review.
const declineOptionID = "decline"

// RequestPermission allows only file reading, searching, and thinking. Every
// other request is refused, preferring a refusal that keeps the turn going.
func (c *reviewClient) RequestPermission(_ context.Context, p acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	if !c.refuseAll && p.ToolCall.Kind != nil && (*p.ToolCall.Kind == acp.ToolKindRead || *p.ToolCall.Kind == acp.ToolKindSearch || *p.ToolCall.Kind == acp.ToolKindThink) {
		if id, ok := permissionOption(p.Options, "", acp.PermissionOptionKindAllowOnce, acp.PermissionOptionKindAllowAlways); ok {
			return selectedPermission(id), nil
		}
		err := fmt.Errorf("reviewer offered no allow permission option")
		c.mu.Lock()
		c.permissionError = err
		c.mu.Unlock()
		return acp.RequestPermissionResponse{}, err
	}
	c.mu.Lock()
	c.deniedEdit = deniedRequest(p.ToolCall)
	if c.stderr != nil {
		fmt.Fprintln(c.stderr, c.deniedEdit)
	}
	c.mu.Unlock()
	if id, ok := permissionOption(p.Options, declineOptionID, acp.PermissionOptionKindRejectOnce, acp.PermissionOptionKindRejectAlways); ok {
		return selectedPermission(id), nil
	}
	return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}}}, nil
}

// permissionOption returns the option with the preferred id among the given
// kinds, else the first option of the earliest kind.
func permissionOption(options []acp.PermissionOption, preferred acp.PermissionOptionId, kinds ...acp.PermissionOptionKind) (acp.PermissionOptionId, bool) {
	if preferred != "" {
		for _, o := range options {
			if o.OptionId == preferred && slices.Contains(kinds, o.Kind) {
				return o.OptionId, true
			}
		}
	}
	for _, kind := range kinds {
		for _, o := range options {
			if o.Kind == kind {
				return o.OptionId, true
			}
		}
	}
	return "", false
}

func selectedPermission(id acp.PermissionOptionId) acp.RequestPermissionResponse {
	return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Selected: &acp.RequestPermissionOutcomeSelected{OptionId: id}}}
}

// deniedRequest names a refused tool call, with its command when the adapter
// sent one, so the reviewer's error says what it tried to run.
func deniedRequest(call acp.ToolCallUpdate) string {
	kind := "missing"
	if call.Kind != nil {
		kind = string(*call.Kind)
	}
	title := ""
	if call.Title != nil {
		title = *call.Title
	}
	out := fmt.Sprintf("reviewer requested %s permission: %s", kind, title)
	if input, ok := call.RawInput.(map[string]any); ok {
		if command, ok := input["command"].(string); ok && command != "" && command != title {
			const limit = 200
			if len(command) > limit {
				command = command[:limit] + "..."
			}
			out += fmt.Sprintf(" (%q)", command)
		}
	}
	return out
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
	// A chat provider's key goes only to its own agent: another adapter's
	// shell or repository-loaded code could otherwise read it.
	if !IsChatAdapter(string(f)) {
		env = slices.DeleteFunc(env, func(v string) bool {
			k, _, _ := strings.Cut(v, "=")
			return slices.Contains(chatagent.KeyEnvs(), k)
		})
	}
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
	watchDone       chan struct{}
	result          Reviewer
	// canDelete records that the adapter advertised session/delete, and
	// canClose session/close, which Copilot offers instead.
	canDelete, canClose bool
	// tempDir, when set, is removed once the adapter has exited.
	tempDir string
	// workDir is the private directory the adapter process starts in.
	workDir string
	// copilot marks a Copilot CLI reviewer, whose errors can arrive as a
	// normal reply.
	copilot bool
	// verify, when set, says what the reviewer's reply did not cover.
	verify func() error
	// ollama, when set, bounds the turn of an Ollama reviewer.
	ollama *ollamaLimits
	// sessionMu guards session, which the timeout's kill reads.
	sessionMu sync.Mutex
	session   acp.SessionId
	cleanOnce sync.Once
}

// cleanupTimeout bounds deleting a reviewer's session, so cleanup never
// holds up a review.
const cleanupTimeout = 2 * time.Second

// cleanup deletes the reviewer's session from the adapter's history, once.
// Each reviewer run, retry and probe is otherwise a saved thread in the
// user's agent app. Codex archives the thread (`codex unarchive <id>` brings
// it back) and Claude deletes it. Failure is ignored and noted in stderr.
// It waits at most cleanupTimeout: a request stuck behind a blocked write to
// the adapter must not hold up the kill that would unblock it.
func (s *runner) cleanup() {
	s.cleanOnce.Do(func() {
		done := make(chan struct{})
		go func() {
			defer close(done)
			s.deleteSession()
		}()
		select {
		case <-done:
		case <-time.After(cleanupTimeout):
		}
	})
}

// deleteSession sends session/delete when the adapter supports it, else
// session/close.
func (s *runner) deleteSession() {
	s.sessionMu.Lock()
	session := s.session
	s.sessionMu.Unlock()
	if session == "" || !s.canDelete && !s.canClose {
		return
	}
	select {
	case <-s.conn.Done():
		return
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if !s.canDelete {
		if _, err := s.conn.CloseSession(ctx, acp.CloseSessionRequest{SessionId: session}); err != nil {
			fmt.Fprintf(s.stderr, "close session %s: %v\n", session, err)
		}
		return
	}
	if _, err := s.conn.UnstableDeleteSession(ctx, acp.UnstableDeleteSessionRequest{SessionId: session}); err != nil {
		fmt.Fprintf(s.stderr, "delete session %s: %v\n", session, err)
	}
}

// startError keeps a setup timeout recognizable as one: the ACP SDK reports
// a canceled request as its own error, which does not wrap the context's.
func startError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil && !errors.Is(err, ctxErr) {
		return fmt.Errorf("%w: %v", ctxErr, err)
	}
	return err
}

// runnerOptions restrict a reviewer beyond the default permission policy.
type runnerOptions struct {
	// guard watches the adapter's messages and stops it on a forbidden tool.
	guard *toolGuard
	// refuseAll refuses every permission request, reads included.
	refuseAll bool
	// environment, when set, adjusts the adapter's environment.
	environment func([]string) []string
	// verify, when set, says what a reviewer's reply did not cover. It fails
	// an approval and marks a reply with findings.
	verify func() error
	// dir, when set, is the adapter's working directory.
	dir string
	// readRoot, when set, makes the client serve file reads from it. Writes
	// are advertised too, since Grok uses client files only with both, and
	// are always refused.
	readRoot string
	// onRead, when set, hears of each read the client served.
	onRead func(path string)
}

// startRunner initializes adapters serially so first-run npx installs never overlap.
func startRunner(ctx context.Context, root string, argv []string, f Family, name, dir string, timeout time.Duration) (*runner, error) {
	return startRunnerWith(ctx, root, argv, f, name, dir, timeout, runnerOptions{})
}

// startRunnerWith is startRunner with extra restrictions on the reviewer.
func startRunnerWith(ctx context.Context, root string, argv []string, f Family, name, dir string, timeout time.Duration, opts runnerOptions) (*runner, error) {
	s := &runner{result: Reviewer{Name: name, Family: f, Log: filepath.Join(dir, name+".jsonl")}, exited: make(chan struct{}), verify: opts.verify}
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
	s.workDir, err = adapterWorkDir(root)
	if err != nil {
		s.closeFiles()
		s.cancel()
		return nil, fmt.Errorf("prepare the reviewer's working directory: %w", err)
	}
	if err = protectCommandParents(path); err != nil {
		s.closeFiles()
		s.cancel()
		return nil, fmt.Errorf("protect the reviewer's command: %w", err)
	}
	s.cmd = exec.Command(path, argv[1:]...)
	// The adapter starts in its private directory; the session still gets the
	// repository as its cwd.
	s.cmd.Dir = s.workDir
	if opts.dir != "" {
		s.cmd.Dir = opts.dir
	}
	s.cmd.Env = reviewerEnvironment(f, root, filepath.Join(dir, name+"-data"), os.Environ())
	if opts.environment != nil {
		s.cmd.Env = opts.environment(s.cmd.Env)
	}
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
	s.client = &reviewClient{stderr: s.stderr, refuseAll: opts.refuseAll, readRoot: opts.readRoot, onRead: opts.onRead}
	// The guard sees each message before the connection handles it, so a
	// violation is recorded before the turn can be reported as finished.
	var incoming io.Reader = io.TeeReader(stdout, s.updates)
	if opts.guard != nil {
		opts.guard.trip = s.violate
		incoming = io.TeeReader(incoming, opts.guard)
	}
	// The SDK starts reading as soon as the connection exists and does not
	// synchronize its logger, so the first read waits until the logger is set.
	loggerSet := make(chan struct{})
	s.conn = acp.NewClientSideConnection(s.client, s.stdin, gatedReader{Reader: incoming, open: loggerSet})
	s.conn.SetLogger(slog.New(slog.NewTextHandler(s.stderr, nil)))
	close(loggerSet)
	// On a timeout or interrupt, delete the session while the adapter still
	// runs, then kill it.
	s.watchDone = make(chan struct{})
	s.stopWatch = context.AfterFunc(s.ctx, func() {
		defer close(s.watchDone)
		s.cleanup()
		killProcessTree(s.cmd)
	})
	capabilities := acp.ClientCapabilities{}
	if opts.readRoot != "" {
		capabilities.Fs = acp.FileSystemCapabilities{ReadTextFile: true, WriteTextFile: true}
	}
	initialized, err := s.conn.Initialize(s.ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber, ClientCapabilities: capabilities})
	if err != nil {
		s.stop()
		return nil, fmt.Errorf("initialize: %w", startError(s.ctx, err))
	}
	s.canDelete = initialized.AgentCapabilities.SessionCapabilities.Delete != nil
	s.canClose = initialized.AgentCapabilities.SessionCapabilities.Close != nil
	return s, nil
}

// gatedReader holds every read until open is closed.
type gatedReader struct {
	io.Reader
	open <-chan struct{}
}

func (g gatedReader) Read(p []byte) (int, error) {
	<-g.open
	return g.Reader.Read(p)
}

// adapterWorkDir creates the private directory an adapter process starts in.
// npm reads .npmrc and node_modules from its working directory and the
// directories above it, joins a parent package.json that lists it as a
// workspace, and puts each parent's node_modules/.bin on the adapter's PATH.
// So neither the repository under review nor a shared directory such as /tmp
// may be above it. Its own package.json stops npm's search for a project.
func adapterWorkDir(root string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	// The parent checks below need the whole path up to the root.
	if !filepath.IsAbs(cache) {
		return "", fmt.Errorf("the user cache directory %s is a relative path; point XDG_CACHE_HOME (HOME on macOS, LocalAppData on Windows) at an absolute one", cache)
	}
	base := filepath.Join(cache, "slopcop", "adapters")
	// A new directory would be open to an inherited grant before it could be
	// checked, so the closest existing parent is checked first.
	existing, err := nearestExisting(base)
	if err != nil {
		return "", err
	}
	if err = refuseInheritingDirectory(existing); err != nil {
		return "", err
	}
	if err = os.MkdirAll(base, 0700); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", err
	}
	// The path as written is checked too: a symbolic link planted under a
	// shared directory could resolve to somewhere that looks private.
	paths := slices.Compact([]string{base, resolved})
	for _, p := range paths {
		if err = refuseInsideRepository(root, p); err != nil {
			return "", err
		}
	}
	for _, p := range paths {
		if err = protectParents(p); err != nil {
			return "", err
		}
	}
	if err = refuseInheritingDirectory(resolved); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(resolved, "run-")
	if err != nil {
		return "", err
	}
	// A parent can hold a grant that applies only to what is created in it, so
	// the directory is checked before anything is written there.
	manifest := filepath.Join(dir, "package.json")
	err = refuseInheritedAccess(dir, resolved)
	if err == nil {
		err = createEmptyManifest(manifest)
	}
	if err == nil {
		err = refuseInheritedAccess(manifest, resolved)
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// nearestExisting returns path or its closest parent that exists.
func nearestExisting(path string) (string, error) {
	for {
		_, err := os.Stat(path)
		if err == nil {
			return path, nil
		}
		parent := filepath.Dir(path)
		if !errors.Is(err, os.ErrNotExist) || parent == path {
			return "", err
		}
		path = parent
	}
}

// refuseInheritingDirectory fails when what is created in dir would get write
// access for other users.
func refuseInheritingDirectory(dir string) error {
	inherits, err := passesWriteToNewEntries(dir)
	if err == nil && inherits {
		return fmt.Errorf("the reviewer's working directory inherits write access for other users from %s; remove it from that directory's access list", dir)
	}
	return err
}

// refuseSharedNodeModules fails when another account can change the
// node_modules or node_modules/.bin in dir, which npm puts on the adapter's
// PATH. A symbolic link is judged by what it points to.
func refuseSharedNodeModules(dir string) error {
	for _, rel := range []string{"node_modules", filepath.Join("node_modules", ".bin")} {
		entry := filepath.Join(dir, rel)
		if _, err := os.Lstat(entry); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		target, err := filepath.EvalSymlinks(entry)
		if err != nil {
			return err
		}
		shared, err := writableByOthers(target)
		if err != nil {
			return err
		}
		if shared {
			return fmt.Errorf("%s can be changed by another account, which could change what the reviewer runs; remove it or restrict its access", entry)
		}
	}
	return nil
}

// refuseInheritedAccess fails when other users can write to path, a new entry
// below parent.
func refuseInheritedAccess(path, parent string) error {
	shared, err := writableByOthers(path)
	if err == nil && shared {
		return fmt.Errorf("the reviewer's working directory inherits write access for other users at %s; remove it from the access list of %s", path, parent)
	}
	return err
}

// createEmptyManifest writes an empty package.json, failing rather than
// following anything already at path.
func createEmptyManifest(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.WriteString("{}\n")
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// refuseInsideRepository refuses a directory below the repository under
// review. It runs before protectParents, which can create a folder.
func refuseInsideRepository(root, dir string) error {
	repo, err := os.Stat(root)
	if err != nil {
		return err
	}
	for p := dir; ; p = filepath.Dir(p) {
		info, err := os.Stat(p)
		if err != nil {
			return err
		}
		// Paths can differ in case on a case-insensitive filesystem.
		if os.SameFile(info, repo) {
			return fmt.Errorf("the user cache directory %s is inside the repository under review; point XDG_CACHE_HOME (HOME on macOS, LocalAppData on Windows) outside it", dir)
		}
		if filepath.Dir(p) == p {
			return nil
		}
	}
}

// protectParents refuses a directory below one other users can write to, and
// makes sure they cannot add a node_modules folder to any directory above it.
func protectParents(dir string) error {
	for p := dir; ; p = filepath.Dir(p) {
		shared, err := protectParent(p)
		if err != nil {
			return err
		}
		if shared {
			return fmt.Errorf("the user cache directory %s is below %s, which other users can write to; point XDG_CACHE_HOME (HOME on macOS, LocalAppData on Windows) at a private directory", dir, p)
		}
		if err = refuseSharedNodeModules(p); err != nil {
			return err
		}
		if filepath.Dir(p) == p {
			return nil
		}
	}
}

// closeFiles closes the reviewer's logs and removes its working directory.
func (s *runner) closeFiles() {
	if s.updates != nil {
		s.updates.Close()
	}
	if s.stderr != nil {
		s.stderr.Close()
	}
	if s.workDir != "" {
		_ = os.RemoveAll(s.workDir)
	}
}
func (s *runner) stop() {
	if s.stopWatch() {
		s.cleanup()
	} else {
		// The timeout fired: let it delete the session before the kill.
		<-s.watchDone
	}
	s.cancel()
	killProcessTree(s.cmd)
	s.stdin.Close()
	<-s.exited
	<-s.conn.Done()
	s.closeFiles()
	if s.tempDir != "" {
		_ = os.RemoveAll(s.tempDir)
	}
}

// violate records why the reviewer must stop and ends it; the timeout's
// watcher then kills the adapter.
func (s *runner) violate(err error) {
	s.client.mu.Lock()
	if s.client.violation == nil {
		s.client.violation = err
	}
	s.client.mu.Unlock()
	s.cancel()
}

// setSession records the open session, which cleanup deletes or closes.
func (s *runner) setSession(id acp.SessionId) {
	s.sessionMu.Lock()
	s.session = id
	s.sessionMu.Unlock()
}

func (s *runner) prepare(root string, efforts ...string) error {
	session, err := s.conn.NewSession(s.ctx, acp.NewSessionRequest{Cwd: root, McpServers: []acp.McpServer{}})
	if err != nil {
		return fmt.Errorf("session/new: %w", err)
	}
	s.setSession(session.SessionId)
	modelOpt := configOption(session.ConfigOptions, "model")
	model, ok := bestModel(s.result.Family, choices(modelOpt))
	if !ok {
		return fmt.Errorf("no model of family %s", s.result.Family)
	}
	response, err := s.conn.SetSessionConfigOption(s.ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: session.SessionId, ConfigId: modelOpt.Id, Value: acp.SessionConfigValueId(model)}})
	if err != nil {
		return fmt.Errorf("select model: %w", err)
	}
	s.result.Model = model
	effortOptions := response.ConfigOptions
	if len(effortOptions) == 0 {
		effortOptions = session.ConfigOptions
	}
	want := effectiveEffort(slotEffort(efforts, 0))
	if s.result.Family != Kimi || want != "none" {
		effort := configOption(effortOptions, "effort")
		if effort == nil {
			return fmt.Errorf("%w: %s effort unavailable (no effort option)", errEffort, want)
		}
		found := false
		for _, v := range choices(effort) {
			if string(v.Value) == want {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("%w: %s effort unavailable", errEffort, want)
		}
		response, err = s.conn.SetSessionConfigOption(s.ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: session.SessionId, ConfigId: effort.Id, Value: acp.SessionConfigValueId(want)}})
		if err != nil {
			return fmt.Errorf("%w: select %s effort: %v", errEffort, want, err)
		}
	}
	s.result.Effort = want
	modeOptions := response.ConfigOptions
	key, value := reviewerMode(s.result.Family)
	if key == "" {
		return nil
	}
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
	_, err = s.conn.SetSessionConfigOption(s.ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: session.SessionId, ConfigId: option.Id, Value: acp.SessionConfigValueId(value)}})
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
	if s.ollama != nil {
		err = s.ollama.finish(err, r.Usage)
	}
	s.client.mu.Lock()
	s.result.Text = s.client.reply()
	// Some adapters end the turn normally with their own connection error
	// as the reply. That is a failed reviewer, not a finding.
	if err == nil && connectionReply(s.result.Text) {
		err = fmt.Errorf("the adapter could not reach its provider and replied with its error instead of a review: %s", s.result.Text)
	}
	if err == nil && s.copilot && copilotErrorReply(s.result.Text) {
		err = fmt.Errorf("Copilot ended the turn with its own error instead of a review: %s", s.result.Text)
	}
	if err == nil && s.verify != nil {
		if unread := s.verify(); unread != nil {
			if s.result.Text == CleanReply {
				err = unread
			} else {
				s.result.UnreadChange = unread.Error()
				s.result.Warning = unread.Error()
			}
		}
	}
	if s.client.permissionError != nil {
		err = s.client.permissionError
	}
	denied := s.client.deniedEdit
	// Antigravity ends its turn with no reply when a request is refused.
	if err == nil && s.client.refuseAll && denied != "" && s.result.Text == "" {
		err = errors.New("the reviewer's turn ended when a tool request was refused")
	}
	violation := s.client.violation
	s.client.mu.Unlock()
	if s.ctx.Err() != nil {
		err = s.ctx.Err()
	}
	if violation != nil {
		err = violation
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
