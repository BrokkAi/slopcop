package gitx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

func filePatch(ctx context.Context, root, base, tree, path string, maxFile int64) ([]byte, error) {
	if maxFile <= 0 {
		maxFile = 1 << 20
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := RunWithoutRepoAttributes(root, "diff", "--no-ext-diff", "--no-textconv", "--ignore-submodules=none", base, tree, "--", ":(literal)"+path)
	if err != nil {
		return nil, err
	}
	if strings.Contains(string(data), "Binary files ") || strings.Contains(string(data), "GIT binary patch") {
		changes, _, err := changedBinaries(root, base, tree)
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
