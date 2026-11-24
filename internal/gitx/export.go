package gitx

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ExportLimits bound what ExportTree writes.
type ExportLimits struct {
	// MaxFile is the largest file exported; larger files are left out.
	MaxFile int64
	// MaxTotal is the most bytes exported; a larger tree is an error.
	MaxTotal int64
}

type exportEntry struct {
	object, path string
	size         int64
}

// ExportTree writes the regular files of tree under dest and returns their
// slash-separated paths. It reads blobs directly, so the repository's
// attributes and filters do not change them. Symlinks and submodules are left
// out, as is any path that could leave dest or name a .git directory, because
// tree is untrusted: a symlink could otherwise point a reader at a file
// outside dest.
func ExportTree(ctx context.Context, root, tree, dest string, limits ExportLimits) ([]string, error) {
	listing, err := runContext(ctx, root, nil, nil, "ls-tree", "-r", "-l", "-z", "--full-tree", tree)
	if err != nil {
		return nil, err
	}
	entries, err := exportEntries(listing, limits)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}
	var input bytes.Buffer
	for _, e := range entries {
		input.WriteString(e.object)
		input.WriteByte('\n')
	}
	command := exec.CommandContext(ctx, "git", "-C", root, "cat-file", "--batch")
	command.Stdin = &input
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = command.Start(); err != nil {
		return nil, err
	}
	paths, err := writeBlobs(bufio.NewReader(stdout), entries, dest)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, err
	}
	if err = command.Wait(); err != nil {
		return nil, fmt.Errorf("git cat-file --batch: %s", strings.TrimSpace(stderr.String()+" "+err.Error()))
	}
	return paths, nil
}

// TreeFile is a regular file of a tree.
type TreeFile struct {
	Object, Path string
	Size         int64
}

// TreeFiles lists the regular files of tree up to maxFile bytes, leaving out
// what ExportTree leaves out: symlinks, submodules and unsafe paths.
func TreeFiles(ctx context.Context, root, tree string, maxFile int64) ([]TreeFile, error) {
	listing, err := runContext(ctx, root, nil, nil, "ls-tree", "-r", "-l", "-z", "--full-tree", tree)
	if err != nil {
		return nil, err
	}
	entries, err := exportEntries(listing, ExportLimits{MaxFile: maxFile, MaxTotal: math.MaxInt64})
	if err != nil {
		return nil, err
	}
	files := make([]TreeFile, len(entries))
	for i, e := range entries {
		files[i] = TreeFile{Object: e.object, Path: e.path, Size: e.size}
	}
	return files, nil
}

// Blob returns the contents of a blob, without filters.
func Blob(ctx context.Context, root, object string) ([]byte, error) {
	return runContext(ctx, root, nil, nil, "cat-file", "blob", object)
}

// ChangedFiles returns the slash-separated paths of the regular files that are
// new or different in tree compared with base. Deletions, symlinks and
// submodules are left out.
func ChangedFiles(ctx context.Context, root, base, tree string) ([]string, error) {
	raw, err := runContext(ctx, root, nil, nil, "diff-tree", "-r", "-z", "--no-renames", "--diff-filter=d", base, tree)
	if err != nil {
		return nil, err
	}
	var paths []string
	records := bytes.Split(bytes.TrimSuffix(raw, []byte{0}), []byte{0})
	for i := 0; i+1 < len(records); i += 2 {
		fields := bytes.Fields(records[i])
		if len(fields) != 5 {
			return nil, fmt.Errorf("git diff-tree returned an unreadable entry: %q", records[i])
		}
		if mode := string(fields[1]); mode == "100644" || mode == "100755" {
			paths = append(paths, string(records[i+1]))
		}
	}
	return paths, nil
}

// exportEntries parses `git ls-tree -r -l -z` and keeps what ExportTree writes.
func exportEntries(listing []byte, limits ExportLimits) ([]exportEntry, error) {
	var entries []exportEntry
	var total int64
	for _, record := range bytes.Split(listing, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		meta, path, ok := bytes.Cut(record, []byte{'\t'})
		fields := bytes.Fields(meta)
		if !ok || len(fields) != 4 {
			return nil, fmt.Errorf("git ls-tree returned an unreadable entry: %q", record)
		}
		if mode := string(fields[0]); (mode != "100644" && mode != "100755") || !safeExportPath(string(path)) {
			continue
		}
		size, err := strconv.ParseInt(string(fields[3]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse git blob size for %q: %w", path, err)
		}
		if size > limits.MaxFile {
			continue
		}
		if total += size; total > limits.MaxTotal {
			return nil, fmt.Errorf("the tree is larger than the %d bytes that can be exported", limits.MaxTotal)
		}
		entries = append(entries, exportEntry{object: string(fields[2]), path: string(path), size: size})
	}
	return entries, nil
}

// safeExportPath rejects a path that could leave the export directory or name
// a .git directory, in any case for case-insensitive filesystems.
func safeExportPath(path string) bool {
	if strings.ContainsAny(path, "\\\x00") || !filepath.IsLocal(filepath.FromSlash(path)) {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}

// writeBlobs reads one `git cat-file --batch` record per entry. A path that
// collides with another on a case-insensitive filesystem is left out.
func writeBlobs(in *bufio.Reader, entries []exportEntry, dest string) ([]string, error) {
	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		header, err := in.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("read blob %s: %w", e.object, err)
		}
		fields := strings.Fields(header)
		if len(fields) != 3 || fields[0] != e.object || fields[1] != "blob" || fields[2] != strconv.FormatInt(e.size, 10) {
			return nil, fmt.Errorf("git cat-file returned %q for blob %s", strings.TrimSpace(header), e.object)
		}
		file, err := createExportFile(filepath.Join(dest, filepath.FromSlash(e.path)))
		if err != nil {
			return nil, err
		}
		var out io.Writer = io.Discard
		if file != nil {
			out = file
		}
		_, err = io.CopyN(out, in, e.size)
		if file != nil {
			if closeErr := file.Close(); err == nil {
				err = closeErr
			}
		}
		if err != nil {
			return nil, fmt.Errorf("write %s: %w", e.path, err)
		}
		if _, err = in.Discard(1); err != nil {
			return nil, fmt.Errorf("read blob %s: %w", e.object, err)
		}
		if file != nil {
			paths = append(paths, e.path)
		}
	}
	return paths, nil
}

// createExportFile creates a new file and its parents, returning nil when the
// path collides with one already written.
func createExportFile(path string) (*os.File, error) {
	collided := func(err error) bool { return errors.Is(err, fs.ErrExist) || errors.Is(err, syscall.ENOTDIR) }
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		if collided(err) {
			return nil, nil
		}
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if collided(err) {
			return nil, nil
		}
		return nil, err
	}
	return file, nil
}
