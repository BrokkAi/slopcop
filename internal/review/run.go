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
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
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
	if capture.Empty() {
		v.Status = StatusEmpty
		save()
		return v
	}
	count := opts.Settings.Count
	if count == 0 {
		count = DefaultCount
	}
	timeout := opts.Settings.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	order := familyOrder(opts.AuthorModels)
	in := discoverIntent(opts.Intent)
	v.IntentSource = in.Source
	commands := map[Family][]string{}
	for _, f := range order {
		cmd := opts.Settings.Adapters[f]
		if len(cmd) == 0 {
			cmd = DefaultCommand(f)
		}
		commands[f] = cmd
	}
	keyData, _ := json.Marshal([]any{capture.BaseTree, capture.Tree, in.Text, opts.Version, count, order, commands})
	hash := sha256.Sum256(keyData)
	key = hex.EncodeToString(hash[:])
	previous, err := store.Last()
	if err != nil {
		v.Error = "read last review: " + err.Error()
		return v
	}
	if previous != nil && (previous.Status == StatusClean || previous.Status == StatusFindings) && previous.ReuseKey == key {
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
	trimLogs(store.dir)
	prompt := reviewPrompt(opts.Root, capture, in, findings)
	if err = os.WriteFile(filepath.Join(dir, "prompt.txt"), []byte(prompt), 0600); err != nil {
		v.Error = "write review prompt: " + err.Error()
		save()
		return v
	}
	var ready []*runner
	next := 0
	for next < len(order) {
		if len(ready) >= count {
			break
		}
		f := order[next]
		next++
		cmd := commands[f]
		name := fmt.Sprintf("%d-%s", len(ready)+1, f)
		s, err := startRunner(ctx, opts.Root, cmd, f, name, dir, timeout)
		if err == nil {
			err = s.prepare(opts.Root)
		}
		if err != nil {
			v.Unavailable = append(v.Unavailable, Unavailable{f, err.Error()})
			if s != nil {
				s.stop()
			}
			continue
		}
		ready = append(ready, s)
	}
	if len(ready) < count {
		for _, s := range ready {
			s.stop()
		}
		v.Error = fmt.Sprintf("only %d of %d reviewers available", len(ready), count)
		save()
		return v
	}
	v.Reviewers = make([]Reviewer, len(ready))
	promptErrors := make([]error, len(ready))
	var wg sync.WaitGroup
	for i, s := range ready {
		wg.Add(1)
		go func() { defer wg.Done(); v.Reviewers[i], promptErrors[i] = s.prompt(prompt) }()
	}
	wg.Wait()
	for i, promptErr := range promptErrors {
		var requestErr *acp.RequestError
		if !errors.As(promptErr, &requestErr) || requestErr.Code != -32000 {
			continue
		}
		v.Unavailable = append(v.Unavailable, Unavailable{ready[i].result.Family, promptErr.Error()})
		v.Reviewers[i] = Reviewer{}
		for next < len(order) {
			f := order[next]
			next++
			name := fmt.Sprintf("%d-%s", i+1, f)
			s, startErr := startRunner(ctx, opts.Root, commands[f], f, name, dir, timeout)
			if startErr == nil {
				startErr = s.prepare(opts.Root)
			}
			if startErr != nil {
				v.Unavailable = append(v.Unavailable, Unavailable{f, startErr.Error()})
				if s != nil {
					s.stop()
				}
				continue
			}
			result, err := s.prompt(prompt)
			if errors.As(err, &requestErr) && requestErr.Code == -32000 {
				v.Unavailable = append(v.Unavailable, Unavailable{f, err.Error()})
				continue
			}
			v.Reviewers[i] = result
			break
		}
	}
	available := 0
	for _, r := range v.Reviewers {
		if r.Name != "" {
			available++
		}
	}
	if available < count {
		var completed []Reviewer
		for _, r := range v.Reviewers {
			if r.Name != "" {
				completed = append(completed, r)
			}
		}
		v.Reviewers = completed
		v.Error = fmt.Sprintf("only %d of %d reviewers available", available, count)
		save()
		return v
	}
	v.Status = groupVerdict(v.Reviewers)
	r := Record{Mode: opts.Mode, Verdict: v, ReuseKey: key}
	if err = store.Save(r); err != nil {
		v.Status = StatusFailed
		v.Error = "save review: " + err.Error()
	}
	return v
}

// trimLogs keeps the newest twenty review directories by timestamped name.
func trimLogs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, v := range entries {
		if v.IsDir() && len(v.Name()) >= 7 && v.Name()[:7] == "review-" {
			names = append(names, v.Name())
		}
	}
	sort.Strings(names)
	for len(names) > 20 {
		_ = os.RemoveAll(filepath.Join(dir, names[0]))
		names = names[1:]
	}
}
