package review

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/BrokkAi/slopcop/internal/gitx"
)

// Run performs or reuses a review; setup failures become a failed Verdict.
func Run(ctx context.Context, store *Store, capture Capture, opts Options) Verdict {
	v := Verdict{Status: StatusFailed, Capture: capture, Time: time.Now().UTC()}
	key := ""
	save := func() {
		if err := store.Save(Record{Mode: opts.Mode, Verdict: v, ReuseKey: key}); err != nil {
			v.Status = StatusFailed
			v.Error = "save state: " + err.Error()
		}
	}
	if len(opts.Settings.Reviewers) == 0 {
		v.Error = "no reviewers are configured: run `slopcop init` in a repository to pick them from what is available, or set review.reviewers in the user config"
		save()
		return v
	}
	if capture.Empty() {
		v.Status = StatusEmpty
		save()
		return v
	}
	timeout := opts.Settings.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	order := opts.Settings.Reviewers
	in := discoverIntent(opts.Intent)
	v.IntentSource = in.Source
	commands := map[Family][]string{}
	for _, f := range order {
		cmd := opts.Settings.Adapters[AdapterName(f)]
		if len(cmd) == 0 {
			cmd = DefaultCommand(f)
		}
		commands[f] = cmd
	}
	keyData, _ := json.Marshal([]any{capture.BaseTree, capture.Tree, capture.CheckedTree(), in.Text, opts.Version, order, commands})
	hash := sha256.Sum256(keyData)
	key = hex.EncodeToString(hash[:])
	previous, err := store.Last()
	if err != nil {
		v.Error = "read last review: " + err.Error()
		return v
	}
	// Only a review every reviewer finished is reused; a reviewer that
	// failed gets another chance on the next check.
	if previous != nil && (previous.Status == StatusClean || previous.Status == StatusFindings) && groupVerdict(previous.Reviewers) == previous.Status && previous.ReuseKey == key {
		v = previous.Verdict
		v.Reused = true
		save()
		return v
	}
	findings, err := store.read("findings.json")
	if err != nil {
		v.Error = "read previous findings: " + err.Error()
		return v
	}
	// A failed record invalidates override eligibility before any adapter starts.
	if err = store.Save(Record{Mode: opts.Mode, Verdict: v}); err != nil {
		v.Error = "invalidate last review: " + err.Error()
		return v
	}
	dir, err := os.MkdirTemp(store.dir, "review-"+time.Now().UTC().Format("20060102T150405")+"-")
	if err != nil {
		v.Error = "create review log directory: " + err.Error()
		save()
		return v
	}
	v.LogDir = dir
	TrimLogs(store.dir)
	diff, err := gitx.RunBytes(opts.Root, nil, "diff", "--no-ext-diff", "--no-textconv", capture.BaseTree, capture.CheckedTree())
	if err != nil {
		v.Error = "read captured diff: " + err.Error()
		save()
		return v
	}
	prompt := reviewPrompt(opts.Root, capture, in, findings, string(diff), order[0])
	if err = os.WriteFile(filepath.Join(dir, "prompt.txt"), []byte(prompt), 0600); err != nil {
		v.Error = "write review prompt: " + err.Error()
		save()
		return v
	}
	ready := make([]*runner, 0, len(order))
	v.Reviewers = make([]Reviewer, len(order))
	failed := false
	for i, f := range order {
		name := fmt.Sprintf("%d-%s", i+1, f)
		s, err := startRunner(ctx, opts.Root, commands[f], f, name, dir, timeout)
		if err == nil {
			err = s.prepare(opts.Root)
		}
		if err != nil {
			failed = true
			v.Reviewers[i] = Reviewer{Name: name, Family: f, Status: StatusFailed, Error: err.Error()}
			if s != nil {
				v.Reviewers[i] = s.result
				v.Reviewers[i].Status = StatusFailed
				v.Reviewers[i].Error = err.Error()
				s.stop()
			}
			continue
		}
		ready = append(ready, s)
	}
	if failed {
		for _, s := range ready {
			s.stop()
		}
		for i, r := range v.Reviewers {
			if r.Name == "" {
				v.Reviewers[i] = Reviewer{Name: fmt.Sprintf("%d-%s", i+1, order[i]), Family: order[i], Status: StatusFailed, Error: "review stopped because another reviewer could not start"}
			}
		}
		save()
		return v
	}
	var wg sync.WaitGroup
	var startMu sync.Mutex
	for i, s := range ready {
		wg.Add(1)
		go func() {
			defer wg.Done()
			prompt := reviewPrompt(opts.Root, capture, in, findings, string(diff), s.result.Family)
			r, err := s.prompt(prompt)
			if parseVerdict(r) == StatusFailed && retryable(ctx, err) {
				r = retryReviewer(ctx, opts.Root, commands[r.Family], r, dir, timeout, prompt, &startMu)
			}
			v.Reviewers[i] = r
		}()
	}
	wg.Wait()
	v.Status = reviewStatus(v.Reviewers, opts.Settings.MinCompleted)
	r := Record{Mode: opts.Mode, Verdict: v, ReuseKey: key}
	if err = store.Save(r); err != nil {
		v.Status = StatusFailed
		v.Error = "save review: " + err.Error()
	}
	return v
}

// retryable reports whether a failed reviewer may get a second attempt. A
// timeout would only double the wait, a canceled check wants no more work,
// and a missing login fails the same way again.
func retryable(ctx context.Context, err error) bool {
	var requestErr *acp.RequestError
	if errors.As(err, &requestErr) && requestErr.Code == authRequiredCode {
		return false
	}
	return ctx.Err() == nil && !errors.Is(err, context.DeadlineExceeded)
}

// authRequiredCode is the ACP error code for an adapter that needs a login.
const authRequiredCode = -32000

// failureText says why a reviewer produced no verdict.
func failureText(r Reviewer) string {
	if r.Error != "" {
		return r.Error
	}
	return "the reviewer replied with no text"
}

// retryReviewer runs a reviewer that ended without a verdict once more, in a
// fresh adapter and session, with a note saying why the first attempt
// failed. A timed-out or canceled reviewer is not retried, and a reviewer
// that reported findings never reaches here.
func retryReviewer(ctx context.Context, root string, argv []string, first Reviewer, dir string, timeout time.Duration, prompt string, startMu *sync.Mutex) Reviewer {
	why := failureText(first)
	// Adapters start one at a time, as in Run, so npx installs never overlap.
	startMu.Lock()
	s, err := startRunner(ctx, root, argv, first.Family, first.Name+"-retry", dir, timeout)
	if err == nil {
		if err = s.prepare(root); err != nil {
			s.stop()
		}
	}
	startMu.Unlock()
	var r Reviewer
	if err != nil {
		r = Reviewer{Family: first.Family, Model: first.Model, Effort: first.Effort, Error: "retry could not start: " + err.Error()}
		if s != nil {
			r.Log = s.result.Log
		}
	} else {
		r, _ = s.prompt(prompt + retryNote(why))
	}
	r.Name = first.Name
	r.FirstError, r.FirstLog = why, first.Log
	r.Status = parseVerdict(r)
	if r.Status == StatusFailed {
		r.Error = fmt.Sprintf("%s (retried once; the first attempt failed with: %s). Run the check again to retry %s; if it keeps failing, its logs say why", failureText(r), why, first.Family)
	}
	return r
}

// TrimLogs keeps the newest twenty review and the newest twenty probe log
// directories in a state directory, by timestamped name.
func TrimLogs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, prefix := range []string{"review-", "probe-"} {
		var names []string
		for _, v := range entries {
			if v.IsDir() && strings.HasPrefix(v.Name(), prefix) {
				names = append(names, v.Name())
			}
		}
		sort.Strings(names)
		for len(names) > 20 {
			_ = os.RemoveAll(filepath.Join(dir, names[0]))
			names = names[1:]
		}
	}
}
