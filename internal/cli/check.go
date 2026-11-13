package cli

import (
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
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/attribution"
	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/gitx"
	agenthooks "github.com/BrokkAi/slopcop/internal/hooks"
	"github.com/BrokkAi/slopcop/internal/report"
	"github.com/BrokkAi/slopcop/internal/review"
	"github.com/BrokkAi/slopcop/internal/service"
)

const (
	policyDirectory = ".slopcop/policies"
	failOnFinding   = "finding"
	// certaintyDefinite and certaintyAll are the --certainty values. The
	// service answers definite findings only unless asked for all.
	certaintyDefinite = "definite"
	certaintyAll      = "all"
	invalidChange     = "invalid_change"
	baseNotFound      = "base_not_found"
	// addRepositoryNeedsWorkspace is the usage error for --add-repository
	// without --workspace: the approval is for one workspace, so the flag
	// names it rather than trusting wherever the login points.
	addRepositoryNeedsWorkspace = "--add-repository needs --workspace <name|id>: name the workspace the user approved adding the repository to, so it cannot be added to another"
	// repositoryNotEnabledCode is the CLI's refusal of a check of a repository
	// the workspace has not added. The CLI refuses locally so a check never
	// adds, and bills for, a repository by itself.
	repositoryNotEnabledCode = "repository_not_enabled"
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
		"Run `slopcop init` in the repository to add the local review, or `slopcop config set mode static` to stop this notice."
	reviewInterruptedText = "slopcop: local reviewers were stopped before the review finished; run the same check again to retry it."
	showExistingNotice    = "slopcop: --show-existing is deprecated: the service lists only the findings a change introduced or touched, and every report counts the rest."
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
	if opts.addRepository && strings.TrimSpace(opts.workspace) == "" {
		return errors.New(addRepositoryNeedsWorkspace)
	}
	if opts.showExisting {
		fmt.Fprintln(cmd.ErrOrStderr(), showExistingNotice)
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
	if cmd.Context().Err() != nil {
		fmt.Fprintln(cmd.ErrOrStderr(), interruptedBeforeSendText)
		return silentExit{interruptCode(cmd.Context())}
	}
	if err != nil {
		return err
	}
	reportOptions.OmittedFiles = prepared.omittedFiles
	if notice := omittedFilesNotice(prepared.omittedFiles); notice != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), notice)
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
	var store *review.Store
	var verdict review.Verdict
	var reviewDone chan review.Verdict
	if mode != modeStatic {
		reviewDone = make(chan review.Verdict, 1)
		fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: reviewing with %s; this can take several minutes.\n", reviewerNames(settings.Reviewers))
		go func() {
			v := review.Verdict{Status: review.StatusFailed}
			var err error
			store, err = review.OpenStore(prepared.repoRoot)
			if err != nil {
				v.Error = err.Error()
				reviewDone <- v
				return
			}
			capture, err := review.NewFilteredCapture(prepared.repoRoot, prepared.request.BaseCommit, prepared.snapshot.Tree, prepared.reviewTree)
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
	var static staticOutcome
	if prepared.onlyOmitted() {
		static = omittedOnlyOutcome(prepared)
		fmt.Fprintln(cmd.ErrOrStderr(), "slopcop: "+static.skipped.Message)
	} else if mode != modeAI {
		static = runStaticCheck(cmd, opts, prepared, failOn, version)
	} else {
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
	if store != nil {
		if mode == modeBoth {
			if err := store.SetStatic(static.exit, static.status, !staticPasses(static, mode, blockLevel)); err != nil {
				verdict.Status = review.StatusFailed
				verdict.Error = err.Error()
			}
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

	exit := static.exit
	if mode != modeStatic {
		if verdict.Status == review.StatusFindings && failOn != "never" {
			exit = service.ExitFindings
		} else if static.exit != service.ExitFindings && static.exit != service.ExitNeedsPerson && static.exit != service.ExitPending && verdict.Status == review.StatusFailed {
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
	if mode != modeAI && status != static.status {
		reportOptions.StaticStatus = static.status
	}
	if mode != modeStatic {
		reportOptions.Review = &verdict
	}
	reportOptions.EmptyChange = prepared.bytes == 0
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
	reviewPassed := mode == modeStatic || verdict.Status == review.StatusClean
	staticPassed := staticPasses(static, mode, blockLevel)
	nonempty := prepared.bytes > 0 && (static.response != nil || verdict.Status == review.StatusClean)
	reportOptions.Approved = reviewPassed && staticPassed && nonempty
	if reportOptions.Approved {
		if err := review.MarkReviewed(prepared.repoRoot, prepared.snapshot.Tree); err != nil {
			return err
		}
	} else if prepared.bytes > 0 || len(prepared.omittedFiles) > 0 {
		// A later failed decision also withdraws an override for this tree.
		if err := review.Unmark(prepared.repoRoot, prepared.snapshot.Tree); err != nil {
			return err
		}
	}
	var writeErr error
	switch {
	case mode == modeAI:
		writeErr = report.WriteAI(cmd.OutOrStdout(), format, reportOptions)
	case static.response != nil:
		writeErr = report.Write(cmd.OutOrStdout(), *static.response, format, reportOptions)
	case static.failure != nil:
		writeErr = report.WriteFailure(cmd.OutOrStdout(), static.failure, format, reportOptions)
	case static.pending != nil:
		writeErr = report.WritePending(cmd.OutOrStdout(), *static.pending, format, reportOptions)
	case static.skipped != nil:
		writeErr = report.WriteSkipped(cmd.OutOrStdout(), *static.skipped, format, reportOptions)
	default:
		writeErr = report.WriteFailure(cmd.OutOrStdout(), &service.Error{Body: service.ErrorBody{Message: "static check failed"}}, format, reportOptions)
	}
	if writeErr != nil {
		return writeErr
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
			reason := approvalReason(prepared.bytes == 0, reviewPassed, verdict, static, mode, blockLevel)
			fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: tree %s may not be pushed yet: %s.\n", tree, reason)
		}
	}
	return silentExit{exit}
}

// runStaticCheck runs only the hosted half and returns its outcome.
func runStaticCheck(cmd *cobra.Command, opts *checkOptions, prepared preparedCheck, failOn, version string) staticOutcome {
	if prepared.onlyOmitted() {
		return omittedOnlyOutcome(prepared)
	}
	failed := func(err error) staticOutcome {
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
	if prepared.staticErr != nil {
		return failed(prepared.staticErr)
	}
	client, request := prepared.client, prepared.request
	noticeService(cmd.ErrOrStderr(), client.BaseURL)
	noticeUpdates(client, cmd.ErrOrStderr(), version)
	if err := useWorkspace(cmd.Context(), client, opts.workspace); err != nil {
		return failed(err)
	}
	if opts.addRepository {
		request.AddRepository = true
	} else {
		repository, err := checkRepository(cmd.Context(), client, prepared.repoRoot)
		if err != nil {
			return failed(err)
		}
		if !repository.Active {
			if opts.ifEnabled {
				name := repository.FullName
				if name == "" {
					name = repository.URL
				}
				why := "is not active in your SlopCop workspace"
				switch repository.State {
				case statusNotAdded:
					why = "is not enabled in your SlopCop workspace"
				case statusDisabled:
					why = "is disabled in your SlopCop workspace"
				case statusOverLimit:
					why = "is over your SlopCop workspace's active-repository limit"
				case statusUnsupported:
					why = "is on a host SlopCop does not check"
				}
				message := fmt.Sprintf("%s %s, so nothing was checked.", name, why)
				fmt.Fprintln(cmd.ErrOrStderr(), "slopcop: "+message)
				return staticOutcome{skipped: &report.Skipped{Code: repositoryNotEnabledCode, State: repository.State, Repository: name, Message: message}, status: "skipped"}
			}
			if repository.State == statusNotAdded {
				return failed(repositoryNotEnabled(repository))
			}
		}
	}
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
		err = &baseRefusal{refusal: rejected, text: baseNotFoundHint(cmd.Context(), prepared.repoRoot, request.BaseCommit)}
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
		return failed(err)
	}
	if response.RepositoryAdded {
		workspace, known := resolvedWorkspace(cmd.Context(), client)
		fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: added %s to %s\n", repositoryName(request.RepoURL), workspaceName(workspace, known))
	}
	if !response.KnownStatus() {
		fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: the service answered status %q, which this version of slopcop does not know, so the check fails. Update slopcop.\n", response.Status)
	} else {
		rememberCheck(prepared, response)
	}
	exit := response.ExitCode()
	// The service's status follows failOn, so with --fail-on never, or new
	// findings below the threshold, it answers clean or incomplete. The
	// report's status names the result instead; the exit keeps the threshold.
	if response.KnownStatus() && hasNewFindings(response) {
		response.Status = service.StatusFindings
	}
	return staticOutcome{response: &response, exit: exit, status: response.Status}
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
func checkFailure(cmd *cobra.Command, err error, format string, options report.Options) error {
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
		if err := report.WriteFailure(cmd.OutOrStdout(), serviceError, format, options); err != nil {
			return err
		}
		if serviceError.NeedsPerson() {
			return silentExit{service.ExitNeedsPerson}
		}
		return silentExit{service.ExitIncomplete}
	}
	return err
}

// checkRepository is lookupRepositoryStatus, a variable so tests can stand in
// for the service when a check only needs to be told the repository is active.
var checkRepository = lookupRepositoryStatus

// repositoryNotEnabled is the local refusal of a check of a repository the
// workspace has not added. It is local so the check never sends the request
// that would add, and bill for, the repository.
func repositoryNotEnabled(repository statusRepository) *service.Error {
	name := repository.FullName
	if name == "" {
		name = repository.URL
	}
	return &service.Error{Body: service.ErrorBody{
		Code:      repositoryNotEnabledCode,
		Message:   fmt.Sprintf("%s is not enabled in your SlopCop workspace, so the check did not run.", name),
		Remedy:    "Run `slopcop enable` in the repository at a terminal to add it to the workspace, then check again.",
		Retryable: false,
	}}
}

// skipInactiveCheck is --if-enabled's answer for a repository the workspace
// does not have active: say on stderr why nothing ran, carry the skip in the
// machine formats, and exit 0 so an automatic check is not a failure.
func skipInactiveCheck(cmd *cobra.Command, repository statusRepository, format string, options report.Options) error {
	name := repository.FullName
	if name == "" {
		name = repository.URL
	}
	why := "is not active in your SlopCop workspace"
	switch repository.State {
	case statusNotAdded:
		why = "is not enabled in your SlopCop workspace"
	case statusDisabled:
		why = "is disabled in your SlopCop workspace"
	case statusOverLimit:
		why = "is over your SlopCop workspace's active-repository limit"
	case statusUnsupported:
		why = "is on a host SlopCop does not check"
	}
	message := fmt.Sprintf("%s %s, so nothing was checked.", name, why)
	fmt.Fprintln(cmd.ErrOrStderr(), "slopcop: "+message)
	if err := report.WriteSkipped(cmd.OutOrStdout(), report.Skipped{
		Code:       repositoryNotEnabledCode,
		State:      repository.State,
		Repository: name,
		Message:    message,
	}, format, options); err != nil {
		return err
	}
	return silentExit{service.ExitClean}
}

// hinted is a service refusal the CLI adds a line of its own to, after the
// service's remedy.
type hinted interface {
	hint() string
}

// baseRefusal is base_not_found with what the local clone says about why.
type baseRefusal struct {
	refusal *service.Error
	text    string
}

func (r *baseRefusal) Error() string { return r.refusal.Error() }

func (r *baseRefusal) Unwrap() error { return r.refusal }

func (r *baseRefusal) hint() string { return r.text }

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
	reviewTree   string
	omittedFiles []report.OmittedFile
	staticErr    error
	request      service.Request
	repoRoot     string
	// workspace is the workspace the check is asked to run in, by the name
	// it was given.
	workspace string
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
	var omitted []report.OmittedFile
	{
		filtered, err := gitx.FilterCheckTree(repoRoot, base, patchTree, opts.include...)
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
			filteredReview, err := gitx.FilterCheckTree(repoRoot, base, snapshot.Tree, opts.include...)
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
	prepared = preparedCheck{repoRoot: repoRoot, snapshot: snapshot, reviewTree: reviewTree,
		omittedFiles: omitted, files: change.Files, bytes: len(change.Patch), base: resolution,
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
	workspace := resolveWorkspaceName(opts.workspace)
	prepared.client = client
	prepared.request = request
	prepared.workspace = workspace
	prepared.fingerprint = fingerprintCheck(target, token, workspace, request)
	return prepared, nil
}

func omittedFilesNotice(files []report.OmittedFile) string {
	if len(files) == 0 {
		return ""
	}
	var total int64
	for _, file := range files {
		total += file.Bytes
	}
	return fmt.Sprintf("slopcop: omitted %d large JSON/binary file(s) (%d bytes) from upload and local review; their contents were not analyzed. The report lists paths, sizes and reasons. Use --include <path-or-glob> to check selected files.", len(files), total)
}

func (p preparedCheck) onlyOmitted() bool {
	return p.bytes == 0 && len(p.omittedFiles) > 0
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
	if level == "never" {
		return 0
	}
	rank := map[string]int{"note": 1, "warning": 2, "error": 3}
	threshold := rank[level]
	count := 0
	for _, finding := range response.Findings {
		if !finding.New || finding.Severity == "info" {
			continue
		}
		severity := rank[finding.Severity]
		if severity == 0 {
			severity = 3
		}
		if level == "finding" || severity >= threshold {
			count++
		}
	}
	return count
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
