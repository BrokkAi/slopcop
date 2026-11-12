package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/service"
)

func newLoginCommand(serviceURL *string, version string) *cobra.Command {
	var token string
	makeDefault := false
	command := &cobra.Command{
		Use:   "login",
		Short: "Sign in to the SlopCop check service",
		Long: `Sign in to the SlopCop check service.

With no flags, login starts the browser device flow: it prints a link that
opens the approval page with the code filled in, then waits for approval. Pass --token to store an existing credential,
which is what CI and agent harnesses use.

Each service keeps its own login. --service or $SLOPCOP_SERVICE signs in to
another deployment and leaves the other logins, and the default service, as
they are; pass --default to also make it the default for every command.

Run slopcop init in a repository to install the agent skill and hooks.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			target := strings.TrimRight(resolveServiceURL(cfg, *serviceURL), "/")
			credential := strings.TrimSpace(token)
			// The check a command makes of its service URL, before a
			// credential is stored for one no command could use.
			client, err := service.New(target, "", "slopcop/"+version, 0)
			if err != nil {
				return err
			}
			hosted := config.Origin(target) == config.DefaultServiceURL
			if credential == "" {
				if !hosted {
					fmt.Fprintf(cmd.ErrOrStderr(), "slopcop: signing in to %s\n", config.Origin(target))
				}
				noticeUpdates(client, cmd.ErrOrStderr(), version)
				start, err := client.StartDevice(cmd.Context())
				if err != nil {
					return remedyError(err)
				}
				fmt.Fprintln(cmd.ErrOrStderr(), start.Instructions())
				credential, err = client.AwaitDeviceToken(cmd.Context(), start, func(line string) {
					fmt.Fprintln(cmd.ErrOrStderr(), line)
				})
				if err != nil {
					return remedyError(err)
				}
			}
			cfg.SetToken(target, credential)
			if makeDefault {
				cfg.ServiceURL = target
			}
			path, err := config.Save(cfg)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "slopcop: logged in to %s (%s)\n", target, path)
			if current := config.Origin(cfg.DefaultService()); current != config.Origin(target) {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"slopcop: the default service is still %s; pass --service %s to use this login, or make it the default with `slopcop service use %s`\n",
					current, target, target)
			}
			return nil
		},
	}
	command.Flags().StringVar(&token, "token", "", "store an existing token instead of starting the browser flow")
	command.Flags().BoolVar(&makeDefault, "default", false, "also make this service the default for every command")
	return command
}

// remedyError prints a service failure the way a check does: the closed
// message plus the remedy.
func remedyError(err error) error {
	var serviceError *service.Error
	if errors.As(err, &serviceError) {
		return errors.New(remedyText(serviceError))
	}
	return err
}

// loginHints name the slopcop command for remedies that are actually to sign
// in. The service's remedies do not name a client, so the client adds its own.
// Repo-state failures (repo_not_indexed, no_access) keep the service's remedy
// alone: those say "sign in again" only as a step after installing the App or
// getting access, and appending a login command sends an already-signed-in
// user back through the login flow for a problem login cannot fix.
//
// The account commands' failures get the command that lists what to choose
// from in the same way. A disabled repository gets whoami: "your workspace"
// is the one this login is bound to, which may not be the one the person
// expects.
var loginHints = map[string]string{
	"auth_required":                  "Sign in with `slopcop login`.",
	"auth_expired":                   "Sign in with `slopcop login`.",
	"account_required":               "Sign in with `slopcop login`.",
	"device_flow_disabled":           "Sign in with `slopcop login --token <token>`.",
	"workspace_not_found":            "List them with `slopcop workspace list`.",
	"workspace_repository_not_found": "List them with `slopcop repo list`.",
	"owner_required":                 "Name a workspace you own with `--workspace <name>`.",
	"repository_disabled":            "`slopcop whoami` shows which workspace this login checks in.",
}

// remedyText is the service's message and remedy, verbatim, plus the slopcop
// command when the remedy is to sign in.
func remedyText(serviceError *service.Error) string {
	text := serviceError.RemedyText()
	if hint := loginHints[serviceError.Body.Code]; hint != "" {
		text += "\n" + hint
	}
	return text
}
