package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
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
		return false, fmt.Errorf("stat %s: %w", dir, err)
	}
	if err := writeSkill(dir, files); err != nil {
		return false, err
	}
	return true, nil
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
// version that runs. A skill that differs is replaced, local edits included.
// It returns the directories it wrote: installed ones held no skill, updated
// ones held a different one.
func syncBundledSkill() (skillSync, error) {
	var result skillSync
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
		if skillMatches(dir, files) {
			continue
		}
		_, statErr := os.Stat(filepath.Join(dir, skillFileName))
		if err := writeSkill(dir, files); err != nil {
			return result, err
		}
		if statErr == nil {
			result.updated = append(result.updated, dir)
		} else {
			result.installed = append(result.installed, dir)
		}
	}
	return result, nil
}

// skillSync is what one sync pass wrote, as skill directories.
type skillSync struct {
	installed []string
	updated   []string
}

// syncSkillAndReport runs syncBundledSkill and names each directory it wrote
// on out. A failed install is reported on errOut and never fails the command
// that asked for it.
func syncSkillAndReport(out, errOut io.Writer) {
	result, err := syncBundledSkill()
	for _, dir := range result.installed {
		fmt.Fprintf(out, "slopcop: installed the slopcop skill at %s\n", dir)
	}
	for _, dir := range result.updated {
		fmt.Fprintf(out, "slopcop: updated the slopcop skill at %s to this version's, replacing what was there\n", dir)
	}
	if err != nil {
		fmt.Fprintf(errOut, "slopcop: could not install the slopcop skill: %v\n", err)
	}
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

func newInitCommand() *cobra.Command {
	var noSkill bool
	command := &cobra.Command{
		Use:   "init",
		Short: "Install the bundled agent skill",
		Long: `Install the agent skill that ships inside this binary, so a coding agent
knows how to run slopcop.

The skill goes into every well-known skills directory whose harness is
installed here: ~/.agents/skills (Codex, opencode), ~/.claude/skills (Claude
Code), and $CODEX_HOME/skills or ~/.codex/skills. A skill that is already
there is left alone. Login and every check also install the skill, and
rewrite one that differs from the bundled skill. Pass --no-skill to leave the skill install and any
update of it undone.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			stdout := cmd.OutOrStdout()
			if noSkill {
				fmt.Fprintln(stdout, "slopcop: left the slopcop skill alone (--no-skill)")
				return nil
			}
			result, err := installBundledSkill()
			if err != nil {
				return err
			}
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
	command.Flags().BoolVar(&noSkill, "no-skill", false, "do not install or update the agent skill")
	return command
}
