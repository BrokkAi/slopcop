package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/attribution"
	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/gitx"
	agenthooks "github.com/BrokkAi/slopcop/internal/hooks"
	"github.com/BrokkAi/slopcop/internal/report"
	"github.com/BrokkAi/slopcop/internal/service"
)

const (
	maxHookInputBytes  = 1 << 20
	maxHookOutputRunes = 30000
	// maxHolds is how many stops in a row the hook holds the agent back
	// at: the first, and once more after the agent changed the tree.
	maxHolds = 2
)

// rolloutSettle is how long the Codex hook waits for a rollout that shows no
// edits yet before it reads it again.
var rolloutSettle = 300 * time.Millisecond

// hookHarnesses maps the harness `slopcop hook` takes to the harness its
// attribution names.
var hookHarnesses = map[string]string{
	"claude": attribution.ClaudeHarness,
	"codex":  attribution.CodexHarness,
	"grok":   attribution.GrokHarness,
	"kimi":   attribution.KimiHarness,
}

// repositoryProviders names the provider the service lists a repository
// under, by the host of its canonical URL. The service checks no other host.
var repositoryProviders = map[string]string{
	"github.com":    "github",
	"gitlab.com":    "gitlab",
	"bitbucket.org": "bitbucket",
}

type hookInput struct {
	// Harness is the attribution harness the hook ran under.
	Harness        string `json:"-"`
	Event          string `json:"hook_event_name"`
	CWD            string `json:"cwd"`
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	StopHookActive bool   `json:"stop_hook_active"`
	// StopHookActiveCamel is stop_hook_active as Grok names it.
	StopHookActiveCamel bool `json:"stopHookActive"`
}

type hookOutput struct {
	Decision      string `json:"decision,omitempty"`
	Reason        string `json:"reason,omitempty"`
	SystemMessage string `json:"systemMessage,omitempty"`
}

func newHookCommand(serviceURL *string, version string) *cobra.Command {
	return &cobra.Command{
		Use:    "hook <claude|codex|grok|kimi>",
		Short:  "Run a check from a Claude Code, Codex, Grok or Kimi Code Stop hook",
		Hidden: true,
		// A harness reads exit 2 from a Stop hook as an order to keep going,
		// so the hook takes any arguments and always exits 0 with its
		// answer on stdout.
		DisableFlagParsing: true,
		Args:               cobra.ArbitraryArgs,
		Run: func(cmd *cobra.Command, args []string) {
			var output hookOutput
			if len(args) == 1 && hookHarnesses[args[0]] != "" {
				output = runHook(cmd, hookHarnesses[args[0]], *serviceURL, version)
			}
			if len(args) == 1 && hookHarnesses[args[0]] == attribution.KimiHarness {
				_ = json.NewEncoder(cmd.OutOrStdout()).Encode(kimiAnswer(output))
				return
			}
			_ = json.NewEncoder(cmd.OutOrStdout()).Encode(output)
		},
	}
}

// runHook answers one Stop hook. Whatever goes wrong, a panic included,
// becomes a message for the user, never a failure that keeps the agent going.
func runHook(cmd *cobra.Command, harness, serviceURL, version string) (output hookOutput) {
	defer func() {
		if recovered := recover(); recovered != nil {
			output = hookOutput{SystemMessage: fmt.Sprintf("slopcop: the automatic check failed: %v", recovered)}
		}
	}()
	var input hookInput
	if err := json.NewDecoder(io.LimitReader(cmd.InOrStdin(), maxHookInputBytes)).Decode(&input); err != nil {
		return hookOutput{SystemMessage: "slopcop: could not read the Stop hook's payload, so no check ran."}
	}
	if input.Event != "Stop" || strings.TrimSpace(input.CWD) == "" || input.SessionID == "" {
		return hookOutput{}
	}
	// Grok also runs the hooks in ~/.claude/settings.json, so SlopCop's
	// Claude Code hook fires there beside its Grok hook. Grok sets
	// GROK_HOOK_EVENT for every hook it runs.
	if harness == attribution.ClaudeHarness && os.Getenv("GROK_HOOK_EVENT") != "" {
		return hookOutput{}
	}
	input.Harness = harness
	input.StopHookActive = input.StopHookActive || input.StopHookActiveCamel
	return stopHook(cmd.Context(), input, serviceURL, version)
}

// stopHook checks the change when this session wrote files in the
// repository, or the working tree has a changed dependency manifest or
// lockfile, and hands the agent the new findings in those files. It sends
// nothing when no one is signed in, when the session wrote nothing here,
// when the workspace does not check the repository, or when it already has
// the result for this tree; and it says nothing unless there is something to
// act on, but for naming, once, a service other than the hosted one that a
// check went to. The cheapest of those tests come first.
func stopHook(ctx context.Context, input hookInput, serviceURL, version string) hookOutput {
	repoRoot, err := gitx.Toplevel(input.CWD)
	if err != nil {
		return hookOutput{}
	}
	_ = cleanupInstalledLegacyRepository(repoRoot)
	cfg, err := config.Load()
	if err != nil || resolveToken(cfg, resolveServiceURL(cfg, serviceURL)) == "" {
		return hookOutput{}
	}
	path := hookStatePath(input.Harness, input.SessionID, repoRoot)
	state, loaded := loadHookState(path)
	state.inWorkspace(resolveWorkspaceName(""))
	if state.Off {
		return hookOutput{}
	}
	attributed := attribution.DetectHook(input.Harness, repoRoot, input.SessionID, input.TranscriptPath)
	written := writtenFiles(attributed)
	if len(written) == 0 && input.Harness == attribution.CodexHarness {
		// Codex writes its rollout as it goes and does not flush it before
		// its Stop hooks, so the turn's last edits can land a moment late.
		time.Sleep(rolloutSettle)
		attributed = attribution.DetectHook(input.Harness, repoRoot, input.SessionID, input.TranscriptPath)
		written = writtenFiles(attributed)
	}
	for path := range changedDependencyFiles(repoRoot) {
		written[path] = true
	}
	if len(written) == 0 {
		return hookOutput{}
	}
	opts := &checkOptions{root: repoRoot, timeout: agenthooks.CheckTimeout}
	prepared, err := prepareCheck(opts, cfg, serviceURL, version, failOnFinding)
	if err != nil {
		// No origin, no commits: a check by hand says why.
		return hookOutput{}
	}
	fresh := prepared.fingerprint != state.Fingerprint
	omittedScope := omittedScopeDigest(prepared.omittedFiles)
	scopeChanged := omittedScope != state.OmittedScope
	if prepared.onlyOmitted() {
		if !fresh && !scopeChanged {
			return hookOutput{}
		}
		state.Fingerprint, state.Status = prepared.fingerprint, "skipped"
		state.OmittedScope = omittedScope
		state.Findings = nil
		saveHookState(path, state)
		return hookOutput{SystemMessage: boundedHookText(hookOmittedFilesNotice(prepared.omittedFiles) + "\nslopcop: " + omittedOnlyOutcome(prepared).skipped.Message)}
	}
	if fresh {
		prepared.request.Attribution = attributed
		if message, ok := sendHookCheck(ctx, prepared, &state); !ok {
			if notice := hookOmittedFilesNotice(prepared.omittedFiles); notice != "" {
				message = boundedHookText(notice + "\n" + message)
			}
			output := hookOutput{SystemMessage: message}
			if message != "" {
				output = state.namingService(prepared.client.BaseURL, output)
			}
			output.SystemMessage = boundedHookText(output.SystemMessage)
			saveHookState(path, state)
			return output
		}
	}
	output := state.answer(written, fresh, input.StopHookActive, loaded)
	if fresh {
		output = state.namingService(prepared.client.BaseURL, output)
	}
	if fresh || scopeChanged {
		if notice := hookOmittedFilesNotice(prepared.omittedFiles); notice != "" {
			output.SystemMessage = boundedHookText(notice + "\n" + output.SystemMessage)
		}
	}
	state.OmittedScope = omittedScope
	saveHookState(path, state)
	return output
}

func hookOmittedFilesNotice(files []report.OmittedFile) string {
	notice := omittedFilesNotice(files)
	if notice == "" {
		return ""
	}
	var out strings.Builder
	out.WriteString(notice)
	runes := utf8.RuneCountInString(notice)
	for i, file := range files {
		line := fmt.Sprintf("\n  %q (%d bytes): %s", file.Path, file.Bytes, file.Reason)
		lineRunes := utf8.RuneCountInString(line)
		// Leave room for other hook messages and make truncation explicit.
		if runes+lineRunes > maxHookOutputRunes/2-100 {
			fmt.Fprintf(&out, "\n  … %d additional omitted file(s); run slopcop check --format json for the complete list.", len(files)-i)
			break
		}
		out.WriteString(line)
		runes += lineRunes
	}
	return out.String()
}

// writtenFiles is the set of repository-relative files a session's edits
// wrote.
func writtenFiles(attributed *service.Attribution) map[string]bool {
	written := map[string]bool{}
	if attributed == nil {
		return written
	}
	for _, model := range attributed.Models {
		for _, file := range model.Files {
			written[file] = true
		}
	}
	return written
}

// sendHookCheck sends the check, unless the workspace does not check the
// repository, and records its result in state. When no result comes back it
// returns what to tell the user, if anything.
func sendHookCheck(ctx context.Context, prepared preparedCheck, state *hookState) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, prepared.client.Timeout())
	defer cancel()
	// The hook runs in the workspace a check by hand would. A workspace
	// the account does not have will not appear later in the session; a
	// failure to ask the service can pass.
	if err := useWorkspace(ctx, prepared.client, ""); err != nil {
		var serviceError *service.Error
		var transport *url.Error
		if !errors.As(err, &serviceError) && !errors.As(err, &transport) && ctx.Err() == nil {
			state.Off = true
		}
		return hookFailure(err, state), false
	}
	if !state.Active {
		active, err := repositoryActive(ctx, prepared.client, prepared.request.RepoURL)
		if err != nil {
			return hookFailure(err, state), false
		}
		if !active {
			state.Off = true
			return "", false
		}
		state.Active = true
	}
	response, err := sendCheck(ctx, prepared.client, &prepared.request, nil)
	if err != nil {
		return hookFailure(err, state), false
	}
	if !response.KnownStatus() {
		return fmt.Sprintf("slopcop: the service answered status %q, which this version of slopcop does not know. Update slopcop.", response.Status), false
	}
	state.completed(prepared.workspace, prepared.fingerprint, response)
	return "", true
}

// repositoryActive reports whether the workspace checks the repository now,
// by its provider and owner/name. A repository it has not added, or has
// disabled, is not sent at all.
func repositoryActive(ctx context.Context, client *service.Client, repoURL string) (bool, error) {
	parsed, err := url.Parse(repoURL)
	if err != nil {
		return false, nil
	}
	provider := repositoryProviders[parsed.Hostname()]
	if provider == "" {
		return false, nil
	}
	name := repositoryName(repoURL)
	listed, err := client.Repositories(ctx)
	if err != nil {
		return false, err
	}
	for _, repository := range listed.Repositories {
		if repository.Active && strings.EqualFold(repository.Provider, provider) && strings.EqualFold(repository.FullName, name) {
			return true, nil
		}
	}
	return false, nil
}

// hookFailure is what to tell the user about a check that got no result. A
// refusal that sending again will not change, such as a login the service no
// longer accepts, turns the hook off for the rest of the session, so it is
// said once. A refusal is final when the service names it and does not call
// it retryable: a gateway's answer with no code, as in a brief outage, is
// not.
func hookFailure(err error, state *hookState) string {
	var pending *service.Pending
	if errors.As(err, &pending) {
		return "slopcop: the automatic check is still running; the next stop picks up its result."
	}
	var serviceError *service.Error
	if errors.As(err, &serviceError) {
		if serviceError.Body.Code != "" && !serviceError.Body.Retryable {
			state.Off = true
		}
		return boundedHookText("slopcop: the automatic check did not run.\n" + remedyText(serviceError))
	}
	return boundedHookText("slopcop: the automatic check did not run: " + err.Error())
}

// answer turns the last completed check into the hook's answer. Only new
// findings in files this session wrote hold the agent back, once for a given
// tree and at most maxHolds stops in a row; then the agent is let go and the
// user is told what remains. Claude Code's stop_hook_active says some hook
// held the agent at the stop before, not that this one did, so the hook
// counts its own holds; without its state it cannot, and holds only at a
// stop that follows none.
func (s *hookState) answer(written map[string]bool, fresh, stopHookActive, loaded bool) hookOutput {
	var mine []service.Finding
	for _, finding := range s.Findings {
		if written[finding.Path] {
			mine = append(mine, finding)
		}
	}
	others := len(s.Findings) - len(mine)
	if !stopHookActive {
		s.Holds = 0
	}
	canHold := !stopHookActive || (loaded && s.Holds < maxHolds)
	switch {
	case len(mine) > 0 && !s.Handed && canHold:
		s.Handed = true
		s.Holds++
		return hookOutput{
			Decision: "block",
			Reason: boundedHookText("SlopCop found new findings in files you wrote this session. " +
				"Fix them before you stop; SlopCop checks the change again when you do.\n" + report.Findings(mine)),
		}
	case len(mine) > 0 && stopHookActive:
		s.Handed = true
		return hookOutput{SystemMessage: boundedHookText(fmt.Sprintf(
			"slopcop: %d new finding(s) remain in files this session wrote.\n%s", len(mine), report.Findings(mine)))}
	case others > 0 && others != s.Others:
		s.Others = others
		return hookOutput{SystemMessage: fmt.Sprintf(
			"slopcop: this branch has %d new finding(s) in files this session did not write. Run 'slopcop check' to see them.", others)}
	case fresh && s.Status == service.StatusIncomplete:
		return hookOutput{SystemMessage: "slopcop: the automatic check did not complete. Run 'slopcop check' for details."}
	}
	return hookOutput{}
}

// kimiHookAnswer is a Stop hook's answer as Kimi Code reads it: a block is a
// hookSpecificOutput whose permissionDecision is deny, with the reason handed
// to the agent, and anything else to say is message.
type kimiHookAnswer struct {
	Message            string              `json:"message,omitempty"`
	HookSpecificOutput *kimiSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

type kimiSpecificOutput struct {
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason"`
}

func kimiAnswer(output hookOutput) kimiHookAnswer {
	if output.Decision == "block" {
		return kimiHookAnswer{HookSpecificOutput: &kimiSpecificOutput{"deny", output.Reason}}
	}
	return kimiHookAnswer{Message: output.SystemMessage}
}

func boundedHookText(text string) string {
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) <= maxHookOutputRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxHookOutputRunes-1]) + "…"
}
