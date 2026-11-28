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
