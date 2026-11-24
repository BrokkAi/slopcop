package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/attribution"
	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/gitx"
	agenthooks "github.com/BrokkAi/slopcop/internal/hooks"
	"github.com/BrokkAi/slopcop/internal/report"
	"github.com/BrokkAi/slopcop/internal/reposize"
	"github.com/BrokkAi/slopcop/internal/review"
	"github.com/BrokkAi/slopcop/internal/service"
)

const (
	policyDirectory = ".slopcop/policies"
	failOnFinding   = "finding"
	// certaintyDefinite and certaintyAll are the --certainty values. The
	// service answers definite findings only unless asked for all.
	certaintyDefinite  = "definite"
	certaintyAll       = "all"
	invalidChange      = "invalid_change"
	baseNotFound       = "base_not_found"
	repositoryTooLarge = "repository_too_large"
	// repositoryNotEnabledCode is --if-enabled's skip of a repository the
	// workspace does not have active, whatever its state.
	repositoryNotEnabledCode = "repository_not_enabled"
	// repositoryDisabledCode is the refusal of a check of a repository the
	// workspace has disabled, which only a person may turn back on.
	repositoryDisabledCode = "repository_disabled"
	// activeLimitCode is the refusal of a check of a repository past the
	// workspace's active-repository limit.
	activeLimitCode = "active_repository_limit_reached"
	// reviewBusyCode is a check that gave up waiting for another review or
	// override in the same checkout.
	reviewBusyCode = "review_busy"
	// interruptedText is printed when Ctrl-C or SIGTERM stops a check that
	// was sent: a service that received it keeps it, so asking again picks up
	// its result.
	interruptedText = "slopcop: interrupted before the check answered. If the service received it, it keeps running it; run the same check again to pick up its result."
	// interruptedBeforeSendText is printed when the interrupt came during the
	// local work, before anything was sent.
	interruptedBeforeSendText = "slopcop: interrupted before the check was sent; nothing reached the service."
	// showExistingNotice is printed on stderr when --show-existing is passed.
	// emptyChangeNotice is printed on stderr with a report for an empty
	// patch, so a clean result is not read as a pass for work never sent.
	emptyChangeNotice = "slopcop: the working tree matches the base %s, so the patch is empty and no change was checked. " +
		"To check committed work, pass --base with the branch it targets, for example --base origin/main."
	// noReviewersNotice is printed when a check with no mode set runs only
	// the static half because no reviewers are configured.
	noReviewersNotice = "slopcop: no reviewers are configured, so only the static check ran. " +
		"Run `slopcop init` to add the local review, or `slopcop config set mode static` to stop this notice."
	reviewInterruptedText = "slopcop: local reviewers were stopped before the review finished; run the same check again to retry it."
	// bareCleanReport is the whole human report of a clean check with
	// nothing more to say.
	bareCleanReport    = "No new findings | Analysis complete\n"
	showExistingNotice = "slopcop: --show-existing is deprecated: the service lists only the findings a change introduced or touched, and every report counts the rest."
)

// staticOutcome holds one terminal result of the hosted half.
type staticOutcome struct {
	// response is the completed service response.
	response *service.Response
	// failure is a refused or locally failed static check.
	failure *service.Error
	// pending means the service is still running.
	pending *service.Pending
	// skipped is an inactive repository bypassed by --if-enabled.
	skipped *report.Skipped
	// exit is the static half's exit code.
	exit int
	// status is the static half's own status.
	status string
	// addedTo is the workspace the check added the repository to.
	addedTo *report.AddedWorkspace
	// sizeBreakdown is the base tree's largest paths, for a repository the
	// service said is near or over its checkout size limit.
	sizeBreakdown *reposize.Breakdown
}

func runCheck(cmd *cobra.Command, opts *checkOptions, serviceFlag, version string) error {
	format, failOn, err := resolveEnums(opts)
	if err != nil {
		return err
	}
	// Definite is the service's own default, so it is not sent: a service
	// from before certainty refuses a field it does not know, and a check by
	// hand keeps the fingerprint of the Stop hook's.
	switch certainty := strings.ToLower(strings.TrimSpace(opts.certainty)); certainty {
	case "", certaintyDefinite:
		opts.certainty = ""
	case certaintyAll:
		opts.certainty = certaintyAll
	default:
		return fmt.Errorf("unknown certainty %q (expected definite or all)", opts.certainty)
	}
	if opts.showExisting {
		fmt.Fprintln(cmd.ErrOrStderr(), showExistingNotice)
	}
	if repo := legacyRepoRoot(opts.root); repo != "" {
		_ = cleanupInstalledLegacyRepository(repo)
	}
	reportOptions := report.Options{ShowExisting: opts.showExisting, Verbose: opts.verbose, SkillUpdated: syncSkillAndReport(cmd.ErrOrStderr(), cmd.ErrOrStderr(), skillRepoRoot(opts.root), version)}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	blockLevel, err := pushBlockLevel(cfg)
	if err != nil {
		return err
	}
	mode, fallback, err := resolveMode(opts.mode, opts.static, opts.ai, cfg, os.Getenv)
	if err != nil {
		return err
	}
	// A CI job keeps its static-only output as before. Elsewhere, say once
	// per check why the review did not run and how to turn it on or quiet
	// this line.
	if fallback == staticForNoReviewers {
		fmt.Fprintln(cmd.ErrOrStderr(), noReviewersNotice)
	}
	var settings review.Settings
	if mode != modeStatic {
		if opts.reviewTimeout < 0 {
			return fmt.Errorf("--review-timeout must be nonnegative")
		}
		settings, err = reviewSettings(cfg, opts.reviewTimeout)
		if err != nil {
			return err
		}
	}
	effectiveOpts := *opts
	effectiveOpts.mode = mode
	prepared, err := prepareCheck(&effectiveOpts, cfg, serviceFlag, version, failOn)
	// The review store is taken before anything is sent, so a check that
	// cannot review spends no hosted check. After a wait the checkout may have
	// moved on, and the check covers what is there now.
	var store *review.Store
	var storeErr error
	if err == nil && mode != modeStatic {
		var waited bool
		store, waited, storeErr = review.WaitStore(cmd.Context(), prepared.repoRoot, settings.Timeout, func() {
			fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: %v; waiting up to %s for it to finish.\n", review.ErrBusy, settings.Timeout)
		})
		defer store.Close()
		if waited && storeErr == nil {
			prepared, err = prepareCheck(&effectiveOpts, cfg, serviceFlag, version, failOn)
		}
	}
	if cmd.Context().Err() != nil {
		fmt.Fprintln(cmd.ErrOrStderr(), interruptedBeforeSendText)
		return silentExit{interruptCode(cmd.Context())}
	}
	if err != nil {
		return err
	}
	if errors.Is(storeErr, review.ErrBusy) {
		return checkFailure(cmd, reviewBusy(settings.Timeout), format, opts.sarifFile, reportOptions)
	}
	reportOptions.OmittedFiles = prepared.omittedFiles
	reportOptions.BinaryFiles = prepared.binaryFiles
	reportOptions.Source = treeSource(prepared.repoRoot, prepared.checkedTree)
	if notice := omittedFilesNotice(prepared.omittedFiles); notice != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), notice)
	}
	if unverified := report.UnverifiedBinaryFiles(prepared.binaryFiles); len(unverified) > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: %d changed binary file(s) could not be verified as a known format, so their content has not been fully reviewed. The report lists them.\n", len(unverified))
	}
	// How the base was chosen applies to both halves, so it is said before
	// either starts.
	if prepared.base.warning != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), prepared.base.warning)
	}
	if opts.verbose {
		for _, note := range prepared.base.notes {
			fmt.Fprintln(cmd.ErrOrStderr(), note)
		}
	}
	attr := attribution.Detect(prepared.repoRoot)
	prepared.request.Attribution = attr
	// Whether the workspace lets the check run at all is settled before the
	// reviewers start, so a refused check neither spends reviewer time nor
	// reports a review beside its refusal.
	var static staticOutcome
	gate := gateSend
	if !prepared.onlyOmitted() && mode != modeAI {
		static, gate = gateCheck(cmd, opts, &prepared, version)
	}
	if gate == gateInactive {
		// The last review on record may be of this same tree. Its static
		// half is now refused, so withdraw the tree's approval first, then
		// record the refusal, or `slopcop override` would still trust the
		// earlier static pass.
		if err := review.Unmark(prepared.repoRoot, prepared.snapshot.Tree); err != nil {
			return err
		}
		// A store another check holds is not a reason to lose the refusal's
		// own report, so failing to record it is only said.
		if err := recordStaticRefusal(prepared.repoRoot, prepared.snapshot.Tree, static, store); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: could not record the refusal in the local review state, so check again before `slopcop override`: %v\n", err)
		}
		mode = modeStatic
	}
	var verdict review.Verdict
	var reviewDone chan review.Verdict
	if mode != modeStatic {
		reviewDone = make(chan review.Verdict, 1)
		fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: reviewing with %s; this can take several minutes.\n", reviewerNames(settings.Reviewers))
		go func() {
			v := review.Verdict{Status: review.StatusFailed}
			if storeErr != nil {
				v.Error = storeErr.Error()
				reviewDone <- v
				return
			}
			capture, err := review.NewFilteredCapture(prepared.repoRoot, prepared.snapshot.Branch, prepared.request.BaseCommit, prepared.snapshot.Tree, prepared.reviewTree)
			if err != nil {
				v.Error = err.Error()
				reviewDone <- v
				return
			}
			v = review.Run(cmd.Context(), store, capture, review.Options{
				Mode:       mode,
				Version:    version,
				Root:       prepared.repoRoot,
				BaseCommit: prepared.request.BaseCommit,
				Intent:     opts.intent,
				Settings:   settings,
				Progress:   cmd.ErrOrStderr(),
			})
			reviewDone <- v
		}()
	}
	switch {
	case gate != gateSend:
	case prepared.onlyOmitted():
		static = omittedOnlyOutcome(prepared)
		fmt.Fprintln(cmd.ErrOrStderr(), "slopcop: "+static.skipped.Message)
	case mode != modeAI:
		static = runStaticCheck(cmd, opts, prepared, failOn)
	default:
		static = staticOutcome{exit: 0, status: "clean"}
	}
	reviewRunning := false
	if reviewDone != nil {
		select {
		case verdict = <-reviewDone:
		default:
			reviewRunning = true
			verdict = <-reviewDone
		}
	}
	var checkRef *review.CheckRef
	if store != nil {
		if mode == modeBoth {
			if err := store.SetStatic(static.exit, static.status, !staticPasses(static, mode, blockLevel)); err != nil {
				verdict.Status = review.StatusFailed
				verdict.Error = err.Error()
			}
		}
		// Only for later overrides to find the check, so a failure here is
		// not the check's.
		if checkRef = reviewCheckRef(mode, static, prepared.client); checkRef != nil {
			_ = store.SetCheck(*checkRef, verdict)
		}
		if err := store.Close(); err != nil {
			verdict.Status = review.StatusFailed
			verdict.Error = err.Error()
		}
	}
	if cmd.Context().Err() != nil {
		if static.exit == service.ExitInterrupted {
			fmt.Fprintln(cmd.ErrOrStderr(), interruptedText)
			if reviewRunning {
				fmt.Fprintln(cmd.ErrOrStderr(), "slopcop: the local review was stopped.")
			}
		} else {
			fmt.Fprintln(cmd.ErrOrStderr(), reviewInterruptedText)
		}
		return silentExit{interruptCode(cmd.Context())}
	}

	// A finding override the review relied on may have been undone while
	// the static half ran: then the review is not what the overrides now say.
	if mode != modeStatic && (verdict.Status == review.StatusClean || verdict.Status == review.StatusFindings) {
		if hold, err := review.OverridesHold(prepared.repoRoot, verdict); err != nil || !hold {
			verdict.Status = review.StatusFailed
			verdict.Error = "a finding override this review relied on was undone while the check ran; run slopcop check again"
			if err != nil {
				verdict.Error = "read review finding overrides: " + err.Error()
			}
		}
	}
	exit := static.exit
	if mode != modeStatic {
		if verdict.Status == review.StatusFindings && failOn != "never" {
			exit = service.ExitFindings
		} else if static.exit != service.ExitFindings && static.exit != service.ExitNeedsPerson && static.exit != service.ExitPending && (verdict.Status == review.StatusFailed || verdict.Incomplete) {
			// A pending static result keeps its exit: it tells the caller to
			// run the same check again, which also runs the review again, and
			// the failed review stays visible in the report. Incomplete would
			// hide that the service has a result coming.
			exit = service.ExitIncomplete
		}
	}
	status := static.status
	switch exit {
	case service.ExitFindings:
		status = "findings"
	case service.ExitNeedsPerson:
		status = "failed"
	case service.ExitIncomplete:
		if static.status == "failed" {
			status = "failed"
		} else {
			status = "incomplete"
		}
	case service.ExitPending:
		status = "pending"
	}
	// Status says what the check found and --fail-on only what exits
	// nonzero, so findings below the threshold, or with --fail-on never,
	// still read as findings.
	if static.status == service.StatusFindings || verdict.Status == review.StatusFindings {
		status = service.StatusFindings
	}
	reportOptions.Status = status
	reviewFailed := mode != modeStatic && verdict.Status == review.StatusFindings && failOn != "never"
	reportOptions.ExitReason = exitReason(exit, static.exit, reviewFailed)
	if mode != modeAI && status != static.status {
		reportOptions.StaticStatus = static.status
	}
	switch {
	case gate == gateInactive:
		reportOptions.ReviewSkipped = report.ReviewSkippedRepositoryInactive
	case mode != modeStatic:
		reportOptions.Review = &verdict
	default:
		reportOptions.ReviewSkipped = reviewSkippedReason(fallback)
	}
	reportOptions.EmptyChange = prepared.bytes == 0
	reportOptions.SizeBreakdown = static.sizeBreakdown
	// The first check 'enable' runs is how the repository gets added, and
	// right after enabling there is usually nothing to check yet. Its line
	// that the repository was added says all there is, so an empty, clean
	// first check whose report would say only that prints no report, no
	// empty-patch notice and no push line.
	if opts.enable != nil && format == report.FormatHuman && prepared.bytes == 0 && len(prepared.omittedFiles) == 0 &&
		static.addedTo != nil && static.response != nil && exit == service.ExitClean {
		withAdded := reportOptions
		withAdded.AddedTo = static.addedTo
		var rendered bytes.Buffer
		if err := report.Write(&rendered, *static.response, format, withAdded); err == nil && rendered.String() == bareCleanReport {
			warnLowCredits(cmd, static.response)
			return silentExit{exit}
		}
	}
	if prepared.bytes == 0 && len(prepared.omittedFiles) == 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), emptyChangeNotice+"\n", shortSHA(prepared.request.BaseCommit))
		// A base the CLI moved explains an empty patch the user did not
		// expect from the base they named, so say so even without --verbose.
		if !opts.verbose {
			for _, note := range prepared.base.notes {
				fmt.Fprintln(cmd.ErrOrStderr(), note)
			}
		}
	}
	// A change the repository's own exclusions emptied leaves the reviewers
	// nothing to read, so an empty review passes it, as the service's
	// "Nothing checked" does. The size filter emptying a change is not the
	// same: those files are in scope but went unread, so it stays unapproved.
	excludedOnly := prepared.onlyExcluded()
	reviewPassed := mode == modeStatic || verdict.Status == review.StatusClean ||
		(excludedOnly && verdict.Status == review.StatusEmpty)
	staticPassed := staticPasses(static, mode, blockLevel)
	nonempty := (prepared.bytes > 0 || excludedOnly) && (static.response != nil || (reviewPassed && mode != modeStatic))
	reportOptions.Approved = reviewPassed && staticPassed && nonempty
	if reportOptions.Approved {
		if err := review.MarkReviewed(prepared.repoRoot, prepared.snapshot.Tree); err != nil {
			return err
		}
		// A finding override the review relied on may have been undone after
		// the review recorded the tree on it and before this approval, too
		// late for the undo to withdraw it. Then the approval does not stand.
		if mode != modeStatic {
			hold, err := review.OverridesHold(prepared.repoRoot, verdict)
			if err != nil {
				if unmarkErr := review.Unmark(prepared.repoRoot, prepared.snapshot.Tree); unmarkErr != nil {
					return unmarkErr
				}
				return err
			}
			if !hold {
				if err := review.Unmark(prepared.repoRoot, prepared.snapshot.Tree); err != nil {
					return err
				}
				// The result fails as the earlier guard makes it fail: the
				// review is no longer what the overrides say.
				reportOptions.Approved = false
				verdict.Status = review.StatusFailed
				verdict.Error = "a finding override this review relied on was undone while the check ran; run slopcop check again"
				if exit == service.ExitClean {
					exit = service.ExitIncomplete
					reportOptions.Status = "incomplete"
				}
			}
		}
	} else if prepared.bytes > 0 || len(prepared.omittedFiles) > 0 {
		// A later failed decision also withdraws an override for this tree.
		if err := review.Unmark(prepared.repoRoot, prepared.snapshot.Tree); err != nil {
			return err
		}
	}
	advisory := 0
	if mode != modeAI && static.response != nil && static.response.KnownStatus() {
		advisory = advisoryFindings(*static.response, blockLevel)
	}
	reportOptions.AdvisoryFindings = advisory
	if static.response != nil {
		reportOptions.AddedTo = static.addedTo
	}
	render := func(w io.Writer, format string) error {
		switch {
		case mode == modeAI:
			return report.WriteAI(w, format, reportOptions)
		case static.response != nil:
			return report.Write(w, *static.response, format, reportOptions)
		case static.failure != nil:
			return report.WriteFailure(w, static.failure, format, reportOptions)
		case static.pending != nil:
			return report.WritePending(w, *static.pending, format, reportOptions)
		case static.skipped != nil:
			return report.WriteSkipped(w, *static.skipped, format, reportOptions)
		default:
			return report.WriteFailure(w, &service.Error{Body: service.ErrorBody{Message: "static check failed"}}, format, reportOptions)
		}
	}
	if err := render(cmd.OutOrStdout(), format); err != nil {
		return err
	}
	if err := writeSARIFFile(opts.sarifFile, render); err != nil {
		return err
	}
	overridable := verdict.Overridable() && staticPasses(static, mode, blockLevel)
	if notice := reviewFailureNotice(reportOptions.ExitReason, failingReviewFindings(verdict), failOn, format, overridable); notice != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), notice)
	}
	if format == report.FormatHuman {
		warnLowCredits(cmd, static.response)
	}
	// Say whether the tree may be pushed only where the push gate is
	// installed, and only when the check produced a result to judge: after a
	// refusal or a pending check, the static half's own message already says
	// what happened.
	judged := static.response != nil || mode != modeStatic
	if format == report.FormatHuman && judged && agenthooks.GateInstalled(prepared.repoRoot) {
		tree := shortSHA(prepared.snapshot.Tree)
		if reportOptions.Approved {
			fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: tree %s may be pushed: it passed the check.\n", tree)
		} else {
			reason := approvalReason(prepared.bytes == 0 && !excludedOnly, reviewPassed, verdict, static, mode, blockLevel)
			fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: tree %s may not be pushed yet: %s.\n", tree, reason)
		}
	}
	// In every format, since an agent reads json and pushes on approved.
	if reportOptions.Approved && advisory > 0 && agenthooks.GateInstalled(prepared.repoRoot) {
		fmt.Fprintln(cmd.ErrOrStderr(), advisoryNotice(advisory, blockLevel))
	}
	reportReview(cmd, checkRef, prepared.client, prepared.repoRoot, verdict, settings, version, opts.verbose)
	return silentExit{exit}
}

// staticFailure prints a refused or locally failed static check's code,
// message, and remedy on stderr and returns it as the static half's outcome.
func staticFailure(cmd *cobra.Command, err error) staticOutcome {
	var serviceError *service.Error
	if !errors.As(err, &serviceError) {
		serviceError = &service.Error{Body: service.ErrorBody{Message: err.Error()}}
	}
	text := remedyText(serviceError)
	var withHint hinted
	if errors.As(err, &withHint) {
		if hint := withHint.hint(); hint != "" {
			text += "\n" + hint
		}
	}
	if code := serviceError.Body.Code; code != "" {
		text = "slopcop: " + code + ": " + text
	}
	fmt.Fprintln(cmd.ErrOrStderr(), text)
	code := service.ExitIncomplete
	if serviceError.NeedsPerson() {
		code = service.ExitNeedsPerson
	}
	return staticOutcome{failure: serviceError, exit: code, status: "failed"}
}

// gateResult is what gateCheck decided about the hosted half.
type gateResult int

const (
	// gateSend lets the check be sent.
	gateSend gateResult = iota
	// gateStatic stops only the hosted half: on a failure of its own, such
	// as no login or an unreachable service, or on --if-enabled's skip of an
	// inactive repository. The local review still runs: it needs neither the
	// service nor an active repository.
	gateStatic
	// gateInactive refuses the whole check: the workspace has not added the
	// repository, has disabled it, or has it past the active limit, so
	// nothing is reviewed either.
	gateInactive
)

// gateCheck decides, before anything is reviewed or sent, whether the check
// may run: it picks the workspace and looks up the repository. A repository
// the workspace has never added is added by the check itself, so an agent
// does not have to stop and ask. It runs before the local reviewers start, so
// a refused check of a repository the workspace has disabled spends no
// reviewer time and reports no review beside its refusal. Unless it returns
// gateSend, the outcome is the static half's.
func gateCheck(cmd *cobra.Command, opts *checkOptions, prepared *preparedCheck, version string) (staticOutcome, gateResult) {
	if prepared.staticErr != nil {
		return staticFailure(cmd, prepared.staticErr), gateStatic
	}
	client := prepared.client
	noticeService(cmd.ErrOrStderr(), client.BaseURL)
	noticeUpdates(cmd, client, version)
	if err := useWorkspace(cmd.Context(), client, prepared.choice); err != nil {
		return staticFailure(cmd, err), gateStatic
	}
	// Before the lookup that may lead to adding the repository, so the
	// person reads which workspace that would be.
	unmappedNotice(cmd, client, prepared.choice)
	repository, err := checkRepository(cmd.Context(), client, prepared.repoRoot)
	if err != nil {
		return staticFailure(cmd, err), gateStatic
	}
	// Adding is not turning back on: a disabled repository stays the
	// workspace's choice.
	if repository.State == statusNotAdded {
		prepared.request.AddRepository = true
		return staticOutcome{}, gateSend
	}
	if repository.Active {
		return staticOutcome{}, gateSend
	}
	if opts.ifEnabled {
		name := repositoryDisplayName(repository)
		message := fmt.Sprintf("%s %s, so nothing was checked.", name, inactiveReason(repository.State))
		fmt.Fprintln(cmd.ErrOrStderr(), "slopcop: "+message)
		return staticOutcome{skipped: &report.Skipped{Code: repositoryNotEnabledCode, State: repository.State, Repository: name, Message: message}, status: "skipped"}, gateStatic
	}
	switch repository.State {
	case statusDisabled:
		return staticFailure(cmd, repositoryDisabled(repository)), gateInactive
	case statusOverLimit:
		return staticFailure(cmd, repositoryOverLimit(repository)), gateInactive
	}
	// On an unsupported host: the service's refusal says what to do, so the
	// check is sent for it.
	return staticOutcome{}, gateSend
}

// refusalLockWait bounds how long recording a refusal waits for another
// slopcop process to release the review store.
const refusalLockWait = 5 * time.Second

// recordStaticRefusal marks the last local review's static half as refused
// and blocking when that review is of tree, so no override builds on a static
// pass the workspace has since refused. A review of another tree keeps its
// own static result. held is the store when this check already has it, and
// stays open.
func recordStaticRefusal(root, tree string, static staticOutcome, held *review.Store) error {
	store := held
	if store == nil {
		// An override holds the store only briefly, so wait it out rather than
		// leave it a passing static result to approve the tree with afterwards.
		var err error
		if store, _, err = review.WaitStore(context.Background(), root, refusalLockWait, nil); err != nil {
			return err
		}
		defer store.Close()
	}
	last, err := store.Last()
	if err == nil && last != nil && last.Capture.Tree == tree {
		err = store.SetStatic(static.exit, static.status, true)
	}
	return err
}

// reviewBusy is the failure of a check that gave up waiting for another
// review or override in this checkout. Nothing was sent or reviewed.
func reviewBusy(limit time.Duration) *service.Error {
	return &service.Error{Body: service.ErrorBody{
		Code:      reviewBusyCode,
		Message:   fmt.Sprintf("Another slopcop review or override in this checkout was still running after %s, so this check did not run and spent no static check.", limit),
		Remedy:    "Run the same check again when it finishes, or give it longer with --review-timeout.",
		Retryable: true,
	}}
}

// repositoryOverLimit is the local refusal of a check of a repository the
// workspace has enabled but cannot keep active past its limit. It carries the
// service's own code for that state.
func repositoryOverLimit(repository statusRepository) *service.Error {
	return &service.Error{Body: service.ErrorBody{
		Code:      activeLimitCode,
		Message:   fmt.Sprintf("%s is over your SlopCop workspace's active-repository limit, so the check did not run.", repositoryDisplayName(repository)),
		Remedy:    "The workspace's owner chooses which repositories stay active on the Repositories page, or unlocks the workspace.",
		Retryable: false,
	}}
}

// runStaticCheck sends the hosted half, which gateCheck already let through,
// and returns its outcome.
func runStaticCheck(cmd *cobra.Command, opts *checkOptions, prepared preparedCheck, failOn string) staticOutcome {
	client, request := prepared.client, prepared.request
	if opts.verbose {
		fmt.Fprintf(
			cmd.ErrOrStderr(),
			"slopcop: POST %s/v1/check base %s (%d file(s), %d bytes, fail-on %s)\n",
			client.BaseURL, shortSHA(request.BaseCommit), prepared.files, prepared.bytes, failOn,
		)
		if attributed := request.Attribution; attributed != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: attribution %s session %s\n", attributed.Harness, attributed.SessionID)
			for _, model := range attributed.Models {
				fmt.Fprintf(cmd.ErrOrStderr(), "slopcop:   %s effort %s wrote %s\n", model.Model, valueOr(model.Effort, "unknown"), strings.Join(model.Files, ", "))
			}
		}
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), client.Timeout())
	defer cancel()
	var verbose io.Writer
	if opts.verbose {
		verbose = cmd.ErrOrStderr()
	}
	response, err := sendCheck(ctx, client, &request, verbose)
	var rejected *service.Error
	if errors.As(err, &rejected) && rejected.Body.Code == baseNotFound {
		err = &localRefusal{refusal: rejected, text: baseNotFoundHint(cmd.Context(), prepared.repoRoot, request.BaseCommit)}
	}
	if errors.As(err, &rejected) && rejected.Body.Code == repositoryTooLarge {
		breakdown := measureRepository(cmd, opts.verbose, prepared.repoRoot, request.BaseCommit, nil)
		outcome := staticFailure(cmd, &localRefusal{refusal: rejected, text: strings.TrimRight(report.SizeAdvice(nil, breakdown), "\n")})
		outcome.sizeBreakdown = breakdown
		return outcome
	}
	if err != nil {
		var pending *service.Pending
		if errors.As(err, &pending) {
			fmt.Fprintln(cmd.ErrOrStderr(), pending.RemedyText())
			return staticOutcome{pending: pending, exit: service.ExitPending, status: "pending"}
		}
		if errors.Is(err, context.Canceled) {
			return staticOutcome{exit: service.ExitInterrupted, status: "failed"}
		}
		return staticFailure(cmd, err)
	}
	var addedTo *report.AddedWorkspace
	switch {
	case response.RepositoryAdded && opts.enable != nil:
		// 'enable' passes the workspace it settled on as --workspace would,
		// so the choice is the one it made, not the flag's.
		addedTo = addedWorkspace(cmd.Context(), client, request.RepoURL, prepared.choice)
		addedTo.Source = opts.enable.source
		for _, line := range enableAddedLines(request.RepoURL, addedTo, opts.enable.workspaces) {
			fmt.Fprintln(cmd.ErrOrStderr(), line)
		}
	case response.RepositoryAdded:
		addedTo = noticeAdded(cmd, client, request.RepoURL, prepared.choice)
	}
	if !response.KnownStatus() {
		fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: the service answered status %q, which this version of slopcop does not know, so the check fails. Update slopcop.\n", response.Status)
	} else {
		rememberCheck(prepared, response)
	}
	exit := response.ExitCode()
	var breakdown *reposize.Breakdown
	if size := response.RepositorySize; size != nil && size.NearLimit {
		breakdown = measureRepository(cmd, opts.verbose, prepared.repoRoot, request.BaseCommit, &reposize.Usage{
			Files: size.Files, MaxFiles: size.MaxFiles, Bytes: size.Bytes, MaxBytes: size.MaxBytes,
		})
	}
	// The service's status follows failOn, so with --fail-on never, or new
	// findings below the threshold, it answers clean or incomplete. The
	// report's status names the result instead; the exit keeps the threshold.
	if response.KnownStatus() && hasNewFindings(response) {
		response.Status = service.StatusFindings
	}
	return staticOutcome{response: &response, exit: exit, status: response.Status, sizeBreakdown: breakdown, addedTo: addedTo}
}

// measureTimeout bounds the local measure of a large repository, which only
// adds advice to a result the service already gave.
const measureTimeout = 30 * time.Second

// measureRepository finds the largest paths of base's tree for a repository
// the service said is near or over its checkout size limit. The service
// names no paths, so this is the only place they come from. It is advice, so
// a failure leaves it out, saying why only under --verbose.
func measureRepository(cmd *cobra.Command, verbose bool, root, base string, usage *reposize.Usage) *reposize.Breakdown {
	ctx, cancel := context.WithTimeout(cmd.Context(), measureTimeout)
	defer cancel()
	breakdown, err := reposize.Compute(ctx, root, base, usage)
	if err != nil {
		if verbose {
			fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: could not measure the repository's largest paths: %v\n", err)
		}
		return nil
	}
	return breakdown
}

// hasNewFindings says the change introduced or touched a finding, listed or
// omitted past the service's bound.
func hasNewFindings(response service.Response) bool {
	if response.Summary.NewFindings > 0 || response.Summary.OmittedFindings > 0 {
		return true
	}
	for _, finding := range response.Findings {
		if finding.New {
			return true
		}
	}
	return false
}

// checkFailure prints a service failure's code, message, and remedy on
// stderr, writes the failure report for machine formats on stdout, and exits
// 4 when only a person can clear it, otherwise 2, the exit of a check that
// did not complete. Any other error is returned as is.
func checkFailure(cmd *cobra.Command, err error, format, sarifFile string, options report.Options) error {
	var serviceError *service.Error
	if errors.As(err, &serviceError) {
		text := remedyText(serviceError)
		var withHint hinted
		if errors.As(err, &withHint) {
			if hint := withHint.hint(); hint != "" {
				text += "\n" + hint
			}
		}
		if code := serviceError.Body.Code; code != "" {
			text = "slopcop: " + code + ": " + text
		}
		fmt.Fprintln(cmd.ErrOrStderr(), text)
		render := func(w io.Writer, format string) error {
			return report.WriteFailure(w, serviceError, format, options)
		}
		if err := render(cmd.OutOrStdout(), format); err != nil {
			return err
		}
		if err := writeSARIFFile(sarifFile, render); err != nil {
			return err
		}
		if serviceError.NeedsPerson() {
			return silentExit{service.ExitNeedsPerson}
		}
		return silentExit{service.ExitIncomplete}
	}
	return err
}

// writeSARIFFile writes the report again, as SARIF, to path, for --sarif-file:
// a CI job reads one format on stdout and uploads SARIF to code scanning from
// the same check, without sending it twice. An empty path writes nothing.
func writeSARIFFile(path string, render func(io.Writer, string) error) error {
	if path == "" {
		return nil
	}
	var out bytes.Buffer
	if err := render(&out, report.FormatSARIF); err != nil {
		return err
	}
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write --sarif-file: %w", err)
	}
	return nil
}

// checkRepository is lookupRepositoryStatus, a variable so tests can stand in
// for the service when a check only needs to be told the repository is active.
var checkRepository = lookupRepositoryStatus

// repositoryDisplayName is the repository's owner/name, or its URL when the
// service did not name it.
func repositoryDisplayName(repository statusRepository) string {
	if repository.FullName != "" {
		return repository.FullName
	}
	return repository.URL
}

// inactiveReason says why a repository in state is not checked.
func inactiveReason(state string) string {
	switch state {
	case statusNotAdded:
		return "has not been added to your SlopCop workspace"
	case statusDisabled:
		return "was disabled in your SlopCop workspace"
	case statusOverLimit:
		return "is over your SlopCop workspace's active-repository limit"
	case statusUnsupported:
		return "is on a host SlopCop does not check"
	}
	return "is not active in your SlopCop workspace"
}

// repositoryDisabled is the local refusal of a check of a repository the
// workspace has disabled, by a person or past its active limit. Turning it
// back on is the workspace's decision, so the remedy names no command an
// agent could run.
func repositoryDisabled(repository statusRepository) *service.Error {
	return &service.Error{Body: service.ErrorBody{
		Code:      repositoryDisabledCode,
		Message:   fmt.Sprintf("%s was disabled in your SlopCop workspace, so the check did not run.", repositoryDisplayName(repository)),
		Remedy:    service.RepositoryDisabledRemedy,
		Retryable: false,
	}}
}

// hinted is a service refusal the CLI adds a line of its own to, after the
// service's remedy.
type hinted interface {
	hint() string
}

// localRefusal is a service refusal with what the local clone says about it:
// for base_not_found, why the service lacks the base; for
// repository_too_large, the base tree's largest paths and how to leave them
// out of checks.
type localRefusal struct {
	refusal *service.Error
	text    string
}

func (r *localRefusal) Error() string { return r.refusal.Error() }

func (r *localRefusal) Unwrap() error { return r.refusal }

func (r *localRefusal) hint() string { return r.text }

// baseNotFoundHint explains a base the service does not have when the clone
// can tell why: origin's default branch moved, as after a rename from main to
// master, and the clone still points origin/HEAD, and so the default base, at
// the old one; or the base is not on origin's default branch at all. It asks
// origin, so it runs only after the service refused, and says nothing when
// origin cannot be asked in time.
func baseNotFoundHint(ctx context.Context, root, base string) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	branch, ok := gitx.RemoteDefaultBranch(ctx, root)
	if !ok {
		return ""
	}
	return describeBase(root, base, branch)
}

// describeBase is baseNotFoundHint once origin has named its default branch.
func describeBase(root, base, branch string) string {
	want := "origin/" + branch
	if local := gitx.OriginHead(root); local != want {
		return fmt.Sprintf(
			"origin's default branch is %s, but this clone's origin/HEAD is %s. Run `git fetch --prune origin` and `git remote set-head origin --auto`, then rebase onto %s or pass `--base %s`.",
			branch, valueOr(local, "unset"), want, want,
		)
	}
	if !gitx.IsAncestor(root, base, want) {
		return fmt.Sprintf(
			"The base %s is not on %s, origin's default branch, as of the last fetch. Run `git fetch origin`, then rebase onto %s or pass `--base %s` if the change targets it.",
			shortSHA(base), want, want, want,
		)
	}
	return ""
}

// repositoryName is owner/name of a canonical repository URL, for a
// question about it.
func repositoryName(repoURL string) string {
	parsed, err := url.Parse(repoURL)
	if err != nil || parsed.Path == "" {
		return repoURL
	}
	return strings.TrimSuffix(strings.Trim(parsed.Path, "/"), ".git")
}

// sendCheck sends a check and waits for its result. A service from before
// attribution rejects the unknown field as invalid_change; the check itself
// must still run, so it is sent again without it, the request keeps it
// dropped for anything sent after, and verbose, when set, hears why.
func sendCheck(ctx context.Context, client *service.Client, request *service.Request, verbose io.Writer) (service.Response, error) {
	response, err := client.Check(ctx, *request)
	var rejected *service.Error
	if request.Attribution != nil && errors.As(err, &rejected) && rejected.Body.Code == invalidChange {
		if verbose != nil {
			fmt.Fprintln(verbose, "slopcop: the service refused the attribution; sending the check without it")
		}
		request.Attribution = nil
		response, err = client.Check(ctx, *request)
	}
	return response, err
}

// preparedCheck is a check request ready to send, and the client to send it.
// The caller adds the attribution.
type preparedCheck struct {
	client   *service.Client
	snapshot gitx.Snapshot
	// reviewTree is the same size-filtered scope as the uploaded patch, but
	// without selected ignored policy files. snapshot.Tree stays unfiltered.
	reviewTree string
	// checkedTree is the tree the uploaded patch makes of the base: the
	// files the service analyzed.
	checkedTree  string
	omittedFiles []report.OmittedFile
	// binaryFiles are the changed binary files that stayed in scope.
	binaryFiles []report.BinaryFile
	staticErr   error
	request     service.Request
	repoRoot    string
	// workspace is the workspace the check is asked to run in, by the name
	// or id it was given or mapped to; empty for the login's own.
	workspace string
	// choice is that workspace with where it came from.
	choice workspaceChoice
	// fingerprint names the check without its attribution, in that
	// workspace, so the Stop hook can tell a check it already has the
	// result of.
	fingerprint string
	// files and bytes describe the patch, for --verbose.
	files, bytes int
	// base says how the base was chosen.
	base baseResolution
}

// prepareCheck does the local work a check needs before it can be sent: it
// finds the repository, its base and selection, builds the patch, and
// resolves the service and credential.
func prepareCheck(opts *checkOptions, cfg config.Config, serviceFlag, version, failOn string) (preparedCheck, error) {
	var prepared preparedCheck
	repoRoot, err := gitx.Toplevel(opts.root)
	if err != nil {
		return prepared, err
	}
	resolution, err := resolveBases(repoRoot, opts.base, opts.baseExact)
	if err != nil {
		return prepared, err
	}
	selection, err := resolveSelection(repoRoot, opts)
	if err != nil {
		return prepared, err
	}
	// A default base that yields an empty patch, such as the upstream of a
	// branch with nothing unpushed, gives way to the next default, so a check
	// right after a push still covers the branch's work.
	snapshot, err := gitx.CaptureWorkingTree(repoRoot, selection.Files)
	if err != nil {
		return prepared, err
	}
	var base string
	var change gitx.WorkingChange
	for _, candidate := range resolution.bases {
		base = candidate
		// Resolve the base from the complete change before filtering. An
		// artifact-only change must not fall back to an unrelated older base.
		var changed []byte
		changed, err = gitx.RunBytes(repoRoot, nil, "diff", "--no-ext-diff", "--no-textconv", "--name-only", "--no-renames", "-z", base, snapshot.PatchTree)
		if err != nil {
			return prepared, err
		}
		if len(changed) > 0 {
			break
		}
	}
	patchTree, reviewTree := snapshot.PatchTree, snapshot.Tree
	exclude := excludeOutsideSelection(repoRoot, snapshot.PatchTree, selection.Files)
	var omitted []report.OmittedFile
	{
		filtered, err := gitx.FilterCheckTree(repoRoot, base, patchTree, exclude, opts.include...)
		if err != nil {
			return prepared, err
		}
		patchTree = filtered.Tree
		for _, file := range filtered.OmittedFiles {
			omitted = append(omitted, report.OmittedFile{Path: file.Path, Bytes: file.Bytes, Reason: file.Reason})
		}
		if snapshot.Tree == snapshot.PatchTree {
			reviewTree = patchTree
		} else {
			filteredReview, err := gitx.FilterCheckTree(repoRoot, base, snapshot.Tree, exclude, opts.include...)
			if err != nil {
				return prepared, err
			}
			reviewTree = filteredReview.Tree
		}
	}
	change, err = gitx.TreePatch(repoRoot, base, patchTree)
	if err != nil {
		return prepared, err
	}
	inspected, err := gitx.InspectBinaryFiles(repoRoot, base, reviewTree)
	if err != nil {
		return prepared, err
	}
	var binaryFiles []report.BinaryFile
	for _, file := range inspected {
		binaryFiles = append(binaryFiles, report.BinaryFile{Path: file.Path, Bytes: file.Bytes, Format: file.Format, Verified: file.Format != "", Deleted: file.Deleted})
	}
	prepared = preparedCheck{repoRoot: repoRoot, snapshot: snapshot, reviewTree: reviewTree, checkedTree: patchTree,
		omittedFiles: omitted, binaryFiles: binaryFiles, files: change.Files, bytes: len(change.Patch), base: resolution,
		request: service.Request{BaseCommit: base, Change: base64.StdEncoding.EncodeToString(change.Patch), FailOn: failOn}}
	if opts.mode == modeAI || opts.ai {
		return prepared, nil
	}
	origin, err := gitx.OriginURL(repoRoot)
	if err != nil {
		if opts.mode == "" {
			return prepared, err
		}
		prepared.staticErr = err
		return prepared, nil
	}
	repoURL, err := gitx.CanonicalRepoURL(origin)
	if err != nil {
		if opts.mode == "" {
			return prepared, err
		}
		prepared.staticErr = err
		return prepared, nil
	}
	target := resolveServiceURL(cfg, serviceFlag)
	token := resolveToken(cfg, target)
	client, err := service.New(target, token, "slopcop/"+version, opts.timeout)
	if err != nil {
		if opts.mode == "" {
			return prepared, err
		}
		prepared.staticErr = err
		return prepared, nil
	}
	request := service.Request{
		RepoURL:    repoURL,
		BaseCommit: base,
		Change:     base64.StdEncoding.EncodeToString(change.Patch),
		FailOn:     failOn,
		Certainty:  opts.certainty,
	}
	if !selection.IsEmpty() {
		request.Selection = &selection
	}
	choice := chooseWorkspace(opts.workspace, cfg, target, repoURL)
	workspace := choice.Value
	prepared.client = client
	prepared.request = request
	prepared.workspace = workspace
	prepared.choice = choice
	prepared.fingerprint = fingerprintCheck(target, token, workspace, request)
	return prepared, nil
}

// excludeOutsideSelection is the gitx.Excluder for a check: the paths the
// .bifrostignore files and [check] exclude of patchTree, the tree the service
// receives, keep out of every check, except the selected policy files, which
// the service must receive.
func excludeOutsideSelection(repoRoot, patchTree string, selected []string) gitx.Excluder {
	return func(paths []string) (map[string]bool, error) {
		excluded, err := reposize.ExcludedPaths(context.Background(), repoRoot, patchTree, paths)
		if err != nil {
			return nil, err
		}
		for _, file := range selected {
			delete(excluded, file)
		}
		return excluded, nil
	}
}

func omittedFilesNotice(files []report.OmittedFile) string {
	var notices []string
	var excludedFiles, largeFiles int
	var excludedBytes, largeBytes int64
	for _, file := range files {
		if file.Reason == gitx.OmittedExcluded {
			excludedFiles++
			excludedBytes += file.Bytes
		} else {
			largeFiles++
			largeBytes += file.Bytes
		}
	}
	if excludedFiles > 0 {
		notices = append(notices, fmt.Sprintf("slopcop: left %d changed file(s) (%d bytes) that .bifrostignore or [check] exclude keeps out of checks out of the upload and local review.", excludedFiles, excludedBytes))
	}
	if largeFiles > 0 {
		notices = append(notices, fmt.Sprintf("slopcop: omitted %d large JSON/binary file(s) (%d bytes) from upload and local review; their contents were not analyzed. The report lists paths, sizes and reasons. Use --include <path-or-glob> to check selected files.", largeFiles, largeBytes))
	}
	return strings.Join(notices, "\n")
}

// onlyOmitted reports a check the size filter emptied. One the exclusions
// helped empty is still sent, so the service can say what its [check]
// leaves out, or that it cannot read it.
func (p preparedCheck) onlyOmitted() bool {
	if p.bytes != 0 || len(p.omittedFiles) == 0 {
		return false
	}
	for _, file := range p.omittedFiles {
		if file.Reason == gitx.OmittedExcluded {
			return false
		}
	}
	return true
}

// onlyExcluded reports a change whose every changed file .bifrostignore or
// [check] exclude keeps out of checks, so nothing in it is in scope.
func (p preparedCheck) onlyExcluded() bool {
	if p.bytes != 0 || len(p.omittedFiles) == 0 {
		return false
	}
	for _, file := range p.omittedFiles {
		if file.Reason != gitx.OmittedExcluded {
			return false
		}
	}
	return true
}

func omittedOnlyOutcome(prepared preparedCheck) staticOutcome {
	return staticOutcome{status: "skipped", skipped: &report.Skipped{
		Code: "all_files_omitted", State: "no-analyzable-change", Repository: prepared.request.RepoURL,
		Message: "Every changed file was omitted by the large-data size filter; nothing was uploaded or analyzed and no push approval was recorded.",
	}}
}

func resolveEnums(opts *checkOptions) (string, string, error) {
	format := strings.ToLower(strings.TrimSpace(opts.format))
	if format == "" {
		format = report.FormatHuman
	}
	switch format {
	case report.FormatHuman, report.FormatJSON, report.FormatSARIF:
	default:
		return "", "", fmt.Errorf("unknown format %q (expected human, json, or sarif)", opts.format)
	}
	failOn := strings.ToLower(strings.TrimSpace(opts.failOn))
	if failOn == "" {
		failOn = failOnFinding
	}
	switch failOn {
	case "never", failOnFinding, "note", "warning", "error":
	default:
		return "", "", fmt.Errorf(
			"unknown fail-on level %q (expected never, finding, note, warning, or error)",
			opts.failOn,
		)
	}
	return format, failOn, nil
}

// resolveBases lists the bases to try, in order. An explicit revision is the
// only one. Otherwise each is a commit the service can already resolve: the
// merge base with the branch upstream, then the remote default branch, or HEAD
// when neither is known. A wrong guess is corrected by the service's
// base_not_found remedy.
//
// --base names the branch the change started from and, like
// `git diff REV...HEAD`, resolves to its merge base with HEAD, so commits that
// landed on that branch after the branch point are not sent as reverse edits.
// When HEAD has since merged a line the named branch lacks, such as a newer
// origin default branch merged into a branch stacked on another PR, the merge
// base alone would send every merged-in commit too; mergedInBase moves past
// it. --base-exact uses the named commit as is.
func resolveBases(root, rev, exact string) (baseResolution, error) {
	var resolution baseResolution
	rev, exact = strings.TrimSpace(rev), strings.TrimSpace(exact)
	if rev != "" && exact != "" {
		return resolution, fmt.Errorf("--base and --base-exact cannot be used together")
	}
	if exact != "" {
		base, err := gitx.ResolveCommit(root, exact)
		if err != nil {
			return resolution, err
		}
		return baseResolution{bases: []string{base}}, nil
	}
	if rev != "" {
		commit, err := gitx.ResolveCommit(root, rev)
		if err != nil {
			return resolution, err
		}
		base, ok, err := gitx.MergeBase(root, "HEAD", commit)
		if err != nil {
			return resolution, err
		}
		if !ok {
			return resolution, fmt.Errorf(
				"%s has no merge base with HEAD; pass --base-exact %s to diff against it directly",
				rev, rev,
			)
		}
		return mergedInBase(root, rev, commit, base), nil
	}
	var bases []string
	if base, ok, err := gitx.UpstreamBase(root); err != nil {
		return resolution, err
	} else if ok {
		bases = append(bases, base)
	}
	if base, ok, err := gitx.OriginHeadBase(root); err != nil {
		return resolution, err
	} else if ok && (len(bases) == 0 || bases[0] != base) {
		bases = append(bases, base)
	}
	if len(bases) > 0 {
		return baseResolution{bases: bases}, nil
	}
	head, err := gitx.HeadCommit(root)
	if err != nil {
		return resolution, fmt.Errorf("this repository has no commits yet, so there is nothing to check")
	}
	return baseResolution{bases: []string{head}}, nil
}

// baseResolution is the bases to try, in order, and what the CLI has to say
// about how it chose them.
type baseResolution struct {
	bases []string
	// notes explain a base the CLI moved, for --verbose.
	notes []string
	// warning says the patch carries commits HEAD merged in from a line the
	// base lacks, which a check always prints.
	warning string
}

// mergedInBase settles the base for --base rev, whose merge base with HEAD
// is base. A branch stacked on rev that has since merged a newer default
// branch, or rev's own upstream, reaches commits rev does not, and diffing
// from base would send all of them as the change.
//
// The service needs a base it can fetch, so the base stays a real commit
// rather than a local synthetic merge: when a merged-in line's merge base
// with HEAD descends from base, as it does once rev has landed on that line,
// it holds everything base does and becomes the base, which leaves exactly
// the branch's own work. When the two lines have diverged, no fetchable
// commit holds both, so base stays and the result warns that the patch
// carries the merged-in commits.
//
// A line HEAD is already on, such as a commit that has landed on the default
// branch, is skipped: its merge base with HEAD is HEAD itself, HEAD merged
// nothing from it, and moving the base there would empty the patch.
func mergedInBase(root, rev, commit, base string) baseResolution {
	type line struct{ name, mergeBase string }
	var lines []line
	seen := map[string]bool{commit: true}
	head, _ := gitx.HeadCommit(root)
	for _, name := range []string{gitx.OriginHead(root), upstreamOf(root, rev)} {
		if name == "" {
			continue
		}
		tip, err := gitx.ResolveCommit(root, name)
		if err != nil || seen[tip] {
			continue
		}
		seen[tip] = true
		mergeBase, ok, err := gitx.MergeBase(root, "HEAD", tip)
		if err != nil || !ok || mergeBase == head {
			continue
		}
		lines = append(lines, line{name, mergeBase})
	}
	resolution := baseResolution{bases: []string{base}}
	for moved := true; moved; {
		moved = false
		for _, merged := range lines {
			if merged.mergeBase == base || !gitx.IsAncestor(root, base, merged.mergeBase) {
				continue
			}
			resolution.notes = append(resolution.notes, fmt.Sprintf(
				"slopcop: HEAD merged %s, which already holds %s's merge base %s, so the base is %s, the merge base with %s",
				merged.name, rev, shortSHA(base), shortSHA(merged.mergeBase), merged.name,
			))
			base = merged.mergeBase
			moved = true
		}
	}
	resolution.bases = []string{base}
	for _, merged := range lines {
		if gitx.IsAncestor(root, merged.mergeBase, base) {
			continue
		}
		count := gitx.CountCommits(root, base, merged.mergeBase)
		resolution.warning = fmt.Sprintf(
			"slopcop: HEAD merged %d commit(s) from %s that %s does not have, so the patch carries their changes too. "+
				"To check only this branch's work, merge %s into %s and push it, then check again; "+
				"to check the whole stack, pass --base %s.",
			count, merged.name, rev, merged.name, rev, merged.name,
		)
		break
	}
	return resolution
}

// upstreamOf is the upstream of the local branch rev names, or "".
func upstreamOf(root, rev string) string {
	if strings.HasPrefix(rev, "-") {
		return ""
	}
	return gitx.UpstreamOf(root, rev)
}

// resolveSelection collects the policy selection the service should evaluate
// in addition to its hosted catalog. The repository's own rules under
// .slopcop/policies join unless discovery is disabled.
func resolveSelection(root string, opts *checkOptions) (service.Selection, error) {
	var selection service.Selection
	var files []string
	if !opts.noDiscovery {
		discovered, err := discoverPolicies(root)
		if err != nil {
			return selection, err
		}
		files = append(files, discovered...)
	}
	for _, file := range opts.policies {
		relative, err := policyPath(root, file)
		if err != nil {
			return selection, err
		}
		files = append(files, relative)
	}
	selection.Files = dedupe(files)
	selection.PolicyIDs = dedupe(opts.policyIDs)
	selection.Packs = dedupe(opts.policyPacks)
	selection.Categories = dedupe(opts.policyCategories)
	return selection, nil
}

func discoverPolicies(root string) ([]string, error) {
	dir := filepath.Join(root, filepath.FromSlash(policyDirectory))
	info, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", policyDirectory, err)
	}
	if !info.IsDir() {
		return nil, nil
	}
	var files []string
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".rqlp") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", policyDirectory, err)
	}
	sort.Strings(files)
	return files, nil
}

func policyPath(root, file string) (string, error) {
	file = strings.TrimSpace(file)
	if file == "" {
		return "", fmt.Errorf("policy file path is empty")
	}
	if !strings.HasSuffix(file, ".rqlp") {
		return "", fmt.Errorf("policy file must end in .rqlp: %s", file)
	}
	absolute := file
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(root, file)
	}
	relative, err := filepath.Rel(root, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("policy file must be inside the repository: %s", file)
	}
	relative = filepath.ToSlash(relative)
	if !safeRelativePath(relative) {
		return "", fmt.Errorf("policy file path is not usable: %s", file)
	}
	return relative, nil
}

func safeRelativePath(path string) bool {
	if path == "" || strings.HasPrefix(path, "/") || strings.Contains(path, "\\") {
		return false
	}
	if len(path) > 4096 || strings.Contains(path, "\x00") {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func dedupe(values []string) []string {
	var out []string
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func resolveServiceURL(cfg config.Config, flagValue string) string {
	for _, candidate := range []string{
		flagValue,
		os.Getenv("SLOPCOP_SERVICE"),
	} {
		if value := strings.TrimSpace(candidate); value != "" {
			return value
		}
	}
	return cfg.DefaultService()
}

// resolveToken is the credential for the service a command talks to:
// $SLOPCOP_TOKEN, which CI sets for the one service it uses, else the login
// stored for that service. A login stored for another service is never sent.
func resolveToken(cfg config.Config, serviceURL string) string {
	if token := strings.TrimSpace(os.Getenv("SLOPCOP_TOKEN")); token != "" {
		return token
	}
	return cfg.TokenFor(serviceURL)
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func shortSHA(value string) string {
	if len(value) > 12 {
		return value[:12]
	}
	return value
}

// staticBlockingCount uses the service's new findings. The service lists the
// most severe findings first, so omitted findings cannot hide a higher rank.
func staticBlockingCount(response service.Response, level string) int {
	count := 0
	for _, finding := range response.Findings {
		if blocksPush(finding, level) {
			count++
		}
	}
	return count
}

// blocksPush says whether a finding is new and at or above the push level.
// A severity the CLI does not know counts as an error; info never blocks.
func blocksPush(finding service.Finding, level string) bool {
	if level == "never" || !finding.New || finding.Severity == "info" {
		return false
	}
	rank := map[string]int{"note": 1, "warning": 2, "error": 3}
	severity := rank[finding.Severity]
	if severity == 0 {
		severity = 3
	}
	return level == "finding" || severity >= rank[level]
}

// pullRequestFailOn is the threshold a SlopCop pull request check applies. It
// is the service's, fixed, and has no setting: any new definite finding fails
// the pull request, whatever its severity. The push level (gate.blockOn) is
// the CLI's and defaults to error, so the two disagree about every finding in
// between. advisoryFindings counts those, and the docs' exit-codes page states
// both rules side by side; change it with this constant if the service's
// rule changes.
const pullRequestFailOn = failOnFinding

// advisoryFindings counts the new findings that do not block the push at
// level but fail a check under pullRequestFailOn: the definite ones below the
// push level. A service from before certainty omits it, and listed only
// definite findings. An info finding fails neither. The new findings the
// service left out of the list (summary.omittedFindings) count too: the push
// gate does not see them, so none blocks, and the pull request check fails
// on them. Under --certainty all some of those may be less certain, so the
// count can then run high, never low.
func advisoryFindings(response service.Response, level string) int {
	count := int(response.Summary.OmittedFindings)
	for _, finding := range response.Findings {
		definite := finding.Certainty == "" || finding.Certainty == certaintyDefinite
		if finding.New && definite && finding.Severity != "info" && !blocksPush(finding, level) {
			count++
		}
	}
	return count
}

// advisoryNotice is the stderr line for a tree that was approved for push
// with findings a pull request check fails on. Without it a change that is
// green at the push gate goes red on the pull request with no warning.
func advisoryNotice(count int, level string) string {
	noun, verb := "findings", "do"
	if count == 1 {
		noun, verb = "finding", "does"
	}
	return fmt.Sprintf("slopcop: %d new static %s below the push level (%s) %s not block the push, but a SlopCop pull request check fails on any new %s. "+
		"Before you open a pull request, fix them, run `slopcop ignore <id>` for a false positive, "+
		"or keep deliberate test inputs out of the rule with [check] in .slopcop/config.toml.",
		count, noun, level, verb, pullRequestFailOn)
}

// staticPasses says whether the static half allows the tree to be approved
// for push. A half that did not run (ai mode) or skipped an inactive
// repository allows it; otherwise it needs a finished response of a known
// status with no new finding at or above the push level and no run failure.
// Findings below that level are advisory, and --fail-on plays no part, so no
// option of a check can loosen what blocks a push. A rule that could not
// answer for a cause of its own (unsupported, timed out) does not block, but
// a run failure left every check of an analyzer run without an answer, such
// as when the analyzer process was killed, so that run checked nothing.
func staticPasses(static staticOutcome, mode, level string) bool {
	return mode == modeAI || static.skipped != nil ||
		(static.response != nil && static.response.KnownStatus() &&
			staticBlockingCount(*static.response, level) == 0 && len(static.response.RunFailures) == 0)
}

// approvalReason names why a check did not approve its tree, for the line a
// person or agent reads before pushing. It checks the causes in the order the
// approval rule does.
func approvalReason(empty, reviewPassed bool, verdict review.Verdict, static staticOutcome, mode, level string) string {
	if empty {
		return "nothing was checked (empty change)"
	}
	if !reviewPassed {
		if verdict.Status == review.StatusFindings {
			return "the review found problems (fix them, or override wrong ones with slopcop override)"
		}
		return "the review did not complete"
	}
	if mode != modeAI && static.response != nil && static.response.KnownStatus() {
		if n := staticBlockingCount(*static.response, level); n > 0 {
			return fmt.Sprintf("%d static finding(s) at or above %s", n, level)
		}
		if failures := static.response.RunFailures; len(failures) > 0 {
			return fmt.Sprintf("the static analyzer run failed (%s), so it checked nothing; run the check again", failures[0].Reason)
		}
	}
	return "the static check did not complete (failed, pending, or unknown status)"
}

func reviewerNames(families []review.Family) string {
	if len(families) == 0 {
		return "no configured reviewers"
	}
	names := make([]string, len(families))
	for i, f := range families {
		names[i] = string(f)
	}
	return strings.Join(names, ", ")
}

// maxQuotedFileBytes bounds the file a report reads to quote a finding's
// span.
const maxQuotedFileBytes = 4 << 20

// treeSource reads files from the tree a check uploaded, so a report quotes
// the text the service analyzed rather than a later edit. The path is the
// service's, so it only ever names an object in that tree.
func treeSource(root, tree string) report.Source {
	if tree == "" {
		return nil
	}
	return func(path string) ([]byte, bool) {
		object := tree + ":" + path
		size, err := gitx.Run(root, "cat-file", "-s", object)
		if err != nil {
			return nil, false
		}
		if bytes, err := strconv.Atoi(size); err != nil || bytes > maxQuotedFileBytes {
			return nil, false
		}
		content, err := gitx.RunBytes(root, nil, "cat-file", "blob", object)
		return content, err == nil
	}
}

// warnLowCredits says on stderr, at the point of use and before a check is
// refused, that the workspace's free checks and credits are running low.
// JSON carries the same figures in its credits block instead.
func warnLowCredits(cmd *cobra.Command, response *service.Response) {
	if response == nil {
		return
	}
	if line := report.LowCreditsLine(response.Credits); line != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), line)
	}
}
