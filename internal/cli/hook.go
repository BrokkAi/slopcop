package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
)

const (
	maxHookInputBytes  = 1 << 20
	maxHookOutputRunes = 30000
)

type hookInput struct {
	Harness        string `json:"-"`
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
		Use:    "hook <claude|codex>",
		Short:  "Run a check from an agent harness hook",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			harness := args[0]
			switch harness {
			case "claude", "codex":
			default:
				return fmt.Errorf("unknown hook harness %q", harness)
			}
			return runHook(cmd, harness, *serviceURL, version)
		},
	}
}

func runHook(cmd *cobra.Command, harness, serviceURL, version string) error {
	var input hookInput
	decoder := json.NewDecoder(io.LimitReader(cmd.InOrStdin(), maxHookInputBytes))
	if err := decoder.Decode(&input); err != nil {
		return writeHookOutput(cmd.OutOrStdout(), hookOutput{SystemMessage: "SlopCop could not read the hook payload; no check ran."})
	}
	if harness == "claude" {
		input.Harness = "claude-code"
	} else {
		input.Harness = harness
	}
	if input.Event != "Stop" || strings.TrimSpace(input.CWD) == "" {
		return writeHookOutput(cmd.OutOrStdout(), hookOutput{SystemMessage: "SlopCop ignored an invalid Stop hook payload; no check ran."})
	}

	opts := &checkOptions{
		root:    input.CWD,
		format:  "human",
		timeout: 10 * time.Minute,
		hook:    &input,
	}
	var stdout, stderr bytes.Buffer
	check := &cobra.Command{}
	check.SetContext(cmd.Context())
	check.SetOut(&stdout)
	check.SetErr(&stderr)
	err := runCheck(check, opts, serviceURL, version)

	code := 0
	var exit silentExit
	if errors.As(err, &exit) {
		code = exit.code
	} else if err != nil {
		code = 2
		stderr.WriteString(err.Error())
	}
	text := boundedHookText(stderr.String(), stdout.String())
	switch {
	case code == 0:
		return writeHookOutput(cmd.OutOrStdout(), hookOutput{})
	case code == 1 && input.StopHookActive:
		return writeHookOutput(cmd.OutOrStdout(), hookOutput{SystemMessage: "SlopCop still reports new findings. It will not request another continuation.\n" + text})
	case code == 1:
		return writeHookOutput(cmd.OutOrStdout(), hookOutput{
			Decision: "block",
			Reason:   "SlopCop found new findings. Fix them in the working tree, then run the same check again.\n" + text,
		})
	default:
		if text == "" {
			text = "The automatic SlopCop check did not complete; the agent may stop."
		}
		return writeHookOutput(cmd.OutOrStdout(), hookOutput{SystemMessage: text})
	}
}

func boundedHookText(values ...string) string {
	var nonempty []string
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			nonempty = append(nonempty, value)
		}
	}
	text := strings.Join(nonempty, "\n")
	if utf8.RuneCountInString(text) <= maxHookOutputRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxHookOutputRunes-1]) + "…"
}

func writeHookOutput(writer io.Writer, output hookOutput) error {
	return json.NewEncoder(writer).Encode(output)
}
