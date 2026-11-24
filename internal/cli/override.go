package cli

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/gitx"
	"github.com/BrokkAi/slopcop/internal/report"
	"github.com/BrokkAi/slopcop/internal/review"
	"github.com/BrokkAi/slopcop/internal/service"
)

type overrideOptions struct {
	root, reason, workspace, undo, format string
	list                                  bool
}

// newOverrideCommand accepts local review findings: the whole latest review
// for the current working tree, or one finding for every later review.
func newOverrideCommand(serviceURL *string, version string) *cobra.Command {
	opts := &overrideOptions{}
	command := &cobra.Command{
		Use:   "override [rv-id] --reason \"why\"",
		Short: "Accept local review findings that are wrong, with a recorded reason",
		Long: `Accept local review findings that are wrong, with a reason. An override
takes effect locally. If the workspace turned on review tracking for the check
that reported the finding, the override and its reason are also sent to it.

  slopcop override <rv-id> --reason "why it does not hold"
      Override one review finding, by the rv- id in review.findings. Later
      reviews in this clone, on any branch or worktree, do not report it
      again: reviewers are shown it with your reason and told not to raise
      it, and a finding that repeats it, even reworded near the same lines,
      is listed under review.overridden and does not fail the check. Run
      'slopcop check' again afterwards: the same review is reused and leaves
      the finding out. Use this for a false positive that keeps coming back.

  slopcop override --reason "why the findings do not hold"
      Accept every finding of the latest complete review, for the current
      working tree only, when the static check passed. The next change is
      reviewed afresh and its findings can come back.

  slopcop override --undo <rv-id>
      Undo a finding override. Takes the overridden id, or the id of a
      finding listed under review.overridden. Every tree whose review relied
      on the override loses its push approval until it is checked again.

  slopcop override --list [--format json]
      List the clone's finding overrides, newest first.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOverride(cmd, opts, args, *serviceURL, version)
		},
	}
	flags := command.Flags()
	flags.StringVar(&opts.root, "root", ".", "repository path")
	flags.StringVar(&opts.reason, "reason", "", "why the findings do not hold; later reviewers are shown it")
	flags.StringVar(&opts.undo, "undo", "", "undo the override of the review finding with this rv- id")
	flags.BoolVar(&opts.list, "list", false, "list the clone's finding overrides")
	addFormatFlag(command, &opts.format)
	// An override goes to the workspace of the check it is about, whatever
	// is named here, but an agent reuses check's flags across the check ->
	// override loop, so -w is accepted and ignored rather than refused.
	flags.StringVarP(&opts.workspace, "workspace", "w", "", "accepted for parity with check and ignored: a tracked override goes to the workspace of its check")
	return command
}

func runOverride(cmd *cobra.Command, opts *overrideOptions, args []string, serviceURL, version string) error {
	modes := 0
	for _, set := range []bool{len(args) > 0, opts.undo != "", opts.list} {
		if set {
			modes++
		}
	}
	if modes > 1 {
		return errors.New("give one of an rv- id, --undo <rv-id> or --list")
	}
	if err := checkFormat(opts.format); err != nil {
		return err
	}
	repo, err := gitx.Toplevel(opts.root)
	if err != nil {
		return err
	}
	switch {
	case opts.list:
		return listFindingOverrides(cmd.OutOrStdout(), repo, opts.format)
	case opts.undo != "":
		removed, err := undoFindingOverride(cmd.OutOrStdout(), repo, strings.TrimSpace(opts.undo))
		if err == nil && removed != "" {
			sendDispositions(cmd.Context(), serviceURL, version, review.TrackedChecksRelyingOn(repo, removed),
				service.ReviewDisposition{Kind: service.DispositionOverrideUndone, Key: removed})
		}
		return err
	}
	if strings.TrimSpace(opts.reason) == "" {
		return errors.New("--reason is required: say why the findings do not hold; later reviewers are shown it")
	}
	if len(args) > 0 {
		id := strings.TrimSpace(args[0])
		if err := overrideFinding(cmd.OutOrStdout(), repo, id, opts.reason); err != nil {
			return err
		}
		if ref, ok := review.TrackedCheckOf(repo, id); ok {
			sendDispositions(cmd.Context(), serviceURL, version, []review.CheckRef{ref}, service.ReviewDisposition{
				Kind: service.DispositionFindingOverridden, Key: id, Reason: review.DispositionReason(opts.reason),
			})
		}
		return nil
	}
	ref, err := overrideReview(cmd.OutOrStdout(), repo, opts.reason)
	if err != nil {
		return err
	}
	if ref != nil {
		sendDispositions(cmd.Context(), serviceURL, version, []review.CheckRef{*ref}, service.ReviewDisposition{
			Kind: service.DispositionReviewOverridden, Reason: review.DispositionReason(opts.reason),
		})
	}
	return nil
}

// checkReviewFindingID refuses an id that is not a review finding's.
func checkReviewFindingID(id string) error {
	if review.IsFindingID(id) {
		return nil
	}
	if uuidPattern.MatchString(id) {
		return fmt.Errorf("%s is a static finding's id; run `slopcop ignore %s` for a static false positive", id, id)
	}
	return fmt.Errorf("%q is not a review finding id: use an rv- id from review.findings of the latest `slopcop check`", id)
}

// overrideFinding overrides one review finding for later reviews.
func overrideFinding(out io.Writer, repo, id, reason string) error {
	if err := checkReviewFindingID(id); err != nil {
		return err
	}
	override, added, err := review.OverrideFinding(repo, id, reason)
	if err != nil {
		return err
	}
	verb := "overrode"
	if !added {
		verb = "updated the reason of the override of"
	}
	fmt.Fprintf(out, "slopcop: %s review finding %s (%s); later reviews in this clone will not report it, even reworded. Run `slopcop check` again: the same review is reused and leaves it out.\n",
		verb, override.ID, report.Printable(override.Finding.Title))
	return nil
}

// undoFindingOverride removes one finding override and returns the id it was
// recorded under, or "" when nothing was overridden.
func undoFindingOverride(out io.Writer, repo, id string) (string, error) {
	if err := checkReviewFindingID(id); err != nil {
		return "", err
	}
	removed, found, err := review.UndoFindingOverride(repo, id)
	if err != nil {
		return "", err
	}
	if !found {
		fmt.Fprintf(out, "slopcop: review finding %s is not overridden; nothing changed\n", id)
		return "", nil
	}
	fmt.Fprintf(out, "slopcop: review finding %s is no longer overridden; later reviews may report it again, and the trees that passed because of it must be checked again before a push\n", removed.ID)
	return removed.ID, nil
}

// listFindingOverrides prints the clone's finding overrides, newest first.
func listFindingOverrides(out io.Writer, repo, format string) error {
	overrides, err := review.LoadFindingOverrides(repo)
	if err != nil {
		return err
	}
	newest := make([]review.FindingOverride, 0, len(overrides))
	for i := len(overrides) - 1; i >= 0; i-- {
		newest = append(newest, overrides[i])
	}
	if format == report.FormatJSON {
		return writeJSON(out, struct {
			Overrides []review.FindingOverride `json:"overrides"`
		}{newest})
	}
	if len(newest) == 0 {
		fmt.Fprintln(out, "slopcop: no review findings are overridden in this clone")
		return nil
	}
	noun := "findings are"
	if len(newest) == 1 {
		noun = "finding is"
	}
	fmt.Fprintf(out, "slopcop: %d review %s overridden in this clone\n", len(newest), noun)
	for _, o := range newest {
		fmt.Fprintf(out, "  %s (overridden %s)\n    Reason: %s\n", report.ReviewFindingLine(o.Finding), o.OverriddenAt.UTC().Format("2006-01-02 15:04 UTC"), report.Printable(o.Reason))
	}
	fmt.Fprintln(out, "slopcop: undo one with `slopcop override --undo <id>`")
	return nil
}

// overrideReview accepts every finding of the latest review for the current
// working tree. It returns the tracked check the review ran beside, if any.
func overrideReview(out io.Writer, repo, reason string) (*review.CheckRef, error) {
	// Never waits: the running check is about to replace the review this
	// override means to accept, and its result has to be read first.
	store, err := review.OpenStore(repo)
	if errors.Is(err, review.ErrBusy) {
		return nil, fmt.Errorf("%w, so nothing was accepted; when it finishes, read its result and run the override again if it still applies", err)
	}
	if err != nil {
		return nil, err
	}
	defer store.Close()
	snapshot, err := gitx.CaptureWorkingTree(repo, nil)
	if err != nil {
		return nil, err
	}
	if err = store.Override(reason, snapshot.Tree); err != nil {
		return nil, err
	}
	var tracked *review.CheckRef
	if last, err := store.Last(); err == nil && last != nil && last.Check != nil && last.Check.Tracked {
		if ref := review.CurrentCheck(repo, *last.Check); ref.Tracked {
			tracked = &ref
		}
	}
	gitDir, err := gitx.Run(repo, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(out, "slopcop: accepted the review's findings for tree %s; the reason is recorded in %s and shown to the reviewers of later rounds\n", snapshot.Tree, filepath.Join(gitDir, "slopcop", "overrides.jsonl"))
	return tracked, nil
}
