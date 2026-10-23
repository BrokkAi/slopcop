package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/report"
	"github.com/BrokkAi/slopcop/internal/service"
)

const (
	// maxReasonLength bounds a report's reason, in characters after
	// trimming, as the service does.
	maxReasonLength = 2000
	// reportTimeout bounds one report request; a report is one row, not an
	// analysis.
	reportTimeout = time.Minute
)

type reportOptions struct {
	reason string
	format string
	// root is accepted so a report can carry the same flags as the check it
	// follows. The request names only the finding, whose check already
	// names the repository.
	root string
}

func newReportCommand(serviceURL *string, version string) *cobra.Command {
	opts := &reportOptions{}
	command := &cobra.Command{
		Use:   "report <finding-id> --reason TEXT",
		Short: "Report a finding as a false positive",
		Long: `Report a finding from a check as a false positive.

The finding id is the Id line of a human report, a finding's id in json, or
a result's properties["slopcop.findingId"] in sarif. The reason says why the
finding is wrong for this code, in 1 to 2000 characters; cite the code.

A reported finding stops failing checks in its repository. The team sees
every report and its reason on the SlopCop dashboard, an owner can reinstate
the finding, and false-positive rates are tracked per rule. Report only a
finding you are confident is wrong, never one that is merely hard to fix.

Exit 0 means the report was recorded, or the finding was already reported;
exit 2 means it was not.`,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return usageError{fmt.Errorf("report takes one finding id, got %d argument(s)", len(args))}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReport(cmd, opts, args[0], *serviceURL, version)
		},
	}
	flags := command.Flags()
	flags.StringVar(&opts.reason, "reason", "", "why the finding is wrong for this code, 1 to 2000 characters (required; the team sees it)")
	flags.StringVar(&opts.format, "format", "human", "output format: human or json")
	flags.StringVar(&opts.root, "root", ".", "repository the finding came from (accepted like check's; the report names only the finding)")
	return command
}

func runReport(cmd *cobra.Command, opts *reportOptions, rawID, serviceFlag, version string) error {
	format := strings.ToLower(strings.TrimSpace(opts.format))
	if format == "" {
		format = report.FormatHuman
	}
	if format != report.FormatHuman && format != report.FormatJSON {
		return fmt.Errorf("unknown format %q (expected human or json)", opts.format)
	}
	findingID, err := reportFindingID(rawID)
	if err != nil {
		return err
	}
	if !cmd.Flags().Changed("reason") {
		return usageError{errors.New("--reason is required: say why the finding is wrong for this code")}
	}
	reason, err := reportReason(opts.reason)
	if err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	client, err := service.New(resolveServiceURL(cfg, serviceFlag), resolveToken(cfg), "slopcop/"+version, reportTimeout)
	if err != nil {
		return err
	}
	response, err := client.Report(cmd.Context(), service.ReportRequest{
		FindingID: findingID,
		Kind:      service.ReportFalsePositive,
		Reason:    reason,
	})
	if err != nil {
		var serviceError *service.Error
		if errors.As(err, &serviceError) {
			fmt.Fprintln(cmd.ErrOrStderr(), remedyText(serviceError))
			return silentExit{service.ExitIncomplete}
		}
		return err
	}
	if response.FindingID == "" {
		response.FindingID = findingID
	}
	return report.WriteFalsePositive(cmd.OutOrStdout(), response, format)
}

// reportFindingID accepts a UUID in its canonical 8-4-4-4-12 hex form, in
// either case, and sends it lowercase.
func reportFindingID(raw string) (string, error) {
	id := strings.ToLower(strings.TrimSpace(raw))
	if !isUUID(id) {
		return "", fmt.Errorf(
			"%q is not a finding id: copy the UUID from the finding's Id line (human) or id (json) in a check report",
			raw,
		)
	}
	return id, nil
}

func isUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index := 0; index < len(value); index++ {
		b := value[index]
		switch index {
		case 8, 13, 18, 23:
			if b != '-' {
				return false
			}
		default:
			if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f') {
				return false
			}
		}
	}
	return true
}

// reportReason trims the reason and holds it to the service's bounds.
func reportReason(raw string) (string, error) {
	reason := strings.TrimSpace(raw)
	if !utf8.ValidString(reason) {
		return "", errors.New("the reason is not valid UTF-8")
	}
	length := utf8.RuneCountInString(reason)
	switch {
	case length == 0:
		return "", errors.New("the reason is empty: say why the finding is wrong for this code")
	case length > maxReasonLength:
		return "", fmt.Errorf("the reason is %d characters; keep it to %d", length, maxReasonLength)
	}
	return reason, nil
}
