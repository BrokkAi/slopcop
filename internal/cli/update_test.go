package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/update"
)

// globalNpmInstall lays out a global npm install of slopcop as npm does on
// this system, and returns the binary and the prefix.
func globalNpmInstall(t *testing.T) (binary, prefix string) {
	t.Helper()
	prefix = t.TempDir()
	nodeModules := filepath.Join(prefix, "lib", "node_modules")
	shim := filepath.Join(prefix, "bin", "slopcop")
	if runtime.GOOS == "windows" {
		nodeModules = filepath.Join(prefix, "node_modules")
		shim = filepath.Join(prefix, "slopcop.cmd")
	}
	binary = filepath.Join(nodeModules, "@brokkai", "slopcop", "native", "x", "slopcop")
	for _, file := range []string{binary, shim} {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return binary, prefix
}

// fakeRunner records each program it is asked to run. The upgrade fails with
// upgradeErr, and afterwards the binary reports installed as its version.
type fakeRunner struct {
	binary     string
	upgradeErr error
	installed  string
	ran        [][]string
}

func (f *fakeRunner) run(_ context.Context, stdout, _ io.Writer, argv []string) error {
	f.ran = append(f.ran, argv)
	if argv[0] == f.binary {
		fmt.Fprintf(stdout, "slopcop version %s\n", f.installed)
		return nil
	}
	fmt.Fprintln(stdout, "added 1 package")
	return f.upgradeErr
}

func TestUpdateRunsThePackageManagerAndConfirmsTheNewVersion(t *testing.T) {
	npmBinary, prefix := globalNpmInstall(t)
	runner := &fakeRunner{binary: npmBinary, installed: "v0.0.28"}
	var out bytes.Buffer
	if err := updateThroughPackageManager(context.Background(), &out, io.Discard, npmBinary, "v0.0.27", "v0.0.28", runner.run); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"npm", "install", "--global", "--prefix", prefix, "@brokkai/slopcop@0.0.28"},
		{npmBinary, "--version"},
	}
	if !reflect.DeepEqual(runner.ran, want) {
		t.Fatalf("ran %q, want %q", runner.ran, want)
	}
	// The package manager's own output is passed through.
	if !strings.Contains(out.String(), "added 1 package") || !strings.Contains(out.String(), "updated from v0.0.27 to v0.0.28") {
		t.Fatalf("printed %q", out.String())
	}
}

func TestAFailedUpgradeNamesTheCommandToRunByHand(t *testing.T) {
	npmBinary, prefix := globalNpmInstall(t)
	runner := &fakeRunner{binary: npmBinary, upgradeErr: errors.New("exit status 243")}
	err := updateThroughPackageManager(context.Background(), io.Discard, io.Discard, npmBinary, "v0.0.27", "v0.0.28", runner.run)
	if err == nil || !strings.Contains(err.Error(), update.CommandLine([]string{"npm", "install", "--global", "--prefix", prefix, "@brokkai/slopcop@0.0.28"})) {
		t.Fatalf("err = %v", err)
	}
	if len(runner.ran) != 1 {
		t.Fatalf("ran %q after the failure", runner.ran)
	}
}

func TestAnUpgradeThatLeftTheOldVersionFails(t *testing.T) {
	npmBinary, _ := globalNpmInstall(t)
	runner := &fakeRunner{binary: npmBinary, installed: "v0.0.27"}
	err := updateThroughPackageManager(context.Background(), io.Discard, io.Discard, npmBinary, "v0.0.27", "v0.0.28", runner.run)
	if err == nil || !strings.Contains(err.Error(), "not v0.0.28") || !strings.Contains(err.Error(), "npm install --global") {
		t.Fatalf("err = %v", err)
	}
}

// npmBinary in a table stands for a global npm install the test lays out.
const npmBinary = "global npm install"

// fakeSource answers latest with tag, or fails when tag is "", and records
// each apply and each program run.
func fakeSource(executable, tag string, applied *[]string) updateSource {
	return updateSource{
		run: func(_ context.Context, _, _ io.Writer, argv []string) error {
			*applied = append(*applied, strings.Join(argv, " "))
			return errors.New("no package manager here")
		},
		executable: executable,
		latest: func(context.Context) (string, error) {
			if tag == "" {
				return "", errors.New("offline")
			}
			return tag, nil
		},
		apply: func(_ context.Context, tag, _ string) error {
			*applied = append(*applied, tag)
			return nil
		},
	}
}

func exitOf(err error) int {
	if err == nil {
		return 0
	}
	var exit silentExit
	if errors.As(err, &exit) {
		return exit.code
	}
	return 2
}

func TestUpdateChecksTheLatestReleaseForEveryChannel(t *testing.T) {
	cases := []struct {
		name       string
		executable string
		version    string
		latest     string
		check      bool
		wantOut    string
		wantExit   int
		wantApply  bool
	}{
		{"npm up to date", npmBinary, "v0.0.28", "v0.0.28", false,
			"slopcop: v0.0.28 is up to date (latest release v0.0.28)\n", 0, false},
		{"npm behind, --check", npmBinary, "v0.0.27", "v0.0.28", true,
			"slopcop: v0.0.27 → v0.0.28 available. Upgrade: npm install --global @brokkai/slopcop@latest\n", updateAvailable, false},
		{"npm up to date, --check", npmBinary, "v0.0.28", "v0.0.28", true,
			"slopcop: v0.0.28 is up to date (latest release v0.0.28)\n", 0, false},
		{"self-managed behind", "/home/me/.local/bin/slopcop", "v0.0.27", "v0.0.28", false,
			"slopcop: updated from v0.0.27 to v0.0.28\n", 0, true},
		{"self-managed behind, --check", "/home/me/.local/bin/slopcop", "v0.0.27", "v0.0.28", true,
			"slopcop: v0.0.27 → v0.0.28 available. Upgrade: slopcop update\n", updateAvailable, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.executable == npmBinary {
				tc.executable, _ = globalNpmInstall(t)
			}
			var out bytes.Buffer
			var applied []string
			err := runUpdate(context.Background(), &out, io.Discard, tc.version, tc.check, fakeSource(tc.executable, tc.latest, &applied))
			if got := exitOf(err); got != tc.wantExit {
				t.Fatalf("exit = %d (err %v), want %d", got, err, tc.wantExit)
			}
			if out.String() != tc.wantOut {
				t.Errorf("stdout = %q, want %q", out.String(), tc.wantOut)
			}
			if (len(applied) > 0) != tc.wantApply {
				t.Errorf("applied = %v, want apply %v", applied, tc.wantApply)
			}
		})
	}
}

func TestUpdateFailsWhenTheLatestReleaseCannotBeRead(t *testing.T) {
	npmBinary, _ := globalNpmInstall(t)
	var out bytes.Buffer
	var applied []string
	err := runUpdate(context.Background(), &out, io.Discard, "v0.0.27", true, fakeSource(npmBinary, "", &applied))
	if err == nil || exitOf(err) != 2 {
		t.Fatalf("err = %v, want a failure that exits 2", err)
	}
	// Without --check, a package-managed install still learns how to upgrade.
	err = runUpdate(context.Background(), &out, io.Discard, "v0.0.27", false, fakeSource(npmBinary, "", &applied))
	if err == nil || err.Error() != "offline. Upgrade: npm install --global @brokkai/slopcop@latest" {
		t.Fatalf("err = %v", err)
	}
	if out.Len() != 0 || len(applied) != 0 {
		t.Errorf("stdout = %q, applied = %v", out.String(), applied)
	}
}

func TestUpdateLeavesADevBuildAlone(t *testing.T) {
	var out bytes.Buffer
	var applied []string
	err := runUpdate(context.Background(), &out, io.Discard, "dev", false, fakeSource(npmBinary, "v0.0.28", &applied))
	if err != nil || out.String() != "slopcop: dev is not a release build, so it is left as it is\n" {
		t.Errorf("err = %v, stdout = %q", err, out.String())
	}
	// --check cannot say up to date or behind, so it fails rather than pass.
	out.Reset()
	err = runUpdate(context.Background(), &out, io.Discard, "dev", true, fakeSource(npmBinary, "v0.0.28", &applied))
	if exitOf(err) != 2 || out.Len() != 0 || len(applied) != 0 {
		t.Errorf("err = %v, stdout = %q, applied = %v", err, out.String(), applied)
	}
}

func TestUpdateRunsThePackageManagerForAPackagedInstall(t *testing.T) {
	binary, prefix := globalNpmInstall(t)
	runner := &fakeRunner{binary: binary, installed: "v0.0.28"}
	source := fakeSource(binary, "v0.0.28", new([]string))
	source.run = runner.run
	var out bytes.Buffer
	if err := runUpdate(context.Background(), &out, io.Discard, "v0.0.27", false, source); err != nil {
		t.Fatal(err)
	}
	if len(runner.ran) == 0 || !reflect.DeepEqual(runner.ran[0], []string{"npm", "install", "--global", "--prefix", prefix, "@brokkai/slopcop@0.0.28"}) {
		t.Fatalf("ran %q", runner.ran)
	}
}

func TestUpdateLeavesAProjectsNpmDependencyToTheProject(t *testing.T) {
	project := t.TempDir()
	binary := filepath.Join(project, "node_modules", "@brokkai", "slopcop", "native", "x", "slopcop")
	for _, file := range []string{binary, filepath.Join(project, "package.json")} {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	var applied []string
	err := runUpdate(context.Background(), &out, io.Discard, "v0.0.27", false, fakeSource(binary, "v0.0.28", &applied))
	if err != nil || len(applied) != 0 || !strings.Contains(out.String(), "in the project that depends on it") {
		t.Fatalf("err = %v, applied = %v, stdout = %q", err, applied, out.String())
	}
}

func TestUpdateCheckComparesAnNpxRunToo(t *testing.T) {
	// npx @brokkai/slopcop@0.0.27 update --check runs an older release.
	binary := filepath.Join(t.TempDir(), "_npx", "0123", "node_modules", "@brokkai", "slopcop", "native", "x", "slopcop")
	var out bytes.Buffer
	var applied []string
	err := runUpdate(context.Background(), &out, io.Discard, "v0.0.27", true, fakeSource(binary, "v0.0.28", &applied))
	if exitOf(err) != updateAvailable || out.String() != "slopcop: v0.0.27 → v0.0.28 available. Upgrade: npx @brokkai/slopcop@latest\n" || len(applied) != 0 {
		t.Fatalf("err = %v, stdout = %q, applied = %v", err, out.String(), applied)
	}
	// Without --check there is nothing to install.
	out.Reset()
	err = runUpdate(context.Background(), &out, io.Discard, "v0.0.27", false, fakeSource(binary, "v0.0.28", &applied))
	if err != nil || !strings.Contains(out.String(), "nothing to update") || len(applied) != 0 {
		t.Fatalf("err = %v, stdout = %q, applied = %v", err, out.String(), applied)
	}
}
