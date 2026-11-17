package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/gitx"
	"github.com/BrokkAi/slopcop/internal/service"
)

// rootRepoURL is the canonical URL of the repository checked out at root.
func rootRepoURL(root string) (string, error) {
	repoRoot, err := gitx.Toplevel(root)
	if err != nil {
		return "", err
	}
	origin, err := gitx.OriginURL(repoRoot)
	if err != nil {
		return "", err
	}
	return gitx.CanonicalRepoURL(origin)
}

// recordMapping stores that checkouts of repoURL check in workspace on the
// service, and says so. Every caller has had a person confirm, at a
// terminal, a step that names the workspace.
func recordMapping(cmd *cobra.Command, serviceURL, repoURL string, workspace service.Workspace) error {
	if repoURL == "" {
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if mapped, ok := cfg.WorkspaceFor(serviceURL, repoURL); ok && strings.EqualFold(mapped.ID, workspace.ID) && mapped.Name == workspace.Name {
		return nil
	}
	cfg.MapRepository(serviceURL, repoURL, config.RepositoryWorkspace{ID: workspace.ID, Name: workspace.Name})
	if _, err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"slopcop: checkouts of %s on this machine now check in, and bill, %q; `slopcop workspace unmap` undoes that\n",
		repositoryName(repoURL), workspace.Name)
	return nil
}

// offerMapping maps a checkout whose repository is already active in
// workspace, when a person at a terminal says yes. Without a terminal it
// only says how, since the mapping decides which workspace pays.
func offerMapping(cmd *cobra.Command, serviceURL, repoURL string, workspace service.Workspace) error {
	cfg, err := config.Load()
	if err != nil || repoURL == "" {
		return err
	}
	if mapped, ok := cfg.WorkspaceFor(serviceURL, repoURL); ok && strings.EqualFold(mapped.ID, workspace.ID) {
		return nil
	}
	question := fmt.Sprintf("Check this checkout in %q from now on, whatever workspace this login is bound to?", workspace.Name)
	yes, err := confirmYes(cmd, question)
	if errors.Is(err, errNotATerminal) {
		fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: to always check this checkout there, run `slopcop workspace map %q` at a terminal\n", workspace.Name)
		return nil
	}
	if err != nil || !yes {
		return err
	}
	return recordMapping(cmd, serviceURL, repoURL, workspace)
}

func newWorkspaceMapCommand(serviceURL *string, version string) *cobra.Command {
	root := ""
	command := &cobra.Command{
		Use:   "map <name|id>",
		Short: "Check this checkout's repository in one workspace of the account",
		Long: `Make every checkout of this repository on this machine run its checks and
ignores in, and bill, the workspace named, whatever workspace the login is
bound to. That is how one login checks work repositories in a team workspace
and your own in your personal one.

The mapping is kept in slopcop's user configuration, by the repository's
origin URL, for the service the command talks to. --workspace and
$SLOPCOP_WORKSPACE still win for one command. 'slopcop enable' maps the
checkout to the workspace it adds the repository to.

It decides which workspace pays, so it asks whoever runs it to confirm, and
refuses when stdin is not a terminal.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoURL, err := rootRepoURL(root)
			if err != nil {
				return err
			}
			client, err := accountClient(cmd, *serviceURL, version)
			if err != nil {
				return err
			}
			whoami, err := client.Whoami(cmd.Context())
			if err != nil {
				return serviceFailure(cmd, err)
			}
			workspace, err := findWorkspace(whoami.Workspaces, args[0])
			if err != nil {
				return err
			}
			name := repositoryName(repoURL)
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if mapped, ok := cfg.WorkspaceFor(client.BaseURL, repoURL); ok && strings.EqualFold(mapped.ID, workspace.ID) {
				fmt.Fprintf(cmd.OutOrStdout(), "slopcop: %s already checks in %q\n", name, workspace.Name)
				return nil
			}
			question := fmt.Sprintf("Checks of %s from this machine will then run in, and bill, %q. Map it?", name, workspace.Name)
			yes, err := confirmYes(cmd, question)
			if errors.Is(err, errNotATerminal) {
				return fmt.Errorf("workspace map decides which workspace pays for this checkout's checks, so it must be confirmed at a terminal; to check in %q once, pass --workspace %q to that command", workspace.Name, workspace.Name)
			}
			if err != nil {
				return err
			}
			if !yes {
				return errors.New("nothing was mapped")
			}
			if err := recordMapping(cmd, client.BaseURL, repoURL, workspace); err != nil {
				return err
			}
			client.Workspace = workspace.ID
			if repository, err := repositoryStatus(cmd.Context(), client, repoURL); err == nil && repository.State == statusNotAdded {
				fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: %s is not added to %q yet; run `slopcop enable` to add it\n", name, workspace.Name)
			}
			return nil
		},
	}
	command.Flags().StringVar(&root, "root", ".", "repository path")
	return command
}

func newWorkspaceUnmapCommand(serviceURL *string) *cobra.Command {
	root := ""
	command := &cobra.Command{
		Use:   "unmap",
		Short: "Forget which workspace this checkout's repository checks in",
		Long: `Forget the workspace this machine maps the checkout's repository to, so its
checks run in, and bill, the workspace the login is bound to again.

It decides which workspace pays, so it asks whoever runs it to confirm, and
refuses when stdin is not a terminal.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			repoURL, err := rootRepoURL(root)
			if err != nil {
				return err
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			target := resolveServiceURL(cfg, *serviceURL)
			name := repositoryName(repoURL)
			mapped, ok := cfg.WorkspaceFor(target, repoURL)
			if !ok {
				fmt.Fprintf(cmd.OutOrStdout(), "slopcop: %s is not mapped to a workspace\n", name)
				return nil
			}
			question := fmt.Sprintf("Checks of %s from this machine will then run in, and bill, the workspace this login is bound to, not %q. Unmap it?", name, valueOr(mapped.Name, mapped.ID))
			yes, err := confirmYes(cmd, question)
			if errors.Is(err, errNotATerminal) {
				return errors.New("workspace unmap decides which workspace pays for this checkout's checks, so it must be confirmed at a terminal")
			}
			if err != nil {
				return err
			}
			if !yes {
				return errors.New("the mapping was kept")
			}
			cfg.UnmapRepository(target, repoURL)
			if _, err := config.Save(cfg); err != nil {
				return err
			}
			forgetWorkspaceNotices()
			fmt.Fprintf(cmd.OutOrStdout(), "slopcop: %s now checks in the workspace this login is bound to\n", name)
			return nil
		},
	}
	command.Flags().StringVar(&root, "root", ".", "repository path")
	return command
}

// mappedRepository is one line of 'workspace mappings'.
type mappedRepository struct {
	Repository    string `json:"repository"`
	WorkspaceID   string `json:"workspaceId"`
	WorkspaceName string `json:"workspaceName"`
	// Usable is false when the account can no longer use the workspace, and
	// absent when the service could not be asked.
	Usable *bool `json:"usable,omitempty"`
}

func newWorkspaceMappingsCommand(serviceURL *string, version string) *cobra.Command {
	format := ""
	command := &cobra.Command{
		Use:   "mappings",
		Short: "List the repositories this machine maps to a workspace",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkFormat(format); err != nil {
				return err
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			target := resolveServiceURL(cfg, *serviceURL)
			// The names are the service's when it answers; the list itself
			// is this machine's and prints without it.
			var workspaces []service.Workspace
			asked := false
			if client, err := accountClient(cmd, *serviceURL, version); err == nil {
				if whoami, err := client.Whoami(cmd.Context()); err == nil {
					workspaces, asked = whoami.Workspaces, true
				}
			}
			listed := []mappedRepository{}
			for _, repoURL := range cfg.MappedRepositories(target) {
				mapped, _ := cfg.WorkspaceFor(target, repoURL)
				entry := mappedRepository{Repository: repoURL, WorkspaceID: mapped.ID, WorkspaceName: mapped.Name}
				if asked {
					usable := false
					for _, workspace := range workspaces {
						if strings.EqualFold(workspace.ID, mapped.ID) {
							usable, entry.WorkspaceName = true, workspace.Name
						}
					}
					entry.Usable = &usable
				}
				listed = append(listed, entry)
			}
			out := cmd.OutOrStdout()
			if format == "json" {
				return writeJSON(out, struct {
					Service  string             `json:"service"`
					Mappings []mappedRepository `json:"mappings"`
				}{config.Origin(target), listed})
			}
			if len(listed) == 0 {
				fmt.Fprintln(out, "slopcop: no repository is mapped to a workspace; every checkout checks in the one this login is bound to")
				return nil
			}
			table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			for _, entry := range listed {
				note := ""
				if entry.Usable != nil && !*entry.Usable {
					note = "not usable by this account: map it again or unmap it"
				}
				fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", entry.Repository, entry.WorkspaceName, entry.WorkspaceID, note)
			}
			return table.Flush()
		},
	}
	addFormatFlag(command, &format)
	return command
}

// workspaceNoticeLifetime is how long "nothing to say" about an unmapped
// checkout is remembered before the service is asked again, so joining a
// second workspace later is still noticed.
const workspaceNoticeLifetime = 7 * 24 * time.Hour

// workspaceNoticeTimeout bounds the lookups behind the note, which must
// never hold up a check.
const workspaceNoticeTimeout = 15 * time.Second

// workspaceNotice records one unmapped checkout the note was considered for.
type workspaceNotice struct {
	At time.Time `json:"at"`
	// Said is whether the note was printed; a printed note is not repeated.
	Said bool `json:"said,omitempty"`
}

func workspaceNoticesPath() (string, error) {
	path, err := config.Path()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), "workspace-notices.json"), nil
}

func loadWorkspaceNotices() map[string]workspaceNotice {
	notices := map[string]workspaceNotice{}
	path, err := workspaceNoticesPath()
	if err != nil {
		return notices
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return notices
	}
	if json.Unmarshal(data, &notices) != nil || notices == nil {
		return map[string]workspaceNotice{}
	}
	return notices
}

func saveWorkspaceNotices(notices map[string]workspaceNotice) {
	path, err := workspaceNoticesPath()
	if err != nil {
		return
	}
	data, err := json.Marshal(notices)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o600)
}

// forgetWorkspaceNotices lets the note be said again, after the login moved
// or a mapping was dropped: what it said about the workspace that pays is
// no longer true.
func forgetWorkspaceNotices() {
	if path, err := workspaceNoticesPath(); err == nil {
		_ = os.Remove(path)
	}
}

// unmappedNotice is noticeUnmapped, a variable so tests about something
// else do not have to answer its lookups.
var unmappedNotice = noticeUnmapped

// noticeUnmapped says, once per repository on this machine, that a checkout
// with no mapping checks in the workspace the login is bound to, and names
// the account's other workspaces that have the repository, since the check
// may be running in, and billing, the wrong one. It never stops the check:
// the person may mean it, and can map or remove the repository after. An
// account with one workspace has nothing to mix up and is told nothing.
func noticeUnmapped(cmd *cobra.Command, client *service.Client, choice workspaceChoice) {
	if choice.Source != sourceLogin || choice.RepoURL == "" {
		return
	}
	key := config.Origin(client.BaseURL) + " " + strings.ToLower(choice.RepoURL)
	notices := loadWorkspaceNotices()
	if seen, ok := notices[key]; ok && (seen.Said || time.Since(seen.At) < workspaceNoticeLifetime) {
		return
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), workspaceNoticeTimeout)
	defer cancel()
	whoami, err := client.Whoami(ctx)
	if err != nil {
		return
	}
	said := false
	if len(whoami.Workspaces) > 1 {
		name := repositoryName(choice.RepoURL)
		var elsewhere []string
		for _, workspace := range whoami.Workspaces {
			if strings.EqualFold(workspace.ID, whoami.Workspace.ID) {
				continue
			}
			other := *client
			other.Workspace, other.WorkspaceRefusal = workspace.ID, nil
			repository, err := repositoryStatus(ctx, &other, choice.RepoURL)
			if err != nil {
				// Half an answer would read as "nowhere else"; ask again
				// on a later check.
				return
			}
			if repository.State != statusNotAdded && repository.State != statusUnsupported {
				elsewhere = append(elsewhere, fmt.Sprintf("%q", workspace.Name))
			}
		}
		stderr := cmd.ErrOrStderr()
		if len(elsewhere) > 0 {
			fmt.Fprintf(stderr, "slopcop: note: %s is also in your workspace %s, and this check runs in, and bills, %q, the one this login is bound to.\n",
				name, strings.Join(elsewhere, " and "), whoami.Workspace.Name)
			fmt.Fprintln(stderr, "slopcop: if that is the wrong one, run `slopcop workspace map <name>` at a terminal to choose where this repository checks. This note is shown once.")
		} else {
			fmt.Fprintf(stderr, "slopcop: note: %s is not mapped to a workspace, so it checks in, and bills, %q, the one this login is bound to.\n",
				name, whoami.Workspace.Name)
			fmt.Fprintf(stderr, "slopcop: your account has %d workspaces; run `slopcop workspace map <name>` at a terminal to choose one for this repository. This note is shown once.\n",
				len(whoami.Workspaces))
		}
		said = true
	}
	notices[key] = workspaceNotice{At: time.Now().UTC(), Said: said}
	saveWorkspaceNotices(notices)
}
