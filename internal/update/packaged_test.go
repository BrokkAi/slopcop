package update

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o755); err != nil {
		t.Fatal(err)
	}
}

func withLauncherPython(t *testing.T, python string) {
	t.Helper()
	saved := launcherPython
	launcherPython = python
	t.Cleanup(func() { launcherPython = saved })
}

func TestPackageUpgradeRunsTheChannelsOwnCommand(t *testing.T) {
	withLauncherPython(t, "")
	venv := t.TempDir()
	pipBinary := filepath.Join(venv, "lib", "python3.12", "site-packages", "brokk_slopcop", "_bin", "slopcop")
	python := filepath.Join(venv, "bin", "python3.12")
	touch(t, python)
	for _, test := range []struct {
		executable string
		want       []string
	}{
		{"/home/a/.local/share/pipx/venvs/brokk-slopcop/lib/python3.12/site-packages/brokk_slopcop/_bin/slopcop", []string{"pipx", "upgrade", "brokk-slopcop"}},
		{"/home/a/.local/share/uv/tools/brokk-slopcop/lib/python3.12/site-packages/brokk_slopcop/_bin/slopcop", []string{"uv", "tool", "upgrade", "brokk-slopcop"}},
		{pipBinary, []string{python, "-m", "pip", "install", "--upgrade", "brokk-slopcop==0.0.28"}},
	} {
		got, err := PackageUpgrade(test.executable, "v0.0.28")
		if err != nil || !reflect.DeepEqual(got, test.want) {
			t.Errorf("PackageUpgrade(%q) = %q, %v; want %q", test.executable, got, err, test.want)
		}
	}
}

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
	touch(t, binary)
	touch(t, shim)
	return binary, prefix
}

func TestAGlobalNpmInstallIsUpgradedInItsOwnPrefix(t *testing.T) {
	binary, prefix := globalNpmInstall(t)
	if got := upgradeCommand(binary); got != "npm install --global @brokkai/slopcop@latest" {
		t.Errorf("upgradeCommand = %q", got)
	}
	want := []string{"npm", "install", "--global", "--prefix", prefix, "@brokkai/slopcop@0.0.28"}
	if got, err := PackageUpgrade(binary, "v0.0.28"); err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("PackageUpgrade = %q, %v; want %q", got, err, want)
	}
}

func TestAnNpmInstallWithoutTheGlobalShimIsNotUpgradedGlobally(t *testing.T) {
	// The global layouts of both systems, but with no shim in the prefix:
	// some other install, such as one made with --prefix and --no-bin-links.
	for _, nodeModules := range []string{filepath.Join("lib", "node_modules"), "node_modules"} {
		binary := filepath.Join(t.TempDir(), nodeModules, "@brokkai", "slopcop", "native", "x", "slopcop")
		touch(t, binary)
		if got, err := PackageUpgrade(binary, "v0.0.28"); err == nil {
			t.Errorf("PackageUpgrade(%q) = %q, want an error", binary, got)
		}
	}
}

func TestAProjectsNpmDependencyIsLeftToTheProject(t *testing.T) {
	project := t.TempDir()
	binary := filepath.Join(project, "node_modules", "@brokkai", "slopcop", "native", "linux_amd64", "slopcop")
	touch(t, binary)
	touch(t, filepath.Join(project, "node_modules", ".bin", "slopcop"))
	if got := ChannelOf(binary); got != NPMProject {
		t.Fatalf("ChannelOf = %v, want NPMProject", got)
	}
	_, err := PackageUpgrade(binary, "v0.0.28")
	if err == nil || !strings.Contains(err.Error(), "npm install @brokkai/slopcop@0.0.28") {
		t.Fatalf("err = %v", err)
	}

	// Installed with --no-bin-links there is no shim, but the project's
	// package.json still gives it away.
	project = t.TempDir()
	binary = filepath.Join(project, "node_modules", "@brokkai", "slopcop", "native", "linux_amd64", "slopcop")
	touch(t, binary)
	touch(t, filepath.Join(project, "package.json"))
	if got := ChannelOf(binary); got != NPMProject {
		t.Fatalf("ChannelOf = %v, want NPMProject", got)
	}
}

func TestPackageUpgradeHasNoCommandWithoutAPackageManager(t *testing.T) {
	for _, executable := range []string{
		"/home/a/.local/bin/slopcop",
		"/home/a/.npm/_npx/0123/node_modules/@brokkai/slopcop/native/linux_amd64/slopcop",
	} {
		if got, err := PackageUpgrade(executable, "v0.0.28"); err == nil {
			t.Errorf("PackageUpgrade(%q) = %q, want an error", executable, got)
		}
	}
}

func TestAPipInstallIsUpgradedByThePythonThatOwnsIt(t *testing.T) {
	withLauncherPython(t, "")
	// A Windows virtual environment keeps its interpreter in Scripts.
	venv := t.TempDir()
	binary := filepath.Join(venv, "Lib", "site-packages", "brokk_slopcop", "_bin", "slopcop.exe")
	python := filepath.Join(venv, "Scripts", "python.exe")
	touch(t, python)
	if got, ok := ownerPython(binary); !ok || got != python {
		t.Fatalf("ownerPython = %q, %v; want %q", got, ok, python)
	}

	// The interpreter the launcher names wins over any found on disk.
	launcher := filepath.Join(t.TempDir(), "python3")
	touch(t, launcher)
	withLauncherPython(t, launcher)
	if got, ok := ownerPython(binary); !ok || got != launcher {
		t.Fatalf("ownerPython = %q, %v; want the launcher's %q", got, ok, launcher)
	}
}

func TestAUserSiteInstallIsUpgradedWithUser(t *testing.T) {
	home := t.TempDir()
	appData := filepath.Join(home, "AppData", "Roaming")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", appData)
	t.Setenv("PYTHONUSERBASE", "")
	python := filepath.Join(t.TempDir(), "python3")
	touch(t, python)
	withLauncherPython(t, python)
	userBase := filepath.Join(t.TempDir(), "userbase")
	for _, test := range []struct {
		site     string
		userBase string
		user     bool
	}{
		{filepath.Join(home, ".local", "lib", "python3.12", "site-packages"), "", true},
		{filepath.Join(home, "Library", "Python", "3.12", "lib", "python", "site-packages"), "", true},
		{filepath.Join(appData, "Python", "Python312", "site-packages"), "", true},
		{filepath.Join(userBase, "lib", "python3.12", "site-packages"), userBase, true},
		// With $PYTHONUSERBASE elsewhere, --user would install there.
		{filepath.Join(home, ".local", "lib", "python3.12", "site-packages"), userBase, false},
		// A virtual environment in the home directory is not the user site.
		{filepath.Join(home, ".venv", "lib", "python3.12", "site-packages"), "", false},
		{filepath.Join(home, "project", ".venv", "Lib", "site-packages"), "", false},
	} {
		t.Setenv("PYTHONUSERBASE", test.userBase)
		binary := filepath.Join(test.site, "brokk_slopcop", "_bin", "slopcop")
		want := []string{python, "-m", "pip", "install", "--upgrade", "brokk-slopcop==0.0.28"}
		if test.user {
			want = []string{python, "-m", "pip", "install", "--upgrade", "--user", "brokk-slopcop==0.0.28"}
		}
		if got, err := PackageUpgrade(binary, "v0.0.28"); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("PackageUpgrade(%q) = %q, %v; want %q", binary, got, err, want)
		}
	}
}

func TestAPipInstallWhosePythonCannotBeFoundSaysSo(t *testing.T) {
	// A user-site install has no interpreter beside its site-packages, and a
	// launcher that names a missing file is not believed.
	withLauncherPython(t, filepath.Join(t.TempDir(), "gone"))
	binary := filepath.Join(t.TempDir(), "lib", "python3.12", "site-packages", "brokk_slopcop", "_bin", "slopcop")
	_, err := PackageUpgrade(binary, "v0.0.28")
	if err == nil || !strings.Contains(err.Error(), "python -m pip install --upgrade brokk-slopcop==0.0.28") {
		t.Fatalf("err = %v", err)
	}
}

func TestCommandLineQuotesWhatAShellWouldSplitOrExpand(t *testing.T) {
	for _, test := range []struct {
		goos string
		argv []string
		want string
	}{
		// PowerShell: single quotes expand nothing, and a quoted program
		// runs through the call operator.
		{"windows", []string{`C:\Program Files\Python\python.exe`, "-m", "pip"}, `& 'C:\Program Files\Python\python.exe' -m pip`},
		{"windows", []string{`C:\Python312\python.exe`, "brokk-slopcop==0.0.28"}, `C:\Python312\python.exe brokk-slopcop==0.0.28`},
		{"windows", []string{"npm", "--prefix", `C:\Users\it's $me`, "@brokkai/slopcop@0.0.28"}, `npm --prefix 'C:\Users\it''s $me' '@brokkai/slopcop@0.0.28'`},
		// Inside single quotes a POSIX shell expands neither $ nor `.
		{"linux", []string{"npm", "--prefix", "/home/a/$TOOLS/`x`", "@brokkai/slopcop@0.0.28"}, "npm --prefix '/home/a/$TOOLS/`x`' @brokkai/slopcop@0.0.28"},
		{"darwin", []string{"/Users/a/it's here/python3"}, `'/Users/a/it'\''s here/python3'`},
	} {
		if got := commandLine(test.argv, test.goos); got != test.want {
			t.Errorf("commandLine(%q, %s) = %s, want %s", test.argv, test.goos, got, test.want)
		}
	}
}

func TestARunningBinaryIsMovedOutOfThePackageAndRestoredOnFailure(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "_bin", "slopcop.exe")
	touch(t, executable)
	aside := t.TempDir()
	restore, err := moveAside(executable, aside)
	if err != nil {
		t.Fatal(err)
	}
	if isFile(executable) {
		t.Fatal("the binary is still in the package")
	}
	if moved, _ := filepath.Glob(filepath.Join(aside, asidePrefix+"*.exe")); len(moved) != 1 {
		t.Fatalf("moved aside: %q", moved)
	}
	// The upgrade failed and left nothing there: the old binary returns.
	restore()
	if !isFile(executable) {
		t.Fatal("the binary was not put back")
	}

	// An upgrade that wrote a new binary keeps it, and the next update
	// clears the copy moved aside.
	restore, err = moveAside(executable, aside)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	restore()
	if data, _ := os.ReadFile(executable); string(data) != "new" {
		t.Fatalf("binary = %q, want the upgrade's", data)
	}
	if _, err := moveAside(executable, aside); err != nil {
		t.Fatal(err)
	}
	if moved, _ := filepath.Glob(filepath.Join(aside, asidePrefix+"*")); len(moved) != 1 {
		t.Fatalf("moved aside: %q, want only the latest", moved)
	}

	// A directory it cannot be moved into, as across volumes, is passed
	// over for the next one.
	touch(t, executable)
	next := t.TempDir()
	if _, err := moveAside(executable, filepath.Join(aside, "missing"), next); err != nil {
		t.Fatal(err)
	}
	if moved, _ := filepath.Glob(filepath.Join(next, asidePrefix+"*")); len(moved) != 1 {
		t.Fatalf("moved aside: %q", moved)
	}

	// With nowhere outside the package to go, it stays, and says so.
	touch(t, executable)
	if _, err := moveAside(executable, filepath.Join(aside, "missing")); err == nil {
		t.Fatal("moveAside with nowhere to go succeeded")
	}
	if !isFile(executable) {
		t.Fatal("the binary moved")
	}
}

func TestTheFolderOutsideThePackageIsAboveIt(t *testing.T) {
	npm := filepath.Join("p", "lib", "node_modules", "@brokkai", "slopcop", "native", "linux_amd64", "slopcop")
	pip := filepath.Join("v", "Lib", "site-packages", "brokk_slopcop", "_bin", "slopcop.exe")
	for executable, want := range map[string]string{npm: filepath.Join("p", "lib"), pip: filepath.Join("v", "Lib"), "slopcop": ""} {
		if got := outsidePackage(executable); got != want {
			t.Errorf("outsidePackage(%q) = %q, want %q", executable, got, want)
		}
	}
}
