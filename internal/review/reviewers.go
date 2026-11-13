package review

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// panel runs the configured reviewers. Each reviewer gets one retry, a
// reviewer that fails never stops the others, and a reviewer that still fails
// hands its place to the next unused fallback while fewer than min reviewers
// have finished.
type panel struct {
	ctx      context.Context
	root     string
	dir      string
	timeout  time.Duration
	commands map[Family][]string
	prompt   func(Family) string
	min      int

	// startMu starts adapters one at a time, so first-run npx installs
	// never overlap.
	startMu sync.Mutex

	mu        sync.Mutex
	fallbacks []Family
	used      int
	completed int
	standIns  []Reviewer
}

// run starts the primaries in order, then reviews with all of them at once.
// It returns the primaries' results followed by any fallbacks', in the order
// the fallbacks started.
func (p *panel) run(order []Family) []Reviewer {
	type started struct {
		s   *runner
		err error
	}
	starts := make([]started, len(order))
	for i, f := range order {
		s, err := p.start(fmt.Sprintf("%d-%s", i+1, f), f)
		starts[i] = started{s, err}
	}
	out := make([]Reviewer, len(order))
	var wg sync.WaitGroup
	for i, f := range order {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = p.slot(fmt.Sprintf("%d-%s", i+1, f), f, starts[i].s, starts[i].err)
		}()
	}
	wg.Wait()
	return append(out, p.standIns...)
}

// slot reviews with one primary and, if it fails, with fallbacks in its place.
func (p *panel) slot(name string, f Family, s *runner, startErr error) Reviewer {
	r := p.review(name, f, s, startErr)
	failed := r
	for parseVerdict(failed) == StatusFailed && p.ctx.Err() == nil {
		fb, n, ok := p.takeFallback(failed.Family)
		if !ok {
			break
		}
		failed = p.review(fmt.Sprintf("fallback%d-%s", n, fb), fb, nil, nil)
		failed.StandsInFor = name
		p.mu.Lock()
		p.standIns = append(p.standIns, failed)
		p.mu.Unlock()
	}
	return r
}

// takeFallback hands out the next unused fallback while fewer than min
// reviewers have finished, preferring one on another adapter than the
// reviewer it replaces, so one provider's outage does not take out both.
func (p *panel) takeFallback(replaced Family) (Family, int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.completed >= p.min || p.used == len(p.fallbacks) {
		return "", 0, false
	}
	pick := p.used
	for i := p.used; i < len(p.fallbacks); i++ {
		if AdapterName(p.fallbacks[i]) != AdapterName(replaced) {
			pick = i
			break
		}
	}
	f := p.fallbacks[pick]
	// Keep the unused fallbacks in their configured order.
	copy(p.fallbacks[p.used+1:pick+1], p.fallbacks[p.used:pick])
	p.fallbacks[p.used] = f
	p.used++
	return f, p.used, true
}

// review runs one reviewer with one retry in a fresh adapter and session.
// s and startErr are the reviewer's first start, or nil to start it here.
func (p *panel) review(name string, f Family, s *runner, startErr error) Reviewer {
	r, err := p.attempt(name, f, s, startErr, "")
	if parseVerdict(r) == StatusFailed && retryable(p.ctx, err) {
		first := r
		r, _ = p.attempt(name+"-retry", f, nil, nil, retryNote(failureText(first)))
		r.Name = name
		r.FirstError, r.FirstLog = failureText(first), first.Log
	}
	r.Status = parseVerdict(r)
	if r.Status == StatusFailed {
		r.Error = explainFailure(r)
	} else {
		p.mu.Lock()
		p.completed++
		p.mu.Unlock()
	}
	return r
}

// attempt starts the reviewer when it is not started yet, then prompts it.
func (p *panel) attempt(name string, f Family, s *runner, startErr error, note string) (Reviewer, error) {
	if s == nil && startErr == nil {
		s, startErr = p.start(name, f)
	}
	if startErr != nil {
		r := Reviewer{Name: name, Family: f}
		if s != nil {
			r = s.result
		}
		r.Error = startErr.Error()
		return r, startErr
	}
	return s.prompt(p.prompt(f) + note)
}

// start launches and prepares an adapter. On a prepare failure it returns
// the stopped runner, whose result names the reviewer's log.
func (p *panel) start(name string, f Family) (*runner, error) {
	p.startMu.Lock()
	defer p.startMu.Unlock()
	s, err := startRunner(p.ctx, p.root, p.commands[f], f, name, p.dir, p.timeout)
	if err != nil {
		return nil, err
	}
	if err = s.prepare(p.root); err != nil {
		s.stop()
		return s, err
	}
	return s, nil
}

// retryable reports whether a failed reviewer may get a second attempt. A
// timeout would only double the wait, a canceled check wants no more work,
// and a missing login or exhausted credits fail the same way again.
func retryable(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var requestErr *acp.RequestError
	if errors.As(err, &requestErr) && requestErr.Code == authRequiredCode {
		return false
	}
	if err != nil {
		switch failureCause(err.Error()) {
		case causeAuth, causeQuota:
			return false
		}
	}
	return true
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

type cause string

const (
	causeAuth    cause = "auth"
	causeQuota   cause = "quota"
	causeRate    cause = "rate"
	causeNetwork cause = "network"
)

// failureCause recognizes the failures a person can act on from an adapter's
// error text.
func failureCause(text string) cause {
	t := strings.ToLower(text)
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(t, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has("authentication required", "unauthorized", "not logged in", "not signed in", "invalid api key", "api key not", "login required", "please log in", "please login"):
		return causeAuth
	case has("insufficient_quota", "insufficient quota", "out of credits", "credit balance", "quota exceeded", "exceeded your current quota", "usage limit", "billing", "payment required"):
		return causeQuota
	case has("rate limit", "rate_limit", "too many requests", "status 429", "code 429"):
		return causeRate
	case has("econnrefused", "enotfound", "econnreset", "etimedout", "network error", "connection refused", "connection reset", "no such host", "could not resolve", "service unavailable", "overloaded"):
		return causeNetwork
	}
	return ""
}

// explainFailure turns a reviewer's final error into one that says what to do.
func explainFailure(r Reviewer) string {
	text := failureText(r)
	if r.FirstError != "" && r.FirstError != text {
		text += " (retried once; the first attempt failed with: " + r.FirstError + ")"
	} else if r.FirstError != "" {
		text += " (retried once with the same result)"
	}
	adapter := AdapterName(r.Family)
	switch failureCause(text) {
	case causeAuth:
		return fmt.Sprintf("%s is not signed in: sign in to its %s adapter, then run the check again. %s", r.Family, adapter, text)
	case causeQuota:
		return fmt.Sprintf("%s's provider account is out of credits or over its usage limit: top it up or wait for the limit to reset, or configure review.fallbacks on another provider. %s", r.Family, text)
	case causeRate:
		return fmt.Sprintf("%s's provider is rate limiting requests: wait a minute, then run the check again. %s", r.Family, text)
	case causeNetwork:
		return fmt.Sprintf("%s's provider could not be reached: check the network and the provider's status, then run the check again. %s", r.Family, text)
	}
	return fmt.Sprintf("%s. Run the check again to retry %s; if it keeps failing, its log says why", text, r.Family)
}
