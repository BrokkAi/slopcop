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
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// AutoDisableEnv keeps slopcop from replacing its own binary when set to
	// any non-empty value; the notice still prints.
	AutoDisableEnv = "SLOPCOP_NO_AUTO_UPDATE"
	// maxArchive bounds a release archive, and maxChecksums its checksum
	// list, so a wrong answer cannot fill memory.
	maxArchive   = 256 << 20
	maxChecksums = 1 << 20
	// stagedPrefix starts the name of a new binary written beside the old
	// one before it is moved into place.
	stagedPrefix = ".slopcop-update-"
)

// releases is where release files and the latest-release redirect live. A
// test points it at its own server.
var releases = "https://github.com/BrokkAi/slopcop/releases"

// SelfManaged reports whether the binary at executable is one slopcop may
// replace itself: installed by the install script or by hand, not through
// npm or a Python tool, whose own upgrade command owns the file. A binary
// under another name, such as a test build, is left alone.
func SelfManaged(executable string) bool {
	// A Windows path is judged the same on any platform.
	name := strings.TrimSuffix(strings.ToLower(path.Base(strings.ReplaceAll(executable, `\`, "/"))), ".exe")
	return name == "slopcop" && ChannelOf(executable) == Unmanaged
}

// autoUpdates reports whether this process may replace its own binary: not
// when turned off, and not in CI, where the version a job installed stays
// the version it runs.
func autoUpdates() bool {
	return os.Getenv(AutoDisableEnv) == "" && os.Getenv("CI") == ""
}

// UpgradeCommand names the command that upgrades the binary at executable
// through the channel it was installed by.
func UpgradeCommand(executable string) string { return upgradeCommand(executable) }

// Executable is the running binary, with symlinks resolved: the file an
// update would replace.
func Executable() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	return executable, nil
}

// IsRelease reports whether raw is exactly a release version, as a release
// build carries, with no pre-release or build suffix.
func IsRelease(raw string) bool { return isRelease(raw) }

// Newer reports whether latest is a release after current. A dev build, or
// anything that is not a release version, is never behind.
func Newer(latest, current string) bool {
	newer, ok := parse(latest)
	if !ok {
		return false
	}
	have, ok := parse(current)
	return ok && newer.after(have)
}

// Latest asks GitHub for the tag of the newest release, from the redirect of
// the latest-release page, which needs no API quota.
func Latest(ctx context.Context) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, releases+"/latest", nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	tag := path.Base(response.Header.Get("Location"))
	if _, ok := parse(tag); !ok {
		return "", fmt.Errorf("the latest release could not be read from %s (HTTP %d)", releases, response.StatusCode)
	}
	return tag, nil
}

// Apply replaces the binary at executable with release tag's build for this
// platform. The archive is checked against the release's checksums.txt
// before anything is written, and the new binary is moved into place whole,
// so a failure leaves the old one as it was.
func Apply(ctx context.Context, tag, executable string) error {
	parsed, ok := parse(tag)
	if !ok {
		return fmt.Errorf("%q is not a release version", tag)
	}
	tag = parsed.String()
	extension := "tar.gz"
	if runtime.GOOS == "windows" {
		extension = "zip"
	}
	name := fmt.Sprintf("slopcop_%s_%s_%s.%s", tag, runtime.GOOS, runtime.GOARCH, extension)
	base := releases + "/download/" + tag + "/"
	sums, err := fetch(ctx, base+"checksums.txt", maxChecksums)
	if err != nil {
		return err
	}
	want, ok := checksum(sums, name)
	if !ok {
		return fmt.Errorf("release %s has no checksum for %s", tag, name)
	}
	archive, err := fetch(ctx, base+name, maxArchive)
	if err != nil {
		return err
	}
	if got := sha256.Sum256(archive); hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("checksum mismatch for %s", name)
	}
	binary, err := extract(archive, extension)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return replace(executable, binary)
}

func fetch(ctx context.Context, url string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("GET %s: larger than %d bytes", url, limit)
	}
	return data, nil
}

// checksum is the SHA-256 that sums, a checksums.txt of "<hash>  <name>"
// lines, lists for name.
func checksum(sums []byte, name string) (string, bool) {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name && len(fields[0]) == sha256.Size*2 {
			return strings.ToLower(fields[0]), true
		}
	}
	return "", false
}

// extract is the slopcop binary inside a release archive.
func extract(archive []byte, extension string) ([]byte, error) {
	if extension == "zip" {
		reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, file := range reader.File {
			if path.Base(file.Name) != "slopcop.exe" {
				continue
			}
			opened, err := file.Open()
			if err != nil {
				return nil, err
			}
			defer opened.Close()
			return readBinary(opened)
		}
		return nil, errors.New("no slopcop.exe in the archive")
	}
	unzipped, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	files := tar.NewReader(unzipped)
	for {
		header, err := files.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("no slopcop in the archive")
		}
		if err != nil {
			return nil, err
		}
		if header.Typeflag == tar.TypeReg && path.Base(header.Name) == "slopcop" {
			return readBinary(files)
		}
	}
}

func readBinary(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxArchive+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > maxArchive {
		return nil, errors.New("the binary in the archive is empty or too large")
	}
	return data, nil
}

// replace writes binary beside executable and moves it into place. Windows
// does not let a running program's file be overwritten, but does let it be
// renamed, so there the old binary is moved aside first and removed by the
// next update.
func replace(executable string, binary []byte) error {
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	// The new binary keeps the old one's permissions.
	mode := os.FileMode(0o755)
	if info, err := os.Stat(executable); err == nil {
		mode = info.Mode().Perm()
	}
	// An update that was killed can leave its staged file behind.
	if stale, err := filepath.Glob(filepath.Join(filepath.Dir(executable), stagedPrefix+"*")); err == nil {
		for _, file := range stale {
			if info, err := os.Stat(file); err == nil && time.Since(info.ModTime()) > time.Hour {
				_ = os.Remove(file)
			}
		}
	}
	staged, err := os.CreateTemp(filepath.Dir(executable), stagedPrefix+"*")
	if err != nil {
		return err
	}
	defer os.Remove(staged.Name())
	if _, err := staged.Write(binary); err != nil {
		staged.Close()
		return err
	}
	if err := staged.Chmod(mode); err != nil {
		staged.Close()
		return err
	}
	if err := staged.Close(); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		return os.Rename(staged.Name(), executable)
	}
	old := executable + ".old"
	_ = os.Remove(old)
	if err := os.Rename(executable, old); err != nil {
		return err
	}
	if err := os.Rename(staged.Name(), executable); err != nil {
		_ = os.Rename(old, executable)
		return err
	}
	return nil
}
