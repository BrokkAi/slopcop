package review

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
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
		v.Error = "no reviewers are configured: run `slopcop init` to pick them from what is available, or set review.reviewers in the user config"
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
	minCompleted := opts.Settings.minCompleted()
	in := discoverIntent(opts.Intent)
	v.IntentSource = in.Source
	commands := map[Family][]string{}
	for _, f := range slices.Concat(order, opts.Settings.Fallbacks) {
		cmd := opts.Settings.Adapters[AdapterName(f)]
		if len(cmd) == 0 {
			cmd = DefaultCommand(f)
		}
		commands[f] = cmd
	}
	keyData, _ := json.Marshal([]any{capture.BaseTree, capture.Tree, capture.CheckedTree(), in.Text, opts.Version, order, opts.Settings.Fallbacks, minCompleted, commands})
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
	p := &panel{ctx: ctx, root: opts.Root, dir: dir, timeout: timeout, commands: commands, min: minCompleted, fallbacks: slices.Clone(opts.Settings.Fallbacks),
		prompt: func(f Family) string { return reviewPrompt(opts.Root, capture, in, findings, string(diff), f) }}
	v.Reviewers = p.run(order)
	v.Status, v.Incomplete = reviewStatus(v.Reviewers, minCompleted)
	r := Record{Mode: opts.Mode, Verdict: v, ReuseKey: key}
	if err = store.Save(r); err != nil {
		v.Status = StatusFailed
		v.Error = "save review: " + err.Error()
	}
	return v
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
