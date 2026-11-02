package cli

import "github.com/BrokkAi/slopcop/internal/gitx"

// dependencyFiles are the base names of the dependency manifests and
// lockfiles a package manager rewrites, such as `npm install`, `cargo add` or
// `uv add`. The hosted service reads them; the client only recognises them
// by path.
var dependencyFiles = []string{
	"package.json", "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock",
	"Cargo.toml", "Cargo.lock",
	"pyproject.toml", "uv.lock", "poetry.lock", "requirements*.txt",
	"go.mod", "go.sum",
}

// dependencyPathspecs match a dependency file in any directory of the
// repository, except the packages a package manager installs into
// node_modules, which carry manifests of their own.
func dependencyPathspecs() []string {
	pathspecs := make([]string, 0, len(dependencyFiles)+1)
	for _, name := range dependencyFiles {
		pathspecs = append(pathspecs, ":(glob)**/"+name)
	}
	return append(pathspecs, ":(glob,exclude)**/node_modules/**")
}

// changedDependencyFiles is the set of dependency files in the repository at
// repoRoot that differ from HEAD in the working tree. The hook counts them as
// written by the session: a package manager the agent ran through its shell
// is the usual cause, and no edit tool records it.
func changedDependencyFiles(repoRoot string) map[string]bool {
	changed := map[string]bool{}
	paths, err := gitx.ChangedPaths(repoRoot, dependencyPathspecs())
	if err != nil {
		return changed
	}
	for _, path := range paths {
		changed[path] = true
	}
	return changed
}
