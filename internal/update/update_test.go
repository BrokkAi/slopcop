package update

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newNotifier(t *testing.T, out *bytes.Buffer, current, stamp string, now time.Time) *Notifier {
	t.Helper()
	t.Setenv(DisableEnv, "")
	n := New(out, current, stamp)
	if n == nil {
		t.Fatalf("New(%q) = nil", current)
	}
	n.now = func() time.Time { return now }
	n.upgrade = "npm install --global @brokkai/slopcop@latest"
	return n
}

func TestNotifyOnlyForANewerRelease(t *testing.T) {
	for _, test := range []struct {
		latest string
		want   string
	}{
		{"v0.0.9", "slopcop v0.0.9 is available (you have v0.0.7). Upgrade: npm install --global @brokkai/slopcop@latest\n"},
		{"0.1.0", "slopcop v0.1.0 is available (you have v0.0.7). Upgrade: npm install --global @brokkai/slopcop@latest\n"},
		{"v0.0.7", ""},
		{"v0.0.6", ""},
		{"latest", ""},
		{"v1.2", ""},
		{"v0.0.x", ""},
	} {
		var out bytes.Buffer
		newNotifier(t, &out, "v0.0.7", "", time.Now()).Notify(test.latest)
		if out.String() != test.want {
			t.Errorf("Notify(%q) printed %q, want %q", test.latest, out.String(), test.want)
		}
	}
}

func TestNoNoticeForADevBuildOrWhenTurnedOff(t *testing.T) {
	t.Setenv(DisableEnv, "")
	for _, current := range []string{"dev", "test", ""} {
		if New(&bytes.Buffer{}, current, "") != nil {
			t.Errorf("New(%q) notifies", current)
		}
	}
	t.Setenv(DisableEnv, "1")
	if New(&bytes.Buffer{}, "v0.0.7", "") != nil {
		t.Errorf("New notifies with %s set", DisableEnv)
	}
	// A nil notifier is a no-op.
	var n *Notifier
	n.Notify("v9.9.9")
}

func TestABuildAheadOfAReleaseIsNotTold(t *testing.T) {
	var out bytes.Buffer
	newNotifier(t, &out, "v0.0.7-3-gabcdef0", "", time.Now()).Notify("v0.0.7")
	if out.Len() != 0 {
		t.Errorf("printed %q", out.String())
	}
}

func TestNotifyAtMostOnceADay(t *testing.T) {
	stamp := filepath.Join(t.TempDir(), "slopcop", "update-notice")
	start := time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)

	var out bytes.Buffer
	first := newNotifier(t, &out, "v0.0.7", stamp, start)
	first.Notify("v0.0.9")
	first.Notify("v0.0.9")
	if strings.Count(out.String(), "is available") != 1 {
		t.Fatalf("first run printed %q, want one notice", out.String())
	}

	out.Reset()
	newNotifier(t, &out, "v0.0.7", stamp, start.Add(23*time.Hour)).Notify("v0.0.9")
	if out.Len() != 0 {
		t.Fatalf("a run within the day printed %q", out.String())
	}

	out.Reset()
	newNotifier(t, &out, "v0.0.7", stamp, start.Add(25*time.Hour)).Notify("v0.0.9")
	if !strings.Contains(out.String(), "is available") {
		t.Fatalf("a run a day later printed %q", out.String())
	}
}

func TestAStampThatCannotBeWrittenStillNotifies(t *testing.T) {
	// A file where the stamp's directory should be makes every write fail.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := filepath.Join(blocker, "update-notice")
	var out bytes.Buffer
	newNotifier(t, &out, "v0.0.7", stamp, time.Now()).Notify("v0.0.9")
	if !strings.Contains(out.String(), "is available") {
		t.Fatalf("printed %q", out.String())
	}
}

func TestAnUnreadableStampStillNotifies(t *testing.T) {
	stamp := filepath.Join(t.TempDir(), "update-notice")
	if err := os.WriteFile(stamp, []byte("not a time\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	newNotifier(t, &out, "v0.0.7", stamp, time.Now()).Notify("v0.0.9")
	if !strings.Contains(out.String(), "is available") {
		t.Fatalf("printed %q", out.String())
	}
}

func TestUpgradeCommandFollowsTheInstallChannel(t *testing.T) {
	for _, test := range []struct{ executable, want string }{
		{"/usr/local/lib/node_modules/@brokkai/slopcop/native/darwin_arm64/slopcop", "npm install --global @brokkai/slopcop@latest"},
		{`C:\Users\a\AppData\Roaming\npm\node_modules\@brokkai\slopcop\native\windows_amd64\slopcop.exe`, "npm install --global @brokkai/slopcop@latest"},
		{"/home/a/.npm/_npx/0123/node_modules/@brokkai/slopcop/native/linux_amd64/slopcop", "npx @brokkai/slopcop@latest"},
		{"/home/a/.local/share/pipx/venvs/brokk-slopcop/lib/python3.12/site-packages/brokk_slopcop/_bin/slopcop", "pipx upgrade brokk-slopcop"},
		{"/home/a/.local/share/uv/tools/brokk-slopcop/lib/python3.12/site-packages/brokk_slopcop/_bin/slopcop", "uv tool upgrade brokk-slopcop"},
		{"/home/a/.venv/lib/python3.12/site-packages/brokk_slopcop/_bin/slopcop", "pip install --upgrade brokk-slopcop"},
		{"/home/a/.local/bin/slopcop", InstallGuide},
		{"", InstallGuide},
	} {
		if got := upgradeCommand(test.executable); got != test.want {
			t.Errorf("upgradeCommand(%q) = %q, want %q", test.executable, got, test.want)
		}
	}
}
