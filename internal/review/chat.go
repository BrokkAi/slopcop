package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	acp "github.com/coder/acp-go-sdk"

	"github.com/BrokkAi/slopcop/internal/gitx"
	"github.com/BrokkAi/slopcop/internal/review/chatagent"
)

// A chat reviewer runs SlopCop's own read-only agent (see chatagent) against
// an OpenAI-compatible service. The family's name is also its adapter's name
// and its provider's name in chatagent.

// chatSelfCommand stands for this slopcop binary in an adapter command. It
// keeps the binary's path out of the review's reuse key.
const chatSelfCommand = "slopcop"

const chatModelFlag = "--model="

// chatRules tells the reviewer what it has. The agent offers nothing else.
const chatRules = "Inspect per-file diffs on demand with read_diff, and full snapshot files with read_file, list_files and search_files. Read the snapshot only with your read_file, list_files and search_files tools, using paths from the repository root. You have no shell, git, file editing or web access.\n"

// chatToolKinds are the ACP tool kinds a chat reviewer may report. Anything
// else stops it, including a server-side tool its provider ran.
var chatToolKinds = []string{string(acp.ToolKindRead), string(acp.ToolKindSearch)}

// errChangeBinary marks a change a chat reviewer cannot read: a binary file,
// or text that is not valid UTF-8, which JSON would alter on its way to the
// model.
var errChangeBinary = errors.New("a changed file is binary or is not valid UTF-8, so this reviewer could not read the change and did not review it; configure a fallback on another provider")

// chatToolCheck names a tool call a chat reviewer must not make: any kind but
// a read or a search that did not fail. The agent reports a tool its provider
// ran by itself as a fetch, so this stops that too, and a tool it does not
// offer as failed, since nothing ran it.
func chatToolCheck(line []byte) string {
	var m struct {
		Method string `json:"method"`
		Params struct {
			Update struct {
				SessionUpdate string `json:"sessionUpdate"`
				Kind          string `json:"kind"`
				Title         string `json:"title"`
				Status        string `json:"status"`
			} `json:"update"`
		} `json:"params"`
	}
	if json.Unmarshal(line, &m) != nil || m.Method != "session/update" {
		return ""
	}
	u := m.Params.Update
	if u.SessionUpdate != "tool_call" || slices.Contains(chatToolKinds, u.Kind) || u.Status == string(acp.ToolCallStatusFailed) {
		return ""
	}
	if u.Title == "" {
		return "an unnamed tool"
	}
	return u.Title
}

// IsChatAdapter reports whether an adapter is SlopCop's own chat agent.
func IsChatAdapter(name string) bool {
	_, ok := chatagent.Lookup(name)
	return ok
}

// TakesModel reports whether family f reviews on a model the user names in
// review.models.<f>.
func TakesModel(f Family) bool {
	return f == Ollama || IsChatAdapter(string(f))
}

// CheckModel validates the model configured for a family that takes one.
func CheckModel(f Family, model string) error {
	if f == Ollama {
		if !ValidOllamaModel(model) {
			return fmt.Errorf("%q is not an Ollama model name; want one from `ollama list`", model)
		}
		return nil
	}
	p, ok := chatagent.Lookup(string(f))
	if !ok {
		return fmt.Errorf("review family %q does not take a model", f)
	}
	return p.CheckModel(model)
}

// chatCommand is the adapter command with the configured model, so a review
// by one model is never reused for another.
func chatCommand(f Family, s Settings) []string {
	base := s.Adapters[string(f)]
	if len(base) == 0 {
		base = DefaultCommand(f)
	}
	return append(slices.Clone(base), chatModelFlag+s.Models[f])
}

// chatPrerequisite names what a chat family's default adapter lacks.
func chatPrerequisite(f Family, settings Settings) string {
	if settings.Models[f] == "" {
		return fmt.Sprintf("review.models.%s is not set", f)
	}
	if p, ok := chatagent.Lookup(string(f)); ok && p.KeyEnv != "" && !anyEnv(p.KeyEnv) {
		return p.KeyEnv + " is not set"
	}
	return ""
}

// startChat starts a chat reviewer on the checked tree. It fails before
// starting when the change touches a file the reviewer could not read.
func startChat(ctx context.Context, root string, trees reviewTrees, argv []string, f Family, name, dir string, timeout time.Duration, efforts ...string) (*runner, error) {
	args := slices.Clone(argv)
	if effort := slotEffort(efforts, 0); effort != "" {
		args = append(args, "--effort="+effort)
	}
	if args[0] == chatSelfCommand {
		self, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("locate the slopcop binary: %w", err)
		}
		args[0] = self
	}
	model := ""
	for _, arg := range args {
		if v, ok := strings.CutPrefix(arg, chatModelFlag); ok {
			model = v
		}
	}
	if err := CheckModel(f, model); err != nil {
		return nil, err
	}
	if trees.checked != "" {
		files, err := gitx.TreeFiles(ctx, root, trees.checked, chatagent.MaxFile)
		if err != nil {
			return nil, fmt.Errorf("list the reviewed tree: %w", err)
		}
		paths := make([]string, len(files))
		for i, file := range files {
			paths[i] = file.Path
		}
		if err = requireChangedFiles(ctx, root, trees, paths); err != nil {
			return nil, err
		}
		// The review diff ignores the repository's attributes, so a binary
		// file reaches the model only as extracted text, which read_file
		// cannot add to. Check both sides, since the old side of a deleted
		// file exists only in the base.
		if err = requireTextChanges(ctx, root, trees.base, trees.checked); err != nil {
			return nil, err
		}
		if err = requireTextChanges(ctx, root, trees.checked, trees.base); err != nil {
			return nil, err
		}
		args = append(args, "--tree="+trees.checked, "--base="+trees.base)
	}
	s, err := startRunnerWith(ctx, root, args, f, name, dir, timeout, runnerOptions{guard: &toolGuard{check: mayHaveRun(chatToolCheck)}, refuseAll: true})
	if err != nil {
		return nil, err
	}
	s.result.Effort = slotEffort(efforts, 0)
	s.result.Adapter = string(f)
	if err = s.prepareChat(root, model); err != nil {
		err = startError(s.ctx, err)
		s.stop()
		return s, err
	}
	return s, nil
}

// requireTextChanges fails when a regular file that differs in tree from
// other is binary in tree, or too large to check.
func requireTextChanges(ctx context.Context, root, other, tree string) error {
	changed, err := gitx.ChangedFiles(ctx, root, other, tree)
	if err != nil {
		return fmt.Errorf("list the changed files: %w", err)
	}
	files, err := gitx.TreeFiles(ctx, root, tree, chatagent.MaxFile)
	if err != nil {
		return fmt.Errorf("list the reviewed tree: %w", err)
	}
	objects := make(map[string]string, len(files))
	for _, file := range files {
		objects[file.Path] = file.Object
	}
	for _, path := range changed {
		// JSON would alter such a path, so the model could not ask for it.
		if !utf8.ValidString(path) {
			return fmt.Errorf("%w: %q", errChangeNotExported, path)
		}
		object, ok := objects[path]
		if !ok {
			return fmt.Errorf("%w: %q", errChangeNotExported, path)
		}
		data, err := gitx.Blob(ctx, root, object)
		if err != nil {
			return fmt.Errorf("read changed file %q: %w", path, err)
		}
		// JSON replaces invalid UTF-8, so two different changes could reach
		// the model as the same text.
		if !chatagent.IsBinary(data) && !utf8.Valid(data) {
			return fmt.Errorf("%w: %q", errChangeBinary, path)
		}
	}
	return nil
}

// prepareChat opens the session and checks the agent runs the model asked
// for. Effort is the model's default: asking for one would exclude every
// endpoint of a model without reasoning settings.
func (s *runner) prepareChat(root, model string) error {
	session, err := s.conn.NewSession(s.ctx, acp.NewSessionRequest{Cwd: root, McpServers: []acp.McpServer{}})
	if err != nil {
		return fmt.Errorf("session/new: %w", err)
	}
	s.setSession(session.SessionId)
	option := configOption(session.ConfigOptions, "model")
	if option == nil || string(option.CurrentValue) != model {
		return errors.New("the adapter did not confirm the configured model, so the reviewer does not run")
	}
	s.result.Model = model
	s.result.Effort = copilotDefaultEffort
	return nil
}
