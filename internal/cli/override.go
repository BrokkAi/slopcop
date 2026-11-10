package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/gitx"
	"github.com/BrokkAi/slopcop/internal/review"
)

// newOverrideCommand accepts review findings for the current working tree.
func newOverrideCommand() *cobra.Command {
	var root, reason string
	command := &cobra.Command{
		Use:   "override",
		Short: "Accept the local review's findings with a recorded reason",
		Long:  "Accept findings only after a complete review of the current working tree when the static check passed. The reason is recorded locally.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(reason) == "" {
				return fmt.Errorf("--reason is required")
			}
			repo, err := gitx.Toplevel(root)
			if err != nil {
				return err
			}
			store, err := review.OpenStore(repo)
			if err != nil {
				return err
			}
			defer store.Close()
			snapshot, err := gitx.CaptureWorkingTree(repo, nil)
			if err != nil {
				return err
			}
			if err = store.Override(reason, snapshot.Tree); err != nil {
				return err
			}
			gitDir, err := gitx.Run(repo, "rev-parse", "--absolute-git-dir")
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "slopcop: accepted the review's findings for tree %s; the reason is recorded in %s\n", snapshot.Tree, filepath.Join(gitDir, "slopcop", "overrides.jsonl"))
			return nil
		},
	}
	command.Flags().StringVar(&root, "root", ".", "repository path")
	command.Flags().StringVar(&reason, "reason", "", "reason for accepting the findings")
	return command
}
