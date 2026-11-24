package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/prcomment"
)

type commentOptions struct {
	reportPath string
	repo       string
	pr         int
	commit     string
	apiURL     string
	dryRun     bool
}

func newCommentCommand(version string) *cobra.Command {
	opts := &commentOptions{}
	command := &cobra.Command{
		Use:   "comment",
		Short: "Comment a check's findings on the GitHub pull request it checked",
		Long: `Comment a check's findings on a GitHub pull request.

Reads the report 'slopcop check --format json' wrote and adds one pull
request review with an inline comment on each new finding whose lines the pull
request changes, static and review findings alike. A finding no line of the
diff can hold is listed in one summary comment, which also says in words what
the check found. A later run updates that comment in place and does not
comment a finding it already commented.

In GitHub Actions the repository, pull request and head commit come from the
workflow's event, and the token from GITHUB_TOKEN, which needs
'pull-requests: write'. With a read-only token, as on a pull request from a
fork, nothing is written to the pull request. When GITHUB_STEP_SUMMARY is
set the summary also goes to the job summary, and when GITHUB_OUTPUT is set
the outcome goes to the step's outputs: status, exit-reason,
static-findings, review-findings, headline and summary-url.

It exits 0 whatever the check found; the check's own exit code is the gate.`,
		Example: `  slopcop check --base origin/main --format json > slopcop.json
  slopcop comment --report slopcop.json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runComment(cmd, opts, version)
		},
	}
	flags := command.Flags()
	flags.StringVar(&opts.reportPath, "report", "", "the JSON report of `slopcop check --format json`, or - for stdin")
	flags.StringVar(&opts.repo, "repo", "", "owner/name of the repository (default: $GITHUB_REPOSITORY)")
	flags.IntVar(&opts.pr, "pr", 0, "pull request number (default: the workflow event's pull request)")
	flags.StringVar(&opts.commit, "commit", "", "head commit the check ran on (default: the workflow event's pull request head)")
	flags.StringVar(&opts.apiURL, "api-url", "", "GitHub REST API URL (default: $GITHUB_API_URL, else https://api.github.com)")
	flags.BoolVar(&opts.dryRun, "dry-run", false, "print the summary comment and write nothing to GitHub")
	_ = command.MarkFlagRequired("report")
	return command
}

func runComment(cmd *cobra.Command, opts *commentOptions, version string) error {
	data, err := readReport(cmd.InOrStdin(), opts.reportPath)
	if err != nil {
		return err
	}
	parsed, err := prcomment.ParseReport(data)
	if err != nil {
		return err
	}
	target := prcomment.Target{Repo: firstOf(opts.repo, os.Getenv("GITHUB_REPOSITORY")), PR: opts.pr, Commit: opts.commit}
	if event := os.Getenv("GITHUB_EVENT_PATH"); event != "" && (target.PR == 0 || target.Commit == "") {
		pr, head := pullRequestFromEvent(event)
		if target.PR == 0 {
			target.PR = pr
		}
		if target.Commit == "" {
			target.Commit = head
		}
	}
	if target.Commit == "" {
		target.Commit = os.Getenv("GITHUB_SHA")
	}

	var result prcomment.Result
	stderr := cmd.ErrOrStderr()
	switch {
	case opts.dryRun:
		result = prcomment.Render(parsed, target.Commit)
		fmt.Fprint(cmd.OutOrStdout(), result.Summary)
	case target.PR == 0 || target.Repo == "":
		result = prcomment.Render(parsed, target.Commit)
		fmt.Fprintln(stderr, "slopcop: no pull request to comment on (pass --repo and --pr outside a pull_request workflow); the outcome goes to the job summary only.")
	default:
		token := firstOf(os.Getenv("GITHUB_TOKEN"), os.Getenv("GH_TOKEN"))
		if token == "" {
			err = fmt.Errorf("set GITHUB_TOKEN to a token that may comment on %s#%d", target.Repo, target.PR)
		} else {
			client := &prcomment.GitHub{
				BaseURL:   firstOf(opts.apiURL, os.Getenv("GITHUB_API_URL"), "https://api.github.com"),
				Token:     token,
				UserAgent: "slopcop/" + version,
			}
			result, err = prcomment.Publish(cmd.Context(), client, parsed, target)
		}
		if err != nil {
			// The outcome still reaches the job summary and the step's
			// outputs, so a workflow can say what the check found.
			result = prcomment.Render(parsed, target.Commit)
			if writeErr := writeGitHubFiles(result); writeErr != nil {
				return writeErr
			}
			return err
		}
		switch {
		case result.ReadOnly:
			fmt.Fprintln(stderr, "slopcop: the token may not comment on this pull request (a pull request from a fork gets a read-only token, and a workflow needs `pull-requests: write`); the findings are in the job summary only.")
		case result.SummaryURL != "":
			fmt.Fprintf(stderr, "slopcop: %d inline comment(s) added; summary at %s\n", result.Posted, result.SummaryURL)
		default:
			fmt.Fprintf(stderr, "slopcop: %d inline comment(s) added\n", result.Posted)
		}
	}
	fmt.Fprintln(stderr, "slopcop: "+result.Outcome.Headline)
	return writeGitHubFiles(result)
}

// writeGitHubFiles puts the summary in the job summary and the outcome in the
// step's outputs, in GitHub Actions.
func writeGitHubFiles(result prcomment.Result) error {
	if err := appendGitHubFile("GITHUB_STEP_SUMMARY", result.Summary); err != nil {
		return err
	}
	return appendGitHubFile("GITHUB_OUTPUT", outputs(result))
}

// readReport reads the report from a file, or stdin for "-".
func readReport(stdin io.Reader, path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(io.LimitReader(stdin, 64<<20))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read --report: %w", err)
	}
	return data, nil
}

// pullRequestFromEvent reads the pull request number and head commit from a
// workflow event payload. Events without a pull request give zero values.
func pullRequestFromEvent(path string) (int, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, ""
	}
	var event struct {
		PullRequest *struct {
			Number int `json:"number"`
			Head   struct {
				SHA string `json:"sha"`
			} `json:"head"`
		} `json:"pull_request"`
	}
	if json.Unmarshal(data, &event) != nil || event.PullRequest == nil {
		return 0, ""
	}
	return event.PullRequest.Number, event.PullRequest.Head.SHA
}

// outputs is the step outputs, one key=value line each. Every value is one
// line: the headline is ours, built from counts and fixed words.
func outputs(result prcomment.Result) string {
	o := result.Outcome
	lines := []string{
		"status=" + oneLine(o.Status),
		"exit-reason=" + oneLine(o.ExitReason),
		"static-findings=" + strconv.Itoa(o.StaticFindings),
		"review-findings=" + strconv.Itoa(o.ReviewFindings),
		"headline=" + oneLine(o.Headline),
		"summary-url=" + oneLine(result.SummaryURL),
	}
	return strings.Join(lines, "\n") + "\n"
}

func oneLine(value string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(value)
}

// appendGitHubFile appends text to the file an Actions variable such as
// GITHUB_OUTPUT names, and does nothing outside Actions.
func appendGitHubFile(variable, text string) error {
	path := os.Getenv(variable)
	if path == "" || text == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("write $%s: %w", variable, err)
	}
	if _, err := io.WriteString(f, text); err != nil {
		f.Close()
		return fmt.Errorf("write $%s: %w", variable, err)
	}
	return f.Close()
}

func firstOf(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
