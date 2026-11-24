package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/review"
	"github.com/BrokkAi/slopcop/internal/service"
)

// reviewReportTimeout bounds everything one command sends about a local
// review. Tracking must never hold up a check or an override.
const reviewReportTimeout = 3 * time.Second

// workspaceLookupTimeout is the part of that a stalled workspace lookup can
// use, so the report itself is still sent.
const workspaceLookupTimeout = time.Second

// maxDispositionSends bounds the requests one undo has open at a time.
const maxDispositionSends = 4

// reviewCheckRef is the hosted check a review ran beside, or nil when the
// check gave no id to name it by. Only a check that ran both halves counts,
// and only the answer's exact "findings" turns tracking on, so a CLI upgrade
// alone never starts sending.
func reviewCheckRef(mode string, static staticOutcome, client *service.Client) *review.CheckRef {
	response := static.response
	if mode != modeBoth || response == nil || client == nil || response.CheckID == "" || !response.KnownStatus() {
		return nil
	}
	return &review.CheckRef{
		ID:        response.CheckID,
		Service:   client.BaseURL,
		Workspace: firstWorkspaceID(response.WorkspaceID, client.Workspace),
		Tracked:   response.ReviewTracking == review.TrackingFindings,
	}
}

// firstWorkspaceID is the first candidate that is a workspace id, or "".
func firstWorkspaceID(candidates ...string) string {
	for _, candidate := range candidates {
		if uuidPattern.MatchString(candidate) {
			return candidate
		}
	}
	return ""
}

// reportReview sends the finished review of a tracked check. It runs after
// the report is printed and has no result: whatever happens here, the check's
// output and exit are what they were. --verbose hears why it did not land.
func reportReview(cmd *cobra.Command, ref *review.CheckRef, client *service.Client, root string, verdict review.Verdict, settings review.Settings, version string, verbose bool) {
	if ref == nil {
		// A review with no check to report to, as in --mode ai, still counts
		// as the newest to hold its findings: an override of one must not
		// reach an older tracked check through it.
		_ = review.RememberLocalReview(root, verdict)
		return
	}
	note := func(format string, args ...any) {
		if verbose {
			fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: review not reported: "+format+"\n", args...)
		}
	}
	command := func(f review.Family) []string { return review.CommandFor(f, settings) }
	body, ok := review.BuildReport(verdict, version, command)
	if !ok {
		return
	}
	checked := *ref
	remember := func(replace bool) {
		// A tracked check holds what its report carried. One that is not holds
		// every finding it reported, so none can be sent to an older check.
		var keys []string
		if checked.Tracked {
			keys = body.Keys()
		}
		if err := review.RememberCheck(root, checked, verdict, keys, replace); err != nil {
			note("could not remember the check: %v", err)
		}
	}
	if !ref.Tracked {
		remember(true)
		return
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), reviewReportTimeout)
	defer cancel()
	limited := client.BestEffort(reviewReportTimeout)
	// An older service's answer names no workspace, and a check in the login's
	// own workspace has none on the client, so ask which one the login is in.
	if checked.Workspace == "" {
		lookup, stop := context.WithTimeout(ctx, workspaceLookupTimeout)
		if whoami, err := limited.Whoami(lookup); err == nil {
			checked.Workspace = firstWorkspaceID(whoami.Workspace.ID)
		}
		stop()
	}
	// Without a workspace id a request would go to whichever workspace the
	// login is in by then, so nothing is sent, now or for later overrides.
	if checked.Workspace == "" {
		note("the check's workspace is not known")
		checked.Tracked = false
		remember(true)
		return
	}
	limited.Workspace = checked.Workspace
	status, err := limited.ReportReview(ctx, ref.ID, body)
	var refused *service.Error
	switch {
	case errors.As(err, &refused) && refused.StatusCode >= 400 && refused.StatusCode < 500:
		// The service answered that it would not store this report. A report
		// recorded here before still stands, so an entry for it is kept.
		note("%v", err)
		checked.Tracked = false
		remember(false)
	case err != nil:
		note("%v", err)
		remember(false) // It may have landed before the answer was lost.
	case status == "recorded":
		remember(true)
	case status == "already_recorded":
		// The first report stands, and its findings with it. Without a record
		// of what it held, as when another checkout sent it, nothing later can
		// name its findings, so this check is not tracked here.
		if !review.KnownCheck(root, checked.ID) {
			checked.Tracked = false
		}
		remember(false)
	case status == "off":
		checked.Tracked = false
		remember(true)
	default:
		note("the service answered status %q", status)
		remember(false)
	}
}

// sendDispositions tells the service of an override made on tracked checks.
// The local override has already succeeded; nothing here can change that, and
// nothing is printed.
func sendDispositions(ctx context.Context, serviceFlag, version string, refs []review.CheckRef, disposition service.ReviewDisposition) {
	if len(refs) == 0 {
		return
	}
	cfg, err := config.Load()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, reviewReportTimeout)
	defer cancel()
	var wg sync.WaitGroup
	slots := make(chan struct{}, maxDispositionSends)
	for _, ref := range refs {
		client := dispositionClient(cfg, serviceFlag, version, ref)
		if !ref.Tracked || client == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				return
			}
			_, _ = client.RecordReviewDisposition(ctx, ref.ID, disposition)
		}()
	}
	wg.Wait()
}

// dispositionClient is a client for the service and workspace the check ran
// against, or nil without a credential for it or a workspace id to name. $SLOPCOP_TOKEN belongs to the
// service this command is pointed at, so it is not sent to any other.
func dispositionClient(cfg config.Config, serviceFlag, version string, ref review.CheckRef) *service.Client {
	if !uuidPattern.MatchString(ref.Workspace) {
		return nil
	}
	token := cfg.TokenFor(ref.Service)
	if strings.TrimRight(strings.TrimSpace(resolveServiceURL(cfg, serviceFlag)), "/") == ref.Service {
		token = resolveToken(cfg, ref.Service)
	}
	if token == "" {
		return nil
	}
	client, err := service.New(ref.Service, token, "slopcop/"+version, reviewReportTimeout)
	if err != nil {
		return nil
	}
	client.Workspace = ref.Workspace
	return client.BestEffort(reviewReportTimeout)
}
