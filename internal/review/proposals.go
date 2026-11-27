package review

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BrokkAi/slopcop/internal/gitx"
)

// proposalLog is the append-only log of proposed overrides and of their
// acceptance, beside overrideLog.
const proposalLog = "proposals.jsonl"

// Proposal is an override of a whole review that an agent proposed with
// `slopcop override --propose` and that only a person accepts, with
// `slopcop override --accept`. It approves nothing by itself.
type Proposal struct {
	ID   string    `json:"id"`
	Time time.Time `json:"time"`
	// Tree is the working tree the review checked and the proposal is for.
	Tree string `json:"tree"`
	// ReviewTime and Findings name the review the proposal answers, so a
	// later check of the same tree does not inherit it.
	ReviewTime time.Time `json:"reviewTime"`
	Findings   []string  `json:"findings,omitempty"`
	// Record is a digest of the whole check record the proposal answers, so
	// a later check that reuses the review but changes the static half does
	// not inherit it either.
	Record string `json:"record"`
	Reason string `json:"reason"`
	// AcceptedAt is set once a person accepted it.
	AcceptedAt *time.Time `json:"acceptedAt,omitempty"`
	// Retired is set when a person accepted another proposal for the same
	// check record; it can no longer be accepted.
	Retired bool `json:"retired,omitempty"`
}

// AcceptedProposal is the proposal an override was accepted from, as the
// override record keeps it.
type AcceptedProposal struct {
	ID         string    `json:"id"`
	ProposedAt time.Time `json:"proposedAt"`
	Tree       string    `json:"tree"`
	ReviewTime time.Time `json:"reviewTime"`
	Findings   []string  `json:"findings,omitempty"`
	Record     string    `json:"record"`
}

// ErrNoProposal says there is no proposed override to accept.
var ErrNoProposal = errors.New("no override is proposed for this clone; an agent proposes one with `slopcop override --propose --reason \"...\"`")

// ErrAlreadyAccepted refuses a proposal for a review an override has already
// accepted, such as an older proposal beside the one accepted.
var ErrAlreadyAccepted = errors.New("the latest review's findings are already accepted for this tree; nothing was changed")

// ErrProposalStale refuses a proposal whose tree or review has changed since.
var ErrProposalStale = errors.New("the latest proposed override is for a tree or review that has changed since, so nothing was accepted; run `slopcop check` again and propose again if the findings are still wrong")

// proposalEvent is one line of proposalLog: a proposal, or the acceptance of
// the proposal with ID.
type proposalEvent struct {
	Proposal *Proposal `json:"proposal,omitempty"`
	Accepted string    `json:"accepted,omitempty"`
	// Retired are the proposals the acceptance made moot.
	Retired []string  `json:"retired,omitempty"`
	Time    time.Time `json:"time"`
}

// matches says the proposal answers record r: the same tree, review and
// findings.
func (p AcceptedProposal) matches(r Record) bool {
	return p.Tree == r.Capture.Tree && p.ReviewTime.Equal(r.Time) && slices.Equal(p.Findings, findingIDs(r.Verdict)) &&
		p.Record != "" && p.Record == recordDigest(r)
}

// recordDigest hashes a check record as a proposal saw it, before any
// override accepted it.
func recordDigest(r Record) string {
	r.Accepted = nil
	b, err := json.Marshal(r)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// open says the proposal may still be accepted.
func (p Proposal) open() bool { return p.AcceptedAt == nil && !p.Retired }

func (p Proposal) accepted() AcceptedProposal {
	return AcceptedProposal{ID: p.ID, ProposedAt: p.Time, Tree: p.Tree, ReviewTime: p.ReviewTime, Findings: p.Findings, Record: p.Record}
}

func findingIDs(v Verdict) []string {
	var ids []string
	for _, f := range v.Findings() {
		ids = append(ids, f.ID)
	}
	return ids
}

// Propose records a proposed override of the latest review for currentTree,
// after checking that an override could accept it now. It approves nothing.
func (s *Store) Propose(reason, currentTree string) (Proposal, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Proposal{}, errors.New("a proposed override requires a nonempty reason")
	}
	r, err := s.overridable(currentTree)
	if err != nil {
		return Proposal{}, err
	}
	id := make([]byte, 4)
	if _, err = rand.Read(id); err != nil {
		return Proposal{}, err
	}
	p := Proposal{
		ID: "op-" + hex.EncodeToString(id), Time: time.Now().UTC(), Tree: r.Capture.Tree,
		ReviewTime: r.Time, Findings: findingIDs(r.Verdict), Record: recordDigest(*r), Reason: reason,
	}
	return p, s.appendProposalEvent(proposalEvent{Proposal: &p, Time: p.Time})
}

// Accept accepts the latest proposed override for the latest check record, as Override would with its
// reason, and records on the override that it came from that proposal. It
// refuses when the working tree or the review changed since the proposal.
// Only a person runs it: the push gate's hook refuses it from an agent.
func (s *Store) Accept(currentTree string) (Proposal, error) {
	proposals, err := loadProposals(s.dir)
	if err != nil {
		return Proposal{}, err
	}
	r, err := s.Last()
	if err != nil {
		return Proposal{}, err
	}
	// The newest open proposal for the latest check record: the one
	// PendingProposals lists first.
	var latest *Proposal
	open := false
	for i := len(proposals) - 1; i >= 0 && latest == nil; i-- {
		if !proposals[i].open() {
			continue
		}
		open = true
		if r != nil && proposals[i].accepted().matches(*r) {
			latest = &proposals[i]
		}
	}
	switch {
	case !open:
		return Proposal{}, ErrNoProposal
	case r != nil && r.Accepted != nil:
		return Proposal{}, ErrAlreadyAccepted
	case latest == nil || latest.Tree != currentTree:
		return Proposal{}, ErrProposalStale
	}
	accepted := latest.accepted()
	if err = s.override(latest.Reason, currentTree, &accepted); err != nil {
		return Proposal{}, err
	}
	now := time.Now().UTC()
	latest.AcceptedAt = &now
	var retired []string
	for _, p := range proposals {
		if p.open() && p.Record == latest.Record {
			retired = append(retired, p.ID)
		}
	}
	return *latest, s.appendProposalEvent(proposalEvent{Accepted: latest.ID, Retired: retired, Time: now})
}

// PendingProposals returns the proposed overrides not yet accepted that
// answer the clone's latest review, newest first. It takes no lock, so the
// push gate can read it while a check runs.
func PendingProposals(root string) ([]Proposal, error) {
	gitDir, err := gitx.Run(root, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(gitDir, "slopcop")
	proposals, err := loadProposals(dir)
	if err != nil || len(proposals) == 0 {
		return nil, err
	}
	r, err := (&Store{root: root, dir: dir}).Last()
	if err != nil || r == nil || r.Accepted != nil {
		return nil, err
	}
	var pending []Proposal
	for i := len(proposals) - 1; i >= 0; i-- {
		if p := proposals[i]; p.open() && p.accepted().matches(*r) {
			pending = append(pending, p)
		}
	}
	return pending, nil
}

// loadProposals reads proposalLog in order, with each acceptance applied.
func loadProposals(dir string) ([]Proposal, error) {
	f, err := os.Open(filepath.Join(dir, proposalLog))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var proposals []Proposal
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		var e proposalEvent
		if json.Unmarshal(scanner.Bytes(), &e) != nil {
			continue
		}
		switch {
		case e.Proposal != nil:
			proposals = append(proposals, *e.Proposal)
		case e.Accepted != "":
			for i := range proposals {
				if proposals[i].ID == e.Accepted && proposals[i].AcceptedAt == nil {
					at := e.Time
					proposals[i].AcceptedAt = &at
				}
				if slices.Contains(e.Retired, proposals[i].ID) {
					proposals[i].Retired = true
				}
			}
		}
	}
	return proposals, scanner.Err()
}

func (s *Store) appendProposalEvent(e proposalEvent) error {
	f, err := os.OpenFile(filepath.Join(s.dir, proposalLog), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	err = json.NewEncoder(f).Encode(e)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}
