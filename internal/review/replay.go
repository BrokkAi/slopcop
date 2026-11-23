package review

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/BrokkAi/slopcop/internal/gitx"
)

// LastRecord reads the latest review of the repository at root without taking
// the state lock, so it works while a check runs; last.json is replaced by a
// rename, never written in place. It returns nil when no review has run.
func LastRecord(root string) (*Record, error) {
	gitDir, err := gitx.Run(root, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	return (&Store{dir: filepath.Join(gitDir, "slopcop")}).Last()
}

// What became of the override of a whole review.
const (
	// OverrideAccepted: the override applies to the working tree as it is now.
	OverrideAccepted = "accepted"
	// OverrideTreeChanged: the working tree is no longer the one accepted.
	OverrideTreeChanged = "tree_changed"
	// OverrideWithdrawn: the tree's push approval is gone, or a finding
	// override the review relied on was undone.
	OverrideWithdrawn = "withdrawn"
	// OverrideUnverified: the override could not be compared with the
	// repository, so it is not claimed to apply.
	OverrideUnverified = "unverified"
)

// OverrideState says whether a review's override applies right now.
type OverrideState struct {
	// Status is one of the Override constants.
	Status string `json:"status"`
	Reason string `json:"reason"`
	// Tree is the working-tree snapshot the override accepted.
	Tree string `json:"tree"`
	// CurrentTree is the working tree now, when it differs from Tree.
	CurrentTree string    `json:"currentTree,omitempty"`
	Time        time.Time `json:"time"`
}

// OverrideState reports on the record's override, or nil when it has none. It
// only reads: the current tree is computed without adding objects to the
// repository, and no approval is recorded or withdrawn. With an error, the
// state is OverrideUnverified.
func (r Record) OverrideState(root string) (*OverrideState, error) {
	accepted := r.Accepted
	if accepted == nil {
		var err error
		if accepted, err = earlierReleaseOverride(root, r); err != nil || accepted == nil {
			return nil, err
		}
	}
	state := &OverrideState{Status: OverrideUnverified, Reason: accepted.Reason, Tree: r.Capture.Tree, Time: accepted.Time}
	current, err := gitx.WorkingTreeID(root)
	if err != nil {
		return state, err
	}
	if current != r.Capture.Tree {
		state.Status, state.CurrentTree = OverrideTreeChanged, current
		return state, nil
	}
	approved, err := Reviewed(root, r.Capture.Tree)
	if err != nil {
		return state, err
	}
	hold, err := OverridesHold(root, r.Verdict)
	if err != nil {
		return state, err
	}
	state.Status = OverrideWithdrawn
	if approved && hold {
		state.Status = OverrideAccepted
	}
	return state, nil
}

// earlierReleaseOverride finds an override of r made by a release that logged
// it without marking the record. Such a release left the record exactly as
// its log entry holds it, so only the log's last entry, unmarked and equal to
// r, is this review's override. A torn last line is an override that failed.
func earlierReleaseOverride(root string, r Record) (*AcceptedOverride, error) {
	if !r.Verdict.Overridable() {
		return nil, nil
	}
	gitDir, err := gitx.Run(root, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(gitDir, "slopcop", overrideLog))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	b = bytes.TrimSpace(b)
	var entry overrideLogEntry
	if json.Unmarshal(b[bytes.LastIndexByte(b, '\n')+1:], &entry) != nil || entry.Record == nil || entry.Record.Accepted != nil {
		return nil, nil
	}
	logged, err := json.Marshal(entry.Record)
	if err != nil {
		return nil, err
	}
	current, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(logged, current) {
		return nil, nil
	}
	return &AcceptedOverride{Reason: strings.TrimSpace(entry.Reason), Time: entry.Time}, nil
}

// Replay rebuilds each reviewer's reply from its ACP log with this release's
// message assembly, then its status and the verdict's, without running a
// reviewer. A reviewer whose log is gone or unreadable keeps its recorded
// reply, and the error says which. Errors recorded at review time stay. The
// minimum number of finished reviewers is not recorded, so a reviewer that
// finished at review time but has no reply in its log fails the verdict
// unless another reviewer has findings.
func Replay(v Verdict) (Verdict, []error) {
	var errs []error
	v.Reviewers = append([]Reviewer(nil), v.Reviewers...)
	for i := range v.Reviewers {
		r := &v.Reviewers[i]
		if r.Log == "" {
			continue
		}
		text, err := replayLog(r.Log)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: kept the recorded reply: %w", r.Name, err))
			continue
		}
		finished := r.Status != StatusFailed
		r.Text = text
		r.Status = parseVerdict(*r)
		if finished && r.Status == StatusFailed {
			v.Incomplete = true
		}
	}
	findings := false
	for _, r := range v.Reviewers {
		findings = findings || r.Status == StatusFindings
	}
	switch {
	case findings:
		v.Status = StatusFindings
	case v.Status != StatusFindings && v.Status != StatusClean:
	case v.Incomplete:
		v.Status = StatusFailed
	default:
		v.Status = StatusClean
	}
	return v, errs
}

// replayLog feeds the session/update notifications of a reviewer's inbound
// ACP stream to a reviewClient and returns the reply it assembles.
func replayLog(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	c := &reviewClient{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	for scanner.Scan() {
		var message struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &message) != nil || message.Method != acp.ClientMethodSessionUpdate {
			continue
		}
		var n acp.SessionNotification
		// An update this SDK cannot decode carries no reply text.
		if json.Unmarshal(message.Params, &n) != nil {
			continue
		}
		if err := c.SessionUpdate(context.Background(), n); err != nil {
			return "", err
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return c.reply(), nil
}
