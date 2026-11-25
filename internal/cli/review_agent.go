package cli

import (
	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/review/chatagent"
)

// newReviewAgentCommand is the read-only ACP agent a check starts for a chat
// reviewer. It is not for people to run, so it is hidden.
func newReviewAgentCommand() *cobra.Command {
	var provider, model, tree, base, effort string
	command := &cobra.Command{
		Use:    "review-agent",
		Short:  "Run the read-only review agent over ACP",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return chatagent.Main(cmd.Context(), provider, model, tree, base, cmd.InOrStdin(), cmd.OutOrStdout(), effort)
		},
	}
	flags := command.Flags()
	flags.StringVar(&effort, "effort", "", "reasoning effort")
	flags.StringVar(&provider, "provider", "", "chat provider")
	flags.StringVar(&model, "model", "", "model id")
	flags.StringVar(&tree, "tree", "", "git tree the tools read")
	flags.StringVar(&base, "base", "", "git tree the change is compared with")
	return command
}
