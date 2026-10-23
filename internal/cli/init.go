package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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

// skillState is what an install pass found in one skills directory.
type skillState int

const (
	// skillInstalled: no skill was there, and the bundled one was written.
	skillInstalled skillState = iota
	// skillUpdated: an earlier release's untouched skill was replaced.
	skillUpdated
	// skillCurrent: this release's skill was already there.
	skillCurrent
	// skillEdited: a skill with local changes was there and is left alone.
	skillEdited
)

// skillInstall is what one install pass did, as skill directories.
type skillInstall struct {
	// installed are the directories that received the bundled skill.
	installed []string
	// updated are the directories whose earlier release's skill was replaced.
	updated []string
	// present are the directories that already held this release's skill.
	present []string
	// edited are the directories whose skill has local changes, which is
	// left as it is.
	edited []string
	// unwritable are the directories whose earlier release's skill could not
	// be replaced (a read-only file, say), with why; the rest still go ahead.
	unwritable []unwritableSkill
}

// unwritableSkill is a skill directory init could not update, and why.
type unwritableSkill struct {
	dir string
	err error
}

// installBundledSkill writes the bundled skill into every well-known skills
// directory whose harness is installed here. An earlier release's skill is
// brought up to date; one with local changes is never rewritten, so local
// edits survive.
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
		state, err := installSkillAt(root.dir, files)
		if err != nil && state == skillUpdated {
			result.unwritable = append(result.unwritable, unwritableSkill{dir: dir, err: err})
			continue
		}
		if err != nil {
			return result, err
		}
		switch state {
		case skillInstalled:
			result.installed = append(result.installed, dir)
		case skillUpdated:
			result.updated = append(result.updated, dir)
		case skillCurrent:
			result.present = append(result.present, dir)
		default:
			result.edited = append(result.edited, dir)
		}
	}
	return result, nil
}

// installSkillAt writes the bundled skill into a skills directory when no
// skill is there, or when the one there is an earlier release's, untouched.
func installSkillAt(root string, files []skillFile) (skillState, error) {
	dir := filepath.Join(root, skillName)
	if _, err := os.Stat(filepath.Join(dir, skillFileName)); errors.Is(err, fs.ErrNotExist) {
		return skillInstalled, writeSkill(dir, files)
	} else if err != nil {
		return 0, fmt.Errorf("stat %s: %w", dir, err)
	}
	state, err := installedSkillState(dir, files)
	if err != nil || state != skillUpdated {
		return state, err
	}
	return skillUpdated, writeSkill(dir, files)
}

// installedSkillState compares the skill in dir with the bundled one. It is
// current when every file matches this release's, an earlier release's when
// each file matches this release's or an earlier one's, and edited otherwise.
// Every release shipped every file, so a missing one was deleted: a local
// change.
func installedSkillState(dir string, files []skillFile) (skillState, error) {
	state := skillCurrent
	for _, file := range files {
		path := filepath.Join(dir, file.relative)
		data, err := os.ReadFile(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return skillEdited, nil
		case err != nil:
			return 0, fmt.Errorf("read %s: %w", path, err)
		case bytes.Equal(data, file.data):
		case shippedBefore(file.relative, data):
			state = skillUpdated
		default:
			return skillEdited, nil
		}
	}
	return state, nil
}

// shippedBefore reports whether data is an earlier release's copy of the
// skill file at relative.
func shippedBefore(relative string, data []byte) bool {
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	for _, previous := range skills.Previous[filepath.ToSlash(relative)] {
		if previous == digest {
			return true
		}
	}
	return false
}

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
Code), and $CODEX_HOME/skills or ~/.codex/skills. A skill an earlier release
installed is brought up to date; one with local changes is left alone.
Nothing else installs the skill: this command is the only thing that writes
it. Pass --no-skill to leave the skill install and any update of it undone.`,
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
			for _, dir := range result.updated {
				fmt.Fprintf(stdout, "slopcop: updated the slopcop skill at %s\n", dir)
			}
			for _, dir := range result.present {
				fmt.Fprintf(stdout, "slopcop: the slopcop skill is already at %s\n", dir)
			}
			for _, dir := range result.edited {
				fmt.Fprintf(stdout,
					"slopcop: the slopcop skill at %s differs from this release's (local changes, "+
						"or a newer slopcop wrote it), so it was left alone; to use this release's "+
						"skill, delete it and run slopcop init again\n", dir)
			}
			for _, skill := range result.unwritable {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"slopcop: could not update the slopcop skill at %s, so it was left as it was: %v\n",
					skill.dir, skill.err)
			}
			if len(result.installed)+len(result.updated)+len(result.present)+len(result.edited)+len(result.unwritable) == 0 {
				fmt.Fprintln(stdout, "slopcop: no agent skills directory found, nothing to install")
			}
			return nil
		},
	}
	command.Flags().BoolVar(&noSkill, "no-skill", false, "do not install or update the agent skill")
	return command
}
