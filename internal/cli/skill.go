package cli

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BrokkAi/slopcop/skills"
)

// skillName is the folder the skill occupies: <skills directory>/slopcop/SKILL.md.
const skillName = "slopcop"

// skillFileName is the file every harness looks for inside that folder.
const skillFileName = "SKILL.md"

// skillRoot is one well-known skills directory, plus the harness directory
// whose presence means the harness is installed on this machine.
type skillRoot struct {
	marker string
	dir    string
}

// skillRoots are the well-known places the agent harnesses read skills from:
//
//	~/.agents/skills             Codex user skills, and the opencode universal directory
//	~/.claude/skills             Claude Code personal skills, also read by opencode
//	$CODEX_HOME/skills           the Codex home, or ~/.codex/skills when unset
func skillRoots() ([]skillRoot, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("locate the user home directory: %w", err)
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome = strings.TrimSpace(codexHome); codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	agents := filepath.Join(home, ".agents")
	claude := filepath.Join(home, ".claude")
	return []skillRoot{
		{marker: agents, dir: filepath.Join(agents, "skills")},
		{marker: claude, dir: filepath.Join(claude, "skills")},
		{marker: codexHome, dir: filepath.Join(codexHome, "skills")},
	}, nil
}

// writeSkill writes the bundled skill files into dir.
func writeSkill(dir string, files []skillFile) error {
	for _, file := range files {
		path := filepath.Join(dir, file.relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, file.data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}

// syncBundledSkill writes the bundled skill into every well-known skills
// directory whose harness is installed here, wherever it is missing or
// differs from the bundled one, so agents follow the instructions of the
// version that runs. It returns the directories it wrote.
func syncBundledSkill() ([]string, error) {
	files, err := bundledSkillFiles()
	if err != nil {
		return nil, err
	}
	roots, err := skillRoots()
	if err != nil {
		return nil, err
	}
	var updated []string
	for _, root := range roots {
		if !isDirectory(root.marker) {
			continue
		}
		dir := filepath.Join(root.dir, skillName)
		if skillMatches(dir, files) {
			continue
		}
		if err := writeSkill(dir, files); err != nil {
			return updated, err
		}
		updated = append(updated, dir)
	}
	return updated, nil
}

// skillMatches reports whether every bundled file is in dir with the bundled
// content.
func skillMatches(dir string, files []skillFile) bool {
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(dir, file.relative))
		if err != nil || !bytes.Equal(data, file.data) {
			return false
		}
	}
	return true
}

// skillFile is one file of the bundled skill, named relative to the skill
// folder.
type skillFile struct {
	relative string
	data     []byte
}

// bundledSkillFiles is the skill compiled into this binary.
func bundledSkillFiles() ([]skillFile, error) {
	var files []skillFile
	err := fs.WalkDir(skills.Files, skillName, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := skills.Files.ReadFile(name)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(skillName, filepath.FromSlash(name))
		if err != nil {
			return err
		}
		files = append(files, skillFile{relative: relative, data: data})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read the bundled skill: %w", err)
	}
	return files, nil
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
