package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

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

// skillInstall is what one install pass did, as skill directories.
type skillInstall struct {
	// installed are the directories that received the bundled skill.
	installed []string
	// present are the directories that already held a skill, which is left as
	// it is.
	present []string
}

// installBundledSkill writes the bundled skill into every well-known skills
// directory whose harness is installed here. A skill that is already in place
// is never rewritten, so local edits survive.
func installBundledSkill() (skillInstall, error) {
	var result skillInstall
	files, err := bundledSkillFiles()
	if err != nil {
		return result, err
	}
	roots, err := skillRoots()
	if err != nil {
		return result, err
	}
	for _, root := range roots {
		if !isDirectory(root.marker) {
			continue
		}
		dir := filepath.Join(root.dir, skillName)
		written, err := installSkillAt(root.dir, files)
		if err != nil {
			return result, err
		}
		if written {
			result.installed = append(result.installed, dir)
			continue
		}
		result.present = append(result.present, dir)
	}
	return result, nil
}

// installSkillAt writes the bundled skill into a skills directory unless a
// skill is already there. The boolean reports whether it wrote.
func installSkillAt(root string, files []skillFile) (bool, error) {
	dir := filepath.Join(root, skillName)
	if _, err := os.Stat(filepath.Join(dir, skillFileName)); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("read %s: %w", dir, err)
	}
	for _, file := range files {
		path := filepath.Join(dir, file.relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return false, fmt.Errorf("create %s: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, file.data, 0o644); err != nil {
			return false, fmt.Errorf("write %s: %w", path, err)
		}
	}
	return true, nil
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

// installSkillOnStartup is the install every run performs, so a harness picks
// the skill up without a separate step. It is best effort: nothing here may
// stop a check.
func installSkillOnStartup() {
	_, _ = installBundledSkill()
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func newInitCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Install the bundled agent skill",
		Long: `Install the agent skill that ships inside this binary, so a coding agent
knows how to run slopcop.

The skill goes into every well-known skills directory whose harness is
installed here: ~/.agents/skills (Codex, opencode), ~/.claude/skills (Claude
Code), and $CODEX_HOME/skills or ~/.codex/skills. A skill that is already
there is left alone. Every slopcop run performs this install too, so a harness
normally has the skill before this command is ever typed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := installBundledSkill()
			if err != nil {
				return err
			}
			stdout := cmd.OutOrStdout()
			for _, dir := range result.installed {
				fmt.Fprintf(stdout, "slopcop: installed the slopcop skill at %s\n", dir)
			}
			for _, dir := range result.present {
				fmt.Fprintf(stdout, "slopcop: the slopcop skill is already at %s\n", dir)
			}
			if len(result.installed) == 0 && len(result.present) == 0 {
				fmt.Fprintln(stdout, "slopcop: no agent skills directory found, nothing to install")
			}
			return nil
		},
	}
}
