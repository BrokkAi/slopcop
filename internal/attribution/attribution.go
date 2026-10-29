// Package attribution learns which agent harness and models wrote a change,
// so a check can send it and the service can tie each finding to the model
// that wrote its file.
//
// Everything here is read from the machine and asserted by the client, so it
// is analytics only. It never fails a check: whatever cannot be read is left
// out.
package attribution

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BrokkAi/slopcop/internal/service"
)

// SourceTranscript says the CLI read the model from the harness's own session
// record at check time.
const SourceTranscript = "transcript"

// maxModels mirrors the service's MAX_ATTRIBUTED_MODELS.
const maxModels = 32

// Detect returns the attribution of a check of the repository at repoRoot,
// run in the current process's environment, or nil when no harness is found.
func Detect(repoRoot string) *service.Attribution {
	return detect(os.Getenv, repoRoot)
}

// hookHarnesses are the harnesses with a Stop hook: the variable each puts
// its session id in, in the commands its agent runs, and how its session is
// read.
var hookHarnesses = map[string]struct {
	variable string
	read     func(func(string) string, string, string) (*service.Attribution, []string)
}{
	ClaudeHarness: {"CLAUDE_CODE_SESSION_ID", claudeCodeFrom},
	CodexHarness:  {"CODEX_THREAD_ID", codexFrom},
}

// DetectHook returns the attribution of the session a Stop hook of harness
// names. Hooks do not necessarily inherit the session environment an agent's
// own commands receive, so the payload's session id and transcript path take
// its place. Its models name the files the session wrote.
func DetectHook(harness, repoRoot, sessionID, transcript string) *service.Attribution {
	h, ok := hookHarnesses[harness]
	if !ok {
		return nil
	}
	getenv := func(name string) string {
		if name == h.variable {
			return sessionID
		}
		return os.Getenv(name)
	}
	attribution, _ := h.read(getenv, repoRoot, transcript)
	return attribution
}

// HookSession is the session id the Stop hook of harness names for the
// session id an attribution carries. They differ for a Codex subagent: its
// commands name its own thread, but its hooks name the session it belongs
// to.
func HookSession(harness, sessionID string) string {
	if harness == CodexHarness {
		return codexSession(os.Getenv, sessionID)
	}
	return sessionID
}

// HookSessions maps each harness whose session the environment names to the
// session id its Stop hook names.
func HookSessions() map[string]string {
	sessions := map[string]string{}
	for harness, h := range hookHarnesses {
		if id := strings.TrimSpace(os.Getenv(h.variable)); sessionIDPattern.MatchString(id) {
			sessions[harness] = HookSession(harness, id)
		}
	}
	return sessions
}

// detect reads every harness whose session the environment names. A harness
// started from another inherits that one's environment too, so when both are
// named the one whose session record was written last is the one running the
// check.
func detect(getenv func(string) string, repoRoot string) *service.Attribution {
	var best *service.Attribution
	var bestTime time.Time
	for _, read := range []func(func(string) string, string) (*service.Attribution, []string){claudeCode, codex} {
		attribution, records := read(getenv, repoRoot)
		if attribution == nil {
			continue
		}
		if written := lastWritten(records); best == nil || written.After(bestTime) {
			best, bestTime = attribution, written
		}
	}
	return best
}

// lastWritten is the latest modification time of the files that exist.
func lastWritten(paths []string) time.Time {
	var latest time.Time
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && info.ModTime().After(latest) {
			latest = info.ModTime()
		}
	}
	return latest
}

// maxLineBytes bounds one session record line held in memory. A longer line
// is skipped. A variable so tests can lower it.
var maxLineBytes = 64 << 20

// readFirst calls each with the first line of a session record, at most
// maxLineBytes long.
func readFirst(path string, each func(line []byte)) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()
	line, err := bufio.NewReaderSize(io.LimitReader(file, int64(maxLineBytes)), 64<<10).ReadBytes('\n')
	if len(line) > 0 && (err == nil || errors.Is(err, io.EOF)) {
		each(line)
	}
}

// readRecord opens one session record, bounded by maxTranscriptBytes, and
// calls each for every line of at most maxLineBytes.
func readRecord(path string, each func(line []byte)) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()
	reader := bufio.NewReaderSize(io.LimitReader(file, maxTranscriptBytes), 64<<10)
	var line []byte
	tooLong := false
	for {
		chunk, err := reader.ReadSlice('\n')
		if !tooLong {
			if len(line)+len(chunk) > maxLineBytes {
				tooLong, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if !tooLong && len(line) > 0 {
			each(line)
		}
		line, tooLong = line[:0], false
		if err != nil {
			return
		}
	}
}

type modelKey struct{ model, effort string }

// fileEdit is one successful file edit a session record names.
type fileEdit struct {
	model, effort, path string
}

// repoRoots is the repository root and, when it differs, the root with its
// symbolic links resolved: a harness can record either form, as with /tmp and
// /private/tmp on macOS.
func repoRoots(repoRoot string) []string {
	roots := []string{filepath.Clean(repoRoot)}
	if resolved, err := filepath.EvalSymlinks(repoRoot); err == nil && resolved != roots[0] {
		roots = append(roots, resolved)
	}
	return roots
}

// relativeToRepo turns an absolute path a harness recorded into the
// slash-separated repository-relative path the service matches findings on.
// A path outside the repository is not attributed.
func relativeToRepo(roots []string, path string) (string, bool) {
	if !filepath.IsAbs(path) {
		return "", false
	}
	candidates := []string{filepath.Clean(path)}
	if dir, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		candidates = append(candidates, filepath.Join(dir, filepath.Base(path)))
	}
	for _, root := range roots {
		for _, candidate := range candidates {
			relative, err := filepath.Rel(root, candidate)
			if err != nil || relative == "." || relative == ".." ||
				strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				continue
			}
			return filepath.ToSlash(relative), true
		}
	}
	return "", false
}

// models groups the edits inside the repository by model and effort into the
// attribution's entries, each with the repository-relative files it wrote,
// ordered by model then effort, at most maxModels of them.
func models(repoRoot string, edits []fileEdit) []service.AttributedModel {
	roots := repoRoots(repoRoot)
	authors := map[modelKey]map[string]bool{}
	for _, edit := range edits {
		relative, ok := relativeToRepo(roots, edit.path)
		if !ok {
			continue
		}
		key := modelKey{edit.model, edit.effort}
		if authors[key] == nil {
			authors[key] = map[string]bool{}
		}
		authors[key][relative] = true
	}
	keys := make([]modelKey, 0, len(authors))
	for key := range authors {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].model != keys[j].model {
			return keys[i].model < keys[j].model
		}
		return keys[i].effort < keys[j].effort
	})
	if len(keys) > maxModels {
		keys = keys[:maxModels]
	}
	var out []service.AttributedModel
	for _, key := range keys {
		files := make([]string, 0, len(authors[key]))
		for file := range authors[key] {
			files = append(files, file)
		}
		sort.Strings(files)
		out = append(out, service.AttributedModel{
			Model:  key.model,
			Effort: key.effort,
			Source: SourceTranscript,
			Files:  files,
		})
	}
	return out
}
