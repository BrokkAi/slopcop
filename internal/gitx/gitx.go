// Package gitx runs the git commands the CLI needs: repository discovery,
// remote canonicalization, base resolution, and working-tree snapshots and packfiles.
package gitx

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
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
	return runBytesInput(root, env, nil, args...)
}

func runBytesInput(root string, env []string, input []byte, args ...string) ([]byte, error) {
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	command.Stdin = bytes.NewReader(input)
	if env != nil {
		command.Env = env
	}
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
