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
	// codex marks the Codex-specific directory. init uses the shared neutral
	// directory for Codex so the same skill is not listed twice.
	codex bool
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
		{marker: claude, dir: filepath.Join(claude, "skills")},
		{marker: codexHome, dir: filepath.Join(codexHome, "skills"), codex: true},
	}, nil
}

// skillTargets are the user-level skills directories init writes. Claude Code
// uses its own directory. Codex, opencode, Cursor, Grok and other agents share
// ~/.agents/skills; init never writes a second Codex copy because Codex lists
// duplicate skills rather than choosing one.
func skillTargets() ([]skillRoot, error) {
	roots, err := skillRoots()
	if err != nil {
		return nil, err
	}
	var targets []skillRoot
	for _, root := range roots {
		switch {
		case root.neutral:
			targets = append(targets, root)
		case root.codex:
			// Codex uses the shared agent directory below.
		case isDirectory(root.marker):
			targets = append(targets, root)
		}
	}
	return targets, nil
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

// syncBundledSkill updates only copies that already exist in user-level
// locations or, while migrating an older checkout, its repository-local
// directories. Missing copies are left for init to install, and tracked
// repository copies stay under the user's control. Local edits are backed up
// before replacement.
func syncBundledSkill(repoRoot, version string) ([]report.SkillUpdate, error) {
	var updates []report.SkillUpdate
	roots, err := skillRoots()
	if err != nil {
		return updates, err
	}
	for _, name := range bundledSkillNames {
		files, err := bundledSkillFiles(name)
		if err != nil {
			return updates, err
		}
		var dirs []string
		if repoRoot != "" {
			dirs = append(dirs, filepath.Join(repoRoot, ".claude", "skills", name), filepath.Join(repoRoot, ".agents", "skills", name))
		}
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
		if repoRoot != "" && strings.HasPrefix(dir, repoRoot+string(filepath.Separator)) && gitTracked(repoRoot, dir) {
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

var legacyExcludeEntries = map[string]bool{
	"# slopcop init":                 true,
	"/.agents/skills/slopcop/":       true,
	"/.agents/skills/slopcop-audit/": true,
	"/.claude/skills/slopcop/":       true,
	"/.claude/skills/slopcop-audit/": true,
	"/.claude/settings.local.json":   true,
	"/.codex/hooks.json":             true,
}

// cleanupLegacyRepository removes the clone-local hooks and skills written by
// releases where init ran once per repository. It only touches SlopCop's own
// handlers and recognized, untracked skill directories; everything else in the
// checkout is left as it was.
func cleanupLegacyRepository(repo string, hooks, skills bool, out io.Writer) error {
	var errs []error
	if hooks {
		result, err := agenthooks.Repository(repo, false, func(path string) bool {
			return gitTracked(repo, path)
		})
		for _, path := range dedupe(result.ChangedPaths) {
			if out != nil {
				fmt.Fprintf(out, "slopcop: removed the legacy repository hook from %s\n", path)
			}
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	if skills {
		if err := removeRepositorySkills(repo, out); err != nil {
			errs = append(errs, err)
		}
	}
	if hooks || skills {
		if err := removeLegacyExcludes(repo); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func removeRepositorySkills(repo string, out io.Writer) error {
	var errs []error
	for _, name := range bundledSkillNames {
		for _, target := range []string{".agents", ".claude"} {
			dir := filepath.Join(repo, target, "skills", name)
			data, err := os.ReadFile(filepath.Join(dir, skillFileName))
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if !skillNamed(data, name) || gitTracked(repo, dir) {
				continue
			}
			present, err := readSkillDir(dir)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			stamp, stamped := readSkillStamp(dir)
			if _, edited := skillOrigin(present, stamp, stamped); edited {
				backup, err := backupSkill(dir, present)
				if err != nil {
					errs = append(errs, err)
					continue
				}
				if out != nil {
					fmt.Fprintf(out, "slopcop: backed up the edited legacy skill at %s\n", backup)
				}
			}
			if err := os.RemoveAll(dir); err != nil {
				errs = append(errs, err)
				continue
			}
			if out != nil {
				fmt.Fprintf(out, "slopcop: removed the legacy repository skill from %s\n", dir)
			}
		}
	}
	return errors.Join(errs...)
}

func removeLegacyExcludes(repo string) error {
	output, err := exec.Command("git", "-C", repo, "rev-parse", "--git-path", "info/exclude").Output()
	if err != nil {
		return err
	}
	path := strings.TrimSpace(string(output))
	if !filepath.IsAbs(path) {
		path = filepath.Join(repo, path)
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	kept := make([]string, 0, len(lines))
	changed := false
	for _, line := range lines {
		if legacyExcludeEntries[line] {
			changed = true
			continue
		}
		kept = append(kept, line)
	}
	if !changed {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.Join(kept, "\n")), info.Mode().Perm())
}

func skillNamed(data []byte, name string) bool {
	lines := strings.Split(string(data), "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return false
	}
	found := false
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			return found
		}
		if strings.TrimSpace(line) == "name: "+name || strings.TrimSpace(line) == `name: "`+name+`"` || strings.TrimSpace(line) == "name: '"+name+"'" {
			found = true
		}
	}
	return false
}

func newInitCommand(version string) *cobra.Command {
	var noSkill, noHooks, removeHooks, noReviewers bool
	var root string
	command := &cobra.Command{Use: "init", Short: "Install user-level skills and hooks", Long: `Install SlopCop's agent skills, Stop hooks and push gates once for the
machine. The skills go into the user-level directories each installed harness
reads. Claude Code gets its Stop hook and push gate in settings.json under
CLAUDE_CONFIG_DIR or ~/.claude. Codex gets both in hooks.json under CODEX_HOME
or ~/.codex, and requires /hooks trust. Grok gets both in
hooks/slopcop.json under GROK_HOME or ~/.grok. The push gate blocks git push,
gh pr create, and GitHub MCP tools that push or open a pull request until
HEAD's tree passed a check. Repositories that have never run SlopCop are left
alone. Init also removes clone-local hooks and skills written by the earlier
per-repository releases when run inside that checkout; other checkouts clean
themselves the next time their old hook runs SlopCop. Init probes available
reviewers unless --no-reviewers is set.`, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		out := cmd.OutOrStdout()
		if removeHooks {
			result, err := agenthooks.Remove()
			for _, path := range result.ChangedPaths {
				fmt.Fprintf(out, "slopcop: removed the user-level hook from %s\n", path)
			}
			if err != nil {
				return err
			}
			if repo := legacyRepoRoot(root); repo != "" {
				if err := cleanupLegacyRepository(repo, true, false, out); err != nil {
					return err
				}
			}
			return nil
		}

		if noSkill {
			fmt.Fprintln(out, "slopcop: left the agent skills alone (--no-skill)")
		} else {
			result, err := installBundledSkill(version)
			if err != nil {
				return err
			}
			for _, dir := range result.installed {
				fmt.Fprintf(out, "slopcop: installed the skill at %s\n", dir)
			}
			for _, dir := range result.present {
				fmt.Fprintf(out, "slopcop: the skill is already at %s\n", dir)
			}
		}

		if noHooks {
			fmt.Fprintln(out, "slopcop: left the automatic hooks alone (--no-hooks)")
		} else {
			result, err := agenthooks.Install()
			for _, path := range result.ChangedPaths {
				fmt.Fprintf(out, "slopcop: installed the user-level hooks in %s\n", path)
			}
			if err != nil {
				return err
			}
			if result.NeedsTrust {
				fmt.Fprintln(out, "slopcop: Codex runs the user-level Stop hook and push gate only after you trust them with /hooks")
			}
			if problem := agenthooks.CommandProblem(result.Installed); problem != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: warning: %s\n", problem)
			}
			if len(result.Gates) > 0 {
				names := map[string]string{"claude": "Claude Code", "codex": "Codex", "grok": "Grok"}
				var gated []string
				for _, name := range result.Gates {
					gated = append(gated, names[name])
				}
				fmt.Fprintf(out, "slopcop: %s now install a user-level push gate for git push, gh pr create, and GitHub MCP pushes\n", strings.Join(gated, ", "))
			}
			if repo := legacyRepoRoot(root); repo != "" {
				if err := cleanupLegacyRepository(repo, true, !noSkill, out); err != nil {
					return err
				}
			}
		}
		if !noReviewers {
			return initReviewers(cmd.Context(), out, root)
		}
		return nil
	}}
	command.Flags().StringVar(&root, "root", "", "deprecated repository path to clean up")
	_ = command.Flags().MarkHidden("root")
	command.Flags().BoolVar(&noReviewers, "no-reviewers", false, "skip reviewer probing")
	command.Flags().BoolVar(&noSkill, "no-skill", false, "do not install the agent skills")
	command.Flags().BoolVar(&noHooks, "no-hooks", false, "do not install the user-level hooks")
	command.Flags().BoolVar(&removeHooks, "remove-hooks", false, "remove user-level and legacy repository SlopCop hooks")
	command.MarkFlagsMutuallyExclusive("no-hooks", "remove-hooks")
	return command
}

func legacyRepoRoot(path string) string {
	if strings.TrimSpace(path) == "" {
		path = "."
	}
	repo, err := gitx.Toplevel(path)
	if err != nil {
		return ""
	}
	return repo
}

// cleanupInstalledLegacyRepository removes clone-local installs only after the
// user-level hooks that replace them are present. An upgraded binary by itself
// must not strip a checkout's working setup.
func cleanupInstalledLegacyRepository(repo string) error {
	if repo == "" || !agenthooks.GateInstalled(repo) {
		return nil
	}
	return cleanupLegacyRepository(repo, true, true, nil)
}

func initReviewers(ctx context.Context, out io.Writer, workDir string) error {
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
	if strings.TrimSpace(workDir) == "" {
		workDir = "."
	}
	probeRoot, err := filepath.Abs(workDir)
	if err != nil {
		return err
	}
	if info, statErr := os.Stat(probeRoot); statErr != nil || !info.IsDir() {
		probeRoot, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	probeParent := filepath.Join(filepath.Dir(path), "probes")
	if err := os.MkdirAll(probeParent, 0700); err != nil {
		return err
	}
	logDir, err := os.MkdirTemp(probeParent, "probe-"+time.Now().UTC().Format("20060102T150405")+"-")
	if err != nil {
		return err
	}
	review.TrimLogs(probeParent)
	fmt.Fprintln(out, "slopcop: checking which reviewers work here; this sends each one a short prompt and can take a few minutes the first time, while npx downloads each adapter")
	results := review.Probe(ctx, probeRoot, logDir, review.Families, settings, func(r review.ProbeResult) {
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
