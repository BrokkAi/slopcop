package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
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

// repositoryProviders names the provider the service lists a repository
// under, by the host of its canonical URL. The service checks no other host.
var repositoryProviders = map[string]string{
	"github.com":    "github",
	"gitlab.com":    "gitlab",
	"bitbucket.org": "bitbucket",
}

type hookInput struct {
	Event          string `json:"hook_event_name"`
	CWD            string `json:"cwd"`
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	StopHookActive bool   `json:"stop_hook_active"`
}

type hookOutput struct {
	Decision      string `json:"decision,omitempty"`
	Reason        string `json:"reason,omitempty"`
	SystemMessage string `json:"systemMessage,omitempty"`
}

func newHookCommand(serviceURL *string, version string) *cobra.Command {
	return &cobra.Command{
		Use:    "hook claude",
		Short:  "Run a check from a Claude Code Stop hook",
		Hidden: true,
		// Claude Code reads exit 2 from a Stop hook as an order to keep
		// going, so the hook takes any arguments and always exits 0 with
		// its answer on stdout.
		DisableFlagParsing: true,
		Args:               cobra.ArbitraryArgs,
		Run: func(cmd *cobra.Command, args []string) {
			var output hookOutput
			if len(args) == 1 && args[0] == "claude" {
				output = runHook(cmd, *serviceURL, version)
			}
			_ = json.NewEncoder(cmd.OutOrStdout()).Encode(output)
		},
	}
}

// runHook answers one Stop hook. Whatever goes wrong, a panic included,
// becomes a message for the user, never a failure that keeps the agent going.
func runHook(cmd *cobra.Command, serviceURL, version string) (output hookOutput) {
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
	return stopHook(cmd.Context(), input, serviceURL, version)
}

// stopHook checks the change when this session wrote files in the
// repository, and hands the agent the new findings in those files. It sends
// nothing when no one is signed in, when the session wrote nothing here,
// when the workspace does not check the repository, or when it already has
// the result for this tree; and it says nothing unless there is something to
// act on. The cheapest of those tests come first.
func stopHook(ctx context.Context, input hookInput, serviceURL, version string) hookOutput {
	repoRoot, err := gitx.Toplevel(input.CWD)
	if err != nil {
		return hookOutput{}
	}
	cfg, err := config.Load()
	if err != nil || resolveToken(cfg) == "" {
		return hookOutput{}
	}
	path := hookStatePath(input.SessionID, repoRoot)
	state, loaded := loadHookState(path)
	state.inWorkspace(resolveWorkspaceName(""))
	if state.Off {
		return hookOutput{}
	}
	attributed := attribution.DetectClaudeHook(repoRoot, input.SessionID, input.TranscriptPath)
	written := writtenFiles(attributed)
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
	if fresh {
		prepared.request.Attribution = attributed
		if message, ok := sendHookCheck(ctx, prepared, &state); !ok {
			saveHookState(path, state)
			return hookOutput{SystemMessage: message}
		}
	}
	output := state.answer(written, fresh, input.StopHookActive, loaded)
	saveHookState(path, state)
	return output
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

func boundedHookText(text string) string {
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) <= maxHookOutputRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxHookOutputRunes-1]) + "…"
}
