package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/gitx"
	agenthooks "github.com/BrokkAi/slopcop/internal/hooks"
)

// The setup states doctor reports for this machine.
const (
	setupReady        = "ready"
	setupNotInstalled = "not_installed"
	setupIncomplete   = "incomplete"
)

// doctorHarnesses are the harnesses init installs into, in report order.
var doctorHarnesses = []string{"claude", "codex", "grok"}

var harnessTitles = map[string]string{"claude": "Claude Code", "codex": "Codex", "grok": "Grok"}

// setupReport is what doctor found. It is built from local files only: no
// service call, reviewer probe or write.
type setupReport struct {
	State     string         `json:"state"`
	Harnesses []setupHarness `json:"harnesses"`
	Missing   []string       `json:"missing"`
	Next      string         `json:"next,omitempty"`
	// Legacy lists per-repository hooks and skills that releases before
	// the user-level install wrote into this checkout.
	Legacy []string `json:"legacy"`
	Notes  []string `json:"notes,omitempty"`
}

// setupHarness is one installed harness's view of SlopCop.
type setupHarness struct {
	Name     string `json:"name"`
	Skill    bool   `json:"skill"`
	StopHook bool   `json:"stopHook"`
	PushGate bool   `json:"pushGate"`
}

func newDoctorCommand() *cobra.Command {
	var root, format string
	command := &cobra.Command{
		Use:   "doctor",
		Short: "Show whether SlopCop's skill and hooks are installed for your agents",
		Long: `Report, for each agent harness installed here (Claude Code, Codex and Grok),
whether it would load SlopCop's skill and run its Stop hook and push gate, and
the init command that adds what is missing. Run inside a checkout, doctor also
lists per-repository hooks and skills left by releases that installed per
repository, which 'slopcop init' there removes.

Doctor reads local files only: it sends nothing, probes no reviewer and
changes nothing, so an agent can run it at the start of a session. Codex runs
user-level hooks only after you trust them with /hooks, which doctor cannot
see.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkFormat(format); err != nil {
				return err
			}
			report := diagnoseSetup(root)
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), report)
			}
			writeSetupReport(cmd.OutOrStdout(), report)
			return nil
		},
	}
	command.Flags().StringVar(&root, "root", ".", "checkout to look in for per-repository leftovers")
	addFormatFlag(command, &format)
	return command
}

// diagnoseSetup inspects the user-level install for every harness configured
// here, and the checkout at dir, when it is one, for legacy files.
func diagnoseSetup(dir string) setupReport {
	report := setupReport{Harnesses: []setupHarness{}, Missing: []string{}, Legacy: []string{}}
	found := agenthooks.Find()
	anyPresent, needsTrust := false, false
	for _, name := range doctorHarnesses {
		p := found[name]
		if !p.Configured {
			continue
		}
		skill, broken := userSkillAvailable(name)
		report.brokenSkill(broken)
		h := setupHarness{Name: name, Skill: skill, StopHook: p.Stop, PushGate: p.Gate}
		report.Harnesses = append(report.Harnesses, h)
		anyPresent = anyPresent || h.Skill || h.StopHook || h.PushGate
		needsTrust = needsTrust || p.NeedsTrust && (h.StopHook || h.PushGate)
		title := harnessTitles[name]
		if !h.Skill {
			report.Missing = append(report.Missing, title+"'s slopcop skill")
		}
		if !h.StopHook {
			report.Missing = append(report.Missing, title+"'s Stop hook")
		}
		if !h.PushGate {
			report.Missing = append(report.Missing, title+"'s push gate")
		}
	}
	if len(report.Harnesses) == 0 {
		// No harness is configured here; only the vendor-neutral skill that
		// other agents read can be installed.
		skill, broken := userSkillAvailable("")
		report.brokenSkill(broken)
		if skill {
			anyPresent = true
		} else {
			report.Missing = append(report.Missing, "the slopcop skill")
		}
	}
	switch {
	case len(report.Missing) == 0:
		report.State = setupReady
	case !anyPresent:
		report.State = setupNotInstalled
	default:
		report.State = setupIncomplete
	}
	if report.State != setupReady {
		report.Next = "slopcop init"
		if note := reviewerProbeNote(); note != "" {
			report.Notes = append(report.Notes, note)
		}
		report.Notes = append(report.Notes, "--no-skill or --no-hooks installs only the other half")
	}
	if needsTrust {
		report.Notes = append(report.Notes, "Codex runs SlopCop's hooks only after you trust them with /hooks")
	}
	if repo, err := gitx.Toplevel(dir); err == nil {
		tracked := func(path string) bool { return gitTracked(repo, path) }
		report.Legacy = append(report.Legacy, agenthooks.LegacyRepositoryHooks(repo, tracked)...)
		report.Legacy = append(report.Legacy, legacyRepositorySkills(repo)...)
		if len(report.Legacy) > 0 {
			report.Notes = append(report.Notes, "`slopcop init` in "+repo+" removes the per-repository leftovers")
		}
	}
	return report
}

// brokenSkill notes each unloadable skill file once, with the step init
// cannot take for it.
func (r *setupReport) brokenSkill(paths []string) {
	for _, path := range paths {
		note := path + " is not a valid slopcop skill, and init keeps an existing file; move its folder aside, then run `slopcop init`"
		if !slices.Contains(r.Notes, note) {
			r.Notes = append(r.Notes, note)
		}
	}
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

// userSkillAvailable reports whether the named harness would find a
// user-level slopcop skill. Claude Code reads its own directory; Codex, Grok
// and the agents an empty name stands for read ~/.agents/skills, and Codex
// also its own home. It also returns the skill files the harness reads that
// lack the skill's frontmatter, which the harness does not load and init,
// keeping an existing file, does not replace.
func userSkillAvailable(name string) (bool, []string) {
	roots, err := skillRoots()
	if err != nil {
		return false, nil
	}
	var broken []string
	for _, root := range roots {
		claudeRoot := !root.neutral && !root.codex
		reads := root.neutral && name != "claude" || claudeRoot && name == "claude" || root.codex && name == "codex"
		if !reads {
			continue
		}
		path := filepath.Join(root.dir, skillName, skillFileName)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if skillNamed(data, skillName) {
			return true, nil
		}
		broken = append(broken, path)
	}
	return false, broken
}

// legacyRepositorySkills lists untracked SlopCop skills in the checkout that
// the per-repository releases wrote, as cleanupLegacyRepository finds them.
func legacyRepositorySkills(repo string) []string {
	var dirs []string
	for _, name := range bundledSkillNames {
		for _, target := range []string{".agents", ".claude"} {
			dir := filepath.Join(repo, target, "skills", name)
			data, err := os.ReadFile(filepath.Join(dir, skillFileName))
			if err != nil || !skillNamed(data, name) || gitTracked(repo, dir) {
				continue
			}
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

func writeSetupReport(out io.Writer, report setupReport) {
	fmt.Fprintf(out, "Setup:       %s\n", setupStateText(report.State))
	for _, h := range report.Harnesses {
		fmt.Fprintf(out, "%-13sskill %s, Stop hook %s, push gate %s\n", harnessTitles[h.Name]+":",
			presentText(h.Skill), presentText(h.StopHook), presentText(h.PushGate))
	}
	for _, item := range report.Missing {
		fmt.Fprintf(out, "Missing:     %s\n", item)
	}
	if report.Next != "" {
		fmt.Fprintf(out, "Next:        run `%s`\n", report.Next)
	}
	for _, path := range report.Legacy {
		fmt.Fprintf(out, "Leftover:    %s\n", path)
	}
	for _, note := range report.Notes {
		fmt.Fprintf(out, "Note:        %s\n", note)
	}
}

func setupStateText(state string) string {
	switch state {
	case setupReady:
		return "ready"
	case setupNotInstalled:
		return "not installed"
	default:
		return "incomplete"
	}
}

func presentText(present bool) string {
	if present {
		return "present"
	}
	return "missing"
}
