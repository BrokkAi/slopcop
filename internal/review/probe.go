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
			err = s.prepare(root)
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
