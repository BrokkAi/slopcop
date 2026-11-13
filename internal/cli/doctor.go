package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/gitx"
	agenthooks "github.com/BrokkAi/slopcop/internal/hooks"
)

// The setup states doctor reports for a checkout.
const (
	setupNotGit   = "not_git"
	setupReady    = "ready"
	setupNotSetUp = "not_set_up"
	setupPartial  = "incomplete"
)

// Git configuration keys init records in the repository, so a later doctor,
// in this checkout or in a fresh worktree of it, can tell a missing piece
// from one the user turned down. slopcop.sessionCheck false, in the
// repository or globally, silences the session-start reminder.
const (
	gitKeyInit         = "slopcop.init"
	gitKeySkill        = "slopcop.skill"
	gitKeyHooks        = "slopcop.hooks"
	gitKeySessionCheck = "slopcop.sessioncheck"
)

// doctorGitTimeout bounds each Git command doctor runs, so a session-start
// hook never waits on a stuck repository.
const doctorGitTimeout = 5 * time.Second

var harnessTitles = map[string]string{"claude": "Claude Code", "codex": "Codex"}

// setupReport is what doctor found in one checkout. It is built from local
// files and Git configuration only: no service call, reviewer probe or write.
type setupReport struct {
	State string `json:"state"`
	Root  string `json:"root,omitempty"`
	// Worktree says the checkout is a linked worktree, which shares the
	// repository's Git configuration but not its untracked hook files.
	Worktree bool `json:"worktree"`
	// Initialized says slopcop init has run in this repository, in this
	// checkout or another worktree of it.
	Initialized bool `json:"initialized"`
	// SkillDeclined and HooksDeclined say init last ran with --no-skill, or
	// with --no-hooks or --remove-hooks, so their absence is not a problem.
	SkillDeclined bool           `json:"skillDeclined"`
	HooksDeclined bool           `json:"hooksDeclined"`
	Harnesses     []setupHarness `json:"harnesses"`
	Missing       []string       `json:"missing"`
	Next          string         `json:"next,omitempty"`
	Notes         []string       `json:"notes,omitempty"`
	SessionCheck  setupReminder  `json:"sessionCheck"`
	seen          map[string]bool
}

// setupHarness is one harness's view of the checkout.
type setupHarness struct {
	Name     string `json:"name"`
	Skill    bool   `json:"skill"`
	StopHook bool   `json:"stopHook"`
	PushGate bool   `json:"pushGate"`
}

// setupReminder says whether the session-start reminder is installed and
// whether this repository silenced it.
type setupReminder struct {
	Installed bool `json:"installed"`
	Silenced  bool `json:"silenced"`
}

func newDoctorCommand() *cobra.Command {
	var root, format, hook string
	command := &cobra.Command{
		Use:   "doctor",
		Short: "Show whether this checkout has SlopCop's skill and hooks",
		Long: `Report whether the agents installed here would load SlopCop's skill and run
its Stop hook and push gate in this checkout, and the init command that adds
what is missing. Doctor reads local files and Git configuration only: it sends
nothing, probes no reviewer and changes nothing.

A piece that init was told to skip, with --no-skill, --no-hooks or
--remove-hooks, is not reported as missing. Codex runs repository hooks only
in a project you trust and after you trust the hooks with /hooks, which
doctor cannot see.

'slopcop init --session-check' installs a user-level SessionStart hook for
Claude Code and Codex that runs 'slopcop doctor --hook <harness>'. It tells
the agent once per checkout, and again only if what is missing changes, when
a session opens in a checkout that is not set up. Silence it in a repository
with 'git config slopcop.sessionCheck false', or everywhere with --global.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if hook != "" {
				runSessionCheck(cmd, hook)
				return nil
			}
			if err := checkFormat(format); err != nil {
				return err
			}
			report := diagnoseSetup(cmd.Context(), root, nil)
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), report)
			}
			writeSetupReport(cmd.OutOrStdout(), report)
			return nil
		},
	}
	command.Flags().StringVar(&root, "root", ".", "repository path")
	command.Flags().StringVar(&hook, "hook", "", "answer a SessionStart hook for claude or codex")
	_ = command.Flags().MarkHidden("hook")
	addFormatFlag(command, &format)
	return command
}

// diagnoseSetup inspects the checkout at dir for the harnesses named in only,
// or for every harness configured here when only is empty.
func diagnoseSetup(ctx context.Context, dir string, only []string) setupReport {
	report := setupReport{Missing: []string{}, Harnesses: []setupHarness{}}
	root, err := gitx.Toplevel(dir)
	if err != nil {
		report.State = setupNotGit
		report.Notes = append(report.Notes, "this is not a Git repository; slopcop init needs one")
		return report
	}
	report.Root = root
	settings := slopcopGitConfig(ctx, root)
	report.Initialized = settings[gitKeyInit] != ""
	report.SkillDeclined = gitFalse(settings[gitKeySkill])
	report.HooksDeclined = gitFalse(settings[gitKeyHooks])
	report.SessionCheck.Silenced = gitFalse(settings[gitKeySessionCheck])
	report.Worktree = linkedWorktree(ctx, root)

	found := agenthooks.Find(root)
	names := only
	if len(names) == 0 {
		for _, name := range []string{"claude", "codex"} {
			if found[name].Configured {
				names = append(names, name)
			}
		}
	}
	for _, p := range found {
		report.SessionCheck.Installed = report.SessionCheck.Installed || p.SessionCheck
	}
	anyPresent := false
	for _, name := range names {
		p := found[name]
		h := setupHarness{Name: name, Skill: skillAvailable(root, name), StopHook: p.Stop, PushGate: p.Gate}
		report.Harnesses = append(report.Harnesses, h)
		anyPresent = anyPresent || h.Skill || h.StopHook || h.PushGate
		title := harnessTitles[name]
		if !report.SkillDeclined && !h.Skill {
			report.Missing = append(report.Missing, fmt.Sprintf("the slopcop skill where %s reads it (%s)", title, skillPathFor(name)))
		}
		if !report.HooksDeclined && !h.StopHook {
			report.Missing = append(report.Missing, title+"'s Stop hook")
		}
		if !report.HooksDeclined && !h.PushGate {
			report.Missing = append(report.Missing, title+"'s push gate")
		}
	}
	if len(names) == 0 && !report.SkillDeclined {
		// No harness is configured here, so only the vendor-neutral skill
		// that other agents read can be set up.
		if skillAvailable(root, "") {
			anyPresent = true
		} else {
			report.Missing = append(report.Missing, "the slopcop skill ("+skillPathFor("")+")")
		}
	}
	switch {
	case len(report.Missing) == 0:
		report.State = setupReady
	case !report.Initialized && !anyPresent:
		report.State = setupNotSetUp
	default:
		report.State = setupPartial
	}
	if report.State != setupReady {
		report.Next = initCommand(report)
		if note := reviewerProbeNote(); note != "" {
			report.Notes = append(report.Notes, note)
		}
	}
	for _, h := range report.Harnesses {
		if h.Name == "codex" && (h.StopHook || h.PushGate || report.State != setupReady && !report.HooksDeclined) {
			report.Notes = append(report.Notes, "Codex runs repository hooks only in a project you trust, and only after you trust them with /hooks")
		}
	}
	return report
}

// initCommand is the init that adds what is missing and keeps what the user
// turned down turned down.
func initCommand(report setupReport) string {
	command := "slopcop init"
	if report.SkillDeclined {
		command += " --no-skill"
	}
	if report.HooksDeclined {
		command += " --no-hooks"
	}
	return command
}

// reviewerProbeNote warns that init will probe reviewers, which sends each a
// short prompt, when none are configured yet. It is empty when init would not.
func reviewerProbeNote() string {
	cfg, err := config.Load()
	if err != nil || cfg.Mode == modeStatic || cfg.Review != nil && len(cfg.Review.Reviewers) > 0 {
		return ""
	}
	return "init also probes which reviewers work, which sends each a short prompt; add --no-reviewers to skip that"
}

// skillAvailable reports whether the named harness would find a slopcop
// skill for this checkout, in the repository or at user level. An empty
// name stands for the agents that read the vendor-neutral directory.
func skillAvailable(root, name string) bool {
	var dirs []string
	roots, _ := skillRoots()
	user := map[string]string{}
	for _, r := range roots {
		user[r.harness] = r.dir
	}
	switch name {
	case "claude":
		dirs = append(dirs, filepath.Join(root, ".claude", "skills"), user["claude"])
	case "codex":
		dirs = append(dirs, filepath.Join(root, ".agents", "skills"), user[""], user["codex"])
	default:
		dirs = append(dirs, filepath.Join(root, ".agents", "skills"), user[""])
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, skillName, skillFileName)); err == nil {
			return true
		}
	}
	return false
}

func skillPathFor(name string) string {
	if name == "claude" {
		return ".claude/skills/slopcop"
	}
	return ".agents/skills/slopcop"
}

// slopcopGitConfig reads the slopcop.* keys Git resolves for root, from the
// repository and the user's global configuration. Git lowercases the names.
func slopcopGitConfig(ctx context.Context, root string) map[string]string {
	ctx, cancel := context.WithTimeout(ctx, doctorGitTimeout)
	defer cancel()
	values := map[string]string{}
	out, err := gitx.RunContext(ctx, root, nil, "config", "--get-regexp", `^slopcop\.`)
	if err != nil {
		return values
	}
	for _, line := range strings.Split(out, "\n") {
		key, value, _ := strings.Cut(strings.TrimSpace(line), " ")
		if key != "" {
			values[strings.ToLower(key)] = value
		}
	}
	return values
}

// gitFalse reads a Git boolean the way git config --type=bool does, and
// reports whether it is set and false.
func gitFalse(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "false", "no", "off", "0":
		return true
	}
	return false
}

// linkedWorktree reports whether root is a worktree added with git worktree
// add, whose Git directory differs from the repository's common one.
func linkedWorktree(ctx context.Context, root string) bool {
	ctx, cancel := context.WithTimeout(ctx, doctorGitTimeout)
	defer cancel()
	out, err := gitx.RunContext(ctx, root, nil, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	if err != nil {
		return false
	}
	lines := strings.Split(out, "\n")
	return len(lines) == 2 && filepath.Clean(strings.TrimSpace(lines[0])) != filepath.Clean(strings.TrimSpace(lines[1]))
}

// recordInitChoices stores what init installed in the repository's Git
// configuration, which every worktree of it shares. A failure only means a
// later doctor cannot tell a declined piece from a missing one.
func recordInitChoices(repo, version string, skill, hooks bool) error {
	for key, value := range map[string]string{
		gitKeyInit:  version,
		gitKeySkill: fmt.Sprint(skill),
		gitKeyHooks: fmt.Sprint(hooks),
	} {
		if _, err := gitx.Run(repo, "config", "--local", key, value); err != nil {
			return err
		}
	}
	return nil
}

func writeSetupReport(out io.Writer, report setupReport) {
	if report.State == setupNotGit {
		fmt.Fprintln(out, "Setup:      not a Git repository; slopcop init needs one")
		return
	}
	root := report.Root
	if report.Worktree {
		root += " (linked worktree)"
	}
	fmt.Fprintf(out, "Checkout:   %s\n", root)
	fmt.Fprintf(out, "Setup:      %s\n", setupStateText(report.State))
	for _, h := range report.Harnesses {
		fmt.Fprintf(out, "%-12s skill %s, Stop hook %s, push gate %s\n", harnessTitles[h.Name]+":",
			presentText(h.Skill, report.SkillDeclined), presentText(h.StopHook, report.HooksDeclined), presentText(h.PushGate, report.HooksDeclined))
	}
	for _, item := range report.Missing {
		fmt.Fprintf(out, "Missing:    %s\n", item)
	}
	if report.Next != "" {
		fmt.Fprintf(out, "Next:       run `%s` in this checkout\n", report.Next)
	}
	reminder := "off (enable with `slopcop init --session-check`)"
	if report.SessionCheck.Installed {
		reminder = "on"
		if report.SessionCheck.Silenced {
			reminder = "on, silenced here by slopcop.sessionCheck"
		}
	}
	fmt.Fprintf(out, "Reminder:   %s\n", reminder)
	for _, note := range report.Notes {
		fmt.Fprintf(out, "Note:       %s\n", note)
	}
}

func setupStateText(state string) string {
	switch state {
	case setupReady:
		return "ready"
	case setupNotSetUp:
		return "not set up"
	default:
		return "incomplete"
	}
}

func presentText(present, declined bool) string {
	switch {
	case present:
		return "present"
	case declined:
		return "skipped by choice"
	default:
		return "missing"
	}
}

// sessionStartInput is the part of a SessionStart payload the check reads.
// Claude Code and Codex send the same names.
type sessionStartInput struct {
	Event string `json:"hook_event_name"`
	CWD   string `json:"cwd"`
}

type sessionStartOutput struct {
	SystemMessage      string                    `json:"systemMessage,omitempty"`
	HookSpecificOutput *sessionStartSpecificPart `json:"hookSpecificOutput,omitempty"`
}

type sessionStartSpecificPart struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

// runSessionCheck answers a SessionStart hook. It prints nothing unless the
// checkout is a Git repository that is not ready, the repository has not
// silenced the reminder, and this checkout has not already been told about
// the same missing pieces. It never fails: the harness starts the session
// whatever it prints, and a crash becomes a message for the user.
func runSessionCheck(cmd *cobra.Command, harness string) {
	defer func() {
		// A crash is reported to the user, not the agent, and never stops
		// the session from starting.
		if recovered := recover(); recovered != nil {
			_ = json.NewEncoder(cmd.OutOrStdout()).Encode(sessionStartOutput{
				SystemMessage: fmt.Sprintf("slopcop: the setup check failed: %v", recovered),
			})
		}
	}()
	if harnessTitles[harness] == "" {
		return
	}
	// Grok also runs the hooks in ~/.claude/settings.json; its SessionStart
	// output is not verified, so the check stays quiet there.
	if harness == "claude" && os.Getenv("GROK_HOOK_EVENT") != "" {
		return
	}
	var input sessionStartInput
	if err := json.NewDecoder(io.LimitReader(cmd.InOrStdin(), maxHookInputBytes)).Decode(&input); err != nil {
		return
	}
	if input.Event != "SessionStart" || strings.TrimSpace(input.CWD) == "" {
		return
	}
	report := diagnoseSetup(cmd.Context(), input.CWD, []string{harness})
	if report.State == setupNotGit || report.State == setupReady || report.SessionCheck.Silenced {
		return
	}
	if !firstReminder(report) {
		return
	}
	_ = json.NewEncoder(cmd.OutOrStdout()).Encode(sessionStartOutput{
		SystemMessage: fmt.Sprintf("slopcop: this checkout lacks %s; run `%s` to set it up", strings.Join(report.Missing, ", "), report.Next),
		HookSpecificOutput: &sessionStartSpecificPart{
			HookEventName:     "SessionStart",
			AdditionalContext: sessionReminder(report),
		},
	})
}

// sessionReminder is the context the agent receives: what is missing, the
// command that adds it, and how to stop the reminder.
func sessionReminder(report setupReport) string {
	var b strings.Builder
	switch {
	case report.State == setupNotSetUp:
		fmt.Fprintf(&b, "SlopCop is not set up in this checkout (%s).", report.Root)
	case report.Worktree && report.Initialized:
		fmt.Fprintf(&b, "This worktree (%s) belongs to a repository set up for SlopCop, but the worktree lacks SlopCop's local files.", report.Root)
	default:
		fmt.Fprintf(&b, "SlopCop's setup in this checkout (%s) is incomplete.", report.Root)
	}
	fmt.Fprintf(&b, " Missing: %s.", strings.Join(report.Missing, "; "))
	fmt.Fprintf(&b, " `%s` in the checkout adds them; it edits this checkout's agent settings, so ask the user before running it.", report.Next)
	for _, note := range report.Notes {
		fmt.Fprintf(&b, " Note: %s.", note)
	}
	b.WriteString(" Hooks added now take effect in the next session. To stop this reminder for the repository, run `git config slopcop.sessionCheck false`. It is shown once per checkout until what is missing changes.")
	return b.String()
}

// firstReminder records that this checkout was told about this state, and
// reports whether it had not been told before. With no cache directory to
// record it in, every session is told.
func firstReminder(report setupReport) bool {
	cache, err := os.UserCacheDir()
	if err != nil {
		return true
	}
	identity := report.Root + "\x00" + report.State + "\x00" + strings.Join(report.Missing, "\x00")
	digest := sha256.Sum256([]byte(identity))
	path := filepath.Join(cache, "slopcop", "session-check", hex.EncodeToString(digest[:]))
	if _, err := os.Stat(path); err == nil {
		return false
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
		_ = os.WriteFile(path, nil, 0o600)
	}
	return true
}
