package review

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/BrokkAi/slopcop/internal/review/chatagent"
)

// panel runs the configured reviewers. Each reviewer gets one retry, a
// reviewer that fails never stops the others, and a reviewer that still fails
// hands its place to the next unused fallback while fewer than min reviewers
// have finished.
type panel struct {
	ctx  context.Context
	root string
	// trees are the captured trees; an isolated reviewer gets a copy of the
	// checked one.
	trees reviewTrees
	// diffSize is the captured diff's length in bytes.
	diffSize int
	// binaryTextCut says a size limit left readable text of a binary file
	// out of the prompt.
	binaryTextCut bool
	dir           string
	timeout       time.Duration
	commands      map[Family][]string
	// adapters names each family's adapter; copilot lists the models for
	// the families Copilot runs.
	adapters map[Family]string
	copilot  *copilotCatalog
	// ollamaModel is the model the ollama family reviews on.
	ollamaModel string
	prompt      func(Family) string
	min         int

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
	// A started reviewer's timeout runs until every reviewer has started, so
	// the Ollama model, which can take minutes to load, loads first.
	if slices.ContainsFunc(order, func(f Family) bool { return p.adapter(f) == ollamaAdapter }) {
		preloadOllama(p.ctx, p.ollamaModel, p.timeout)
	}
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
		if p.adapter(p.fallbacks[i]) != p.adapter(replaced) {
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
		r, err = p.attempt(name+"-retry", f, nil, nil, retryNote(failureText(first)))
		r.Name = name
		r.FirstError, r.FirstLog = failureText(first), first.Log
	}
	r.Status = parseVerdict(r)
	if r.Status == StatusFailed {
		if r.Model == copilotAuto {
			p.copilot.release(f)
		}
		r.Error = explainFailure(r, err)
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
	r, err := s.prompt(p.prompt(f) + note)
	if err == nil && p.adapter(f) == ollamaAdapter {
		if err = verdictFormatError(r.Text); err != nil {
			r.Error = err.Error()
			r.Status = StatusFailed
		}
	}
	return r, err
}

// start launches and prepares an adapter. On a prepare failure it returns
// the stopped runner, whose result names the reviewer's log.
func (p *panel) start(name string, f Family) (*runner, error) {
	if isolatedAdapter(p.adapter(f)) && p.diffSize > promptDiffLimit {
		return nil, errDiffTooLarge
	}
	if isolatedAdapter(p.adapter(f)) && p.binaryTextCut {
		return nil, errBinaryTextCut
	}
	p.startMu.Lock()
	defer p.startMu.Unlock()
	return launch(p.ctx, p.root, p.trees, p.adapter(f), p.commands[f], p.copilot, p.ollamaModel, f, name, p.dir, p.timeout)
}

// adapter names the adapter that runs a family in this panel.
func (p *panel) adapter(f Family) string {
	if a, ok := p.adapters[f]; ok {
		return a
	}
	return AdapterName(f)
}

// retryable reports whether a failed reviewer may get a second attempt. A
// timeout would only double the wait, a canceled check wants no more work,
// and a missing login or exhausted credits fail the same way again.
func retryable(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errForbiddenTool) || errors.Is(err, errDiffTooLarge) || errors.Is(err, errBinaryTextCut) || errors.Is(err, errChangeNotExported) || errors.Is(err, errOllamaSetup) || errors.Is(err, errOllamaCoverage) || errors.Is(err, errKimiNoKey) || errors.Is(err, errChangeBinary) {
		return false
	}
	if err != nil && chatagent.IsLimitError(err.Error()) {
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
	case has(networkWords...):
		return causeNetwork
	}
	return ""
}

// networkWords are the transport failures adapters report.
var networkWords = []string{"econnrefused", "enotfound", "econnreset", "etimedout", "network error", "connection refused", "connection reset", "no such host", "could not resolve", "service unavailable", "overloaded", "can't reach", "cannot reach", "could not reach", "unable to reach", "check your network connection", "stream disconnected", "error sending request"}

// priorityTag matches the [Pn] tag that starts each review finding.
var priorityTag = regexp.MustCompile(`\[P[0-3]\]`)

// connectionReply reports whether a reply is an adapter's own connection
// error rather than a review: it has no [Pn] finding and names a transport
// failure. A review always tags its findings, so a finding that mentions a
// network problem is never mistaken for one.
func connectionReply(text string) bool {
	if text == "" || priorityTag.MatchString(text) {
		return false
	}
	return failureCause(text) == causeNetwork
}

// explainFailure turns a reviewer's final error into one that says what to do.
// err is the error of its last attempt.
func explainFailure(r Reviewer, err error) string {
	text := failureText(r)
	var stop toolStop
	if errors.As(err, &stop) {
		return text + ". " + stop.advice()
	}
	if strings.Contains(text, errDiffTooLarge.Error()) || strings.Contains(text, errBinaryTextCut.Error()) || strings.Contains(text, errChangeNotExported.Error()) || strings.Contains(text, errKimiNoKey.Error()) || strings.Contains(text, errOllamaSetup.Error()) || strings.Contains(text, errOllamaCoverage.Error()) || strings.Contains(text, errChangeBinary.Error()) || chatagent.IsLimitError(text) {
		return text
	}
	if r.FirstError != "" && r.FirstError != text {
		text += " (retried once; the first attempt failed with: " + r.FirstError + ")"
	} else if r.FirstError != "" {
		text += " (retried once with the same result)"
	}
	adapter := r.Adapter
	if adapter == "" {
		adapter = AdapterName(r.Family)
	}
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
