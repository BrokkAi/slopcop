package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/gitx"
	agenthooks "github.com/BrokkAi/slopcop/internal/hooks"
	"github.com/BrokkAi/slopcop/internal/report"
	"github.com/BrokkAi/slopcop/internal/review"
	"github.com/BrokkAi/slopcop/skills"
)

// skillName is the folder the check skill occupies: <skills directory>/slopcop/SKILL.md.
const skillName = "slopcop"

// auditSkillName is the folder of the skill that audits landed code by module.
const auditSkillName = "slopcop-audit"

// bundledSkillNames are the skills this binary carries, each a folder in
// skills.Files, installed and kept current side by side.
var bundledSkillNames = []string{skillName, auditSkillName}

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
	// harness names the harness that reads this directory, empty for the
	// vendor-neutral one.
	harness string
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
	claude := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
	if claude == "" {
		claude = filepath.Join(home, ".claude")
	}
	return []skillRoot{
		{marker: agents, dir: filepath.Join(agents, "skills"), neutral: true},
		{marker: claude, dir: filepath.Join(claude, "skills"), harness: "claude"},
		{marker: codexHome, dir: filepath.Join(codexHome, "skills"), harness: "codex"},
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
	targets, err := skillTargets()
	if err != nil {
		return result, err
	}
	for _, name := range bundledSkillNames {
		files, err := bundledSkillFiles(name)
		if err != nil {
			return result, err
		}
		for _, root := range targets {
			dir := filepath.Join(root.dir, name)
			written, err := installSkillAt(root.dir, name, files, version)
			if err != nil {
				return result, err
			}
			if written {
				result.installed = append(result.installed, dir)
				continue
			}
			result.present = append(result.present, dir)
		}
	}
	return result, nil
}

// installSkillAt writes the bundled skill name into a skills directory unless
// a skill is already there. The boolean reports whether it wrote.
func installSkillAt(root, name string, files []skillFile, version string) (bool, error) {
	dir := filepath.Join(root, name)
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

// skillRepoRoot resolves the check's repository before syncing its installed
// skill copies. A failed lookup makes the best-effort sync a no-op.
func skillRepoRoot(path string) string {
	root, err := gitx.Toplevel(path)
	if err != nil {
		return ""
	}
	return root
}

// syncBundledSkill updates only copies that already exist in the checked
// repository or the legacy user locations. Missing copies are left for init
// to install, and tracked repository copies stay under the user's control.
// Local edits are backed up before replacement.
func syncBundledSkill(repoRoot, version string) ([]report.SkillUpdate, error) {
	var updates []report.SkillUpdate
	if repoRoot == "" {
		return updates, nil
	}
	roots, err := skillRoots()
	if err != nil {
		return updates, err
	}
	for _, name := range bundledSkillNames {
		files, err := bundledSkillFiles(name)
		if err != nil {
			return updates, err
		}
		dirs := []string{filepath.Join(repoRoot, ".claude", "skills", name), filepath.Join(repoRoot, ".agents", "skills", name)}
		for _, root := range roots {
			dirs = append(dirs, filepath.Join(root.dir, name))
		}
		updates, err = syncSkillDirs(updates, repoRoot, dirs, files, version)
		if err != nil {
			return updates, err
		}
	}
	return updates, nil
}

// syncSkillDirs syncs each existing, untracked copy in dirs to files and
// appends what it wrote to updates.
func syncSkillDirs(updates []report.SkillUpdate, repoRoot string, dirs []string, files []skillFile, version string) ([]report.SkillUpdate, error) {
	for _, dir := range dirs {
		if _, err := os.Stat(filepath.Join(dir, skillFileName)); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return updates, err
		}
		if strings.HasPrefix(dir, repoRoot+string(filepath.Separator)) && gitTracked(repoRoot, dir) {
			continue
		}
		update, wrote, err := syncSkillAt(dir, files, version)
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
func syncSkillAndReport(out, errOut io.Writer, repoRoot, version string) []report.SkillUpdate {
	updates, err := syncBundledSkill(repoRoot, version)
	for _, update := range updates {
		fmt.Fprintln(out, skillNotice(update))
	}
	if err != nil {
		fmt.Fprintf(errOut, "slopcop: could not install the slopcop skills: %v\n", err)
	}
	return updates
}

// skillNotice says what one sync wrote: the versions, the files, and where a
// copy of local edits went, so an agent that loaded the old skill knows to
// reread it.
func skillNotice(update report.SkillUpdate) string {
	name := filepath.Base(update.Dir)
	if update.From == "" {
		return fmt.Sprintf("slopcop: installed the %s skill at %s", name, update.Dir)
	}
	from := update.From
	if from == skillVersionUnknown {
		from = "an unrecognised version"
	}
	text := fmt.Sprintf("slopcop: updated the %s skill at %s from %s to %s (changed: %s). Reread it if you loaded the old one.",
		name, update.Dir, from, update.To, strings.Join(update.Changed, ", "))
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

// bundledSkillFiles is the skill name as compiled into this binary.
func bundledSkillFiles(name string) ([]skillFile, error) {
	var files []skillFile
	err := fs.WalkDir(skills.Files, name, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := skills.Files.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(name, filepath.FromSlash(path))
		if err != nil {
			return err
		}
		files = append(files, skillFile{relative: relative, data: data})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read the bundled %s skill: %w", name, err)
	}
	return files, nil
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// gitTracked reports whether Git tracks a target or any file under a skill directory.
func gitTracked(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	cmd := exec.Command("git", "-C", root, "ls-files", "--cached", "--", rel)
	out, err := cmd.Output()
	return err == nil && len(out) > 0
}

func excludeInitPaths(root string, paths []string) error {
	out, err := exec.Command("git", "-C", root, "rev-parse", "--git-path", "info/exclude").Output()
	if err != nil {
		return err
	}
	path := strings.TrimSpace(string(out))
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	lines := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		lines[line] = true
	}
	var add []string
	for _, p := range paths {
		if !lines[p] {
			add = append(add, p)
			lines[p] = true
		}
	}
	if len(add) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if len(data) > 0 && data[len(data)-1] != '\n' {
		if _, err = f.WriteString("\n"); err != nil {
			return err
		}
	}
	if !lines["# slopcop init"] {
		if _, err = f.WriteString("# slopcop init\n"); err != nil {
			return err
		}
	}
	for _, p := range add {
		if _, err = f.WriteString(p + "\n"); err != nil {
			return err
		}
	}
	return nil
}

func removeUserSkills(out io.Writer) error {
	roots, err := skillRoots()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, root := range roots {
		dir := filepath.Join(root.dir, skillName)
		if seen[dir] {
			continue
		}
		seen[dir] = true
		data, err := os.ReadFile(filepath.Join(dir, skillFileName))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !slopcopSkill(data) {
			continue
		}
		present, err := readSkillDir(dir)
		if err != nil {
			return err
		}
		stamp, stamped := readSkillStamp(dir)
		if _, edited := skillOrigin(present, stamp, stamped); edited {
			backup, err := backupSkill(dir, present)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "slopcop: backed up the edited skill at %s\n", backup)
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		fmt.Fprintf(out, "slopcop: removed the user-level skill at %s\n", dir)
	}
	return nil
}

func slopcopSkill(data []byte) bool {
	lines := strings.Split(string(data), "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return false
	}
	found := false
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			return found
		}
		if strings.TrimSpace(line) == "name: slopcop" || strings.TrimSpace(line) == `name: "slopcop"` || strings.TrimSpace(line) == "name: 'slopcop'" {
			found = true
		}
	}
	return false
}

func newInitCommand(version string) *cobra.Command {
	var noSkill, noHooks, removeHooks, noReviewers, sessionCheck bool
	var root string
	command := &cobra.Command{Use: "init", Short: "Install repository-local skills and hooks", Long: `Install SlopCop's skill and hooks in the current Git repository. The skill
is written to .agents/skills/slopcop and, when Claude Code is installed,
.claude/skills/slopcop. Claude Code gets a Stop hook and a push gate in
.claude/settings.local.json, which Grok also runs in a folder it trusts. Codex
gets a Stop hook and a push gate in .codex/hooks.json, and requires /hooks
trust for both. The push gate blocks git push, gh pr create, and GitHub MCP
tools that push or open a pull request until HEAD's tree passed a check. Written paths are added to the
repository's info/exclude. Tracked targets are left untouched. Grok, which
has no repository hook location SlopCop has verified, gets a user-level Stop
hook in hooks/slopcop.json under GROK_HOME or ~/.grok. Existing user-level
Claude Code and Codex hooks and SlopCop skills are removed, except that
--no-hooks keeps the user-level hooks and --no-skill the user-level skills.
--remove-hooks removes both repository and user-level hooks, Grok's included,
and the session check. Init probes available reviewers unless --no-reviewers
is set.

Init records what it installed, and what --no-skill, --no-hooks or
--remove-hooks turned down, in the repository's Git configuration
(slopcop.init, slopcop.skill, slopcop.hooks), which its worktrees share.
'slopcop doctor' reads it to tell a declined piece from a missing one.
--session-check also installs a user-level SessionStart hook for Claude Code
and Codex that tells the agent, once, when a session opens in a checkout or
worktree that lacks SlopCop's skill or hooks; see 'slopcop doctor --help'.`, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		repo, err := gitx.Toplevel(root)
		if err != nil {
			return fmt.Errorf("slopcop init requires a Git repository: %w", err)
		}
		out := cmd.OutOrStdout()
		skip := func(path string) bool {
			if gitTracked(repo, path) {
				fmt.Fprintf(out, "slopcop: left tracked target %s alone\n", path)
				return true
			}
			return false
		}
		// --no-hooks and --no-skill leave the user-level hooks and skills
		// alone as well; init removes only what it would replace. Grok's
		// hook has no repository location yet, so only --remove-hooks takes
		// it out.
		if !noHooks {
			removeUser := agenthooks.RemoveReplaced
			if removeHooks {
				removeUser = agenthooks.Remove
			}
			user, err := removeUser()
			for _, p := range user.ChangedPaths {
				fmt.Fprintf(out, "slopcop: removed the user-level hook from %s\n", p)
			}
			if err != nil {
				return err
			}
		}
		if removeHooks {
			r, err := agenthooks.Repository(repo, false, skip)
			for _, p := range dedupe(r.ChangedPaths) {
				fmt.Fprintf(out, "slopcop: removed SlopCop's hooks from %s\n", p)
			}
			if err != nil {
				return err
			}
			session, err := agenthooks.RemoveSessionCheck()
			for _, p := range session.ChangedPaths {
				fmt.Fprintf(out, "slopcop: removed the session check from %s\n", p)
			}
			if err != nil {
				return err
			}
			recordChoices(cmd.ErrOrStderr(), repo, version, skillRecorded(repo), false)
			return nil
		}
		if !noSkill {
			if err := removeUserSkills(out); err != nil {
				return err
			}
		}
		var excluded []string
		if !noSkill {
			targets := []string{filepath.Join(repo, ".agents", "skills")}
			home, _ := os.UserHomeDir()
			claude := os.Getenv("CLAUDE_CONFIG_DIR")
			if claude == "" {
				claude = filepath.Join(home, ".claude")
			}
			if isDirectory(claude) {
				targets = append(targets, filepath.Join(repo, ".claude", "skills"))
			}
			for _, name := range bundledSkillNames {
				files, err := bundledSkillFiles(name)
				if err != nil {
					return err
				}
				for _, target := range targets {
					dir := filepath.Join(target, name)
					if skip(dir) {
						continue
					}
					written, err := installSkillAt(target, name, files, version)
					if err != nil {
						return err
					}
					if written {
						fmt.Fprintf(out, "slopcop: installed the %s skill at %s\n", name, dir)
					}
					rel, _ := filepath.Rel(repo, dir)
					excluded = append(excluded, "/"+filepath.ToSlash(rel)+"/")
				}
			}
		}
		if !noHooks {
			r, err := agenthooks.Repository(repo, true, skip)
			if err != nil {
				return err
			}
			// The Stop hook and the push gate share Claude Code's settings
			// file, so each file is named once.
			for _, p := range dedupe(r.ChangedPaths) {
				fmt.Fprintf(out, "slopcop: installed SlopCop's hooks in %s\n", p)
			}
			for _, p := range []string{filepath.Join(repo, ".claude", "settings.local.json"), filepath.Join(repo, ".codex", "hooks.json")} {
				if !gitTracked(repo, p) {
					rel, _ := filepath.Rel(repo, p)
					excluded = append(excluded, "/"+filepath.ToSlash(rel))
				}
			}
			user, err := agenthooks.InstallUserOnly()
			for _, p := range user.ChangedPaths {
				fmt.Fprintf(out, "slopcop: installed the user-level Stop hook in %s\n", p)
			}
			if err != nil {
				return err
			}
			r.Installed = append(r.Installed, user.Installed...)
			if r.NeedsTrust {
				fmt.Fprintln(out, "slopcop: Codex reads .codex/hooks.json only in a project you trust, and runs the Stop hook and push gate only after you trust them with /hooks")
			}
			if problem := agenthooks.CommandProblem(r.Installed); problem != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: warning: %s\n", problem)
			}
			// Name the harnesses whose settings now hold the gate, not one
			// whose tracked settings file was left alone.
			if len(r.Gates) > 0 {
				names := map[string]string{"claude": "Claude Code", "codex": "Codex"}
				var gated []string
				for _, name := range r.Gates {
					gated = append(gated, names[name])
				}
				verb := "blocks"
				if len(gated) > 1 {
					verb = "block"
				}
				fmt.Fprintf(out, "slopcop: %s now %s git push, gh pr create, and GitHub MCP pushes until HEAD's tree passed a check\n", strings.Join(gated, " and "), verb)
			}
		}
		if err := excludeInitPaths(repo, excluded); err != nil {
			return err
		}
		if sessionCheck {
			session, err := agenthooks.InstallSessionCheck()
			for _, p := range session.ChangedPaths {
				fmt.Fprintf(out, "slopcop: installed the session check in %s\n", p)
			}
			if err != nil {
				return err
			}
			if len(session.Installed) == 0 {
				fmt.Fprintln(out, "slopcop: no Claude Code or Codex configuration directory here, so no session check was installed")
			}
			if session.NeedsTrust {
				fmt.Fprintln(out, "slopcop: Codex runs the session check only after you trust it with /hooks")
			}
		}
		recordChoices(cmd.ErrOrStderr(), repo, version, !noSkill, !noHooks)
		if !noReviewers {
			return initReviewers(cmd.Context(), out, repo)
		}
		return nil
	}}
	command.Flags().StringVar(&root, "root", ".", "Git repository directory")
	command.Flags().BoolVar(&noReviewers, "no-reviewers", false, "skip reviewer probing")
	command.Flags().BoolVar(&noSkill, "no-skill", false, "do not install the agent skill")
	command.Flags().BoolVar(&noHooks, "no-hooks", false, "do not install repository hooks")
	command.Flags().BoolVar(&removeHooks, "remove-hooks", false, "remove repository and user-level SlopCop hooks")
	command.Flags().BoolVar(&sessionCheck, "session-check", false, "also install a user-level SessionStart hook that reports checkouts lacking SlopCop's skill or hooks")
	command.MarkFlagsMutuallyExclusive("no-hooks", "remove-hooks")
	command.MarkFlagsMutuallyExclusive("session-check", "remove-hooks")
	return command
}

// recordChoices stores init's choices for doctor. Failing to record them is
// reported and leaves init's work in place.
func recordChoices(errOut io.Writer, repo, version string, skill, hooks bool) {
	if err := recordInitChoices(repo, version, skill, hooks); err != nil {
		fmt.Fprintf(errOut, "slopcop: warning: could not record init's choices in the Git configuration: %v\n", err)
	}
}

// skillRecorded reports whether the repository's last recorded init kept
// the skill, which --remove-hooks leaves as it was.
func skillRecorded(repo string) bool {
	value, err := gitx.Run(repo, "config", "--local", "--get", gitKeySkill)
	return err != nil || !gitFalse(value)
}

func initReviewers(ctx context.Context, out io.Writer, repo string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Mode == modeStatic {
		return nil
	}
	path, err := config.Path()
	if err != nil {
		return err
	}
	if cfg.Review != nil && len(cfg.Review.Reviewers) > 0 {
		fmt.Fprintf(out, "slopcop: reviewers are %s (from %s); to pick again, run `slopcop config unset review.reviewers` and then `slopcop init`\n", strings.Join(cfg.Review.Reviewers, ", "), path)
		return nil
	}
	settings, err := reviewSettings(cfg, 0)
	if err != nil {
		return err
	}
	gitDir, err := exec.Command("git", "-C", repo, "rev-parse", "--absolute-git-dir").Output()
	if err != nil {
		return err
	}
	probeParent := filepath.Join(strings.TrimSpace(string(gitDir)), "slopcop")
	if err := os.MkdirAll(probeParent, 0700); err != nil {
		return err
	}
	logDir, err := os.MkdirTemp(probeParent, "probe-"+time.Now().UTC().Format("20060102T150405")+"-")
	if err != nil {
		return err
	}
	review.TrimLogs(probeParent)
	fmt.Fprintln(out, "slopcop: checking which reviewers work here; this sends each one a short prompt and can take a few minutes the first time, while npx downloads each adapter")
	results := review.Probe(ctx, repo, logDir, review.Families, settings, func(r review.ProbeResult) {
		if r.Available {
			fmt.Fprintf(out, "slopcop: %s available (%s)\n", r.Family, r.Model)
		} else {
			fmt.Fprintf(out, "slopcop: %s unavailable: %s\n", r.Family, r.Reason)
		}
	})
	// The first two that work review; the rest that work stand by as
	// fallbacks for a reviewer that fails.
	var names, fallbacks []string
	for _, r := range results {
		if !r.Available {
			continue
		}
		if len(names) < review.DefaultReviewerCount {
			names = append(names, string(r.Family))
		} else {
			fallbacks = append(fallbacks, string(r.Family))
		}
	}
	if len(names) == 0 {
		fmt.Fprintln(out, "slopcop: no reviewers available; log in to Codex or Claude Code, set DEEPSEEK_API_KEY or configure the dsh adapter with --api-key-file, then rerun init; or set review.reviewers by hand, or use mode static")
		return nil
	}
	if cfg.Review == nil {
		cfg.Review = &config.Review{}
	}
	cfg.Review.Reviewers = names
	cfg.Review.Fallbacks = fallbacks
	if cfg.Review.MinCompleted > len(names) {
		fmt.Fprintf(out, "slopcop: review.minCompleted was %d, more than the %d reviewers found; unset it, so the default applies\n", cfg.Review.MinCompleted, len(names))
		cfg.Review.MinCompleted = 0
	}
	path, err = config.Save(cfg)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "slopcop: reviewers set to %s in %s\n", strings.Join(names, ", "), path)
	if len(fallbacks) > 0 {
		fmt.Fprintf(out, "slopcop: fallbacks set to %s\n", strings.Join(fallbacks, ", "))
	}
	return nil
}
