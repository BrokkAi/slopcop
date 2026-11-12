package cli

import (
	"errors"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/service"
)

// noticeService says on stderr which service a command talks to when it is
// not the hosted one, so a default that was moved, or a --service left in an
// alias, shows on every command. Stdout stays the report.
func noticeService(stderr io.Writer, serviceURL string) {
	if origin := config.Origin(serviceURL); origin != config.DefaultServiceURL {
		fmt.Fprintf(stderr, "slopcop: using service %s\n", origin)
	}
}

// storedService is one service the configuration knows: the default, and
// every one a login is stored for.
type storedService struct {
	Service  string `json:"service"`
	Default  bool   `json:"default"`
	SignedIn bool   `json:"signedIn"`
}

// storedServices lists the default service and every signed-in one, the
// default first.
func storedServices(cfg config.Config) []storedService {
	defaultOrigin := config.Origin(cfg.DefaultService())
	services := []storedService{{
		Service:  defaultOrigin,
		Default:  true,
		SignedIn: cfg.TokenFor(defaultOrigin) != "",
	}}
	for _, origin := range cfg.SignedIn() {
		if origin != defaultOrigin {
			services = append(services, storedService{Service: origin, SignedIn: true})
		}
	}
	return services
}

func newServiceCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "service",
		Short: "List the services this machine is signed in to, or change the default",
		Long: `The CLI keeps one login per service, so production and a staging
deployment stay signed in side by side. --service or $SLOPCOP_SERVICE picks
the service for one command; the default is the one every other command uses.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	command.AddCommand(newServiceListCommand(), newServiceUseCommand())
	return command
}

func newServiceListCommand() *cobra.Command {
	format := ""
	command := &cobra.Command{
		Use:   "list",
		Short: "List the default service and every service with a stored login",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkFormat(format); err != nil {
				return err
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			services := storedServices(cfg)
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), struct {
					Services []storedService `json:"services"`
				}{services})
			}
			table := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(table, "SERVICE\tLOGIN\tDEFAULT")
			for _, entry := range services {
				fmt.Fprintf(table, "%s\t%s\t%s\n", entry.Service, loginText(entry.SignedIn), defaultMark(entry.Default))
			}
			return table.Flush()
		},
	}
	addFormatFlag(command, &format)
	return command
}

func loginText(signedIn bool) string {
	if signedIn {
		return "signed in"
	}
	return "not signed in"
}

func defaultMark(isDefault bool) string {
	if isDefault {
		return "*"
	}
	return ""
}

func newServiceUseCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "use <url>",
		Short: "Make a service the default for every command",
		Long: `Make a service the default: the one every command without --service or
$SLOPCOP_SERVICE talks to. Stored logins do not change.

Every agent and script on this machine moves with it, so this asks whoever
runs it to confirm, and refuses when stdin is not a terminal. To use another
service once, pass --service to that command instead.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// The client's own check refuses a URL no command could use.
			client, err := service.New(args[0], "", "", 0)
			if err != nil {
				return err
			}
			target := config.Origin(client.BaseURL)
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			current := config.Origin(cfg.DefaultService())
			if target != current {
				question := fmt.Sprintf("Every slopcop command on this machine without --service, agents' included, will then go to %s. Make it the default?", target)
				yes, err := confirmYes(cmd, question)
				if errors.Is(err, errNotATerminal) {
					return fmt.Errorf("service use moves every agent on this machine, so it must be confirmed at a terminal; to use %s once, pass --service %s to that command", target, target)
				}
				if err != nil {
					return err
				}
				if !yes {
					return fmt.Errorf("the default service is still %s", current)
				}
				cfg.ServiceURL = client.BaseURL
				if _, err := config.Save(cfg); err != nil {
					return err
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "slopcop: the default service is now %s\n", target)
			if cfg.TokenFor(target) == "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: not signed in to %s; run `slopcop login`\n", target)
			}
			return nil
		},
	}
}
