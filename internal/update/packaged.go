package update

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Channel is how a slopcop binary was installed, judged by where it lives.
type Channel int

const (
	// Unmanaged is a binary from the install script, a release archive, or
	// a build of one's own: no package manager owns it.
	Unmanaged Channel = iota
	// NPX is a copy npx fetched into its cache for one run.
	NPX
	// NPM is a global npm install of @brokkai/slopcop.
	NPM
	// NPMProject is @brokkai/slopcop installed as a project's dependency,
	// whose version the project pins.
	NPMProject
	// Pipx is a pipx install of brokk-slopcop.
	Pipx
	// UV is a `uv tool` install of brokk-slopcop.
	UV
	// Pip is a pip install of brokk-slopcop into some Python environment.
	Pip
)

// PythonEnv is set by the PyPI launcher to the interpreter that runs it, so
// a pip install is upgraded by the Python that owns it.
const PythonEnv = "SLOPCOP_PYTHON"

// launcherPython is the interpreter the PyPI launcher named. It is taken out
// of the environment as the process starts, so a program slopcop runs, which
// may be another slopcop, never inherits this one's.
var launcherPython = takeEnv(PythonEnv)

func takeEnv(name string) string {
	value := os.Getenv(name)
	_ = os.Unsetenv(name)
	return value
}

// ChannelOf reports the channel the binary at executable was installed
// through.
func ChannelOf(executable string) Channel {
	// Backslashes are Windows separators; a Unix path almost never has one.
	path := strings.ToLower(strings.ReplaceAll(executable, `\`, "/"))
	switch {
	case strings.Contains(path, "/_npx/") && strings.Contains(path, "/node_modules/@brokkai/slopcop/"):
		return NPX
	case strings.Contains(path, "/node_modules/@brokkai/slopcop/"):
		if nodeModules, ok := npmNodeModules(executable); ok {
			if _, global := npmGlobalPrefix(executable); !global || isProjectInstall(nodeModules) {
				return NPMProject
			}
		}
		return NPM
	case strings.Contains(path, "/brokk_slopcop/_bin/") && strings.Contains(path, "/pipx/"):
		return Pipx
	case strings.Contains(path, "/brokk_slopcop/_bin/") && strings.Contains(path, "/uv/tools/"):
		return UV
	case strings.Contains(path, "/brokk_slopcop/_bin/"):
		return Pip
	default:
		return Unmanaged
	}
}

// PackageUpgrade is the command that installs release tag over the binary at
// executable through the package manager that installed it, as a program and
// its arguments. npm and pip are pinned to tag, so a registry that lags
// cannot install an older build. A pip install is upgraded by the Python that
// owns it, and an error says when that Python cannot be found.
func PackageUpgrade(executable, tag string) ([]string, error) {
	parsed, ok := parse(tag)
	if !ok {
		return nil, fmt.Errorf("%q is not a release version", tag)
	}
	release := strings.TrimPrefix(parsed.String(), "v")
	switch ChannelOf(executable) {
	case NPM:
		argv := []string{"npm", "install", "--global"}
		if prefix, ok := npmGlobalPrefix(executable); ok {
			// The prefix that holds this binary, which need not be the one
			// the npm on PATH installs to by default.
			argv = append(argv, "--prefix", prefix)
		}
		return append(argv, "@brokkai/slopcop@"+release), nil
	case NPMProject:
		return nil, fmt.Errorf("this slopcop is a dependency of the project with %s, which pins its version. Upgrade it there: npm install @brokkai/slopcop@%s", executable, release)
	case Pipx:
		return []string{"pipx", "upgrade", "brokk-slopcop"}, nil
	case UV:
		return []string{"uv", "tool", "upgrade", "brokk-slopcop"}, nil
	case Pip:
		python, ok := ownerPython(executable)
		if !ok {
			return nil, fmt.Errorf("cannot tell which Python owns %s. Upgrade with that Python: python -m pip install --upgrade brokk-slopcop==%s", executable, release)
		}
		argv := []string{python, "-m", "pip", "install", "--upgrade"}
		if isUserSite(filepath.Dir(filepath.Dir(filepath.Dir(executable)))) {
			// Installed with --user: without it, a Python that may also
			// write its own site-packages would upgrade a copy there.
			argv = append(argv, "--user")
		}
		return append(argv, "brokk-slopcop=="+release), nil
	default:
		return nil, errors.New("no package manager installed " + executable)
	}
}

// npmNodeModules finds the node_modules directory that holds the
// @brokkai/slopcop package the binary at executable belongs to.
func npmNodeModules(executable string) (string, bool) {
	for dir := filepath.Dir(executable); filepath.Dir(dir) != dir; dir = filepath.Dir(dir) {
		scope := filepath.Dir(dir)
		if strings.EqualFold(filepath.Base(dir), "slopcop") && strings.EqualFold(filepath.Base(scope), "@brokkai") &&
			strings.EqualFold(filepath.Base(filepath.Dir(scope)), "node_modules") {
			return filepath.Dir(scope), true
		}
	}
	return "", false
}

// isProjectInstall reports whether nodeModules belongs to a project rather
// than a global prefix: a package.json sits beside it, or it has the .bin
// shim npm writes for a project's dependency. A global install has neither.
func isProjectInstall(nodeModules string) bool {
	if isFile(filepath.Join(filepath.Dir(nodeModules), "package.json")) {
		return true
	}
	for _, name := range []string{"slopcop", "slopcop.cmd", "slopcop.ps1"} {
		if _, err := os.Lstat(filepath.Join(nodeModules, ".bin", name)); err == nil {
			return true
		}
	}
	return false
}

// npmGlobalPrefix is the npm prefix a global install lives under:
// <prefix>/lib/node_modules on Unix, <prefix>\node_modules on Windows, with
// npm's slopcop shim in the prefix. Any other layout is not taken for a
// global install, so slopcop never upgrades an install it is unsure of.
func npmGlobalPrefix(executable string) (string, bool) {
	nodeModules, ok := npmNodeModules(executable)
	if !ok {
		return "", false
	}
	parent := filepath.Dir(nodeModules)
	if filepath.Separator == '\\' {
		// <prefix>\node_modules, with the slopcop.cmd shim npm writes for a
		// global install in <prefix>.
		return parent, isFile(filepath.Join(parent, "slopcop.cmd"))
	}
	// <prefix>/lib/node_modules, with the link npm writes for a global
	// install in <prefix>/bin.
	prefix := filepath.Dir(parent)
	if filepath.Base(parent) != "lib" {
		return "", false
	}
	_, err := os.Lstat(filepath.Join(prefix, "bin", "slopcop"))
	return prefix, err == nil
}

// ownerPython finds the interpreter of the environment a pip-installed binary
// lives in: the one the launcher named, or else the one beside the
// site-packages directory that holds the binary, as a virtual environment or
// a Python install lays it out.
func ownerPython(executable string) (string, bool) {
	if launcherPython != "" && isFile(launcherPython) {
		return launcherPython, true
	}
	// <prefix>/lib/pythonX.Y/site-packages/brokk_slopcop/_bin/slopcop, or
	// <prefix>\Lib\site-packages\brokk_slopcop\_bin\slopcop.exe on Windows.
	site := filepath.Dir(filepath.Dir(filepath.Dir(executable)))
	if !strings.EqualFold(filepath.Base(site), "site-packages") {
		return "", false
	}
	lib := filepath.Dir(site)
	var candidates []string
	if versioned := filepath.Base(lib); strings.HasPrefix(versioned, "python") {
		// lib/pythonX.Y, whose name is the interpreter's.
		bin := filepath.Join(filepath.Dir(filepath.Dir(lib)), "bin")
		candidates = []string{filepath.Join(bin, versioned), filepath.Join(bin, "python3"), filepath.Join(bin, "python")}
	} else {
		prefix := filepath.Dir(lib)
		candidates = []string{filepath.Join(prefix, "Scripts", "python.exe"), filepath.Join(prefix, "python.exe")}
	}
	for _, candidate := range candidates {
		if isFile(candidate) {
			return candidate, true
		}
	}
	return "", false
}

// isUserSite reports whether site is a Python user site-packages directory,
// where pip install --user puts packages, under $PYTHONUSERBASE or the
// platform's default user base:
//
//	<base>/lib/pythonX.Y/site-packages      ~/.local on Linux
//	<base>/X.Y/lib/python/site-packages     ~/Library/Python on macOS
//	<base>/PythonXY/site-packages           %APPDATA%\Python on Windows
//
// A virtual environment in the home directory has none of these shapes.
func isUserSite(site string) bool {
	if !strings.EqualFold(filepath.Base(site), "site-packages") {
		return false
	}
	// pip --user installs under $PYTHONUSERBASE when it is set, and only
	// there, so a default base no longer counts.
	var bases []string
	if base := os.Getenv("PYTHONUSERBASE"); base != "" {
		bases = []string{base}
	} else {
		if home, err := os.UserHomeDir(); err == nil {
			bases = append(bases, filepath.Join(home, ".local"), filepath.Join(home, "Library", "Python"))
		}
		if appData := os.Getenv("APPDATA"); appData != "" {
			bases = append(bases, filepath.Join(appData, "Python"))
		}
	}
	parent := filepath.Dir(site)
	var candidates []string
	switch name := strings.ToLower(filepath.Base(parent)); {
	case name == "python" && strings.EqualFold(filepath.Base(filepath.Dir(parent)), "lib"):
		// <base>/X.Y/lib/python, or <base>/lib/python for a macOS
		// framework build given $PYTHONUSERBASE.
		lib := filepath.Dir(parent)
		candidates = []string{filepath.Dir(filepath.Dir(lib)), filepath.Dir(lib)}
	case strings.HasPrefix(name, "python") && strings.EqualFold(filepath.Base(filepath.Dir(parent)), "lib"):
		// <base>/lib/pythonX.Y
		candidates = []string{filepath.Dir(filepath.Dir(parent))}
	case strings.HasPrefix(name, "python"):
		// <base>\PythonXY
		candidates = []string{filepath.Dir(parent)}
	}
	for _, candidate := range candidates {
		for _, base := range bases {
			if strings.EqualFold(filepath.Clean(candidate), filepath.Clean(base)) {
				return true
			}
		}
	}
	return false
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// asidePrefix starts the name of a running binary moved out of a package's
// way.
const asidePrefix = ".slopcop-replaced-"

// ReleaseRunning lets a package manager replace the binary at executable
// while it runs. Windows refuses to delete or overwrite a running program's
// file but lets it be renamed, so there it is moved out of the package
// first; elsewhere nothing needs doing. restore puts it back when the
// upgrade left nothing in its place.
func ReleaseRunning(executable string) (restore func(), err error) {
	if runtime.GOOS != "windows" {
		return func() {}, nil
	}
	return moveAside(executable, os.TempDir(), outsidePackage(executable))
}

// outsidePackage is a directory on the binary's own volume that a package
// manager does not remove while it replaces the package: the one above
// node_modules, or above site-packages.
func outsidePackage(executable string) string {
	if nodeModules, ok := npmNodeModules(executable); ok {
		return filepath.Dir(nodeModules)
	}
	site := filepath.Dir(filepath.Dir(filepath.Dir(executable)))
	if strings.EqualFold(filepath.Base(site), "site-packages") {
		return filepath.Dir(site)
	}
	return ""
}

// moveAside moves executable into the first of dirs it can be renamed into,
// where it does not stop a package manager from removing the package's
// directory. A rename fails across volumes, so after the temporary directory
// comes one on the binary's own volume.
func moveAside(executable string, dirs ...string) (func(), error) {
	aside := ""
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		// Copies earlier updates moved aside; a process still running one
		// keeps it, and it goes next time.
		if stale, err := filepath.Glob(filepath.Join(dir, asidePrefix+"*")); err == nil {
			for _, file := range stale {
				_ = os.Remove(file)
			}
		}
		placeholder, err := os.CreateTemp(dir, asidePrefix+"*"+filepath.Ext(executable))
		if err != nil {
			continue
		}
		name := placeholder.Name()
		placeholder.Close()
		// Windows will not rename onto an existing file.
		_ = os.Remove(name)
		if os.Rename(executable, name) == nil {
			aside = name
			break
		}
	}
	if aside == "" {
		// Beside itself it would still block the package's removal.
		return nil, errors.New("no directory outside the package took it")
	}
	return func() {
		if _, err := os.Lstat(executable); errors.Is(err, fs.ErrNotExist) {
			_ = os.Rename(aside, executable)
		}
	}, nil
}

// CommandLine prints argv as a command to paste into this platform's shell,
// PowerShell on Windows, quoting each argument that shell would split or
// expand.
func CommandLine(argv []string) string { return commandLine(argv, runtime.GOOS) }

func commandLine(argv []string, goos string) string {
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = quoteArg(arg, goos)
	}
	line := strings.Join(quoted, " ")
	if goos == "windows" && len(quoted) > 0 && quoted[0] != argv[0] {
		// PowerShell runs a quoted program only through its call operator.
		line = "& " + line
	}
	return line
}

// quoteArg leaves an argument of plain characters as it is, and otherwise
// single-quotes it: neither a POSIX shell nor PowerShell expands anything in
// single quotes.
func quoteArg(arg, goos string) string {
	plain := arg != "" && strings.IndexFunc(arg, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			strings.ContainsRune("%+=:,./_-", r) || r == '@' && goos != "windows" || goos == "windows" && r == '\\')
	}) < 0
	if plain {
		return arg
	}
	if goos == "windows" {
		return "'" + strings.ReplaceAll(arg, "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}
