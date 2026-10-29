// Package cli implements the slopcop command surface.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/service"
)

// silentExit ends the process with a specific exit code and no extra message;
// the report or service failure has already been printed.
type silentExit struct{ code int }

func (e silentExit) Error() string { return fmt.Sprintf("exit %d", e.code) }

// Execute runs the CLI and returns the process exit code: 0 clean, 1 a new
// finding at or above the fail-on level, 2 incomplete or failed, 3 still
// running when --timeout ran out, 130 interrupted before the check answered.
func Execute(args []string, version string) int {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		select {
		case received := <-signals:
			// A second signal gets the default behavior and ends the
			// process at once.
			signal.Stop(signals)
			cancel(interrupt{received})
		case <-ctx.Done():
		}
	}()
	return execute(ctx, args, version, os.Stdout, os.Stderr)
}

// interrupt is the cause of a context that a signal canceled.
type interrupt struct{ signal os.Signal }

func (i interrupt) Error() string { return "interrupted by " + i.signal.String() }

// interruptCode is the exit code for a command a signal stopped: the shell's
// 128 plus the signal number, so 130 for Ctrl-C and 143 for SIGTERM.
func interruptCode(ctx context.Context) int {
	var cause interrupt
	if errors.As(context.Cause(ctx), &cause) {
		if number, ok := cause.signal.(syscall.Signal); ok {
			return 128 + int(number)
		}
	}
	return service.ExitInterrupted
}

func execute(ctx context.Context, args []string, version string, stdout, stderr io.Writer) int {
	root := newRoot(version)
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	var exit silentExit
	if errors.As(err, &exit) {
		return exit.code
	}
	fmt.Fprintf(stderr, "slopcop: %v\n", err)
	if isUsageError(err) {
		fmt.Fprintln(stderr, "Run 'slopcop --help' for usage.")
	}
	return 2
}

func newRoot(version string) *cobra.Command {
	opts := &checkOptions{}
	serviceURL := ""
	root := &cobra.Command{
		Use:   "slopcop",
		Short: "Check a change through the hosted SlopCop check API",
		Long: `SlopCop checks a working-tree change through the hosted check API.

Running slopcop with no subcommand runs the check. There is no local
analyzer: the service applies the change to the base commit and returns the
report, so a check never needs a Bifrost install.`,
		Version:       version,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCheck(cmd, opts, serviceURL, version)
		},
	}
	root.SetVersionTemplate("slopcop version {{.Version}}\n")
	root.PersistentFlags().StringVar(&serviceURL, "service", "", "SlopCop check service URL (also $SLOPCOP_SERVICE)")
	registerCheckFlags(root, opts)
	root.AddCommand(
		newCheckCommand(opts, &serviceURL, version),
		newIgnoreCommand(&serviceURL, version),
		newInitCommand(),
		newHookCommand(&serviceURL, version),
		newLoginCommand(&serviceURL, version),
		newLogoutCommand(),
		newVersionCommand(version),
		newWhoamiCommand(&serviceURL, version),
		newWorkspaceCommand(&serviceURL, version),
		newRepoCommand(&serviceURL, version),
	)
	return root
}

func newCheckCommand(opts *checkOptions, serviceURL *string, version string) *cobra.Command {
	command := &cobra.Command{
		Use:   "check",
		Short: "Check the working tree through the hosted check API",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCheck(cmd, opts, *serviceURL, version)
		},
	}
	registerCheckFlags(command, opts)
	return command
}

type checkOptions struct {
	root         string
	base         string
	format       string
	failOn       string
	policies     []string
	noDiscovery  bool
	verbose      bool
	showExisting bool
	timeout      time.Duration
	workspace    string
	hook         *hookInput
}

func registerCheckFlags(command *cobra.Command, opts *checkOptions) {
	flags := command.Flags()
	flags.StringVar(&opts.root, "root", ".", "repository path to check")
	flags.StringVar(&opts.base, "base", "", "base commit or revision (default: merge-base with the upstream branch)")
	flags.StringVar(&opts.format, "format", "human", "report format: human, json, or sarif")
	flags.StringVar(&opts.failOn, "fail-on", "finding", "exit 1 when a new finding is at or above this level: never, finding, note, warning, or error")
	flags.BoolVar(&opts.showExisting, "show-existing", false, "also list the findings that were already in the code (they never fail the check)")
	// The service stopped listing findings outside the changed lines; it
	// counts them in summary.existingFindings, which every format prints.
	// The flag stays, hidden, so scripts that pass it keep working. Cobra's
	// own deprecation notice goes to stdout, where it would break --format
	// json, so runCheck says it on stderr instead.
	_ = flags.MarkHidden("show-existing")
	flags.StringArrayVar(&opts.policies, "policy", nil, "repository-relative .rqlp policy file to evaluate (repeatable)")
	flags.BoolVar(&opts.noDiscovery, "no-policy-discovery", false, "do not include .slopcop/policies rules automatically")
	flags.BoolVar(&opts.verbose, "verbose", false, "print the resolved request to stderr")
	flags.DurationVar(&opts.timeout, "timeout", 10*time.Minute, "how long to wait for the result; a check still running then exits 3")
	addWorkspaceFlag(command, false, &opts.workspace)
}

func newVersionCommand(version string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "slopcop version %s\n", version)
		},
	}
}

func isUsageError(err error) bool {
	message := err.Error()
	return strings.Contains(message, "unknown flag") ||
		strings.Contains(message, "unknown shorthand flag") ||
		strings.Contains(message, "unknown command") ||
		strings.Contains(message, "unknown agent") ||
		strings.Contains(message, "invalid argument")
}
