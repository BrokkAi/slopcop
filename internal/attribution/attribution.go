// Package attribution learns which agent harness and models wrote a change,
// so a check can send it and the service can tie each finding to the model
// that wrote its file.
//
// Everything here is read from the machine and asserted by the client, so it
// is analytics only. It never fails a check: whatever cannot be read is left
// out.
package attribution

import (
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
