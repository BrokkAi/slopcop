package attribution

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BrokkAi/slopcop/internal/service"

	// A pure-Go SQLite: OpenCode keeps its sessions in a SQLite database in
	// WAL mode, and the release builds have no cgo.
	_ "modernc.org/sqlite"
)

// OpenCodeHarness is the harness name an OpenCode session's attribution
// carries.
const OpenCodeHarness = "opencode"

// openCodeTimeout bounds the read of OpenCode's database, which OpenCode
// may be writing to while the check runs.
const openCodeTimeout = 5 * time.Second

// openCodeEditTools are the tools that write a file. OpenCode offers patch to
// GPT models in place of edit and write.
var openCodeEditTools = []string{"edit", "write", "patch"}

// openCode reads the attribution of a check run inside an OpenCode session.
// OpenCode puts OPENCODE_SESSION_ID in the commands the agent runs, and its
// database records the model and variant of every assistant step and the
// state of every tool call in it. A failed edit ends in the error state.
//
// The layout read here is OpenCode 2's, from its source: the session_v2,
// session_message and session_part tables (packages/core/src/session/sql.ts)
// and the assistant message and tool part shapes
// (packages/schema/src/session-message.ts) at
// github.com/anomalyco/opencode@893e8dc43f05e872d4cac603486ce8a7181170fa.
func openCode(getenv func(string) string, repoRoot string) (*service.Attribution, []string) {
	sessionID := strings.TrimSpace(getenv("OPENCODE_SESSION_ID"))
	if !sessionIDPattern.MatchString(sessionID) || strings.Trim(sessionID, ".") == "" {
		return nil, nil
	}
	attribution := &service.Attribution{Harness: OpenCodeHarness, SessionID: sessionID}
	for _, database := range openCodeDatabases(getenv) {
		version, edits, ok := openCodeSession(database, sessionID)
		if !ok {
			continue
		}
		attribution.HarnessVersion = version
		attribution.Models = models(repoRoot, edits)
		return attribution, []string{database, database + "-wal"}
	}
	return attribution, nil
}

// openCodeDatabases are the databases that may hold the session: the one
// OPENCODE_DB names, or, under OpenCode's data directory, opencode.db and
// the database of each other release channel.
func openCodeDatabases(getenv func(string) string) []string {
	data := strings.TrimSpace(getenv("XDG_DATA_HOME"))
	if data == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		data = filepath.Join(home, ".local", "share")
	}
	data = filepath.Join(data, "opencode")
	if named := strings.TrimSpace(getenv("OPENCODE_DB")); named != "" {
		if named == ":memory:" {
			return nil
		}
		if !filepath.IsAbs(named) {
			named = filepath.Join(data, named)
		}
		return []string{named}
	}
	databases := []string{filepath.Join(data, "opencode.db")}
	channels, _ := filepath.Glob(filepath.Join(data, "opencode-*.db"))
	for _, channel := range channels {
		// opencode-next.db is the pre-release database OpenCode 2 imported.
		if filepath.Base(channel) != "opencode-next.db" {
			databases = append(databases, channel)
		}
	}
	return databases
}

// openCodeSession reads the version that created the session and the
// successful edits of the session and its subagents' sessions from one
// database. ok is false when the database does not hold the session.
func openCodeSession(path, sessionID string) (version string, edits []fileEdit, ok bool) {
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return "", nil, false
	}
	db, err := sql.Open("sqlite", openCodeDSN(path))
	if err != nil {
		return "", nil, false
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), openCodeTimeout)
	defer cancel()
	if db.QueryRowContext(ctx, `SELECT version FROM session_v2 WHERE id = ?`, sessionID).Scan(&version) != nil {
		return "", nil, false
	}
	var parts int
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'session_part'`).Scan(&parts)
	rows, err := db.QueryContext(ctx, openCodeEditsQuery(parts > 0), sessionID)
	if err != nil {
		return version, nil, true
	}
	defer rows.Close()
	for rows.Next() {
		var directory, model, part string
		var variant sql.NullString
		if rows.Scan(&directory, &model, &variant, &part) != nil {
			continue
		}
		effort := variant.String
		// "default" names the provider's default, not a level.
		if effort == "default" {
			effort = ""
		}
		for _, target := range openCodeTargets([]byte(part)) {
			if resolved, ok := openCodePath(directory, target); ok {
				edits = append(edits, fileEdit{model, effort, resolved})
			}
		}
	}
	return version, edits, true
}

// openCodeDSN opens the database read-only, waiting a moment for a writer.
func openCodeDSN(path string) string {
	slashed := filepath.ToSlash(path)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	query := url.Values{"mode": {"ro"}, "_pragma": {"busy_timeout(2000)"}}
	return (&url.URL{Scheme: "file", Path: slashed, RawQuery: query.Encode()}).String()
}

// openCodeEditsQuery selects, for each completed edit tool call of the
// session and its descendants, the session's directory, the model and
// variant of the step that made it, and the tool part. A step's parts are
// inline in its message's content, or, since part rows, in session_part.
func openCodeEditsQuery(partRows bool) string {
	tools := "'" + strings.Join(openCodeEditTools, "', '") + "'"
	query := `
WITH RECURSIVE tree(id) AS (
  SELECT id FROM session_v2 WHERE id = ?1
  UNION SELECT s.id FROM session_v2 s JOIN tree ON s.parent_id = tree.id
)
SELECT s.directory, json_extract(m.data, '$.model.id'), json_extract(m.data, '$.model.variant'), c.value
FROM session_message m
JOIN session_v2 s ON s.id = m.session_id, json_each(m.data, '$.content') c
WHERE m.session_id IN tree AND m.type = 'assistant'
  AND json_extract(m.data, '$.model.id') IS NOT NULL
  AND json_extract(c.value, '$.type') = 'tool'
  AND json_extract(c.value, '$.state.status') = 'completed'
  AND json_extract(c.value, '$.name') IN (` + tools + `)`
	if partRows {
		query += `
UNION ALL
SELECT s.directory, json_extract(m.data, '$.model.id'), json_extract(m.data, '$.model.variant'), p.data
FROM session_part p
JOIN session_message m ON m.id = p.message_id
JOIN session_v2 s ON s.id = p.session_id
WHERE p.session_id IN tree AND p.type = 'tool'
  AND json_extract(m.data, '$.model.id') IS NOT NULL
  AND json_extract(p.data, '$.state.status') = 'completed'
  AND json_extract(p.data, '$.name') IN (` + tools + `)`
	}
	return query
}

// openCodeToolPart is the part of a completed tool call this package reads:
// the path edit and write name, and the files patch wrote.
type openCodeToolPart struct {
	Name  string `json:"name"`
	State struct {
		Input struct {
			Path string `json:"path"`
		} `json:"input"`
		Metadata struct {
			Files []struct {
				File   string `json:"file"`
				Status string `json:"status"`
			} `json:"files"`
		} `json:"metadata"`
	} `json:"state"`
}

// openCodeTargets are the files one completed edit tool call wrote. A
// patch's move names its destination; a delete writes nothing.
func openCodeTargets(data []byte) []string {
	var part openCodeToolPart
	if json.Unmarshal(data, &part) != nil {
		return nil
	}
	if part.Name != "patch" {
		if part.State.Input.Path == "" {
			return nil
		}
		return []string{part.State.Input.Path}
	}
	var targets []string
	for _, file := range part.State.Metadata.Files {
		if file.File != "" && file.Status != "deleted" {
			targets = append(targets, file.File)
		}
	}
	return targets
}

// openCodePath resolves a path a tool call named as OpenCode does: a leading
// ~ is the home directory, and a relative path is the session directory's.
func openCodePath(directory, path string) (string, bool) {
	path = filepath.FromSlash(path)
	if path == "~" || strings.HasPrefix(path, "~"+string(filepath.Separator)) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		return filepath.Join(home, path[1:]), true
	}
	resolved := resolvePath(filepath.FromSlash(directory), path)
	return resolved, resolved != ""
}
