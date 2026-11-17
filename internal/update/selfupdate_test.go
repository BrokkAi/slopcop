package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// releaseServer serves release tag with one archive for this platform that
// holds binary, and a checksums.txt that lists sum for it, or the archive's
// own when sum is "".
func releaseServer(t *testing.T, tag string, binary []byte, sum string) {
	t.Helper()
	var archive bytes.Buffer
	extension := "tar.gz"
	if runtime.GOOS == "windows" {
		extension = "zip"
		writer := zip.NewWriter(&archive)
		file, _ := writer.Create("slopcop.exe")
		_, _ = file.Write(binary)
		_ = writer.Close()
	} else {
		zipped := gzip.NewWriter(&archive)
		writer := tar.NewWriter(zipped)
		_ = writer.WriteHeader(&tar.Header{Name: "README.md", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg})
		_, _ = writer.Write([]byte("hi"))
		_ = writer.WriteHeader(&tar.Header{Name: "slopcop", Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg})
		_, _ = writer.Write(binary)
		_ = writer.Close()
		_ = zipped.Close()
	}
	name := fmt.Sprintf("slopcop_%s_%s_%s.%s", tag, runtime.GOOS, runtime.GOARCH, extension)
	if sum == "" {
		hashed := sha256.Sum256(archive.Bytes())
		sum = hex.EncodeToString(hashed[:])
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			http.Redirect(w, r, "/tag/"+tag, http.StatusFound)
		case "/download/" + tag + "/checksums.txt":
			fmt.Fprintf(w, "%s  other.tar.gz\n%s  %s\n", strings.Repeat("0", 64), sum, name)
		case "/download/" + tag + "/" + name:
			_, _ = w.Write(archive.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	previous := releases
	releases = server.URL
	t.Cleanup(func() { releases = previous })
}

func installed(t *testing.T) string {
	t.Helper()
	executable := filepath.Join(t.TempDir(), "slopcop")
	if err := os.WriteFile(executable, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	return executable
}

func TestApplyReplacesTheBinaryWithTheVerifiedRelease(t *testing.T) {
	releaseServer(t, "v0.0.9", []byte("new binary"), "")
	executable := installed(t)
	if err := Apply(context.Background(), "0.0.9", executable); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil || string(data) != "new binary" {
		t.Fatalf("binary = %q, %v", data, err)
	}
	if info, _ := os.Stat(executable); runtime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want the old binary's 0755", info.Mode().Perm())
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(executable), ".slopcop-update-*"))
	if len(left) != 0 {
		t.Errorf("staged files left behind: %v", left)
	}
	if tag, err := Latest(context.Background()); err != nil || tag != "v0.0.9" {
		t.Errorf("Latest = %q, %v", tag, err)
	}
}

func TestApplyLeavesTheBinaryWhenTheReleaseDoesNotVerify(t *testing.T) {
	executable := installed(t)
	t.Run("a checksum mismatch", func(t *testing.T) {
		releaseServer(t, "v0.0.9", []byte("tampered"), strings.Repeat("a", 64))
		if err := Apply(context.Background(), "v0.0.9", executable); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a release that is not there", func(t *testing.T) {
		releaseServer(t, "v0.0.9", []byte("new"), "")
		if err := Apply(context.Background(), "v0.1.0", executable); err == nil {
			t.Error("no error for a missing release")
		}
	})
	t.Run("a version that is not one", func(t *testing.T) {
		if err := Apply(context.Background(), "../../evil", executable); err == nil {
			t.Error("no error for a tag that is not a version")
		}
	})
	if data, _ := os.ReadFile(executable); string(data) != "old" {
		t.Errorf("binary = %q, want it untouched", data)
	}
}

func TestOnlyAnInstallScriptBinaryIsSelfManaged(t *testing.T) {
	for executable, want := range map[string]bool{
		"/home/me/.local/bin/slopcop":                                       true,
		`C:\Users\me\AppData\Local\Programs\slopcop\slopcop.exe`:            true,
		"/usr/lib/node_modules/@brokkai/slopcop/vendor/slopcop":             false,
		"/home/me/.local/share/pipx/venvs/x/lib/brokk_slopcop/_bin/slopcop": false,
		"/home/me/.local/share/uv/tools/x/lib/brokk_slopcop/_bin/slopcop":   false,
		"/tmp/go-build1/b001/cli.test":                                      false,
	} {
		if got := SelfManaged(executable); got != want {
			t.Errorf("SelfManaged(%q) = %v, want %v", executable, got, want)
		}
	}
}

func TestASelfManagedBinaryUpdatesWhenTheCommandIsDone(t *testing.T) {
	var out bytes.Buffer
	stamp := filepath.Join(t.TempDir(), "update-notice")
	now := time.Now()
	applied := ""
	var failure error
	notifier := func() *Notifier {
		n := newNotifier(t, &out, "v0.0.7", stamp, now)
		n.upgrade = InstallGuide
		n.apply = func(_ context.Context, tag string) error { applied = tag; return failure }
		return n
	}

	n := notifier()
	n.Notify("v0.0.9")
	if out.Len() != 0 || applied != "" {
		t.Fatalf("Notify acted before the command was done: %q, applied %q", out.String(), applied)
	}
	n.Finish(context.Background())
	n.Finish(context.Background())
	if applied != "v0.0.9" || out.String() != "slopcop: updating from v0.0.7 to v0.0.9...\nslopcop: updated from v0.0.7 to v0.0.9; the next run uses it.\n" {
		t.Errorf("applied %q, printed %q", applied, out.String())
	}

	// A failed update says so with the way to upgrade by hand, and is not
	// tried again the same day.
	out.Reset()
	now = now.Add(25 * time.Hour)
	failure = errors.New("permission denied")
	n = notifier()
	n.Notify("v0.0.9")
	n.Finish(context.Background())
	want := "slopcop: updating from v0.0.7 to v0.0.9...\nslopcop v0.0.9 is available (you have v0.0.7), but updating to it failed: permission denied. Upgrade: " + InstallGuide + "\n"
	if out.String() != want {
		t.Errorf("printed %q, want %q", out.String(), want)
	}
	out.Reset()
	applied = ""
	n = notifier()
	n.Notify("v0.0.9")
	n.Finish(context.Background())
	if applied != "" || out.Len() != 0 {
		t.Errorf("a second try the same day: applied %q, printed %q", applied, out.String())
	}

	// A pre-release is announced, never installed.
	now = now.Add(25 * time.Hour)
	n = notifier()
	n.Notify("v0.1.0-rc1")
	n.Finish(context.Background())
	if applied != "" || !strings.Contains(out.String(), "slopcop v0.1.0 is available") {
		t.Errorf("a pre-release: applied %q, printed %q", applied, out.String())
	}

	// An interrupted update is tried again on the next run.
	out.Reset()
	now = now.Add(25 * time.Hour)
	failure = nil
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	n = notifier()
	n.Notify("v0.0.9")
	n.Finish(canceled)
	applied = ""
	n = notifier()
	n.Notify("v0.0.9")
	n.Finish(context.Background())
	if applied != "v0.0.9" {
		t.Errorf("an interrupted update was not tried again")
	}

	// Nothing newer, nothing to do.
	applied = ""
	n = notifier()
	n.Finish(context.Background())
	if applied != "" {
		t.Errorf("Finish updated with no newer release")
	}
}

func TestOnlyAReleaseBuildIsReplaced(t *testing.T) {
	for raw, want := range map[string]bool{
		"v0.0.9": true, "0.0.9": true, "v0.0.9-3-gabc123-dirty": false, "v0.1.0-rc1": false, "v0.0.9+build": false, "dev": false,
	} {
		if got := IsRelease(raw); got != want {
			t.Errorf("IsRelease(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestNoAutoUpdateInCIOrWhenTurnedOff(t *testing.T) {
	t.Setenv("CI", "")
	t.Setenv(AutoDisableEnv, "")
	if !autoUpdates() {
		t.Error("auto update is off by default")
	}
	t.Setenv("CI", "true")
	if autoUpdates() {
		t.Error("auto update is on in CI")
	}
	t.Setenv("CI", "")
	t.Setenv(AutoDisableEnv, "1")
	if autoUpdates() {
		t.Errorf("auto update is on with %s set", AutoDisableEnv)
	}
}
