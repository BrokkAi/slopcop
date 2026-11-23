package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BrokkAi/slopcop/internal/gitx"
)

// Antigravity CLI as an opt-in reviewer adapter for Gemini. Checked live
// against agy 1.5.14. Its behavior shapes how it runs:
//
//   - It loads hooks and MCP servers from the session's workspace and runs
//     them on the first prompt, with no trust prompt, in every mode. So the
//     reviewer's workspace is an empty folder outside any repository, with an
//     export of the reviewed tree one level down, where they are not loaded.
//   - It reads user-level hooks and MCP servers too, and saves each session.
//     So each reviewer gets its own --gemini-dir, which keeps the login.
//   - Reads inside the workspace need no permission, and anything else that
//     asks is refused, including a read outside the workspace.
//   - Web search and subagents run without asking, so they cannot be refused.
//     toolGuard stops the reviewer as soon as it calls one.
const antigravityAdapter = "antigravity"

// antigravityDefaultCommand starts the installed CLI's ACP server. Antigravity
// is not published to npm, so unlike the other adapters it is not pinned.
var antigravityDefaultCommand = []string{"agy", "acp"}

const (
	// antigravityRepoDir holds the exported tree inside the workspace.
	antigravityRepoDir = "repo"
	// antigravityIndexFile lists the exported files. Antigravity has no
	// listing or search tool a reviewer may use.
	antigravityIndexFile = "files.txt"
	// antigravityMaxFile leaves large files out of the export. A change to
	// one fails the reviewer, which could not read the code around it.
	antigravityMaxFile = 1 << 20
	// antigravityMaxTotal bounds the disk one reviewer's export can use.
	antigravityMaxTotal = 1 << 30
	// toolGuardMaxLine is the longest ACP message the guard will inspect.
	toolGuardMaxLine = 64 << 20
)

// antigravityRules tells the reviewer what it may use. The client and the
// guard enforce it; this keeps an honest reviewer from ending its own turn.
const antigravityRules = "The snapshot's files are in the `repo` folder of your workspace, and `files.txt` in the workspace lists them. Read them only with the read_file tool. Do not call any other tool: shell commands, edits, web search and subagents are refused or stop your review.\n"

// errForbiddenTool marks a reviewer stopped for calling a tool that runs
// without asking. It is not retried: a repository that talked the reviewer
// into the call would get a second attempt.
var errForbiddenTool = errors.New("the reviewer called a tool SlopCop cannot restrict, so it was stopped")

// errDiffTooLarge marks a change a Gemini reviewer cannot cover. Other
// reviewers read the rest of a truncated diff with git; this one has no git
// history and no way to see deletions or files left out of its export, so it
// could only approve a change it had not seen.
var errDiffTooLarge = errors.New("the change's diff is over the 256 KiB a Gemini or Kimi reviewer can read, so it did not review it; configure a fallback on another provider or check a smaller change")

// errChangeNotExported marks a change to a file the reviewer's workspace does
// not hold: one over the size limit, or one whose path cannot be written
// safely. The reviewer could not read the code around that change.
var errChangeNotExported = errors.New("a changed file is over 1 MiB or has a path a Gemini or Kimi reviewer cannot read, so it did not review the change; configure a fallback on another provider")

// reviewTrees names the trees of a review. A probe, which reads nothing, has
// neither.
type reviewTrees struct {
	base, checked string
}

// antigravityAskingTools ask permission before they run, so the client's
// refusal stops them. Every other tool except read_file runs without asking.
var antigravityAskingTools = []string{"run_command", "write_file", "edit_file"}

// toolGuard watches the adapter's ACP stream for tool calls. The tool's name
// is an Antigravity extension the ACP SDK does not expose, so the guard reads
// the raw messages. It allows only known tools: an unknown one is treated as
// one that runs without asking.
type toolGuard struct {
	line    []byte
	asking  map[string]bool
	trip    func(error)
	tripped bool
	// check, when set, replaces the Antigravity rules: it returns what the
	// reviewer must be stopped for, or "".
	check func(line []byte) string
}

// Write inspects each complete line and never fails, so it cannot disturb the
// connection it watches.
func (g *toolGuard) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 && !g.tripped {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			g.line = append(g.line, p...)
			if len(g.line) > toolGuardMaxLine {
				g.stop("a message too long to inspect")
			}
			break
		}
		g.line = append(g.line, p[:i]...)
		g.inspect(g.line)
		g.line = g.line[:0]
		p = p[i+1:]
	}
	return n, nil
}

func (g *toolGuard) inspect(line []byte) {
	if g.check != nil {
		if what := g.check(line); what != "" {
			g.stop(what)
		}
		return
	}
	var m struct {
		Method string `json:"method"`
		Params struct {
			Update struct {
				SessionUpdate string `json:"sessionUpdate"`
				ToolCallID    string `json:"toolCallId"`
				Name          string `json:"name"`
				Status        string `json:"status"`
			} `json:"update"`
		} `json:"params"`
	}
	if json.Unmarshal(line, &m) != nil || m.Method != "session/update" {
		return
	}
	u := m.Params.Update
	switch u.SessionUpdate {
	case "tool_call":
		switch {
		case u.Name == "read_file":
		case slices.Contains(antigravityAskingTools, u.Name):
			if g.asking == nil {
				g.asking = map[string]bool{}
			}
			g.asking[u.ToolCallID] = true
			if u.Status == "completed" {
				g.stop(u.Name + " without a permission request")
			}
		case u.Name == "":
			g.stop("an unnamed tool")
		default:
			g.stop(u.Name)
		}
	case "tool_call_update":
		// The client refuses every request, so one of these finishing means
		// it ran without asking.
		if g.asking[u.ToolCallID] && u.Status == "completed" {
			g.stop("a tool that was not approved")
		}
	}
}

func (g *toolGuard) stop(what string) {
	g.tripped = true
	g.line = nil
	if g.trip != nil {
		g.trip(fmt.Errorf("%w: %s", errForbiddenTool, what))
	}
}

// startAntigravity exports the checked tree into a fresh workspace and starts
// a reviewer there. On a failure after the adapter started it returns the
// stopped runner, whose result names the log.
func startAntigravity(ctx context.Context, root string, trees reviewTrees, base []string, f Family, name, dir string, timeout time.Duration) (*runner, error) {
	if slices.ContainsFunc(base, func(arg string) bool { return strings.HasPrefix(arg, "--dangerously-skip-permissions") }) {
		return nil, errors.New("review.adapters.antigravity.command turns off permission requests, so the reviewer cannot be restricted")
	}
	tmp, workspace, err := antigravityWorkspace(ctx, root, trees)
	if err != nil {
		return nil, fmt.Errorf("prepare the Antigravity workspace: %w", err)
	}
	args := append(slices.Clone(base), "--gemini-dir="+filepath.Join(tmp, "config"), "--disable-telemetry")
	s, err := startRunnerWith(ctx, root, args, f, name, dir, timeout, runnerOptions{guard: &toolGuard{}, refuseAll: true})
	if err != nil {
		_ = os.RemoveAll(tmp)
		return nil, err
	}
	s.tempDir = tmp
	s.result.Adapter = antigravityAdapter
	if err = s.prepare(workspace); err != nil {
		err = startError(s.ctx, err)
		s.stop()
		return s, err
	}
	return s, nil
}

// antigravityWorkspace creates the temporary directory for one reviewer and
// returns it with the workspace inside it. The directory must not sit inside
// a git repository: Antigravity loads customizations from each parent up to
// the repository root.
func antigravityWorkspace(ctx context.Context, root string, trees reviewTrees) (string, string, error) {
	return isolatedWorkspace(ctx, root, trees, "slopcop-antigravity-")
}

// isolatedAdapter reports whether an adapter reviews an export of the tree,
// with no git history to read the rest of a truncated diff.
func isolatedAdapter(adapter string) bool {
	return adapter == antigravityAdapter || adapter == kimiAdapter
}

// isolatedWorkspace creates a temporary directory holding a workspace with an
// export of the checked tree one level down, and returns both. The directory
// must not sit inside a git repository, whose config the agent would load.
func isolatedWorkspace(ctx context.Context, root string, trees reviewTrees, prefix string) (string, string, error) {
	tmp, err := os.MkdirTemp("", prefix)
	if err != nil {
		return "", "", err
	}
	fail := func(err error) (string, string, error) {
		_ = os.RemoveAll(tmp)
		return "", "", err
	}
	// The agent compares paths after resolving symlinks, and macOS's temporary
	// directory is behind one.
	resolved, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		return fail(err)
	}
	if insideGitRepository(resolved) {
		return fail(fmt.Errorf("the temporary directory %s is inside a git repository, whose hooks the reviewer would load; set TMPDIR to a directory outside it", resolved))
	}
	workspace := filepath.Join(resolved, "workspace")
	repo := filepath.Join(workspace, antigravityRepoDir)
	if err = os.MkdirAll(repo, 0700); err != nil {
		return fail(err)
	}
	var paths []string
	if trees.checked != "" {
		paths, err = gitx.ExportTree(ctx, root, trees.checked, repo, gitx.ExportLimits{MaxFile: antigravityMaxFile, MaxTotal: antigravityMaxTotal})
		if err != nil {
			return fail(err)
		}
		if err = requireChangedFiles(ctx, root, trees, paths); err != nil {
			return fail(err)
		}
	}
	var index strings.Builder
	for _, p := range paths {
		index.WriteString(antigravityRepoDir + "/" + p + "\n")
	}
	if err = os.WriteFile(filepath.Join(workspace, antigravityIndexFile), []byte(index.String()), 0600); err != nil {
		return fail(err)
	}
	return resolved, workspace, nil
}

// requireChangedFiles fails when a regular file the change touches is not
// among the exported paths.
func requireChangedFiles(ctx context.Context, root string, trees reviewTrees, exported []string) error {
	changed, err := gitx.ChangedFiles(ctx, root, trees.base, trees.checked)
	if err != nil {
		return err
	}
	have := make(map[string]bool, len(exported))
	for _, p := range exported {
		have[p] = true
	}
	for _, p := range changed {
		if !have[p] {
			return fmt.Errorf("%w: %q", errChangeNotExported, p)
		}
	}
	return nil
}

func insideGitRepository(dir string) bool {
	for {
		if exists(filepath.Join(dir, ".git")) {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}
