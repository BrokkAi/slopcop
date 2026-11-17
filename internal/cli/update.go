package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
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
	if collected, ok := cmd.Context().Value(updatersKey{}).(*updaters); ok && notifier != nil {
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
itself after a check when the service reports a newer release, unless
$` + update.AutoDisableEnv + ` or $CI is set. A slopcop installed through npm, pipx, uv
or pip is upgraded by that tool; this command names the command to run.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUpdate(cmd.Context(), cmd.OutOrStdout(), version)
		},
	}
}

func runUpdate(ctx context.Context, out io.Writer, version string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if !update.SelfManaged(executable) {
		fmt.Fprintf(out, "slopcop: this slopcop is not one it updates itself. Upgrade: %s\n", update.UpgradeCommand(executable))
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, updateWait)
	defer cancel()
	latest, err := update.Latest(ctx)
	if err != nil {
		return err
	}
	// A dev build is behind nothing, so it is told so rather than replaced.
	if !update.Newer(latest, version) {
		fmt.Fprintf(out, "slopcop: %s is up to date (latest release %s)\n", version, latest)
		return nil
	}
	if err := update.Apply(ctx, latest, executable); err != nil {
		return errors.New("updating to " + latest + " failed: " + err.Error())
	}
	fmt.Fprintf(out, "slopcop: updated from %s to %s\n", version, latest)
	return nil
}
