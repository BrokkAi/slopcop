package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/config"
)

func newLogoutCommand(serviceURL *string) *cobra.Command {
	all := false
	command := &cobra.Command{
		Use:   "logout",
		Short: "Forget the stored credential for a service",
		Long: `Forget the credential stored for one service: the one --service or
$SLOPCOP_SERVICE names, else the default. Logins to other services stay.
Pass --all to forget every stored credential.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			var forgotten []string
			if all {
				forgotten = cfg.SignedIn()
				cfg.Logins = nil
			} else {
				target := config.Origin(resolveServiceURL(cfg, *serviceURL))
				if !cfg.Forget(target) {
					fmt.Fprintf(out, "slopcop: no stored credential for %s\n", target)
					return nil
				}
				forgotten = []string{target}
			}
			if len(forgotten) == 0 {
				fmt.Fprintln(out, "slopcop: no stored credential")
				return nil
			}
			if _, err := config.Save(cfg); err != nil {
				return err
			}
			fmt.Fprintf(out, "slopcop: logged out of %s\n", strings.Join(forgotten, ", "))
			return nil
		},
	}
	command.Flags().BoolVar(&all, "all", false, "forget the credential for every service")
	return command
}
