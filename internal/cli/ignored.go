package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/gitx"
	"github.com/BrokkAi/slopcop/internal/report"
	"github.com/BrokkAi/slopcop/internal/service"
)

// maxIgnoredPage is the last page, counted from 1, the service lists.
const maxIgnoredPage = 10001

type ignoredOptions struct {
	root      string
	page      int
	format    string
	workspace string
}

func newIgnoredCommand(serviceURL *string, version string) *cobra.Command {
	opts := &ignoredOptions{}
	command := &cobra.Command{
		Use:   "ignored",
		Short: "List the findings your workspace ignored in this repository",
		Long: `List the findings your workspace ignored in this repository, newest first,
with the id 'slopcop unignore' takes to report one again.

The repository is the one the origin remote names, as for a check. A finding
the service no longer records is listed without an id.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runIgnored(cmd, opts, *serviceURL, version)
		},
	}
	flags := command.Flags()
	flags.StringVar(&opts.root, "root", ".", "repository path")
	flags.IntVar(&opts.page, "page", 1, "page of the list to show, counting from 1")
	addFormatFlag(command, &opts.format)
	addWorkspaceFlag(command, false, &opts.workspace)
	return command
}

func runIgnored(cmd *cobra.Command, opts *ignoredOptions, serviceFlag, version string) error {
	if err := checkFormat(opts.format); err != nil {
		return err
	}
	if opts.page < 1 || opts.page > maxIgnoredPage {
		return fmt.Errorf("invalid argument %d for --page: use a page from 1 to %d", opts.page, maxIgnoredPage)
	}
	repoRoot, err := gitx.Toplevel(opts.root)
	if err != nil {
		return err
	}
	origin, err := gitx.OriginURL(repoRoot)
	if err != nil {
		return err
	}
	repoURL, err := gitx.CanonicalRepoURL(origin)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	target := resolveServiceURL(cfg, serviceFlag)
	client, err := service.New(target, resolveToken(cfg, target), "slopcop/"+version, ignoreTimeout)
	if err != nil {
		return err
	}
	noticeService(cmd.ErrOrStderr(), client.BaseURL)
	noticeUpdates(client, cmd.ErrOrStderr(), version)
	if err := useWorkspace(cmd.Context(), client, opts.workspace); err != nil {
		return serviceFailure(cmd, err)
	}
	listed, err := client.Ignores(cmd.Context(), repoURL, opts.page-1)
	if err != nil {
		return serviceFailure(cmd, err)
	}
	if opts.format == "json" {
		return writeJSON(cmd.OutOrStdout(), listed)
	}
	writeIgnored(cmd.OutOrStdout(), listed, repositoryName(repoURL))
	return nil
}

// writeIgnored prints one page of ignores for a person or an agent.
func writeIgnored(out io.Writer, listed service.IgnoreList, fallbackName string) {
	name := report.Printable(valueOr(listed.Repository.FullName, fallbackName))
	page := listed.Page + 1
	pages := 1
	if listed.PageSize > 0 && listed.Total > listed.PageSize {
		pages = (listed.Total + listed.PageSize - 1) / listed.PageSize
	}
	if listed.Total == 0 {
		fmt.Fprintf(out, "slopcop: no ignored findings in %s\n", name)
		return
	}
	if len(listed.Ignores) == 0 {
		fmt.Fprintf(out, "slopcop: no ignored findings on page %d of %d in %s\n", page, pages, name)
		return
	}
	noun := "findings"
	if listed.Total == 1 {
		noun = "finding"
	}
	fmt.Fprintf(out, "slopcop: %d ignored %s in %s (page %d of %d)\n", listed.Total, noun, name, page, pages)
	undoable := false
	for _, ignored := range listed.Ignores {
		fmt.Fprintf(out, "  %s\n", ignoredLine(ignored))
		undoable = undoable || ignored.FindingID != nil
	}
	if page < pages {
		fmt.Fprintf(out, "slopcop: see the next page with `slopcop ignored --page %d`\n", page+1)
	}
	if undoable {
		fmt.Fprintln(out, "slopcop: undo one with `slopcop unignore <id>`")
	}
}

// ignoredLine is one ignore: the finding id, severity, location, policy and
// title, then when and by whom.
func ignoredLine(ignored service.IgnoredFinding) string {
	parts := []string{"(finding no longer recorded)"}
	if ignored.FindingID != nil {
		parts[0] = report.Printable(*ignored.FindingID)
	}
	parts = append(parts, "["+report.Printable(ignored.Severity)+"]")
	if ignored.Path != nil && *ignored.Path != "" {
		location := report.Printable(*ignored.Path)
		if ignored.StartLine != nil {
			location = fmt.Sprintf("%s:%d", location, *ignored.StartLine)
		}
		parts = append(parts, location)
	}
	policy := report.Printable(ignored.PolicyID)
	if ignored.Title != nil && *ignored.Title != "" {
		policy += ": " + report.Printable(*ignored.Title)
	}
	parts = append(parts, policy)
	parts = append(parts, fmt.Sprintf("(ignored %s by %s)", ignoredWhen(ignored.IgnoredAt), ignoredBy(ignored.IgnoredBy)))
	return strings.Join(parts, " ")
}

func ignoredWhen(value string) string {
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return report.Printable(value)
	}
	return at.UTC().Format("2006-01-02 15:04 UTC")
}

func ignoredBy(by *service.IgnoredBy) string {
	if by == nil || by.Username == "" && by.DisplayName == "" {
		return "an unrecorded account"
	}
	username := report.Printable(by.Username)
	if by.DisplayName == "" {
		return "@" + username
	}
	if username == "" {
		return report.Printable(by.DisplayName)
	}
	return fmt.Sprintf("%s (@%s)", report.Printable(by.DisplayName), username)
}
