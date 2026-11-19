package review

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

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
