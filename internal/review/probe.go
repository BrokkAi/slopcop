package review

import (
	"context"
	"fmt"
	"os"
	"strings"
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

// probeTimeout bounds one reviewer's probe, from adapter launch to its reply.
// The first run of an adapter includes its npx download.
var probeTimeout = 3 * time.Minute

// Probe checks families without changing the configured review list.
//
// Families that share an adapter command are probed one after another, so a
// first-run npx install of one package never overlaps itself; different
// adapters are probed at the same time. done, when not nil, is called once per
// family as soon as its result is known, never concurrently.
func Probe(ctx context.Context, root, logDir string, families []Family, settings Settings, done func(ProbeResult)) []ProbeResult {
	results := make([]ProbeResult, len(families))
	var mu sync.Mutex
	report := func(i int, r ProbeResult) {
		mu.Lock()
		defer mu.Unlock()
		results[i] = r
		if done != nil {
			done(r)
		}
	}
	if err := os.MkdirAll(logDir, 0700); err != nil {
		for i, f := range families {
			report(i, ProbeResult{Family: f, Reason: err.Error()})
		}
		return results
	}
	var groups [][]int
	byCommand := map[string]int{}
	commands := make([][]string, len(families))
	for i, f := range families {
		argv := settings.Adapters[AdapterName(f)]
		if len(argv) == 0 {
			if reason := missingCredential(f); reason != "" {
				report(i, ProbeResult{Family: f, Reason: reason})
				continue
			}
			argv = DefaultCommand(f)
		}
		commands[i] = argv
		key := strings.Join(argv, "\x00")
		g, ok := byCommand[key]
		if !ok {
			g = len(groups)
			byCommand[key] = g
			groups = append(groups, nil)
		}
		groups[g] = append(groups[g], i)
	}
	var wg sync.WaitGroup
	for _, group := range groups {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, i := range group {
				report(i, probeOne(ctx, root, logDir, commands[i], families[i], fmt.Sprintf("%d-%s", i+1, families[i])))
			}
		}()
	}
	wg.Wait()
	return results
}

// probeOne starts one adapter, prepares a restricted session and sends it a
// one-word prompt.
func probeOne(ctx context.Context, root, logDir string, argv []string, f Family, name string) ProbeResult {
	r := ProbeResult{Family: f}
	s, err := startRunner(ctx, root, argv, f, name, logDir, probeTimeout)
	if err == nil {
		err = s.prepare(root)
	}
	if err != nil {
		r.Reason = err.Error()
		if s != nil {
			s.stop()
		}
		return r
	}
	r.Model = s.result.Model
	reply, err := s.prompt("Reply with exactly the word ok and nothing else. Do not use any tools.")
	switch {
	case err != nil:
		r.Reason = err.Error()
	case reply.Text == "":
		r.Reason = "empty reply"
	default:
		r.Available = true
	}
	return r
}

// missingCredential names what a family's default adapter needs and lacks,
// so the probe skips it instead of downloading an adapter that cannot work.
// A configured adapter command may carry its own credentials, so it is always
// probed.
func missingCredential(f Family) string {
	if f == Flash && os.Getenv("DEEPSEEK_API_KEY") == "" {
		return "DEEPSEEK_API_KEY is not set"
	}
	return ""
}
