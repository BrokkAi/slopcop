package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/attribution"
	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/gitx"
	"github.com/BrokkAi/slopcop/internal/report"
	"github.com/BrokkAi/slopcop/internal/service"
)

const (
	policyDirectory = ".slopcop/policies"
	failOnFinding   = "finding"
	invalidChange   = "invalid_change"
	// repositoryAddUnconfirmed is the service's refusal of a check that would
	// add a repository its workspace then pays for.
	repositoryAddUnconfirmed = "repository_add_unconfirmed"
	// interruptedText is printed when Ctrl-C or SIGTERM stops a check that
	// was sent: a service that received it keeps it, so asking again picks up
	// its result.
	interruptedText = "slopcop: interrupted before the check answered. If the service received it, it keeps running it; run the same check again to pick up its result."
	// interruptedBeforeSendText is printed when the interrupt came during the
	// local work, before anything was sent.
	interruptedBeforeSendText = "slopcop: interrupted before the check was sent; nothing reached the service."
	// showExistingNotice is printed on stderr when --show-existing is passed.
	showExistingNotice = "slopcop: --show-existing is deprecated: the service lists only the findings a change introduced or touched, and every report counts the rest."
)

func runCheck(cmd *cobra.Command, opts *checkOptions, serviceFlag, version string) error {
	format, failOn, err := resolveEnums(opts)
	if err != nil {
		return err
	}
	if opts.showExisting {
		fmt.Fprintln(cmd.ErrOrStderr(), showExistingNotice)
	}
	// The installed skill follows the binary that runs.
	syncSkillAndReport(cmd.ErrOrStderr(), cmd.ErrOrStderr())
	stdout := cmd.OutOrStdout()
	prepared, err := prepareCheck(opts, serviceFlag, version, failOn)
	// An interrupt during the git work stops git too, which then fails;
	// either way nothing reached the service.
	if cmd.Context().Err() != nil {
		fmt.Fprintln(cmd.ErrOrStderr(), interruptedBeforeSendText)
		return silentExit{interruptCode(cmd.Context())}
	}
	if err != nil {
		return err
	}
	client, request := prepared.client, prepared.request
	if err := useWorkspace(cmd.Context(), client, opts.workspace); err != nil {
		return checkFailure(cmd, err)
	}
	if opts.hook != nil && duplicateHookCheck(prepared, opts.hook) {
		return nil
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
				fmt.Fprintf(cmd.ErrOrStderr(), "slopcop:   %s effort %s wrote %s\n",
					model.Model, valueOr(model.Effort, "unknown"), strings.Join(model.Files, ", "))
			}
		}
	}

	// --timeout bounds the whole wait for a result: the client asks again
	// while the service answers check_running, and every ask attaches to the
	// same running check.
	ctx, cancel := context.WithTimeout(cmd.Context(), client.Timeout())
	defer cancel()
	response, err := client.Check(ctx, request)
	// A service from before attribution rejects the unknown field as
	// invalid_change; the check itself must still run.
	var rejected *service.Error
	if request.Attribution != nil && errors.As(err, &rejected) && rejected.Body.Code == invalidChange {
		if opts.verbose {
			fmt.Fprintln(cmd.ErrOrStderr(), "slopcop: the service refused the attribution; sending the check without it")
		}
		request.Attribution = nil
		response, err = client.Check(ctx, request)
	}
	if errors.As(err, &rejected) && rejected.Body.Code == repositoryAddUnconfirmed {
		response, err = addRepositoryOnConfirm(cmd, client, request, rejected)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(cmd.ErrOrStderr(), interruptedText)
			return silentExit{interruptCode(cmd.Context())}
		}
		var pending *service.Pending
		if errors.As(err, &pending) {
			fmt.Fprintln(cmd.ErrOrStderr(), pending.RemedyText())
			if err := report.WritePending(stdout, *pending, format); err != nil {
				return err
			}
			return silentExit{service.ExitPending}
		}
		return checkFailure(cmd, err)
	}
	options := report.Options{ShowExisting: opts.showExisting}
	if err := report.Write(stdout, response, format, options); err != nil {
		return err
	}
	if !response.KnownStatus() {
		// The report alone could read as a pass; say why the exit is 2.
		fmt.Fprintf(cmd.ErrOrStderr(),
			"slopcop: the service answered status %q, which this version of slopcop does not know, so the check fails. Update slopcop.\n",
			response.Status)
	} else {
		rememberCompletedCheck(prepared, request.Attribution, opts.hook)
	}
	return silentExit{response.ExitCode()}
}

// checkFailure prints a service failure's message and remedy and exits 2,
// the exit of a check that did not complete. Any other error is returned as
// is.
func checkFailure(cmd *cobra.Command, err error) error {
	var serviceError *service.Error
	if errors.As(err, &serviceError) {
		fmt.Fprintln(cmd.ErrOrStderr(), remedyText(serviceError))
		return silentExit{service.ExitIncomplete}
	}
	return err
}

// addRepositoryOnConfirm asks the person at the terminal whether to add the
// repository to the workspace, which then pays for it, and sends the check
// again, confirmed, on a yes. The service cannot tell a person from an agent
// using the same login, so with no terminal, as for an agent or CI, the
// refusal stands and its remedy tells the person to run the check.
func addRepositoryOnConfirm(cmd *cobra.Command, client *service.Client, request service.Request, refusal *service.Error) (service.Response, error) {
	if _, ok := confirmInput(cmd); !ok {
		return service.Response{}, refusal
	}
	fmt.Fprintln(cmd.ErrOrStderr(), refusal.Error())
	question := fmt.Sprintf("Add %s to %s?", repositoryName(request.RepoURL), workspaceName(cmd.Context(), client))
	yes, err := confirmYes(cmd, question)
	if err != nil {
		return service.Response{}, err
	}
	if !yes {
		return service.Response{}, errors.New("nothing was added, so the check did not run")
	}
	request.AddRepository = true
	// The wait for the person is not part of the check's own.
	ctx, cancel := context.WithTimeout(cmd.Context(), client.Timeout())
	defer cancel()
	return client.Check(ctx, request)
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

// preparedCheck is a check request ready to send, and the client to send it.
type preparedCheck struct {
	client      *service.Client
	request     service.Request
	repoRoot    string
	fingerprint string
	// files and bytes describe the patch, for --verbose.
	files, bytes int
}

// prepareCheck does the local work a check needs before it can be sent: it
// finds the repository, its base and selection, builds the patch, and
// resolves the service and credential.
func prepareCheck(opts *checkOptions, serviceFlag, version, failOn string) (preparedCheck, error) {
	var prepared preparedCheck
	repoRoot, err := gitx.Toplevel(opts.root)
	if err != nil {
		return prepared, err
	}
	base, err := resolveBase(repoRoot, opts.base)
	if err != nil {
		return prepared, err
	}
	selection, err := resolveSelection(repoRoot, opts)
	if err != nil {
		return prepared, err
	}
	change, err := gitx.WorkingTreePatch(repoRoot, base, selection.Files)
	if err != nil {
		return prepared, err
	}
	origin, err := gitx.OriginURL(repoRoot)
	if err != nil {
		return prepared, err
	}
	repoURL, err := gitx.CanonicalRepoURL(origin)
	if err != nil {
		return prepared, err
	}
	cfg, err := config.Load()
	if err != nil {
		return prepared, err
	}
	target := resolveServiceURL(cfg, serviceFlag)
	token := resolveToken(cfg)
	client, err := service.New(target, token, "slopcop/"+version, opts.timeout)
	if err != nil {
		return prepared, err
	}
	request := service.Request{
		RepoURL:    repoURL,
		BaseCommit: base,
		Change:     base64.StdEncoding.EncodeToString(change.Patch),
		FailOn:     failOn,
	}
	if !selection.IsEmpty() {
		request.Selection = &selection
	}
	if opts.hook != nil {
		request.Attribution = attribution.DetectHook(repoRoot, opts.hook.Harness, opts.hook.SessionID)
	} else {
		request.Attribution = attribution.Detect(repoRoot)
	}
	return preparedCheck{
		client:      client,
		request:     request,
		repoRoot:    repoRoot,
		fingerprint: fingerprintCheck(target, token, request),
		files:       change.Files,
		bytes:       len(change.Patch),
	}, nil
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

// resolveBase picks a commit the service can already resolve: the merge base
// with the branch upstream, then the remote default branch, then HEAD. A wrong
// guess is corrected by the service's base_not_found remedy.
func resolveBase(root, rev string) (string, error) {
	if strings.TrimSpace(rev) != "" {
		return gitx.ResolveCommit(root, strings.TrimSpace(rev))
	}
	if base, ok, err := gitx.UpstreamBase(root); err != nil {
		return "", err
	} else if ok {
		return base, nil
	}
	if base, ok, err := gitx.OriginHeadBase(root); err != nil {
		return "", err
	} else if ok {
		return base, nil
	}
	head, err := gitx.HeadCommit(root)
	if err != nil {
		return "", fmt.Errorf("this repository has no commits yet, so there is nothing to check")
	}
	return head, nil
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
		cfg.ServiceURL,
		config.DefaultServiceURL,
	} {
		if value := strings.TrimSpace(candidate); value != "" {
			return value
		}
	}
	return config.DefaultServiceURL
}

func resolveToken(cfg config.Config) string {
	if token := strings.TrimSpace(os.Getenv("SLOPCOP_TOKEN")); token != "" {
		return token
	}
	return strings.TrimSpace(cfg.Token)
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
