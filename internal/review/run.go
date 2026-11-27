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
	key, contentKey := "", ""
	// The reviewers can run for minutes and the checkout may switch branches
	// meanwhile, so every read and write of the round ledger below names the
	// branch the snapshot was taken on, not the one checked out by then.
	branch := capture.Branch
	save := func() {
		if err := store.saveOn(branch, Record{Mode: opts.Mode, Verdict: v, ReuseKey: key, ContentKey: contentKey}); err != nil {
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
	adapters := map[Family]string{}
	var copilot *copilotCatalog
	for _, f := range slices.Concat(order, opts.Settings.Fallbacks) {
		commands[f] = CommandFor(f, opts.Settings)
		adapters[f] = AdapterFor(f, opts.Settings)
		if adapters[f] == copilotAdapter && copilot == nil {
			copilot = newCopilotCatalog(commands[f])
		}
	}
	keyData, _ := json.Marshal([]any{capture.MergedIn, capture.Unattributed, capture.BaseTree, capture.Tree, capture.CheckedTree(), in.Text, opts.Version, order, opts.Settings.Fallbacks, effortKeys(order, opts.Settings.ReviewerEfforts, opts.Settings), effortKeys(opts.Settings.Fallbacks, opts.Settings.FallbackEfforts, opts.Settings), minCompleted, commands, ollamaIdentity(opts.Settings)})
	hash := sha256.Sum256(keyData)
	key = hex.EncodeToString(hash[:])
	contentData, _ := json.Marshal([]any{capture.MergedIn, capture.Unattributed, capture.BaseTree, capture.Tree, capture.CheckedTree(), opts.Version, order, opts.Settings.Fallbacks, effortKeys(order, opts.Settings.ReviewerEfforts, opts.Settings), effortKeys(opts.Settings.Fallbacks, opts.Settings.FallbackEfforts, opts.Settings), minCompleted, commands, ollamaIdentity(opts.Settings)})
	contentHash := sha256.Sum256(contentData)
	contentKey = hex.EncodeToString(contentHash[:])
	previous, err := store.Last()
	if err != nil {
		v.Error = "read last review: " + err.Error()
		return v
	}
	// The finding overrides are not part of the reuse key: overriding a
	// finding of the last review takes effect on that review, without running
	// it again.
	overrides, err := LoadFindingOverrides(opts.Root)
	if err != nil {
		v.Error = "read review finding overrides: " + err.Error()
		save()
		return v
	}
	rounds, err := store.roundsOn(branch)
	if err != nil {
		v.Error = "read earlier review rounds: " + err.Error()
		return v
	}
	// Like the finding overrides, the earlier rounds' overrides are not part
	// of the reuse key.
	accepted := acceptedFindings(opts.Root, rounds, capture)
	// Only a review every reviewer finished is reused; a reviewer that
	// failed gets another chance on the next check. Its status is worked out
	// again from the replies, since the finding overrides may have changed
	// since. A review whose reviewers were told to leave out a finding whose
	// override was since undone runs again, so they can report it.
	if previous != nil && (previous.Status == StatusClean || previous.Status == StatusFindings) && groupVerdict(previous.Reviewers) != StatusFailed && previous.ReuseKey == key && stillOverridden(previous.PromptOverrides, overrides) {
		v = previous.Verdict
		v.Reused = true
		v.Status, v.Incomplete = reviewStatus(v.Reviewers, minCompleted)
		v.applyOverrides(overrides, accepted)
		v.settleOverrides(opts.Root)
		save()
		return v
	}
	// Asking the agent to amend a commit message or squash commits adds a
	// user message, which changes the intent but not the code. A tree this
	// review already passed and that is still approved for push is not
	// reviewed again only for that: the gate would let it through anyway,
	// and a second run could only withdraw an approval the code still has.
	if previous != nil && previous.Status == StatusClean && groupVerdict(previous.Reviewers) == StatusClean &&
		previous.ContentKey != "" && previous.ContentKey == contentKey && approvedTree(opts.Root, capture.Tree) &&
		stillOverridden(previous.PromptOverrides, overrides) {
		v = previous.Verdict
		v.Reused = true
		v.settleOverrides(opts.Root)
		save()
		return v
	}
	// A failed record invalidates override eligibility before any adapter starts.
	if err = store.saveOn(branch, Record{Mode: opts.Mode, Verdict: v}); err != nil {
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
	overriddenText, shown := overriddenSection(overrides, capture)
	v.PromptOverrides = shown
	history := earlierRounds(opts.Root, rounds, capture) + overriddenText
	p := &panel{ctx: ctx, root: opts.Root, trees: reviewTrees{base: capture.BaseTree, checked: capture.CheckedTree()}, dir: dir, timeout: timeout, commands: commands, adapters: adapters, copilot: copilot, ollamaModel: opts.Settings.Models[Ollama], min: minCompleted, fallbacks: slices.Clone(opts.Settings.Fallbacks), reviewerEfforts: opts.Settings.ReviewerEfforts, fallbackEfforts: slices.Clone(opts.Settings.FallbackEfforts)}
	defer p.cleanupPatches()
	binaryDiffPath := ""
	if hasBinaryNumstat(capture.Numstat) {
		for _, adapter := range adapters {
			if adapter != "codex" {
				continue
			}
			text, extractErr := gitx.BinaryFileText(opts.Root, capture.BaseTree, capture.CheckedTree())
			if extractErr == nil {
				body := text.Text
				if !text.Complete {
					body += "\n[Readable binary diffs truncated; inspect the files with git diff --text.]\n"
				}
				path := filepath.Join(dir, "binary-diffs.txt")
				if os.WriteFile(path, []byte(body), 0600) == nil {
					binaryDiffPath = path
				}
			}
			break
		}
	}
	p.prompt = func(f Family) string {
		patchDir := ""
		if needsPatchDir(adapters[f]) {
			patchDir, _ = p.ensurePatches()
		}
		return reviewPrompt(opts.Root, capture, in, history, patchDir, adapters[f], binaryDiffPath)
	}
	promptPatchDir := ""
	if needsPatchDir(adapters[order[0]]) {
		promptPatchDir, _ = p.ensurePatches()
	}
	prompt := reviewPrompt(opts.Root, capture, in, history, promptPatchDir, adapters[order[0]], binaryDiffPath)
	if err = os.WriteFile(filepath.Join(dir, "prompt.txt"), []byte(prompt), 0600); err != nil {
		v.Error = "write review prompt: " + err.Error()
		save()
		return v
	}
	v.Reviewers = p.run(order)
	v.DurationsMs = p.durations
	v.Status, v.Incomplete = reviewStatus(v.Reviewers, minCompleted)
	v.applyOverrides(overrides, accepted)
	v.settleOverrides(opts.Root)
	r := Record{Mode: opts.Mode, Verdict: v, ReuseKey: key, ContentKey: contentKey}
	if err = store.saveOn(branch, r); err != nil {
		v.Status = StatusFailed
		v.Error = "save review: " + err.Error()
	}
	return v
}

// approvedTree says whether tree is approved for push. A tree whose approval
// cannot be read is treated as not approved, so it is reviewed again.
func approvedTree(root, tree string) bool {
	approved, err := Reviewed(root, tree)
	return err == nil && approved
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

func effortKeys(families []Family, efforts []string, settings Settings) []string {
	out := make([]string, len(families))
	for i, f := range families {
		e := slotEffort(efforts, i)
		if a := AdapterFor(f, settings); !IsChatAdapter(a) && a != ollamaAdapter && a != copilotAdapter {
			e = effectiveEffort(e)
		}
		out[i] = e
	}
	return out
}
