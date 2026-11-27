// Package gitx runs the git commands the CLI needs: repository discovery,
// remote canonicalization, base resolution, and working-tree patch building.
package gitx

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Run executes git in root and returns trimmed stdout.
func Run(root string, args ...string) (string, error) {
	out, err := RunBytes(root, nil, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// RunBytes executes git in root with an optional environment and returns raw
// stdout. Failures include git's own stderr so the caller can print it.
func RunBytes(root string, env []string, args ...string) ([]byte, error) {
	return RunInput(root, env, nil, args...)
}

// RunInput is RunBytes with input written to git's stdin.
func RunInput(root string, env []string, input []byte, args ...string) ([]byte, error) {
	return runContext(context.Background(), root, env, input, args...)
}

// RunWithoutRepoAttributes runs a command that compares two trees, ignoring
// every .gitattributes file the repository can supply, so the blobs alone
// decide what is text. A change can otherwise mark an edited text file
// `binary` or `-diff` and leave only "Binary files differ" for a reviewer.
//
// Git reads those files from the directory it runs in, so the command runs in
// an empty one with an empty index. attr.tree is cleared and GIT_ATTR_SOURCE
// dropped because either would name a tree of the repository's to read them
// from. The user's own core.attributesFile and info/attributes still apply: a
// repository cannot commit them. The flags work on every git version, where
// --attr-source would need 2.41.
func RunWithoutRepoAttributes(root string, args ...string) ([]byte, error) {
	out, err := RunBytes(root, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	gitDir := strings.TrimSuffix(string(out), "\n")
	dir, err := os.MkdirTemp("", "slopcop-no-attributes-")
	if err != nil {
		return nil, fmt.Errorf("create empty work tree: %w", err)
	}
	defer os.RemoveAll(dir)
	env := slices.DeleteFunc(os.Environ(), func(entry string) bool {
		return strings.HasPrefix(entry, "GIT_ATTR_SOURCE=")
	})
	env = withEnv(env, "GIT_INDEX_FILE", filepath.Join(dir, "index"))
	return RunBytes(dir, env, append([]string{"--git-dir=" + gitDir, "--work-tree=" + dir, "-c", "attr.tree="}, args...)...)
}

// RunContext is Run, stopped when ctx ends, for git commands that reach the
// network.
func RunContext(ctx context.Context, root string, env []string, args ...string) (string, error) {
	out, err := runContext(ctx, root, env, nil, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func runContext(ctx context.Context, root string, env []string, input []byte, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	if env != nil {
		command.Env = env
	}
	if input != nil {
		command.Stdin = bytes.NewReader(input)
	}
	// A child git leaves behind, such as ssh, can hold the output pipes
	// open after ctx kills git; stop waiting for them soon after.
	command.WaitDelay = 2 * time.Second
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), message)
	}
	return stdout.Bytes(), nil
}

// Toplevel returns the absolute repository root that contains dir.
func Toplevel(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		dir = "."
	}
	root, err := Run(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("cannot find the repository root: %w", err)
	}
	if root == "" {
		return "", fmt.Errorf("git returned no repository root for %s", dir)
	}
	return root, nil
}

// ResolveCommit resolves a revision to a full commit id.
func ResolveCommit(root, rev string) (string, error) {
	out, err := Run(root, "rev-parse", "--verify", rev+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", rev, err)
	}
	if out == "" {
		return "", fmt.Errorf("resolve %s: git returned no commit", rev)
	}
	return out, nil
}

// HeadCommit returns the current HEAD commit.
func HeadCommit(root string) (string, error) {
	return ResolveCommit(root, "HEAD")
}
