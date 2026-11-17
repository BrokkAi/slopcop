// Package update keeps slopcop current: when the service reports a newer
// release than the one running, a binary slopcop manages replaces itself, and
// any other is told, at most once a day, how to upgrade.
package update

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DisableEnv turns the notice off when set to any non-empty value.
	DisableEnv = "SLOPCOP_NO_UPDATE_NOTICE"
	// InstallGuide is where to upgrade from when the install channel is
	// unknown.
	InstallGuide = "https://github.com/BrokkAi/slopcop#install"
	interval     = 24 * time.Hour
)

// Notifier prints one upgrade notice per process, and none when the stamp
// says one was printed in the last day.
type Notifier struct {
	out     io.Writer
	current version
	stamp   string
	now     func() time.Time
	upgrade string
	// apply, when set, replaces this binary with a release; Notify then
	// leaves the newer release to Finish instead of printing the notice.
	apply func(ctx context.Context, tag string) error

	mu      sync.Mutex
	done    bool
	pending *version
}

// New returns the notifier for this build, or nil when there is never a
// notice: a dev or otherwise unparseable build, or $SLOPCOP_NO_UPDATE_NOTICE
// set. stamp is the file that records the last notice; "" keeps no record.
func New(out io.Writer, current, stamp string) *Notifier {
	if os.Getenv(DisableEnv) != "" {
		return nil
	}
	parsed, ok := parse(current)
	if !ok {
		return nil
	}
	executable, _ := os.Executable()
	// The file a symlink names is the one that would be replaced, so it is
	// the one judged.
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	notifier := &Notifier{
		out:     out,
		current: parsed,
		stamp:   stamp,
		now:     time.Now,
		upgrade: upgradeCommand(executable),
	}
	// Only a release build is replaced: a build ahead of a release, such as
	// a developer's own, stays the one they built.
	if executable != "" && isRelease(current) && SelfManaged(executable) && autoUpdates() {
		notifier.apply = func(ctx context.Context, tag string) error { return Apply(ctx, tag, executable) }
	}
	return notifier
}

// Notify prints the notice when latest is newer than this build and none was
// printed in the last day. A stamp that cannot be read or written never stops
// the notice or the command.
func (n *Notifier) Notify(latest string) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.done {
		return
	}
	newer, ok := parse(latest)
	if !ok || !newer.after(n.current) {
		return
	}
	n.done = true
	now := n.now()
	if last, ok := n.lastNotice(); ok && now.Sub(last) < interval && !last.After(now) {
		return
	}
	// A pre-release is announced but never installed by itself. The update
	// is recorded once it has been tried, so one that was interrupted is
	// tried again on the next run.
	if n.apply != nil && isRelease(latest) {
		n.pending = &newer
		return
	}
	n.record(now)
	fmt.Fprintf(n.out, "slopcop %s is available (you have %s). Upgrade: %s\n", newer, n.current, n.upgrade)
}

// Finish, called once the command's own work is done, replaces this binary
// with the newer release Notify heard of, and says so; the next run uses it.
// An update that fails is tried again a day later, and never fails the
// command: it prints the notice instead, with why.
func (n *Notifier) Finish(ctx context.Context) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.pending == nil {
		return
	}
	newer := *n.pending
	n.pending = nil
	// Said first, so a caller waiting on the process knows what it waits for.
	fmt.Fprintf(n.out, "slopcop: updating from %s to %s...\n", n.current, newer)
	err := n.apply(ctx, newer.String())
	if ctx.Err() != nil {
		// Interrupted: nothing was replaced, and the next run tries again.
		return
	}
	n.record(n.now())
	if err != nil {
		fmt.Fprintf(n.out, "slopcop %s is available (you have %s), but updating to it failed: %v. Upgrade: %s\n", newer, n.current, err, n.upgrade)
		return
	}
	fmt.Fprintf(n.out, "slopcop: updated from %s to %s; the next run uses it.\n", n.current, newer)
}

func (n *Notifier) lastNotice() (time.Time, bool) {
	if n.stamp == "" {
		return time.Time{}, false
	}
	data, err := os.ReadFile(n.stamp)
	if err != nil {
		return time.Time{}, false
	}
	last, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data)))
	return last, err == nil
}

func (n *Notifier) record(now time.Time) {
	if n.stamp == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(n.stamp), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(n.stamp, []byte(now.UTC().Format(time.RFC3339)+"\n"), 0o600)
}

// upgradeCommand names the command for the channel the binary was installed
// through, judged by where it lives, or the install guide when it cannot
// tell.
func upgradeCommand(executable string) string {
	// Backslashes are Windows separators; a Unix path almost never has one.
	path := strings.ToLower(strings.ReplaceAll(executable, `\`, "/"))
	switch {
	case strings.Contains(path, "/_npx/") && strings.Contains(path, "/node_modules/@brokkai/slopcop/"):
		return "npx @brokkai/slopcop@latest"
	case strings.Contains(path, "/node_modules/@brokkai/slopcop/"):
		return "npm install --global @brokkai/slopcop@latest"
	case strings.Contains(path, "/brokk_slopcop/_bin/") && strings.Contains(path, "/pipx/"):
		return "pipx upgrade brokk-slopcop"
	case strings.Contains(path, "/brokk_slopcop/_bin/") && strings.Contains(path, "/uv/tools/"):
		return "uv tool upgrade brokk-slopcop"
	case strings.Contains(path, "/brokk_slopcop/_bin/"):
		return "pip install --upgrade brokk-slopcop"
	default:
		return InstallGuide
	}
}

// version is a release's major.minor.patch. A pre-release or build suffix is
// dropped, so a build ahead of a release is never told to upgrade to it.
type version [3]int

func parse(raw string) (version, bool) {
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "v")
	if cut := strings.IndexAny(raw, "-+"); cut >= 0 {
		raw = raw[:cut]
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return version{}, false
	}
	var parsed version
	for i, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return version{}, false
		}
		parsed[i] = number
	}
	return parsed, true
}

// isRelease reports whether raw is exactly a release version, with no
// pre-release or build suffix.
func isRelease(raw string) bool {
	parsed, ok := parse(raw)
	return ok && parsed.String() == "v"+strings.TrimPrefix(strings.TrimSpace(raw), "v")
}

func (v version) after(other version) bool {
	for i := range v {
		if v[i] != other[i] {
			return v[i] > other[i]
		}
	}
	return false
}

func (v version) String() string {
	return fmt.Sprintf("v%d.%d.%d", v[0], v[1], v[2])
}
