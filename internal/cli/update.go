package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
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

// updateAvailable is the exit code of `slopcop update --check` when a newer
// release is out: distinct from 0 (up to date) and 2 (the check failed).
const updateAvailable = 1

func newUpdateCommand(version string) *cobra.Command {
	var check bool
	command := &cobra.Command{
		Use:   "update",
		Short: "Update slopcop to the latest release",
		Long: `Update slopcop to the latest release.

A slopcop installed by the install script replaces its own binary, after
checking the download against the release's checksums. It also does this by
itself after a command when the service reports a newer release, unless
$` + update.AutoDisableEnv + ` or $CI is set. A slopcop installed through npm, pipx, uv
or pip is upgraded by that tool; this command says whether it is behind and
names the command to run.

With --check it only reports and never installs: it exits 0 when up to date,
1 when a newer release is available, and 2 when the latest release could not
be read or this is not a release build.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			executable, err := update.Executable()
			if err != nil {
				return err
			}
			return runUpdate(cmd.Context(), cmd.OutOrStdout(), version, check, updateSource{
				executable: executable,
				latest:     update.Latest,
				apply:      update.Apply,
			})
		},
	}
	command.Flags().BoolVar(&check, "check", false, "only report whether a newer release is available: exit 0 when up to date, 1 when one is, 2 on failure or a non-release build; never install")
	return command
}

// updateSource is what runUpdate reads and changes, so a test can stand in
// for the running binary and the release server.
type updateSource struct {
	executable string
	latest     func(ctx context.Context) (string, error)
	apply      func(ctx context.Context, tag, executable string) error
}

func runUpdate(ctx context.Context, out io.Writer, version string, check bool, source updateSource) error {
	if !update.IsRelease(version) {
		if check {
			// Not up to date and not behind: a script must not read it as either.
			return fmt.Errorf("%s is not a release build, so it cannot be compared with the latest release", version)
		}
		fmt.Fprintf(out, "slopcop: %s is not a release build, so it is left as it is\n", version)
		return nil
	}
	selfManaged := update.SelfManaged(source.executable)
	upgrade := "slopcop update"
	if !selfManaged {
		upgrade = update.UpgradeCommand(source.executable)
	}
	ctx, cancel := context.WithTimeout(ctx, updateWait)
	defer cancel()
	latest, err := source.latest(ctx)
	if err != nil {
		if !selfManaged && !check {
			return fmt.Errorf("%w. Upgrade: %s", err, upgrade)
		}
		return err
	}
	if !update.Newer(latest, version) {
		fmt.Fprintf(out, "slopcop: %s is up to date (latest release %s)\n", version, latest)
		return nil
	}
	if check || !selfManaged {
		fmt.Fprintf(out, "slopcop: %s → %s available. Upgrade: %s\n", version, latest, upgrade)
		if check {
			return silentExit{updateAvailable}
		}
		return nil
	}
	if err := source.apply(ctx, latest, source.executable); err != nil {
		return errors.New("updating to " + latest + " failed: " + err.Error())
	}
	fmt.Fprintf(out, "slopcop: updated from %s to %s\n", version, latest)
	return nil
}
