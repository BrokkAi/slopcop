package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
)

type hookInput struct {
	Event          string `json:"hook_event_name"`
	CWD            string `json:"cwd"`
	SessionID      string `json:"session_id"`
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
// nothing when the session wrote nothing here, when no one is signed in,
// when the workspace does not check the repository, or when it already has
// the result for this tree; and it says nothing unless there is something to
// act on.
func stopHook(ctx context.Context, input hookInput, serviceURL, version string) hookOutput {
	repoRoot, err := gitx.Toplevel(input.CWD)
	if err != nil {
		return hookOutput{}
	}
	attributed := attribution.DetectClaudeHook(repoRoot, input.SessionID)
	written := writtenFiles(attributed)
	if len(written) == 0 {
		return hookOutput{}
	}
	if cfg, err := config.Load(); err != nil || resolveToken(cfg) == "" {
		return hookOutput{}
	}
	path := hookStatePath(input.SessionID, repoRoot)
	state := loadHookState(path)
	if state.Off {
		return hookOutput{}
	}
	opts := &checkOptions{root: repoRoot, timeout: agenthooks.CheckTimeout}
	prepared, err := prepareCheck(opts, serviceURL, version, failOnFinding)
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
	output := state.answer(written, fresh, input.StopHookActive)
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
	// The hook runs in the workspace a check by hand would, and a
	// workspace that cannot be found now will not be found later.
	if err := useWorkspace(ctx, prepared.client, ""); err != nil {
		state.Off = true
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
	response, err := sendCheck(ctx, prepared.client, prepared.request, nil)
	if err != nil {
		return hookFailure(err, state), false
	}
	if !response.KnownStatus() {
		return fmt.Sprintf("slopcop: the service answered status %q, which this version of slopcop does not know. Update slopcop.", response.Status), false
	}
	state.completed(prepared.fingerprint, response)
	return "", true
}

// repositoryActive reports whether the login's workspace checks the
// repository now. A repository it has not added, or has disabled, is not
// sent at all.
func repositoryActive(ctx context.Context, client *service.Client, repoURL string) (bool, error) {
	name := repositoryName(repoURL)
	listed, err := client.Repositories(ctx)
	if err != nil {
		return false, err
	}
	for _, repository := range listed.Repositories {
		if repository.Active && strings.EqualFold(repository.FullName, name) {
			return true, nil
		}
	}
	return false, nil
}

// hookFailure is what to tell the user about a check that got no result. A
// refusal that sending again will not change, such as a login the service no
// longer accepts, turns the hook off for the rest of the session, so it is
// said once.
func hookFailure(err error, state *hookState) string {
	var pending *service.Pending
	if errors.As(err, &pending) {
		return "slopcop: the automatic check is still running; the next stop picks up its result."
	}
	var serviceError *service.Error
	if errors.As(err, &serviceError) {
		if !serviceError.Body.Retryable {
			state.Off = true
		}
		return boundedHookText("slopcop: the automatic check did not run.\n" + remedyText(serviceError))
	}
	return boundedHookText("slopcop: the automatic check did not run: " + err.Error())
}

// answer turns the last completed check into the hook's answer. Only new
// findings in files this session wrote hold the agent back, and only once
// for a given tree. An agent already held back once is let go, and the user
// is told what remains.
func (s *hookState) answer(written map[string]bool, fresh, stopHookActive bool) hookOutput {
	var mine []service.Finding
	for _, finding := range s.Findings {
		if written[finding.Path] {
			mine = append(mine, finding)
		}
	}
	others := len(s.Findings) - len(mine)
	switch {
	case len(mine) > 0 && stopHookActive:
		s.Handed = true
		return hookOutput{SystemMessage: boundedHookText(fmt.Sprintf(
			"slopcop: %d new finding(s) remain in files this session wrote.\n%s", len(mine), report.Findings(mine)))}
	case len(mine) > 0 && !s.Handed:
		s.Handed = true
		return hookOutput{
			Decision: "block",
			Reason: boundedHookText("SlopCop found new findings in files you wrote this session. " +
				"Fix them before you stop; SlopCop checks the change again when you do.\n" + report.Findings(mine)),
		}
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
