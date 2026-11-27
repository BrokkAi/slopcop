package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BrokkAi/slopcop/internal/gitx"
)

// ErrBusy means another process owns the checkout's review state lock.
var ErrBusy = errors.New("another slopcop review or override is running in this checkout")

// storeLockPoll is how often a waiting process tries the review state lock.
const storeLockPoll = 100 * time.Millisecond

// Store owns the checkout's review state lock until Close. The state is under
// the worktree's own git directory, so worktrees do not contend for it.
type Store struct {
	root, dir string
	lock      *os.File
}

// OpenStore creates the checkout's state directory and takes its exclusive
// lock, or returns ErrBusy at once if another process holds it.
func OpenStore(root string) (*Store, error) {
	store, _, err := WaitStore(context.Background(), root, 0, nil)
	return store, err
}

// WaitStore is OpenStore that waits up to limit for another process to release
// the lock, calling waiting once if it has to. It returns ErrBusy when the
// limit runs out and ctx's error when ctx ends first. waited says the lock was
// not free at first, so anything read from the checkout before may be stale.
func WaitStore(ctx context.Context, root string, limit time.Duration, waiting func()) (store *Store, waited bool, err error) {
	gitDir, err := gitx.Run(root, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, false, err
	}
	dir := filepath.Join(gitDir, "slopcop")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, false, err
	}
	deadline := time.Now().Add(limit)
	for {
		err = lockFile(f)
		if err == nil {
			return &Store{root, dir, f}, waited, nil
		}
		if !lockBusy(err) {
			f.Close()
			return nil, waited, fmt.Errorf("lock review state: %w", err)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			f.Close()
			return nil, waited, ErrBusy
		}
		if !waited {
			waited = true
			if waiting != nil {
				waiting()
			}
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, waited, ctx.Err()
		case <-time.After(min(remaining, storeLockPoll)):
		}
	}
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
func (s *Store) write(name string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
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

// reviewStatus keeps any finding, and is otherwise clean when at least min
// reviewers finished, so one reviewer that keeps failing cannot block a
// change the others reviewed. The failure stays in the reviewer's result.
// incomplete reports fewer than min finished reviewers, findings or not.
func reviewStatus(rs []Reviewer, min int) (status Status, incomplete bool) {
	completed, findings := 0, false
	for _, r := range rs {
		switch parseVerdict(r) {
		case StatusFindings:
			completed++
			findings = true
		case StatusClean:
			completed++
		}
	}
	incomplete = completed < max(min, 1)
	switch {
	case findings:
		return StatusFindings, incomplete
	case incomplete:
		return StatusFailed, true
	}
	return StatusClean, false
}

// groupVerdict fails on any reviewer failure and retains any finding. An
// override and the reuse of an earlier review need every reviewer complete.
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

// Overridable says whether `slopcop override` can accept the verdict's
// findings as far as the review goes: it has findings and every reviewer
// finished and read the change. The static half must also not block the push.
func (v Verdict) Overridable() bool {
	return v.Status == StatusFindings && groupVerdict(v.Reviewers) == StatusFindings && v.unreadChange() == ""
}

// Save atomically stores a record, adds its findings to the checked-out
// branch's rounds for later reviews, and forgets those rounds once a review
// is clean.
func (s *Store) Save(r Record) error { return s.saveOn(s.branch(), r) }

// saveOn is Save for a review of a snapshot taken on the named branch. Its
// rounds stay that branch's when the checkout has moved to another one since.
func (s *Store) saveOn(branch string, r Record) error {
	if err := s.recordRound(branch, r); err != nil {
		return err
	}
	if r.Status == StatusClean {
		if err := s.clearRounds(branch, r.Capture); err != nil {
			return err
		}
	}
	return s.write("last.json", r)
}

// SetStatic records the hosted half's exit and status for override checks.
// runFailed says the half blocks only because an analyzer run failed.
func (s *Store) SetStatic(code int, status string, blocks, runFailed bool) error {
	r, err := s.Last()
	if err != nil {
		return err
	}
	if r == nil {
		return errors.New("no last review")
	}
	r.StaticExit = &code
	r.StaticStatus = status
	r.StaticBlocksPush = blocks
	r.StaticRunFailed = blocks && runFailed
	return s.write("last.json", *r)
}

// SetCheck records the hosted check that ran beside the review v, when the
// latest record is that review. A record of anything else keeps its own.
func (s *Store) SetCheck(ref CheckRef, v Verdict) error {
	r, err := s.Last()
	if err != nil || r == nil {
		return err
	}
	if r.Capture.Tree != v.Capture.Tree || !r.Time.Equal(v.Time) {
		return nil
	}
	r.Check = &ref
	return s.write("last.json", *r)
}

// SetStaticRequired records on the review v, when the latest record is that
// review, that the static half could have run beside it. A record of
// anything else keeps its own.
func (s *Store) SetStaticRequired(v Verdict) error {
	r, err := s.Last()
	if err != nil || r == nil {
		return err
	}
	if r.Capture.Tree != v.Capture.Tree || !r.Time.Equal(v.Time) {
		return nil
	}
	r.StaticRequired = true
	return s.write("last.json", *r)
}

// ErrStaticRequired refuses to approve a tree on a review-only record where
// the static half could have run.
var ErrStaticRequired = errors.New("the last check ran only the review (--mode ai), and this repository can run the static check; run `slopcop check` so both halves check the tree, then override what remains")

// overrideLog is the append-only log of whole-review overrides.
const overrideLog = "overrides.jsonl"

// overrideLogEntry is one line of overrideLog. Record has Accepted set, except
// in an entry from a release that did not record it.
type overrideLogEntry struct {
	Time   time.Time `json:"time"`
	Reason string    `json:"reason"`
	Record *Record   `json:"record"`
}

// StaticRunFailedOnly says the record's static half blocks the push only
// because an analyzer run failed, which an override may accept.
func (r Record) StaticRunFailedOnly() bool {
	return r.StaticExit != nil && r.StaticBlocksPush && r.StaticRunFailed
}

// Override logs a justified acceptance, keeps the reason for later rounds,
// and marks only the reviewed tree approved. It accepts the findings of a
// complete review, and a static half that blocks only because its analyzer
// run failed when the review passed or its findings are accepted with it.
func (s *Store) Override(reason, currentTree string) error {
	return s.override(reason, currentTree, nil)
}

// overridable returns the latest record when an override may accept it for
// currentTree, and why not otherwise. It writes nothing.
func (s *Store) overridable(currentTree string) (*Record, error) {
	r, err := s.Last()
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.New("override requires complete findings")
	}
	if r.StaticRequired {
		return nil, ErrStaticRequired
	}
	runFailed := r.StaticRunFailedOnly()
	if r.StaticExit != nil && r.StaticBlocksPush && !runFailed {
		if r.StaticStatus == "findings" {
			return nil, errors.New("static finding at or above the push block level does not allow override")
		}
		return nil, fmt.Errorf("static status %s does not allow override", r.StaticStatus)
	}
	// The check recorded runFailed only beside a review it judged to pass,
	// an empty one included, or whose findings are overridable.
	if !r.Verdict.Overridable() && !runFailed {
		return nil, errors.New("override requires complete findings")
	}
	if currentTree != r.Capture.Tree {
		return nil, errors.New("working tree differs from reviewed tree")
	}
	// The review may have left a finding out, or its reviewers may have been
	// told to, because of a finding override since undone.
	if hold, err := OverridesHold(s.root, r.Verdict); err != nil {
		return nil, err
	} else if !hold {
		return nil, errors.New("a finding override this review relied on was undone since; run slopcop check again before overriding")
	}
	return r, nil
}

// override is Override, recording the proposal it accepts when there is one.
func (s *Store) override(reason, currentTree string, proposal *AcceptedProposal) error {
	if strings.TrimSpace(reason) == "" {
		return errors.New("override requires a nonempty reason")
	}
	r, err := s.overridable(currentTree)
	if err != nil {
		return err
	}
	if proposal != nil && !proposal.matches(*r) {
		return ErrProposalStale
	}
	accepted := *r
	accepted.Accepted = &AcceptedOverride{Reason: strings.TrimSpace(reason), Time: time.Now().UTC(), Proposal: proposal}
	f, err := os.OpenFile(filepath.Join(s.dir, overrideLog), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	err = json.NewEncoder(f).Encode(overrideLogEntry{accepted.Accepted.Time, reason, &accepted})
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
	// Later rounds show reviewers the reason beside the findings it answers.
	// A clean review has none, and the reason is about the static half.
	if r.Verdict.Status == StatusFindings {
		if err = s.overrideRound(*r, reason); err != nil {
			return err
		}
	}
	// Recorded before the approval: if the approval then fails, the record
	// names an override that OverrideState reports as not in effect.
	if err = s.write("last.json", accepted); err != nil {
		return err
	}
	mark := MarkReviewed
	if r.Mode == "ai" {
		mark = MarkReviewedWithoutStatic
	}
	if err = mark(s.root, currentTree); err != nil {
		return err
	}
	// An undo that ran after the check above withdrew the approvals it knew
	// of, not this one; then this one does not stand either.
	if hold, err := OverridesHold(s.root, r.Verdict); err != nil || !hold {
		if unmarkErr := Unmark(s.root, currentTree); unmarkErr != nil {
			return unmarkErr
		}
		if err != nil {
			return err
		}
		return errors.New("a finding override this review relied on was undone while overriding; run slopcop check again")
	}
	return nil
}

// reviewOnlyRef marks an approval that only the review gave, with no static
// half beside it. The push gate refuses such an approval once the static half
// can run, so `--mode ai` stands in for it only while it cannot.
func reviewOnlyRef(tree string) string { return "refs/slopcop/review-only/" + tree }

// MarkReviewed creates the approval ref for a reviewed tree, and withdraws
// any mark that a review alone approved it.
func MarkReviewed(root, tree string) error {
	return updateRefs(root, "delete "+reviewOnlyRef(tree), "update "+approvalRef(tree)+" "+tree)
}

// MarkReviewedWithoutStatic creates the approval ref for a tree only a review
// checked, marked as such for the push gate.
func MarkReviewedWithoutStatic(root, tree string) error {
	return updateRefs(root, "update "+reviewOnlyRef(tree)+" "+tree, "update "+approvalRef(tree)+" "+tree)
}

// ReviewedWithoutStatic reports whether the tree's approval came from a
// review alone.
func ReviewedWithoutStatic(root, tree string) (bool, error) {
	return refExists(root, reviewOnlyRef(tree))
}

// Unmark removes the approval ref for a tree. Deleting an absent ref succeeds.
func Unmark(root, tree string) error {
	return updateRefs(root, "delete "+approvalRef(tree), "delete "+reviewOnlyRef(tree))
}

func approvalRef(tree string) string { return "refs/slopcop/reviewed/" + tree }

// updateRefs applies update-ref commands in one transaction, so an approval
// and its review-only mark never part, even when checks in two worktrees of
// the same repository, which share these refs but not a review lock, change
// them at once.
func updateRefs(root string, commands ...string) error {
	_, err := gitx.RunInput(root, nil, []byte(strings.Join(commands, "\n")+"\n"), "update-ref", "--stdin")
	return err
}

func refExists(root, ref string) (bool, error) {
	found, err := gitx.Run(root, "for-each-ref", "--format=%(refname)", ref)
	if err != nil {
		return false, err
	}
	return found == ref, nil
}

// Reviewed reports whether the exact tree has an approval ref.
func Reviewed(root, tree string) (bool, error) {
	return refExists(root, "refs/slopcop/reviewed/"+tree)
}

// NewCapture resolves the base tree and records the diffstat for a snapshot
// of the checked-out branch taken just now.
func NewCapture(root, baseCommit, tree string) (Capture, error) {
	return NewFilteredCapture(root, gitx.CurrentBranch(root), baseCommit, tree, tree)
}

// NewFilteredCapture keeps the complete snapshot's identity while recording
// the smaller tree whose changes are in review scope. branch is the branch
// that was checked out when the snapshot was taken, "" for a detached HEAD.
func NewFilteredCapture(root, branch, baseCommit, tree, diffTree string) (Capture, error) {
	return NewCaptureWithBaseTree(root, branch, baseCommit, "", tree, diffTree, nil, "")
}

func NewCaptureWithBaseTree(root, branch, baseCommit, baseTree, tree, diffTree string, mergedIn []string, unattributed string) (Capture, error) {
	var err error
	if baseTree == "" {
		baseTree, err = gitx.Run(root, "rev-parse", baseCommit+"^{tree}")
		if err != nil {
			return Capture{}, err
		}
	}
	if diffTree == "" {
		diffTree = tree
	}
	stat, err := gitx.RunWithoutRepoAttributes(root, "diff", "--no-ext-diff", "--no-textconv", "--ignore-submodules=none", "--numstat", baseTree, diffTree)
	capture := Capture{BaseCommit: baseCommit, BaseTree: baseTree, Tree: tree, Numstat: strings.TrimSpace(string(stat)), Branch: branch, MergedIn: mergedIn, Unattributed: unattributed}
	for _, line := range strings.Split(capture.Numstat, "\n") {
		cols := strings.Split(line, "\t")
		if len(cols) >= 3 {
			a, _ := strconv.Atoi(cols[0])
			d, _ := strconv.Atoi(cols[1])
			capture.ChangedLines += a + d
		}
	}
	if diffTree != tree {
		capture.DiffTree = diffTree
	}
	return capture, err
}
