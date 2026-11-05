package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	agenthooks "github.com/BrokkAi/slopcop/internal/hooks"
	"github.com/BrokkAi/slopcop/internal/report"
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
	// neutral marks the vendor-neutral directory, which init uses when no
	// harness directory is present.
	neutral bool
}

// skillRoots are the well-known places the agent harnesses read skills from:
//
//	~/.agents/skills             the vendor-neutral directory, read by Codex, opencode, Cursor and others
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
		{marker: agents, dir: filepath.Join(agents, "skills"), neutral: true},
		{marker: claude, dir: filepath.Join(claude, "skills")},
		{marker: codexHome, dir: filepath.Join(codexHome, "skills")},
	}, nil
}

// skillTargets are the skills directories init writes: one for every harness
// installed here, or the vendor-neutral ~/.agents/skills when none is. That
// directory is read by Codex, opencode, Cursor and other agents, so init has
// somewhere to put the skill on a machine whose harness has not created its
// configuration directory yet.
func skillTargets() ([]skillRoot, error) {
	roots, err := skillRoots()
	if err != nil {
		return nil, err
	}
	var found []skillRoot
	for _, root := range roots {
		if isDirectory(root.marker) {
			found = append(found, root)
		}
	}
	if len(found) == 0 {
		for _, root := range roots {
			if root.neutral {
				return []skillRoot{root}, nil
			}
		}
	}
	return found, nil
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
func installBundledSkill(version string) (skillInstall, error) {
	var result skillInstall
	files, err := bundledSkillFiles()
	if err != nil {
		return result, err
	}
	targets, err := skillTargets()
	if err != nil {
		return result, err
	}
	for _, root := range targets {
		dir := filepath.Join(root.dir, skillName)
		written, err := installSkillAt(root.dir, files, version)
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
func installSkillAt(root string, files []skillFile, version string) (bool, error) {
	dir := filepath.Join(root, skillName)
	if _, err := os.Stat(filepath.Join(dir, skillFileName)); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("stat %s: %w", dir, err)
	}
	if err := writeSkill(dir, files, version); err != nil {
		return false, err
	}
	return true, nil
}

// writeSkill writes the bundled skill files into dir, and the stamp that
// records them as this version's.
func writeSkill(dir string, files []skillFile, version string) error {
	for _, file := range files {
		path := filepath.Join(dir, file.relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, file.data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return writeSkillStamp(dir, version, files)
}

// syncBundledSkill writes the bundled skill into every well-known skills
// directory whose harness is installed here, wherever it is missing or
// differs from the bundled one, so agents follow the instructions of the
// version that runs. A skill that differs is replaced, and files the bundle
// no longer has are removed; a copy of one with local edits is kept first.
// It returns what it wrote, one entry per directory.
func syncBundledSkill(version string) ([]report.SkillUpdate, error) {
	var updates []report.SkillUpdate
	files, err := bundledSkillFiles()
	if err != nil {
		return updates, err
	}
	roots, err := skillRoots()
	if err != nil {
		return updates, err
	}
	for _, root := range roots {
		if !isDirectory(root.marker) {
			continue
		}
		update, wrote, err := syncSkillAt(filepath.Join(root.dir, skillName), files, version)
		if wrote {
			updates = append(updates, update)
		}
		if err != nil {
			return updates, err
		}
	}
	return updates, nil
}

// syncSkillAt brings the skill in dir to the bundled one. The boolean
// reports whether it wrote any skill file; a skill that already matches only
// has its stamp brought to this version.
func syncSkillAt(dir string, files []skillFile, version string) (report.SkillUpdate, bool, error) {
	update := report.SkillUpdate{Dir: dir, To: version}
	present, err := readSkillDir(dir)
	if err != nil {
		return update, false, err
	}
	stamp, stamped := readSkillStamp(dir)
	update.Changed = changedSkillFiles(present, files)
	if len(update.Changed) == 0 {
		if stamped && stamp.Version == version {
			return update, false, nil
		}
		return update, false, writeSkillStamp(dir, version, files)
	}
	from, edited := skillOrigin(present, stamp, stamped)
	if _, ok := present[skillFileName]; ok {
		update.From = from
	}
	if edited {
		if update.Backup, err = backupSkill(dir, present); err != nil {
			return update, false, err
		}
	}
	if err := removeUnbundled(dir, present, files); err != nil {
		return update, true, err
	}
	return update, true, writeSkill(dir, files, version)
}

// syncSkillAndReport runs syncBundledSkill and says on out what it wrote,
// which it also returns for the machine reports. A failed install is reported
// on errOut and never fails the command that asked for it.
func syncSkillAndReport(out, errOut io.Writer, version string) []report.SkillUpdate {
	updates, err := syncBundledSkill(version)
	for _, update := range updates {
		fmt.Fprintln(out, skillNotice(update))
	}
	if err != nil {
		fmt.Fprintf(errOut, "slopcop: could not install the slopcop skill: %v\n", err)
	}
	return updates
}

// skillNotice says what one sync wrote: the versions, the files, and where a
// copy of local edits went, so an agent that loaded the old skill knows to
// reread it.
func skillNotice(update report.SkillUpdate) string {
	if update.From == "" {
		return fmt.Sprintf("slopcop: installed the slopcop skill at %s", update.Dir)
	}
	from := update.From
	if from == skillVersionUnknown {
		from = "an unrecognised version"
	}
	text := fmt.Sprintf("slopcop: updated the slopcop skill at %s from %s to %s (changed: %s). Reread it if you loaded the old one.",
		update.Dir, from, update.To, strings.Join(update.Changed, ", "))
	if releaseTag.MatchString(update.To) {
		text += " Release notes: " + releaseNotesURL + update.To
	}
	if update.Backup != "" {
		text += fmt.Sprintf("\nslopcop: the replaced skill had local edits; a copy is at %s", update.Backup)
	}
	return text
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

func newInitCommand(version string) *cobra.Command {
	var noSkill, noHooks, removeHooks bool
	command := &cobra.Command{
		Use:   "init",
		Short: "Install the bundled agent skill and the automatic check hook",
		Long: `Install the agent skill that ships inside this binary, so a coding agent
knows how to run slopcop, and a Claude Code and Codex hook that checks what a
session wrote when the agent stops.

The skill goes into every well-known skills directory whose harness is
installed here: ~/.agents/skills (Codex, opencode), ~/.claude/skills (Claude
Code), and $CODEX_HOME/skills or ~/.codex/skills. When no harness is found it
goes into ~/.agents/skills, the vendor-neutral directory those agents read, so
a fresh machine has the skill before its harness creates its own directory. A
skill that is already there is left alone. Login and every check also install
the skill, and rewrite one that differs from the bundled skill, naming the
versions and files that changed and keeping a copy of local edits under
skill-backups next to the slopcop configuration. Pass --no-skill to leave the
skill install and any update of it undone.

A Stop hook goes into Claude Code's settings.json, under CLAUDE_CONFIG_DIR or
~/.claude, Codex's hooks.json, under CODEX_HOME or ~/.codex, and Grok's
hooks/slopcop.json, under GROK_HOME or ~/.grok, for each harness installed
here. Codex runs it only once you trust it with /hooks.
Pass --no-hooks to skip the hook, or --remove-hooks to remove only SlopCop's.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			stdout := cmd.OutOrStdout()
			if removeHooks {
				// Each harness is updated on its own, so what changed is
				// said even when another harness failed.
				result, err := agenthooks.Remove()
				for _, path := range result.ChangedPaths {
					fmt.Fprintf(stdout, "slopcop: removed the automatic check hook from %s\n", path)
				}
				if len(result.ChangedPaths) == 0 && err == nil {
					fmt.Fprintln(stdout, "slopcop: no automatic check hook found, nothing to remove")
				}
				return err
			}

			if noSkill {
				fmt.Fprintln(stdout, "slopcop: left the slopcop skill alone (--no-skill)")
			} else {
				result, err := installBundledSkill(version)
				if err != nil {
					return err
				}
				for _, dir := range result.installed {
					fmt.Fprintf(stdout, "slopcop: installed the slopcop skill at %s\n", dir)
				}
				for _, dir := range result.present {
					fmt.Fprintf(stdout, "slopcop: the slopcop skill is already at %s\n", dir)
				}
			}

			if noHooks {
				fmt.Fprintln(stdout, "slopcop: left the automatic check hook alone (--no-hooks)")
				return nil
			}
			hookResult, err := agenthooks.Install()
			for _, path := range hookResult.ChangedPaths {
				fmt.Fprintf(stdout, "slopcop: installed the automatic check hook in %s\n", path)
			}
			// Codex says nothing when it skips a hook it does not trust,
			// so this is said every time, not only when the hook changed.
			if hookResult.NeedsTrust {
				fmt.Fprintln(stdout, "slopcop: Codex runs the hook only once you trust it; if you have not, open Codex and review it with /hooks")
			}
			if len(hookResult.Installed) > 0 {
				if problem := agenthooks.CommandProblem(hookResult.Installed); problem != "" {
					fmt.Fprintf(cmd.ErrOrStderr(),
						"slopcop: warning: %s. Put this slopcop first on PATH, or run 'slopcop init --remove-hooks'.\n", problem)
				}
			}
			return err
		},
	}
	command.Flags().BoolVar(&noSkill, "no-skill", false, "do not install or update the agent skill")
	command.Flags().BoolVar(&noHooks, "no-hooks", false, "do not install or update the automatic check hook")
	command.Flags().BoolVar(&removeHooks, "remove-hooks", false, "remove SlopCop's automatic check hook")
	command.MarkFlagsMutuallyExclusive("no-hooks", "remove-hooks")
	return command
}
