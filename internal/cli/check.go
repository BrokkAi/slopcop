package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/gitx"
	"github.com/BrokkAi/slopcop/internal/report"
	"github.com/BrokkAi/slopcop/internal/service"
)

const (
	policyDirectory = ".slopcop/policies"
	failOnFinding   = "finding"
)

func runCheck(cmd *cobra.Command, opts *checkOptions, serviceFlag, version string) error {
	format, failOn, err := resolveEnums(opts)
	if err != nil {
		return err
	}
	// The installed skill follows the binary that runs.
	syncSkillAndReport(cmd.ErrOrStderr(), cmd.ErrOrStderr())
	repoRoot, err := gitx.Toplevel(opts.root)
	if err != nil {
		return err
	}
	base, err := resolveBase(repoRoot, opts.base)
	if err != nil {
		return err
	}
	selection, err := resolveSelection(repoRoot, opts)
	if err != nil {
		return err
	}
	change, err := gitx.WorkingTreePatch(repoRoot, base, selection.Files)
	if err != nil {
		return err
	}

	stdout := cmd.OutOrStdout()
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
	client, err := service.New(target, resolveToken(cfg), "slopcop/"+version, opts.timeout)
	if err != nil {
		return err
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
	if opts.verbose {
		fmt.Fprintf(
			cmd.ErrOrStderr(),
			"slopcop: POST %s/v1/check base %s (%d file(s), %d bytes, fail-on %s)\n",
			client.BaseURL, shortSHA(base), change.Files, len(change.Patch), failOn,
		)
	}

	// --timeout bounds the whole wait for a result: the client asks again
	// while the service answers check_running, and every ask attaches to the
	// same running check.
	ctx, cancel := context.WithTimeout(cmd.Context(), client.Timeout())
	defer cancel()
	response, err := client.Check(ctx, request)
	if err != nil {
		var pending *service.Pending
		if errors.As(err, &pending) {
			fmt.Fprintln(cmd.ErrOrStderr(), pending.RemedyText())
			if err := report.WritePending(stdout, *pending, format); err != nil {
				return err
			}
			return silentExit{service.ExitPending}
		}
		var serviceError *service.Error
		if errors.As(err, &serviceError) {
			fmt.Fprintln(cmd.ErrOrStderr(), remedyText(serviceError))
			return silentExit{service.ExitIncomplete}
		}
		return err
	}
	options := report.Options{ShowExisting: opts.showExisting}
	if err := report.Write(stdout, response, format, options); err != nil {
		return err
	}
	return silentExit{response.ExitCode()}
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

func shortSHA(value string) string {
	if len(value) > 12 {
		return value[:12]
	}
	return value
}
