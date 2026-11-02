package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BrokkAi/slopcop/internal/config"
)

// skillStampName is the file in the skill folder that records which version
// wrote the skill and what each file held, so the next update can name the
// version it replaces and tell local edits from it.
const skillStampName = ".slopcop-skill.json"

// skillVersionUnknown is the version of a skill that matches no release.
const skillVersionUnknown = "unknown"

// releaseNotesURL, followed by a release tag, is that release's notes.
const releaseNotesURL = "https://github.com/BrokkAi/slopcop/releases/tag/"

// releaseTag matches the version of a released build.
var releaseTag = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// releasedSkillFiles are the skill files the releases before the stamp
// installed, by path and SHA-256, each with the newest release that shipped
// it. A skill from those releases has no stamp, so this is how an update
// tells one from a skill with local edits. Later releases write a stamp, so
// this list never grows.
var releasedSkillFiles = map[string]map[string]string{
	"SKILL.md": {
		"b81c9461e0bee46ce2a5739643d02d795259a5e61bec70df5974717d6046a86e": "v0.0.3",
		"d1700486a79987c6b086d7689edecf7ce437386a483b59180649544c31f64a6c": "v0.0.4",
		"b65fbd8c6b9568354905687c8582ac9feb01269f5d3897183cb006f8df161940": "v0.0.5",
		"3fd0cd788bbfc15c0a45b71de37d3775f586b4694e598f39a5502684490656fe": "v0.0.6",
		"a9d45700a229313ed50a91c88fe7cf52990a91628e4bd8fb198cb120ae90a927": "v0.0.7",
		"a7baf7f5be828b734a0b1d0d3b8eb04f9f941087931af4581c4bcf72f063e5a4": "v0.0.8",
		"506acaa83b67693f56aff616f32d0224091d5602462e1b330166ade1840bd7f8": "v0.0.9",
		"8c43eef3165cdd517e4c6a0e8ce97f17a6cdf68332834fd51db79d854b7cc25d": "v0.0.10",
	},
	"agents/openai.yaml": {
		"2b3ebe4e9299ac6c1eed486258fb9789338fb904622773a462ec3f227c180765": "v0.0.10",
	},
}

// skillStamp is the content of the stamp file. Files maps each skill file,
// by slash-separated path, to the SHA-256 it was written with.
type skillStamp struct {
	Version string            `json:"version"`
	Files   map[string]string `json:"files"`
}

func fileDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// writeSkillStamp records files as what version wrote into dir.
func writeSkillStamp(dir, version string, files []skillFile) error {
	stamp := skillStamp{Version: version, Files: map[string]string{}}
	for _, file := range files {
		stamp.Files[filepath.ToSlash(file.relative)] = fileDigest(file.data)
	}
	data, err := json.MarshalIndent(stamp, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the skill stamp: %w", err)
	}
	path := filepath.Join(dir, skillStampName)
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// readSkillStamp reads the stamp in dir. A missing or unreadable stamp is
// reported as none.
func readSkillStamp(dir string) (skillStamp, bool) {
	var stamp skillStamp
	data, err := os.ReadFile(filepath.Join(dir, skillStampName))
	if err != nil || json.Unmarshal(data, &stamp) != nil || stamp.Files == nil {
		return skillStamp{}, false
	}
	return stamp, true
}

// readSkillDir returns every file in dir but the stamp, by slash-separated
// path, with its SHA-256. A missing dir holds no files.
func readSkillDir(dir string) (map[string]string, error) {
	present := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if path == dir && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if relative == skillStampName {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		present[filepath.ToSlash(relative)] = fileDigest(data)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	return present, nil
}

// changedSkillFiles names, sorted, the files the bundle adds, rewrites or no
// longer has, compared with what is present.
func changedSkillFiles(present map[string]string, files []skillFile) []string {
	bundled := map[string]bool{}
	var changed []string
	for _, file := range files {
		path := filepath.ToSlash(file.relative)
		bundled[path] = true
		if present[path] != fileDigest(file.data) {
			changed = append(changed, path)
		}
	}
	for path := range present {
		if !bundled[path] {
			changed = append(changed, path)
		}
	}
	sort.Strings(changed)
	return changed
}

// skillOrigin names the version of the skill present and reports whether it
// has local edits: a file its stamp, or without one any release, did not
// write. Without a stamp, the version is the release whose SKILL.md is there.
func skillOrigin(present map[string]string, stamp skillStamp, stamped bool) (string, bool) {
	edited := false
	for path, digest := range present {
		if stamped {
			edited = edited || stamp.Files[path] != digest
		} else if _, released := releasedSkillFiles[path][digest]; !released {
			edited = true
		}
	}
	from := stamp.Version
	if !stamped {
		from = releasedSkillFiles[skillFileName][present[skillFileName]]
	}
	if from == "" {
		from = skillVersionUnknown
	}
	return from, edited
}

// backupSkill copies the files present in dir into a new folder under the
// skill backup directory, named for the harness and the time, and returns
// that folder.
func backupSkill(dir string, present map[string]string) (string, error) {
	root, err := config.SkillBackupDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", root, err)
	}
	// dir is <harness home>/skills/slopcop.
	harness := strings.TrimPrefix(filepath.Base(filepath.Dir(filepath.Dir(dir))), ".")
	name := harness + "-" + time.Now().UTC().Format("20060102T150405Z")
	backup := filepath.Join(root, name)
	for attempt := 2; ; attempt++ {
		err := os.Mkdir(backup, 0o755)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("create %s: %w", backup, err)
		}
		backup = filepath.Join(root, name+"-"+strconv.Itoa(attempt))
	}
	for path := range present {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
		if err != nil {
			return "", fmt.Errorf("read %s: %w", path, err)
		}
		target := filepath.Join(backup, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", fmt.Errorf("create %s: %w", filepath.Dir(target), err)
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return "", fmt.Errorf("write %s: %w", target, err)
		}
	}
	return backup, nil
}

// removeUnbundled deletes the files present in dir that the bundle does not
// have, and the folders that leaves empty.
func removeUnbundled(dir string, present map[string]string, files []skillFile) error {
	bundled := map[string]bool{}
	for _, file := range files {
		bundled[filepath.ToSlash(file.relative)] = true
	}
	for path := range present {
		if bundled[path] {
			continue
		}
		full := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.Remove(full); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", full, err)
		}
		// A folder that still holds something fails to go, and stays.
		for parent := filepath.Dir(full); parent != dir; parent = filepath.Dir(parent) {
			if os.Remove(parent) != nil {
				break
			}
		}
	}
	return nil
}
