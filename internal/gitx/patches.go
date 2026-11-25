package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// PatchExport describes a complete index of changed paths.
type PatchExport struct {
	Files int
	Bytes int64
}

// ErrPatchTruncated means a per-file patch cannot be reviewed completely.
var ErrPatchTruncated = errors.New("per-file patch exceeds readable limit")

// FilePatch reads a literal path's captured diff, including readable binary text.
func FilePatch(ctx context.Context, root, base, tree, path string) ([]byte, error) {
	return filePatch(ctx, root, base, tree, path, 1<<20)
}

// FilePatchLimit reads a literal path's captured diff up to maxBytes.
func FilePatchLimit(ctx context.Context, root, base, tree, path string, maxBytes int64) ([]byte, error) {
	return filePatch(ctx, root, base, tree, path, maxBytes)
}

func filePatch(ctx context.Context, root, base, tree, path string, maxFile int64) ([]byte, error) {
	if maxFile <= 0 {
		maxFile = 1 << 20
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, truncated, err := runWithoutRepoAttributesBounded(ctx, root, maxFile, "diff", "--no-ext-diff", "--no-textconv", "--ignore-submodules=none", base, tree, "--", ":(literal)"+path)
	if err != nil {
		return nil, err
	}
	if truncated {
		return nil, fmt.Errorf("%w: %q (limit %d)", ErrPatchTruncated, path, maxFile)
	}
	if strings.Contains(string(data), "Binary files ") || strings.Contains(string(data), "GIT binary patch") {
		changes, _, err := changedBinaries(root, base, tree, path)
		if err != nil {
			return nil, err
		}
		for _, change := range changes {
			if change.path == path {
				section, complete, err := binaryFileSection(root, change, int(maxFile))
				if err != nil {
					return nil, err
				}
				data = append(data, []byte(section)...)
				if !complete {
					return nil, fmt.Errorf("%w: binary readable text for %q", ErrPatchTruncated, path)
				}
			}
		}
	}
	if maxFile > 0 && int64(len(data)) > maxFile {
		return nil, fmt.Errorf("%w: %q (limit %d)", ErrPatchTruncated, path, maxFile)
	}
	return data, nil
}

// runWithoutRepoAttributesBounded runs a read-only git tree command ignoring
// repository .gitattributes, stopping as soon as stdout exceeds maxBytes or
// ctx is canceled so huge diffs are never buffered in full.
func runWithoutRepoAttributesBounded(ctx context.Context, root string, maxBytes int64, args ...string) ([]byte, bool, error) {
	gitDirOut, err := runContext(ctx, root, nil, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, false, err
	}
	gitDir := strings.TrimSuffix(string(gitDirOut), "\n")
	dir, err := os.MkdirTemp("", "slopcop-no-attributes-")
	if err != nil {
		return nil, false, fmt.Errorf("create empty work tree: %w", err)
	}
	defer os.RemoveAll(dir)
	env := slices.DeleteFunc(os.Environ(), func(entry string) bool {
		return strings.HasPrefix(entry, "GIT_ATTR_SOURCE=")
	})
	env = withEnv(env, "GIT_INDEX_FILE", filepath.Join(dir, "index"))
	cmdCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	fullArgs := append([]string{"-C", dir, "--git-dir=" + gitDir, "--work-tree=" + dir, "-c", "attr.tree="}, args...)
	cmd := exec.CommandContext(cmdCtx, "git", fullArgs...)
	cmd.Env = env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, false, err
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, maxBytes+1))
	if int64(len(data)) > maxBytes {
		cancel()
		_ = stdout.Close()
		_ = cmd.Wait()
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		return nil, true, nil
	}
	waitErr := cmd.Wait()
	if readErr != nil {
		return nil, false, readErr
	}
	if waitErr != nil {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		return nil, false, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), waitErr, strings.TrimSpace(stderr.String()))
	}
	return data, false, nil
}

// ExportPerFilePatches writes a deterministic index and one patch per changed path.
// Numbered names avoid path traversal, filename collisions and platform limits.
func ExportPerFilePatches(ctx context.Context, root, baseTree, tree, destDir string, maxFile, maxTotal int64) (PatchExport, error) {
	var result PatchExport
	raw, err := RunWithoutRepoAttributes(root, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--ignore-submodules=none", "--name-status", "-z", baseTree, tree)
	if err != nil {
		return result, err
	}
	dir := filepath.Join(destDir, "patches")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return result, err
	}
	entries := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	var index strings.Builder
	for i := 0; i+1 < len(entries); i += 2 {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		status, path := entries[i], entries[i+1]
		data, err := filePatch(ctx, root, baseTree, tree, path, maxFile)
		if err != nil {
			return result, err
		}
		rel := fmt.Sprintf("patches/%06d.patch", i/2+1)
		display := path
		if strings.ContainsAny(path, "\t\r\n") {
			display = strconv.Quote(path)
		}
		line := status + "\t" + display + "\t" + rel + "\n"
		result.Bytes += int64(len(data) + len(line))
		if maxTotal > 0 && result.Bytes > maxTotal {
			return result, fmt.Errorf("per-file patch export exceeds total limit %d", maxTotal)
		}
		if err = os.WriteFile(filepath.Join(destDir, rel), data, 0400); err != nil {
			return result, err
		}
		index.WriteString(line)
		result.Files++
	}
	err = os.WriteFile(filepath.Join(destDir, "patches.txt"), []byte(index.String()), 0400)
	return result, err
}
