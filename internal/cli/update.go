package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/service"
	"github.com/BrokkAi/slopcop/internal/update"
)

// updateWait bounds the download of a newer release after a command.
const updateWait = 2 * time.Minute

// updaters collects the notifiers of one command run, so execute can let
// each update the binary once the command's own work is done.
type updaters struct {
	mu   sync.Mutex
	list []*update.Notifier
}

type updatersKey struct{}

func withUpdaters(ctx context.Context) (context.Context, *updaters) {
	collected := &updaters{}
	return context.WithValue(ctx, updatersKey{}, collected), collected
}

// finish updates the binary when a notifier heard of a newer release. An
// interrupted command updates nothing.
func (u *updaters) finish(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, updateWait)
	defer cancel()
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, notifier := range u.list {
		notifier.Finish(ctx)
	}
}

// noticeUpdates has client act when the service reports a release newer than
// this one: a binary slopcop manages replaces itself once the command is
// done, and any other says on stderr, at most once a day, how to upgrade.
// Stdout stays the report.
func noticeUpdates(cmd *cobra.Command, client *service.Client, version string) {
	// Without a stamp path the notice still prints, once per process.
	stamp, _ := config.NoticePath()
	notifier := update.New(cmd.ErrOrStderr(), version, stamp)
	client.LatestVersion = notifier.Notify
	if notifier == nil || cmd.Context() == nil {
		return
	}
	if collected, ok := cmd.Context().Value(updatersKey{}).(*updaters); ok {
		collected.mu.Lock()
		collected.list = append(collected.list, notifier)
		collected.mu.Unlock()
	}
}

func newUpdateCommand(version string) *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Update slopcop to the latest release",
		Long: `Update slopcop to the latest release.

A slopcop installed by the install script replaces its own binary, after
checking the download against the release's checksums. It also does this by
itself after a command when the service reports a newer release, unless
$` + update.AutoDisableEnv + ` or $CI is set.

A slopcop installed through npm, pipx, uv or pip is upgraded by running that
tool, with its output shown as it goes: npm install --global, pipx upgrade,
uv tool upgrade, or the owning Python's -m pip install --upgrade. When the
tool fails, the command exits non-zero and names the command to run by hand,
for example with sudo. An npx run always fetches the latest release, so it
has nothing to update.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUpdate(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), version)
		},
	}
}

func runUpdate(ctx context.Context, out, errOut io.Writer, version string) error {
	executable, err := update.Executable()
	if err != nil {
		return err
	}
	if !update.IsRelease(version) {
		fmt.Fprintf(out, "slopcop: %s is not a release build, so it is left as it is\n", version)
		return nil
	}
	channel := update.ChannelOf(executable)
	if channel == update.NPX {
		fmt.Fprintln(out, "slopcop: npx @brokkai/slopcop@latest always runs the latest release, so there is nothing to update")
		return nil
	}
	lookup, cancel := context.WithTimeout(ctx, updateWait)
	defer cancel()
	latest, err := update.Latest(lookup)
	if err != nil {
		return err
	}
	if !update.Newer(latest, version) {
		fmt.Fprintf(out, "slopcop: %s is up to date (latest release %s)\n", version, latest)
		return nil
	}
	if channel == update.NPMProject {
		// The project pins this version; changing its package.json is the
		// project's call.
		fmt.Fprintf(out, "slopcop: %s is available (you have %s). This slopcop is a project's npm dependency; upgrade it there: npm install @brokkai/slopcop@%s\n", latest, version, strings.TrimPrefix(latest, "v"))
		return nil
	}
	if channel != update.Unmanaged {
		// A package manager's install can take minutes, and shows its own
		// progress, so it is not held to the download's bound.
		return updateThroughPackageManager(ctx, out, errOut, executable, version, latest, runProgram)
	}
	if !update.SelfManaged(executable) {
		fmt.Fprintf(out, "slopcop: this slopcop is not one it updates itself. Upgrade: %s\n", update.UpgradeCommand(executable))
		return nil
	}
	if err := update.Apply(lookup, latest, executable); err != nil {
		return errors.New("updating to " + latest + " failed: " + err.Error())
	}
	fmt.Fprintf(out, "slopcop: updated from %s to %s\n", version, latest)
	return nil
}

// programRunner runs argv to completion, writing its output to stdout and
// stderr.
type programRunner func(ctx context.Context, stdout, stderr io.Writer, argv []string) error

func runProgram(ctx context.Context, stdout, stderr io.Writer, argv []string) error {
	// On Windows npm is npm.cmd, which CreateProcess runs through cmd by
	// itself, with Go quoting each argument.
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Stdin = os.Stdin
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

// updateThroughPackageManager upgrades the binary at executable to latest
// with the package manager that installed it, then asks the binary for its
// version to confirm the upgrade reached this install. Any failure names the
// command to run by hand.
func updateThroughPackageManager(ctx context.Context, out, errOut io.Writer, executable, version, latest string, run programRunner) error {
	argv, err := update.PackageUpgrade(executable, latest)
	if err != nil {
		return err
	}
	command := update.CommandLine(argv)
	fmt.Fprintf(out, "slopcop: updating from %s to %s: %s\n", version, latest, command)
	restore, err := update.ReleaseRunning(executable)
	if err != nil {
		return fmt.Errorf("the running slopcop could not be moved out of the package's way (%v). Run it by hand once slopcop has exited: %s", err, command)
	}
	updated := false
	defer func() {
		if !updated {
			restore()
		}
	}()
	if err := run(ctx, out, errOut, argv); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%s failed (%v). Run it by hand, with the permissions it needs: %s", argv[0], err, command)
	}
	var printed strings.Builder
	if err := run(ctx, &printed, io.Discard, []string{executable, "--version"}); err != nil {
		return fmt.Errorf("%s ran, but the updated slopcop at %s did not run (%v). Run it by hand: %s", argv[0], executable, err, command)
	}
	fields := strings.Fields(printed.String())
	installed := ""
	if len(fields) > 0 {
		installed = fields[len(fields)-1]
	}
	if !update.IsRelease(installed) || update.Newer(latest, installed) {
		return fmt.Errorf("%s ran, but the slopcop at %s is %s, not %s; the registry may not have it yet, or the command upgraded another install. Run it by hand: %s", argv[0], executable, strings.TrimSpace(printed.String()), latest, command)
	}
	updated = true
	fmt.Fprintf(out, "slopcop: updated from %s to %s\n", version, installed)
	return nil
}
