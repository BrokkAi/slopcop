package cli

import (
	"bytes"
	"crypto/rand"
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

// initFailed is init's exit code when a harness is left without a usable
// skill: none could be installed, or the one there could not be read.
const initFailed = 2

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
	// skillUnknown: the directory could not be read or written; it goes with
	// an error.
	skillUnknown skillState = iota
	// skillInstalled: no skill was there, and the bundled one was written.
	skillInstalled
	// skillUpdated: an earlier release's untouched skill was replaced.
	skillUpdated
	// skillCurrent: this release's skill was already there.
	skillCurrent
	// skillEdited: a skill with local changes was there and is left alone.
	// With an error, a file besides SKILL.md could not be read, so the skill
	// is left alone the same way.
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
	// failed are the directories init could not read or write, with why;
	// the other directories still go ahead.
	failed []failedSkill
}

// failedSkill is a skill directory init could not read or write. state is
// what installSkillAt was doing there when it failed.
type failedSkill struct {
	dir   string
	err   error
	state skillState
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
		if err != nil {
			result.failed = append(result.failed, failedSkill{dir: dir, err: err, state: state})
			continue
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
// With a failure, the state says what was there before anything was
// written: skillInstalled for no skill, skillUpdated for an earlier
// release's readable skill, which a harness still reads, skillEdited for a
// skill with a file besides SKILL.md that could not be read, which is left
// alone, and skillUnknown for a SKILL.md that could not be read.
func installSkillAt(root string, files []skillFile) (skillState, error) {
	dir := filepath.Join(root, skillName)
	if _, err := os.Stat(filepath.Join(dir, skillFileName)); errors.Is(err, fs.ErrNotExist) {
		return skillInstalled, writeSkill(dir, files)
	} else if err != nil {
		return skillUnknown, fmt.Errorf("stat %s: %w", dir, err)
	}
	state, err := installedSkillState(dir, files)
	if err != nil || state != skillUpdated {
		return state, err
	}
	return skillUpdated, writeSkill(dir, files)
}

// installedSkillState compares the skill in dir with the bundled one. It is
// current when every file matches this release's, an earlier release's when
// each file matches this release's or an earlier one's (a file the earlier
// release did not ship is missing), and edited otherwise. A SKILL.md that
// cannot be read is an error, since a harness cannot read it either; any
// other file that cannot be read makes the skill edited, with the error.
func installedSkillState(dir string, files []skillFile) (skillState, error) {
	state := skillCurrent
	var unreadable error
	for _, file := range files {
		path := filepath.Join(dir, file.relative)
		data, err := os.ReadFile(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			state = skillUpdated
		case err != nil && file.relative == skillFileName:
			return skillUnknown, fmt.Errorf("read %s: %w", path, err)
		case err != nil:
			if unreadable == nil {
				unreadable = fmt.Errorf("read %s: %w", path, err)
			}
		case bytes.Equal(data, file.data):
		case shippedBefore(file.relative, data):
			state = skillUpdated
		default:
			return skillEdited, nil
		}
	}
	if unreadable != nil {
		return skillEdited, unreadable
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
		if err := writeFileAtomically(path, file.data); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}

// errReadOnly is a skill file without its owner's write bit, which init
// leaves as it is.
var errReadOnly = errors.New("the file is read-only")

// writeFileAtomically replaces path through a synced temporary file and a
// rename, so an interrupted write never leaves a truncated skill file. A
// symlink is resolved and its target replaced the same way, so a dotfile
// manager keeps its link (and a read-only target directory fails, as it
// should). A file whose content already matches is left as it is, and one
// without its owner's write bit is read-only and is not replaced. A replaced
// file keeps its permission bits; a new one is created 0644 under the umask,
// as os.WriteFile would.
func writeFileAtomically(path string, data []byte) error {
	path, err := linkTarget(path)
	if err != nil {
		return err
	}
	var keep fs.FileMode
	if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
		if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, data) {
			return nil
		}
		if info.Mode().Perm()&0o200 == 0 {
			return errReadOnly
		}
		keep = info.Mode().Perm()
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	temp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+"."+hex.EncodeToString(suffix[:]))
	file, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer os.Remove(temp)
	if keep != 0 {
		// Chmod, unlike the create, is not cut by the umask.
		if err := file.Chmod(keep); err != nil {
			_ = file.Close()
			return err
		}
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

// linkTarget is the file a write to path lands in: path itself, or the
// target of the symlink at path. A dangling link, which a dotfile manager
// may set up before its target exists, names its target relative to the
// link's directory, and the target is created there when its directory
// exists.
func linkTarget(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&fs.ModeSymlink == 0 {
		return path, nil
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	target, err := os.Readlink(path)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(target) {
		dir, err := filepath.EvalSymlinks(filepath.Dir(path))
		if err != nil {
			return "", err
		}
		target = filepath.Join(dir, target)
	}
	if !isDirectory(filepath.Dir(target)) {
		return "", fmt.Errorf("it links to %s, whose directory %s does not exist", target, filepath.Dir(target))
	}
	// The target may be a dangling link in turn; a loop of links already
	// failed EvalSymlinks above.
	return linkTarget(target)
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
		Args: usageArgs(cobra.NoArgs),
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
			unusable := false
			for _, skill := range result.failed {
				switch skill.state {
				case skillUpdated:
					// A skill a harness can read is still there, the
					// earlier release's or partly this one's: say so and go
					// on.
					fmt.Fprintf(cmd.ErrOrStderr(),
						"slopcop: could not update the slopcop skill at %s (%v); it may be partly updated, so run slopcop init again once it is writable\n",
						skill.dir, skill.err)
				case skillEdited:
					// SKILL.md is readable, so a harness still reads the
					// skill: leave it alone, as with local changes.
					fmt.Fprintf(cmd.ErrOrStderr(),
						"slopcop: could not read all of the slopcop skill at %s (%v), so it was left alone; its SKILL.md is readable, so an agent harness can still use it\n",
						skill.dir, skill.err)
				case skillInstalled:
					unusable = true
					fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: could not install the slopcop skill at %s: %v\n", skill.dir, skill.err)
				default:
					unusable = true
					fmt.Fprintf(cmd.ErrOrStderr(),
						"slopcop: could not read the slopcop skill at %s, so an agent harness cannot either: %v\n",
						skill.dir, skill.err)
				}
			}
			if len(result.installed)+len(result.updated)+len(result.present)+len(result.edited)+len(result.failed) == 0 {
				fmt.Fprintln(stdout, "slopcop: no agent skills directory found, nothing to install")
			}
			if unusable {
				return silentExit{initFailed}
			}
			return nil
		},
	}
	command.Flags().BoolVar(&noSkill, "no-skill", false, "do not install or update the agent skill")
	return command
}
