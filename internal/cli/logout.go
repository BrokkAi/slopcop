package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/config"
)

func newLogoutCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Forget the stored credential",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			removed, err := config.ClearToken()
			if err != nil {
				return err
			}
			if !removed {
				fmt.Fprintln(cmd.OutOrStdout(), "slopcop: no stored credential")
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "slopcop: logged out")
			return nil
		},
	}
}
