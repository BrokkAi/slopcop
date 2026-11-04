package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BrokkAi/slopcop/internal/gitx"
)

// ErrBusy means another process owns the nonblocking review state lock.
var ErrBusy = errors.New("another slopcop review or override is running")

// Store owns the repository review state lock until Close.
type Store struct {
	root, dir string
	lock      *os.File
}

// OpenStore creates the repository state directory and takes an exclusive lock.
func OpenStore(root string) (*Store, error) {
	gitDir, err := gitx.Run(root, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(gitDir, "slopcop")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = lockFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("%w: %v", ErrBusy, err)
	}
	return &Store{root, dir, f}, nil
}

// Close releases the lock and closes its file handle.
func (s *Store) Close() error {
	if s == nil || s.lock == nil {
		return nil
	}
	err := unlockFile(s.lock)
	closeErr := s.lock.Close()
	s.lock = nil
	if err != nil {
		return err
	}
	return closeErr
}
func (s *Store) read(name string) (*Record, error) {
	b, err := os.ReadFile(filepath.Join(s.dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Record
	if err = json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Last returns the most recent review record, or nil if none exists.
func (s *Store) Last() (*Record, error) { return s.read("last.json") }
func (s *Store) write(name string, r Record) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, "last-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), filepath.Join(s.dir, name))
}

// parseVerdict treats empty or errored reviewer replies as failed.
func parseVerdict(r Reviewer) Status {
	if r.Error != "" || strings.TrimSpace(r.Text) == "" {
		return StatusFailed
	}
	if strings.TrimSpace(r.Text) == CleanReply {
		return StatusClean
	}
	return StatusFindings
}

// groupVerdict fails on any reviewer failure and retains any finding.
func groupVerdict(rs []Reviewer) Status {
	if len(rs) == 0 {
		return StatusFailed
	}
	out := StatusClean
	for _, r := range rs {
		v := parseVerdict(r)
		if v == StatusFailed {
			return v
		}
		if v == StatusFindings {
			out = v
		}
	}
	return out
}

// Save atomically stores a record and retains findings for corrective reviews.
func (s *Store) Save(r Record) error {
	for _, v := range r.Reviewers {
		if parseVerdict(v) == StatusFindings {
			if err := s.write("findings.json", r); err != nil {
				return err
			}
			break
		}
	}
	if r.Status == StatusClean {
		if err := os.Remove(filepath.Join(s.dir, "findings.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return s.write("last.json", r)
}

// SetStatic records the hosted half's exit and status for override checks.
func (s *Store) SetStatic(code int, status string) error {
	r, err := s.Last()
	if err != nil {
		return err
	}
	if r == nil {
		return errors.New("no last review")
	}
	r.StaticExit = &code
	r.StaticStatus = status
	return s.write("last.json", *r)
}

// Override logs a justified acceptance and marks only the reviewed tree approved.
func (s *Store) Override(reason, currentTree string) error {
	if strings.TrimSpace(reason) == "" {
		return errors.New("override requires a nonempty reason")
	}
	r, err := s.Last()
	if err != nil {
		return err
	}
	if r == nil || r.Status != StatusFindings || groupVerdict(r.Reviewers) != StatusFindings {
		return errors.New("override requires complete findings")
	}
	if r.StaticExit != nil {
		allowed := r.StaticStatus == "clean" || r.StaticStatus == "skipped" || r.StaticStatus == "incomplete"
		if !allowed || *r.StaticExit == 1 {
			status := r.StaticStatus
			if status == "" {
				status = "unknown"
			}
			return fmt.Errorf("static check status %s does not allow override", status)
		}
	}
	if currentTree != r.Capture.Tree {
		return errors.New("working tree differs from reviewed tree")
	}
	f, err := os.OpenFile(filepath.Join(s.dir, "overrides.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	err = json.NewEncoder(f).Encode(struct {
		Time   time.Time `json:"time"`
		Reason string    `json:"reason"`
		Record *Record   `json:"record"`
	}{time.Now().UTC(), reason, r})
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return MarkReviewed(s.root, currentTree)
}

// MarkReviewed creates the approval ref for a reviewed tree.
func MarkReviewed(root, tree string) error {
	_, err := gitx.Run(root, "update-ref", "refs/slopcop/reviewed/"+tree, tree)
	return err
}

// Reviewed reports whether the exact tree has an approval ref.
func Reviewed(root, tree string) (bool, error) {
	ref := "refs/slopcop/reviewed/" + tree
	found, err := gitx.Run(root, "for-each-ref", "--format=%(refname)", ref)
	if err != nil {
		return false, err
	}
	return found == ref, nil
}

// NewCapture resolves the base tree and records the diffstat for a fixed snapshot.
func NewCapture(root, baseCommit, tree string) (Capture, error) {
	base, err := gitx.Run(root, "rev-parse", baseCommit+"^{tree}")
	if err != nil {
		return Capture{}, err
	}
	stat, err := gitx.Run(root, "diff", "--no-ext-diff", "--no-textconv", "--numstat", base, tree)
	return Capture{BaseCommit: baseCommit, BaseTree: base, Tree: tree, Numstat: stat}, err
}
