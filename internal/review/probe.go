package review

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

// ProbeResult records whether a reviewer can complete a short prompt.
type ProbeResult struct {
	Family    Family
	Model     string
	Available bool
	Reason    string
}

// DefaultReviewers matches Mjolnir's autoconfiguration for our supported
// adapters: Terra and Sol together, otherwise two of the preferred family.
// Reduced models remain a last resort when no full model is available.
func DefaultReviewers(results []ProbeResult) []Family {
	available := map[Family]bool{}
	for _, r := range results {
		if r.Available {
			available[r.Family] = true
		}
	}
	if available[Terra] && available[Sol] {
		return []Family{Terra, Sol}
	}
	for _, f := range []Family{Sonnet, DeepSeek, Terra, Sol, Haiku, Flash} {
		if available[f] {
			return []Family{f, f}
		}
	}
	return nil
}

// Probe checks families without changing the configured review list.
func Probe(ctx context.Context, root, logDir string, families []Family, settings Settings) []ProbeResult {
	results := make([]ProbeResult, len(families))
	ready := make([]*runner, len(families))
	if err := os.MkdirAll(logDir, 0700); err != nil {
		for i, f := range families {
			results[i] = ProbeResult{Family: f, Reason: err.Error()}
		}
		return results
	}
	for i, f := range families {
		results[i].Family = f
		argv := settings.Adapters[AdapterName(f)]
		if len(argv) == 0 {
			argv = DefaultCommand(f)
		}
		s, err := startRunner(ctx, root, argv, f, fmt.Sprintf("%d-%s", i+1, f), logDir, 3*time.Minute)
		if err == nil {
			// Probe Sol at the Terra/Sol preset's effort. A lone-family
			// fallback can still select the nearest supported effort later.
			effort := "high"
			if f == Sol {
				effort = "medium"
			}
			err = s.prepare(root, effort)
		}
		if err != nil {
			results[i].Reason = err.Error()
			if s != nil {
				s.stop()
			}
			continue
		}
		results[i].Model = s.result.Model
		ready[i] = s
	}
	var wg sync.WaitGroup
	for i, s := range ready {
		if s == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.prompt("Reply with exactly the word ok and nothing else. Do not use any tools.")
			if err != nil {
				results[i].Reason = err.Error()
				return
			}
			if r.Text == "" {
				results[i].Reason = "empty reply"
				return
			}
			results[i].Available = true
		}()
	}
	wg.Wait()
	return results
}
