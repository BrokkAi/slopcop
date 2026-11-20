package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/gitx"
	"github.com/BrokkAi/slopcop/internal/report"
	"github.com/BrokkAi/slopcop/internal/review"
)

func newReviewCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "review",
		Short: "Look at the local LLM review",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(newReviewShowCommand())
	return command
}

func newReviewShowCommand() *cobra.Command {
	var root, format string
	command := &cobra.Command{
		Use:   "show",
		Short: "Show the latest local review again, without running reviewers",
		Long: `Show the latest local review of this checkout again, without running a
reviewer or spending a check. Each reviewer's reply is rebuilt from its log,
so it is shown in full even when the report that check printed was not, and
its findings are parsed again.

The review is only shown: it approves nothing and changes no state. Run
'slopcop check' to review the working tree as it is now.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runReviewShow(cmd, root, format)
		},
	}
	command.Flags().StringVar(&root, "root", ".", "repository path")
	command.Flags().StringVar(&format, "format", report.FormatHuman, "output format: human, json, or sarif")
	return command
}

func runReviewShow(cmd *cobra.Command, root, format string) error {
	format = strings.ToLower(strings.TrimSpace(format))
	switch format {
	case report.FormatHuman, report.FormatJSON, report.FormatSARIF:
	default:
		return fmt.Errorf("unknown format %q (expected human, json, or sarif)", format)
	}
	repo, err := gitx.Toplevel(root)
	if err != nil {
		return err
	}
	record, err := review.LastRecord(repo)
	if err != nil {
		return err
	}
	if record == nil {
		return errors.New("no local review has run in this checkout; run 'slopcop check'")
	}
	verdict, problems := review.Replay(record.Verdict)
	for _, problem := range problems {
		fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: %v\n", problem)
	}
	if verdict, err = review.ApplyFindingOverrides(repo, verdict); err != nil {
		return err
	}
	status := string(verdict.Status)
	if verdict.Status == review.StatusEmpty {
		status = string(review.StatusClean)
	}
	return report.WriteAI(cmd.OutOrStdout(), format, report.Options{Review: &verdict, Status: status})
}
