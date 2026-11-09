package attribution

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/service"
)

const testPiSession = "0b6c1d52-6f4e-4f7e-9d0c-6a3f1e2b7c11"

// installPiSession writes testdata/pi-session-synthesized.jsonl as
// testPiSession's file under the Pi session directory sessionDir, with
// {{repo}} replaced by repo, and returns its path. The fixture is built from
// the session format Pi's source defines (earendil-works/pi at 136a7b2d7d46),
// not captured from a live session.
func installPiSession(t *testing.T, sessionDir, repo string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "pi-session-synthesized.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	content := strings.ReplaceAll(string(data), "{{repo}}", filepath.ToSlash(repo))
	content = strings.ReplaceAll(content, "{{session}}", testPiSession)
	path := filepath.Join(sessionDir, "--some-project--", "2026-11-05T10-00-00-000Z_"+testPiSession+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPiAttributesEachSucceededEditToItsModel(t *testing.T) {
	want := &service.Attribution{
		Harness:   "pi",
		SessionID: testPiSession,
		Models: []service.AttributedModel{
			// c.go's edit failed, and a read names no author.
			{Model: "claude-opus-5-5", Effort: "high", Source: SourceTranscript, Files: []string{"a.go", "pkg/b.go"}},
			// d.go is written by absolute path, f.go by a codemode script
			// that failed after its edit. The message records no thinking
			// level, so the last change's applies. /elsewhere is outside the
			// repository, g.go's nested write failed, the third nested call
			// kept no arguments, and h.go's edit never answered.
			{Model: "gpt-6-luna", Effort: "low", Source: SourceTranscript, Files: []string{"d.go", "f.go"}},
		},
	}
	t.Run("session file", func(t *testing.T) {
		repo := t.TempDir()
		path := installPiSession(t, t.TempDir(), repo)
		got := detect(environment(map[string]string{
			"PI_SESSION_ID":   testPiSession,
			"PI_SESSION_FILE": path,
			// Not where the session is: PI_SESSION_FILE wins.
			"PI_CODING_AGENT_SESSION_DIR": t.TempDir(),
		}), repo)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("attribution =\n%+v\nwant\n%+v", got, want)
		}
	})
	t.Run("session dir", func(t *testing.T) {
		repo, sessionDir := t.TempDir(), t.TempDir()
		installPiSession(t, sessionDir, repo)
		got := detect(environment(map[string]string{
			"PI_SESSION_ID":               testPiSession,
			"PI_CODING_AGENT_SESSION_DIR": sessionDir,
		}), repo)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("attribution =\n%+v\nwant\n%+v", got, want)
		}
	})
	t.Run("agent dir", func(t *testing.T) {
		repo, agentDir := t.TempDir(), t.TempDir()
		installPiSession(t, filepath.Join(agentDir, "sessions"), repo)
		got := detect(environment(map[string]string{
			"PI_SESSION_ID":       testPiSession,
			"PI_CODING_AGENT_DIR": agentDir,
		}), repo)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("attribution =\n%+v\nwant\n%+v", got, want)
		}
	})
}

func TestPiIgnoresASessionFileOfAnotherSession(t *testing.T) {
	sessionDir, repo := t.TempDir(), t.TempDir()
	path := installPiSession(t, sessionDir, repo)
	got := detect(environment(map[string]string{
		"PI_SESSION_ID":               "other-session",
		"PI_SESSION_FILE":             path,
		"PI_CODING_AGENT_SESSION_DIR": sessionDir,
	}), repo)
	want := &service.Attribution{Harness: "pi", SessionID: "other-session"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attribution = %+v, want %+v", got, want)
	}
}
