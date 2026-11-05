package review

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

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
	keyData, _ := json.Marshal([]any{capture.BaseTree, capture.Tree, in.Text, opts.Version, order, commands})
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
	diff, err := gitx.RunBytes(opts.Root, nil, "diff", "--no-ext-diff", "--no-textconv", capture.BaseTree, capture.Tree)
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
	for i, s := range ready {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v.Reviewers[i], _ = s.prompt(reviewPrompt(opts.Root, capture, in, findings, string(diff), s.result.Family))
		}()
	}
	wg.Wait()
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
