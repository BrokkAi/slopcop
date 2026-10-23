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
)

// silentExit ends the process with a specific exit code and no extra message;
// the report or service failure has already been printed.
type silentExit struct{ code int }

func (e silentExit) Error() string { return fmt.Sprintf("exit %d", e.code) }

// usageError is a command used wrongly, so the failure also points at the
// help.
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }

func (e usageError) Unwrap() error { return e.err }

// Execute runs the CLI and returns the process exit code: 0 clean, 1 a new
// finding at or above the fail-on level, 2 incomplete or failed, 3 still
// running when --timeout ran out. A report exits 0 once the finding is
// dismissed and 2 otherwise.
func Execute(args []string, version string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return execute(ctx, args, version, os.Stdout, os.Stderr)
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
		newInitCommand(),
		newLoginCommand(&serviceURL, version),
		newLogoutCommand(),
		newReportCommand(&serviceURL, version),
		newVersionCommand(version),
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
}

func registerCheckFlags(command *cobra.Command, opts *checkOptions) {
	flags := command.Flags()
	flags.StringVar(&opts.root, "root", ".", "repository path to check")
	flags.StringVar(&opts.base, "base", "", "base commit or revision (default: merge-base with the upstream branch)")
	flags.StringVar(&opts.format, "format", "human", "report format: human, json, or sarif")
	flags.StringVar(&opts.failOn, "fail-on", "finding", "exit 1 when a new finding is at or above this level: never, finding, note, warning, or error")
	flags.BoolVar(&opts.showExisting, "show-existing", false, "also list the findings already in the code and those reported as false positives (they never fail the check)")
	flags.StringArrayVar(&opts.policies, "policy", nil, "repository-relative .rqlp policy file to evaluate (repeatable)")
	flags.BoolVar(&opts.noDiscovery, "no-policy-discovery", false, "do not include .slopcop/policies rules automatically")
	flags.BoolVar(&opts.verbose, "verbose", false, "print the resolved request to stderr")
	flags.DurationVar(&opts.timeout, "timeout", 10*time.Minute, "how long to wait for the result; a check still running then exits 3")
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
	var usage usageError
	if errors.As(err, &usage) {
		return true
	}
	message := err.Error()
	return strings.Contains(message, "unknown flag") ||
		strings.Contains(message, "unknown shorthand flag") ||
		strings.Contains(message, "unknown command") ||
		strings.Contains(message, "unknown agent") ||
		strings.Contains(message, "invalid argument")
}
