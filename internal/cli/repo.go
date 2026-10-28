package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/gitx"
	"github.com/BrokkAi/slopcop/internal/service"
)

// repoTimeout bounds one repository request; the service checks the caller's
// repository access with the provider before it answers.
const repoTimeout = time.Minute

// confirmInput is where enable and disable read the person's confirmation:
// stdin, and only when it is a terminal, so an agent or a CI job, which runs
// slopcop with the same login, cannot change a repository's state. A
// variable so tests can stand in for a terminal.
var confirmInput = func(cmd *cobra.Command) (io.Reader, bool) {
	file, ok := cmd.InOrStdin().(*os.File)
	if !ok {
		return nil, false
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return nil, false
	}
	return file, true
}

func newRepoCommand(serviceURL *string, version string) *cobra.Command {
	root := "."
	command := &cobra.Command{
		Use:   "repo",
		Short: "Show, enable, or disable this repository in your workspace",
		Long: `Show, enable, or disable the checkout's repository in your SlopCop workspace.

A disabled repository is not checked and costs nothing from the next UTC day;
the day it is disabled was already charged. Its reports, findings, and
settings are kept and apply again once it is enabled. Only the workspace's
owner can enable or disable a repository, and only at a terminal: enable and
disable ask you to type the repository's name, and refuse when no terminal is
attached, so an agent or a CI job cannot change it.`,
		Args: cobra.NoArgs,
	}
	command.PersistentFlags().StringVar(&root, "root", ".", "repository checkout")
	command.AddCommand(
		&cobra.Command{
			Use:   "status",
			Short: "Show whether this repository is enabled",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return runRepo(cmd, *serviceURL, version, root, nil)
			},
		},
		&cobra.Command{
			Use:   "enable",
			Short: "Check this repository again",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				enabled := true
				return runRepo(cmd, *serviceURL, version, root, &enabled)
			},
		},
		&cobra.Command{
			Use:   "disable",
			Short: "Stop checking this repository",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				enabled := false
				return runRepo(cmd, *serviceURL, version, root, &enabled)
			},
		},
	)
	return command
}

// runRepo reads the repository's state, or sets it when enabled is not nil.
func runRepo(cmd *cobra.Command, serviceFlag, version, root string, enabled *bool) error {
	repoRoot, err := gitx.Toplevel(root)
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
	if enabled != nil {
		if err := confirmChange(cmd, repoURL, *enabled); err != nil {
			return err
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	client, err := service.New(resolveServiceURL(cfg, serviceFlag), resolveToken(cfg), "slopcop/"+version, repoTimeout)
	if err != nil {
		return err
	}
	var state service.RepositoryState
	if enabled == nil {
		state, err = client.Repository(cmd.Context(), repoURL)
	} else {
		state, err = client.SetRepositoryEnabled(cmd.Context(), repoURL, *enabled)
	}
	if err != nil {
		var serviceError *service.Error
		if errors.As(err, &serviceError) {
			fmt.Fprintln(cmd.ErrOrStderr(), remedyText(serviceError))
			return silentExit{service.ExitIncomplete}
		}
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), describeRepository(state, enabled != nil))
	return nil
}

// confirmChange asks the person at the terminal to type the repository's name
// before its state changes.
func confirmChange(cmd *cobra.Command, repoURL string, enabled bool) error {
	action := "disable"
	if enabled {
		action = "enable"
	}
	input, ok := confirmInput(cmd)
	if !ok {
		return fmt.Errorf("repo %s must be confirmed at a terminal; run it yourself, or change it on the Repositories page in SlopCop", action)
	}
	name := repositoryName(repoURL)
	fmt.Fprintf(cmd.ErrOrStderr(), "Type %s to %s it: ", name, action)
	answer, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if strings.TrimSpace(answer) != name {
		return fmt.Errorf("that is not %s, so nothing was changed", name)
	}
	return nil
}

// repositoryName is the owner/name path of a canonical repository URL.
func repositoryName(repoURL string) string {
	parsed, err := url.Parse(repoURL)
	if err != nil {
		return repoURL
	}
	return strings.Trim(parsed.Path, "/")
}

func describeRepository(state service.RepositoryState, changed bool) string {
	switch {
	case !state.Enabled && changed:
		return fmt.Sprintf("slopcop: disabled %s; it is not checked, and costs nothing from the next UTC day", state.Repository)
	case !state.Enabled:
		return fmt.Sprintf("slopcop: %s is disabled; it is not checked", state.Repository)
	case !state.Active:
		return fmt.Sprintf("slopcop: %s is enabled but over your workspace's active-repository limit, so it is not checked; choose which repositories are active on the Repositories page in SlopCop", state.Repository)
	case changed:
		return fmt.Sprintf("slopcop: enabled %s; it is checked again", state.Repository)
	default:
		return fmt.Sprintf("slopcop: %s is enabled and checked", state.Repository)
	}
}
